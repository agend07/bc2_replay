"""r266 offline tests for release_pack.py (temp dirs and fakes only; the exe / build.ps1 / git runner is injected).

Run from the repo root:  python -m pytest replay/test_release_pack.py -q -p no:cacheprovider
"""
import json
import shutil
import tempfile
import unittest
import zipfile
from pathlib import Path

import release_pack as rp
from replay import client_install as ci
from replay.client_install import SHARED_MARKER

V = rp.VERSION
DLL = b'MZ' + SHARED_MARKER + ci.VERSION_MARKER + V.encode('ascii') + b'\0'


GO_INFO = {'name': 'bc2-builder', 'engine': 'go', 'version': V, 'go': 'go1.27.3', 'format': 1}
EXE_BYTES = b'MZ go builder stub'


def ok_run(args, cwd, timeout):
    """Panel exe --version, builder.exe version --json, git, and build.ps1 (writes the -Out exe)."""
    if args[-2:] == ['version', '--json']:
        return 0, json.dumps(GO_INFO) + '\n'
    if args[0] == 'git':
        return (0, 'c0ffee' * 6 + 'beef\n') if args[1] == 'rev-parse' else (0, '')
    if '-File' in args:
        Path(args[args.index('-Out') + 1]).write_bytes(EXE_BYTES)
        return 0, 'sha  builder.exe\n'
    return 0, V + '\r\n'


def run_with(**over):
    """ok_run with one answer replaced: version=(rc, text), panel=(rc, text), build=(rc, text)."""
    def run(args, cwd, timeout):
        if 'version' in over and args[-2:] == ['version', '--json']:
            return over['version']
        if 'build' in over and '-File' in args:
            return over['build']
        if 'panel' in over and args[-1:] == ['--version']:
            return over['panel']
        return ok_run(args, cwd, timeout)
    return run


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        r = self.root = Path(self.tmp.name)
        self.app = r / 'app'
        for rel in rp.APP_FILES:
            self.put(self.app / rel, b'text ' + rel.encode())
        self.proxy = r / 'out_shared' / 'dinput8.dll'
        self.put(self.proxy, DLL)
        self.builder_exe = r / 'go' / 'builder.exe'
        self.put(self.builder_exe, EXE_BYTES)
        self.datapack = r / 'datapack'
        self.put(self.datapack / 'manifest.json', b'{}')
        self.put(self.datapack / 'baselines' / 'baselines.json', b'{}')
        self.put(self.datapack / 'mappack.json', b'{}')
        self.put(self.datapack / 'gate_whitelist.json.zlib', b'z')
        self.dist = r / 'dist'
        self.put(self.dist / 'BC2LocalReplay.exe', b'MZ exe stub')
        self.put(self.dist / '_internal' / 'python314.dll', b'MZ dll')
        self.out = r / 'release'
        self.lines = []

    def put(self, path, data):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)

    def pack(self, *flags, run=ok_run, exe=True):
        """Goes through the real CLI parser (main) with the runner injected; exe=False builds via build.ps1."""
        self.lines = []
        argv = (['--builder-exe', str(self.builder_exe)] if exe else []) + [
            '--datapack', str(self.datapack), '--dist', str(self.dist),
            '--proxy', str(self.proxy), '--out', str(self.out)] + list(flags)
        old = rp.APP_ROOT
        rp.APP_ROOT = self.app
        try:
            return rp.main(argv, run=run, say=self.lines.append)
        finally:
            rp.APP_ROOT = old

    def check(self, path, run=ok_run):
        self.lines = []
        return rp.main(['--check', str(path)], run=run, say=self.lines.append)

    def text(self):
        return '\n'.join(self.lines)

    @property
    def zip(self):
        return self.out / (rp.release_name() + '.zip')

    def assert_refused(self, rc, needle):
        self.assertEqual(rc, 1, self.text())
        self.assertTrue(self.lines[-1].startswith('REFUSED'), self.text())
        self.assertIn(needle, self.text())
        self.assertFalse(self.out.exists(), 'nothing may be written')


class HappyPath(Base):
    def test_zip_sums_and_check(self):
        rc = self.pack()
        self.assertEqual(rc, 0, self.text())
        self.assertTrue(self.lines[-1].startswith('RESULT ' + str(self.zip)), self.text())
        self.assertIn('OK proxy', self.text())
        self.assertIn('build ' + V + ' sha ', self.text())
        self.assertIn('OK exe version ' + V, self.text())
        self.assertIn('OK datapack 4 files', self.text())
        top = rp.release_name()
        with zipfile.ZipFile(self.zip) as z:
            names = [n for n in z.namelist() if not n.endswith('/')]
            self.assertTrue(all(n.startswith(top + '/') for n in names))
            self.assertTrue(all(i.compress_type == zipfile.ZIP_DEFLATED for i in z.infolist()))
            sums = z.read(top + '/SHA256.txt').decode().splitlines()
        rels = sorted(n[len(top) + 1:] for n in names if n != top + '/SHA256.txt')
        self.assertEqual([line.split('  ', 1)[1] for line in sums], rels, 'every file, sorted by path')
        for want in ('BC2LocalReplay.exe', '_internal/python314.dll', 'proxy/dinput8.dll', 'builder/builder.json',
                     'builder/builder.exe', 'datapack/mappack.json', 'datapack/gate_whitelist.json.zlib',
                     'USER_GUIDE.md'):
            self.assertIn(want, rels)
        self.assertEqual([r for r in rels if r.startswith('builder')], ['builder/builder.exe', 'builder/builder.json'])
        with zipfile.ZipFile(self.zip) as z:
            meta = json.loads(z.read(top + '/builder/builder.json'))
        self.assertEqual(meta, {'format': 2, 'runtime': 'go', 'go': 'go1.27.3', 'version': V,
                                'commit': 'c0ffee' * 6 + 'beef', 'exe_sha256': rp.sha256(self.builder_exe)})
        self.assertTrue((self.out / top / 'SHA256.txt').is_file())
        side = Path(str(self.zip) + '.sha256').read_text().split()
        self.assertEqual(side, [rp.sha256(self.zip), self.zip.name])
        self.assertEqual(self.check(self.zip), 0, self.text())
        self.assertIn('OK %d files verified' % len(rels), self.text())
        self.assertIn('matches', self.text())
        self.assertIn('OK builder/builder.exe sha', self.text())
        self.assertIn('OK builder.exe version --json: %s go1.27.3' % V, self.text())

    def test_builds_with_build_ps1(self):
        calls = []

        def run(args, cwd, timeout):
            calls.append(args)
            return ok_run(args, cwd, timeout)
        (self.app / rp.GO_DIR).mkdir(parents=True)
        (self.app / rp.GO_DIR / 'build.ps1').write_text('# stub', encoding='utf-8')
        self.assertEqual(self.pack(run=run, exe=False), 0, self.text())
        build = [a for a in calls if '-File' in a][0]
        self.assertTrue(build[build.index('-File') + 1].endswith('build.ps1'))
        self.assertEqual(build[build.index('-Version') + 1], V)
        self.assertIn('OK built builder.exe', self.text())
        with zipfile.ZipFile(self.zip) as z:
            self.assertEqual(z.read(rp.release_name() + '/builder/builder.exe'), EXE_BYTES)

    def test_dirty_commit(self):
        status = []

        def run(args, cwd, timeout):
            if args[:2] == ['git', 'status']:
                status.append(args)
                return 0, ' M release_pack.py\n'
            return ok_run(args, cwd, timeout)
        self.assertEqual(self.pack(run=run), 0, self.text())
        with zipfile.ZipFile(self.zip) as z:
            self.assertTrue(json.loads(z.read(rp.release_name() + '/builder/builder.json'))['commit'].endswith('-dirty'))
        self.assertEqual(status, [['git', 'status', '--porcelain', '--untracked-files=no']], 'whole repo, tracked only')

    def test_git_commit(self):
        head = 'c0ffee' * 6 + 'beef'

        def run_status(rc, text):
            return lambda args, cwd, timeout: (rc, text) if args[:2] == ['git', 'status'] else ok_run(args, cwd, timeout)
        self.assertEqual(rp.git_commit(self.app, run_status(0, '')), head)
        self.assertEqual(rp.git_commit(self.app, run_status(0, ' M docs/x.md\n')), head + '-dirty')
        self.assertEqual(rp.git_commit(self.app, run_status(128, 'fatal')), head)
        self.assertEqual(rp.git_commit(self.app, lambda a, c, t: (128, '')), 'unknown')

    def test_rerun_replaces_stage(self):
        self.assertEqual(self.pack(), 0)
        self.put(self.out / rp.release_name() / 'stale.txt', b'old')
        self.assertEqual(self.pack(), 0)
        self.assertFalse((self.out / rp.release_name() / 'stale.txt').exists())

    def test_skipped_files(self):
        self.put(self.dist / '_internal' / '__pycache__' / 'x.cpython-314.pyc', b'c')
        self.put(self.dist / '_internal' / 'mod.pyc', b'c')
        self.put(self.datapack / 'baselines' / 'b.json.bak_r1', b'old')
        self.put(self.datapack / 'Thumbs.db', b't')
        self.assertEqual(self.pack(), 0, self.text())
        self.assertIn('SKIP _internal/__pycache__/x.cpython-314.pyc', self.text())
        self.assertIn('SKIP datapack/baselines/b.json.bak_r1', self.text())
        with zipfile.ZipFile(self.zip) as z:
            joined = '\n'.join(z.namelist())
        for bad in ('__pycache__', '.bak_r1', 'mod.pyc', 'Thumbs.db'):
            self.assertNotIn(bad, joined)

    def test_dry_run_writes_nothing(self):
        rc = self.pack('--dry-run')
        self.assertEqual(rc, 0, self.text())
        self.assertFalse(self.out.exists())
        self.assertIn('FILE %s/proxy/dinput8.dll' % rp.release_name(), self.text())
        self.assertTrue(self.lines[-1].startswith('RESULT dry-run'))


class Refusals(Base):
    def test_dll_version_mismatch(self):
        self.put(self.proxy, b'MZ' + SHARED_MARKER + ci.VERSION_MARKER + b'0.1.0-preview.1\0')
        self.assert_refused(self.pack(), 'build 0.1.0-preview.1')

    def test_dll_without_marker(self):
        self.put(self.proxy, b'MZ' + SHARED_MARKER)
        self.assert_refused(self.pack(), 'no version marker')

    def test_dll_not_shared(self):
        self.put(self.proxy, b'MZ plain dinput8_rome.dll BC2_ROME_HOST ' + ci.VERSION_MARKER + V.encode() + b'\0')
        self.assert_refused(self.pack(), "classifies 'proxy'")

    def test_builder_version_other(self):
        info = dict(GO_INFO, version='0.1.0-preview.1')
        self.assert_refused(self.pack(run=run_with(version=(0, json.dumps(info)))), "version '0.1.0-preview.1'")

    def test_builder_not_go(self):
        self.assert_refused(self.pack(run=run_with(version=(0, json.dumps(dict(GO_INFO, engine='py'))))), "engine 'py'")

    def test_builder_version_not_json(self):
        self.assert_refused(self.pack(run=run_with(version=(1, 'boom'))), "version --json printed 'boom' (rc 1)")

    def test_builder_exe_missing(self):
        self.builder_exe.unlink()
        self.assert_refused(self.pack(), 'builder exe')

    def test_build_ps1_fails(self):
        (self.app / rp.GO_DIR).mkdir(parents=True)
        (self.app / rp.GO_DIR / 'build.ps1').write_text('# stub', encoding='utf-8')
        self.assert_refused(self.pack(run=run_with(build=(1, 'go: error\n')), exe=False), 'build.ps1 rc 1')

    def test_no_build_ps1(self):
        self.assert_refused(self.pack(exe=False), 'build.ps1')  # the temp app root has no builder_go

    def test_datapack_default(self):
        """r278: without --datapack the committed <app root>/datapack is used."""
        shutil.copytree(self.datapack, self.app / 'datapack')
        self.lines = []
        argv = ['--builder-exe', str(self.builder_exe), '--dist', str(self.dist), '--proxy', str(self.proxy),
                '--out', str(self.out), '--dry-run']
        old, rp.APP_ROOT = rp.APP_ROOT, self.app
        try:
            self.assertEqual(rp.main(argv, run=ok_run, say=self.lines.append), 0, self.text())
        finally:
            rp.APP_ROOT = old
        self.assertIn('OK datapack 4 files', self.text())

    def test_datapack_baselines_missing(self):
        (self.datapack / 'baselines' / 'baselines.json').unlink()
        self.assert_refused(self.pack(), 'baselines/baselines.json')

    def test_dist_internal_missing(self):
        (self.dist / '_internal' / 'python314.dll').unlink()
        (self.dist / '_internal').rmdir()
        self.assert_refused(self.pack(), '_internal')

    def test_exe_version_other_string(self):
        self.assert_refused(self.pack(run=lambda a, c, t: (0, '0.1.0-preview.1\n')), "printed '0.1.0-preview.1'")

    def test_exe_version_raises(self):
        def boom(a, c, t):
            raise TimeoutError('timed out')
        self.assert_refused(self.pack(run=boom), 'timed out')

    def test_exe_not_run_when_other_check_fails(self):
        calls = []
        self.builder_exe.unlink()

        def run(args, cwd, timeout):
            calls.append(args)
            return ok_run(args, cwd, timeout)
        self.assert_refused(self.pack(run=run), 'builder exe')
        self.assertEqual([a for a in calls if a[-1:] == ['--version']], [])

    def test_skip_exe_version(self):
        calls = []

        def run(args, cwd, timeout):
            calls.append(args)
            return ok_run(args, cwd, timeout)
        self.assertEqual(self.pack('--skip-exe-version', run=run), 0)
        self.assertEqual([a for a in calls if a[-1:] == ['--version']], [])
        self.assertIn('WARN exe --version check skipped', self.text())

    def test_private_content_planted(self):
        for rel, needle in (('preview.json', 'preview.json'), ('tools/x.private.ps1', '*.private.*'),
                            ('cap/m.bc2raw', '*.bc2raw'), ('runtime/a.txt', 'runtime directory'),
                            ('logs/run.LOG', '*.log'), ('.env.local', '.env*'), ('s/trace.pcapng', '*.pcap*'),
                            ('m-log.bin', 'm-log.bin'), ('dinput8.dll.pre_proxy', '*.pre_proxy')):
            with self.subTest(rel=rel):
                p = self.datapack / rel
                self.put(p, b'secret')
                self.assert_refused(self.pack(), 'datapack/' + rel)
                self.assertIn(needle, self.text())
                p.unlink()
        self.assertEqual(self.pack('--dry-run'), 0, self.text())


class CheckZip(Base):
    def setUp(self):
        super().setUp()
        self.assertEqual(self.pack(), 0)
        self.top = rp.release_name()

    def rewrite(self, change=None, add=None, drop=None):
        bad = self.root / 'bad.zip'
        with zipfile.ZipFile(self.zip) as src, zipfile.ZipFile(bad, 'w') as dst:
            for info in src.infolist():
                if drop and info.filename == self.top + '/' + drop:
                    continue
                data = src.read(info)
                if change and info.filename == self.top + '/' + change:
                    data += b'tampered'
                dst.writestr(info, data)
            if add:
                dst.writestr(self.top + '/' + add, b'extra')
        return bad

    def test_tampered_entry(self):
        self.assertEqual(self.check(self.rewrite(change='proxy/dinput8.dll')), 1)
        self.assertIn('MISMATCH proxy/dinput8.dll', self.text())

    def test_extra_entry(self):
        self.assertEqual(self.check(self.rewrite(add='builder/evil.py')), 1)
        self.assertIn('EXTRA builder/evil.py', self.text())
        self.assertIn('UNEXPECTED builder/evil.py', self.text())

    def test_python_builder_refused(self):
        self.assertEqual(self.check(self.rewrite(add='builder_src/v3_trial.py')), 1)
        self.assertIn('UNEXPECTED builder_src/v3_trial.py', self.text())

    def test_exe_sha_vs_builder_json(self):
        self.assertEqual(self.check(self.rewrite(change='builder/builder.exe')), 1)
        self.assertIn('MISMATCH builder/builder.exe', self.text())
        self.assertIn('builder.json says', self.text())

    def test_exe_version_run_on_check(self):
        calls = []

        def run(args, cwd, timeout):
            calls.append(args)
            return ok_run(args, cwd, timeout)
        self.assertEqual(self.check(self.zip, run=run), 0, self.text())
        self.assertEqual(calls[0][1:], ['version', '--json'])
        self.assertTrue(calls[0][0].endswith('builder.exe'))
        self.assertEqual(self.check(self.zip, run=run_with(version=(0, json.dumps(dict(GO_INFO, version='x'))))), 1)
        self.assertIn("MISMATCH builder.exe version --json 'go'/'x'", self.text())

    def test_missing_entry(self):
        self.assertEqual(self.check(self.rewrite(drop='USER_GUIDE.md')), 1)
        self.assertIn('MISSING USER_GUIDE.md', self.text())

    def test_sidecar_mismatch(self):
        Path(str(self.zip) + '.sha256').write_text('0' * 64 + '  x.zip\n')
        self.assertEqual(self.check(self.zip), 1)
        self.assertIn('MISMATCH ' + self.zip.name + '.sha256', self.text())


if __name__ == '__main__':
    unittest.main()
