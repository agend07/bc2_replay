"""r263 R3 offline tests: game-folder dinput8.dll proxy install / upgrade / uninstall.

Run from the repo root:  python -m unittest replay.test_client_install -v
Temp folders and fakes only. The only real files read (read-only, skipped when absent) are the
Rome dll in the default game folder and the local shared build in rome_proxy\\out_shared.
"""
import struct
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import preview_config as pc
from replay import client_install as ci

SHARED = b'MZ shared ... BC2_REPLAY_PATCH ... dinput8_rome.dll BC2_ROME_HOST bfbc2.emulatornexus.com v2'
SHARED_OLD = b'MZ shared ... BC2_REPLAY_PATCH ... dinput8_rome.dll BC2_ROME_HOST v1'
PROXY = b'MZ plain out proxy dinput8_rome.dll BC2_ROME_HOST'
ROME = b'MZ rome original bfbc2.emulatornexus.com'
OTHER = b'MZ something else'

REAL_ROME = Path(r'C:\Program Files\EA Games\Battlefield Bad Company 2\dinput8_rome.dll')
REAL_SHARED = pc.APP_ROOT / 'rome_proxy' / 'out_shared' / 'dinput8.dll'


def fake_exports(path):
    return {'DirectInput8Create'}


def no_procs():
    return None


def running():
    raise RuntimeError('Close Battlefield: Bad Company 2 first (BFBC2Game.exe is running).')


def build_pe(names, magic=0x10B):
    """Minimal PE with one section holding an export directory, name pointer table and strings."""
    rva, raw = 0x1000, 0x200
    strings, pointers = b'', []
    names_off = 40
    str_off = names_off + 4 * len(names)
    for n in names:
        pointers.append(rva + str_off + len(strings))
        strings += n.encode('ascii') + b'\0'
    edir = bytearray(40)
    struct.pack_into('<IIII', edir, 20, len(names), len(names), 0, rva + names_off)
    section = bytes(edir) + b''.join(struct.pack('<I', p) for p in pointers) + strings
    dos = bytearray(64)
    dos[:2] = b'MZ'
    struct.pack_into('<I', dos, 0x3C, 64)
    opt_size = 224 if magic == 0x10B else 240
    dir_off = 96 if magic == 0x10B else 112
    coff = struct.pack('<HHIIIHH', 0x14C, 1, 0, 0, 0, opt_size, 0x2102)
    opt = bytearray(opt_size)
    struct.pack_into('<H', opt, 0, magic)
    struct.pack_into('<I', opt, dir_off - 4, 16)
    struct.pack_into('<II', opt, dir_off, rva, len(section))
    sect = bytearray(40)
    sect[:6] = b'.edata'
    struct.pack_into('<IIII', sect, 8, len(section), rva, len(section), raw)
    head = bytes(dos) + b'PE\0\0' + coff + bytes(opt) + bytes(sect)
    return head + b'\0' * (raw - len(head)) + section


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.game = root / "Bad Company O'Brien"
        self.game.mkdir()
        self.source = root / 'src' / 'dinput8.dll'
        self.source.parent.mkdir()
        self.source.write_bytes(SHARED)
        self.runtime = root / 'runtime'

    def put(self, name, data):
        (self.game / name).write_bytes(data)

    def get(self, name):
        p = self.game / name
        return p.read_bytes() if p.is_file() else None

    def snapshot(self):
        return {p.name: p.read_bytes() for p in self.game.iterdir()}

    def status(self, **kw):
        return ci.status(self.game, kw.pop('source', self.source), exports=fake_exports, **kw)


class Exports(unittest.TestCase):
    def test_minimal_pe32(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'x.dll'
            p.write_bytes(build_pe(['DirectInput8Create', 'DllCanUnloadNow']))
            self.assertEqual(ci.exports(p), {'DirectInput8Create', 'DllCanUnloadNow'})
            p.write_bytes(build_pe(['A'], magic=0x20B))
            self.assertEqual(ci.exports(p), {'A'})

    def test_malformed_never_raises(self):
        good = build_pe(['DirectInput8Create'])
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'x.dll'
            for data in (b'', b'MZ', OTHER, good[:300], good[:0x210], b'MZ' + b'\xff' * 100):
                p.write_bytes(data)
                self.assertEqual(ci.exports(p), set())
            self.assertEqual(ci.exports(Path(d) / 'absent.dll'), set())

    def test_real_rome(self):
        if not REAL_ROME.is_file():
            self.skipTest('no real dinput8_rome.dll')
        self.assertIn('DirectInput8Create', ci.exports(REAL_ROME))
        self.assertEqual(ci.classify(REAL_ROME), 'rome')

    def test_real_shared(self):
        if not REAL_SHARED.is_file():
            self.skipTest('no rome_proxy\\out_shared\\dinput8.dll')
        self.assertIn('DirectInput8Create', ci.exports(REAL_SHARED))
        self.assertEqual(ci.classify(REAL_SHARED), 'shared')


class Classify(Base):
    def kind(self, data, exports=fake_exports):
        self.put('x.dll', data)
        return ci.classify(self.game / 'x.dll', exports)

    def test_markers(self):
        self.assertEqual(ci.classify(self.game / 'absent.dll', fake_exports), 'missing')
        self.assertEqual(self.kind(SHARED), 'shared')
        self.assertEqual(self.kind(PROXY), 'proxy')
        self.assertEqual(self.kind(ROME), 'rome')
        self.assertEqual(self.kind(OTHER), 'unknown')
        self.assertEqual(self.kind(b'XX BC2_REPLAY_PATCH'), 'unknown')
        self.assertEqual(self.kind(b'MZ dinput8_rome.dll only'), 'unknown')
        self.assertEqual(self.kind(b'MZ BC2_ROME_HOST only'), 'unknown')
        self.assertEqual(self.kind(b'MZ bfbc2.emulatornexus.com dinput8_rome.dll'), 'unknown')

    def test_rome_needs_export(self):
        self.assertEqual(self.kind(ROME, exports=lambda p: set()), 'unknown')
        self.assertEqual(self.kind(ROME, exports=lambda p: {'Other'}), 'unknown')


class Status(Base):
    def test_no_folder(self):
        st = ci.status(self.game / 'nope', self.source, exports=fake_exports)
        self.assertEqual(st['state'], 'no-folder')
        self.assertTrue(st['source_ok'])
        self.assertEqual(set(st), {'state', 'dll', 'chain_ok', 'rome_present', 'msimg32', 'backup', 'sha256',
                                   'size', 'source', 'source_ok', 'source_sha256', 'version', 'source_version', 'text'})

    def test_mute_wins_over_installed(self):
        self.put('dinput8.dll', SHARED)
        self.put('dinput8_rome.dll', ROME)
        self.put('msimg32.dll', OTHER)
        st = self.status()
        self.assertEqual(st['state'], 'mute')
        self.assertTrue(st['msimg32'] and st['chain_ok'])

    def test_missing(self):
        self.assertEqual(self.status()['state'], 'missing')

    def test_unknown(self):
        self.put('dinput8.dll', OTHER)
        self.assertEqual(self.status()['state'], 'unknown')

    def test_broken_rome_dll_not_rome(self):
        self.put('dinput8.dll', SHARED)
        self.put('dinput8_rome.dll', OTHER)
        st = self.status()
        self.assertEqual(st['state'], 'broken')
        self.assertTrue(st['rome_present'])
        self.assertFalse(st['chain_ok'])

    def test_broken_proxy_without_rome(self):
        for data in (SHARED, PROXY):
            self.put('dinput8.dll', data)
            self.assertEqual(self.status()['state'], 'broken')

    def test_broken_half_uninstalled(self):
        self.put('dinput8.dll', ROME)
        self.put('dinput8_rome.dll', ROME)
        self.assertEqual(self.status()['state'], 'broken')

    def test_rome(self):
        self.put('dinput8.dll', ROME)
        st = self.status()
        self.assertEqual((st['state'], st['dll'], st['chain_ok'], st['size']), ('rome', 'rome', False, len(ROME)))

    def test_installed_byte_equal(self):
        self.put('dinput8.dll', SHARED)
        self.put('dinput8_rome.dll', ROME)
        self.put('dinput8.dll.old_r259', PROXY)
        st = self.status()
        self.assertEqual(st['state'], 'installed')
        self.assertTrue(st['chain_ok'])
        self.assertEqual(st['sha256'], st['source_sha256'])
        self.assertIn('Client proxy installed (sha ' + st['sha256'][:8], st['text'])

    def test_installed_source_missing(self):
        self.put('dinput8.dll', SHARED_OLD)
        self.put('dinput8_rome.dll', ROME)
        st = self.status(source=self.game / 'absent.dll')
        self.assertEqual(st['state'], 'installed')
        self.assertFalse(st['source_ok'])
        self.assertIsNone(st['source_sha256'])

    def test_upgrade(self):
        self.put('dinput8_rome.dll', ROME)
        for data in (PROXY, SHARED_OLD):
            self.put('dinput8.dll', data)
            st = self.status()
            self.assertEqual(st['state'], 'upgrade')
            self.assertIn('upgrade available', st['text'])

    def test_never_raises_on_unreadable(self):
        self.put('dinput8.dll', ROME)
        with mock.patch.object(Path, 'read_bytes', side_effect=PermissionError('denied')):
            st = self.status()
        self.assertEqual(st['state'], 'unknown')


class Version(Base):
    V = b'MZ shared ... BC2_REPLAY_PATCH ... dinput8_rome.dll BC2_ROME_HOST BC2_PROXY_VERSION=0.1.0-preview.2\0x'

    def test_dll_version(self):
        self.assertEqual(ci.dll_version(self.V), '0.1.0-preview.2')
        self.assertEqual(ci.dll_version(bytearray(self.V)), '0.1.0-preview.2')
        self.assertIsNone(ci.dll_version(SHARED))
        self.assertIsNone(ci.dll_version(b'BC2_PROXY_VERSION=\0'))
        self.assertIsNone(ci.dll_version(self.game / 'absent.dll'))
        self.assertIsNone(ci.dll_version(self.game))  # a folder: unreadable
        self.assertIsNone(ci.dll_version(None))
        self.assertEqual(ci.dll_version(b'BC2_PROXY_VERSION=' + b'a' * 100), 'a' * 64)  # no NUL: 64-byte cap
        self.assertEqual(ci.dll_version(b'BC2_PROXY_VERSION=' + b'b' * 70 + b'\0'), 'b' * 64)
        self.put('dinput8.dll', self.V)
        self.assertEqual(ci.dll_version(self.game / 'dinput8.dll'), '0.1.0-preview.2')

    def test_status_installed_with_version(self):
        self.source.write_bytes(self.V)
        self.put('dinput8.dll', self.V)
        self.put('dinput8_rome.dll', ROME)
        st = self.status()
        self.assertEqual((st['state'], st['version'], st['source_version']),
                         ('installed', '0.1.0-preview.2', '0.1.0-preview.2'))
        self.assertEqual(st['text'], 'Client proxy installed (build 0.1.0-preview.2, sha ' + st['sha256'][:8] +
                         ', ' + str(len(self.V)) + ' B), Rome chain ok.')

    def test_status_installed_without_version(self):
        self.put('dinput8.dll', SHARED)
        self.put('dinput8_rome.dll', ROME)
        st = self.status()
        self.assertEqual((st['state'], st['version'], st['source_version']), ('installed', None, None))
        self.assertEqual(st['text'], 'Client proxy installed (sha ' + st['sha256'][:8] + ', ' + str(len(SHARED)) +
                         ' B), Rome chain ok.')

    def test_status_upgrade_names_old_build(self):
        self.put('dinput8.dll', SHARED_OLD + b' BC2_PROXY_VERSION=0.1.0-preview.1\0')
        self.put('dinput8_rome.dll', ROME)
        self.source.write_bytes(self.V)
        st = self.status()
        self.assertEqual((st['state'], st['version'], st['source_version']),
                         ('upgrade', '0.1.0-preview.1', '0.1.0-preview.2'))
        self.assertIn('installed (build 0.1.0-preview.1, sha ', st['text'])

    def test_version_only_for_proxy_kinds(self):
        self.put('dinput8.dll', ROME + b' BC2_PROXY_VERSION=9\0')
        st = self.status()
        self.assertEqual((st['state'], st['version']), ('rome', None))
        st = ci.status(self.game / 'absent', self.source, exports=fake_exports)
        self.assertEqual((st['state'], st['version'], st['source_version']), ('no-folder', None, None))


class PlanInstall(Base):
    def plan(self, **kw):
        kw.setdefault('check_processes', no_procs)
        return ci.plan_install(self.game, kw.pop('source', self.source), exports=fake_exports, **kw)

    def test_rome(self):
        self.put('dinput8.dll', ROME)
        g = self.game
        self.assertEqual(self.plan(), [('rename', g / 'dinput8.dll', g / 'dinput8_rome.dll'),
                                       ('copy', self.source, g / 'dinput8.dll')])

    def test_upgrade(self):
        self.put('dinput8.dll', PROXY)
        self.put('dinput8_rome.dll', ROME)
        g = self.game
        self.assertEqual(self.plan(), [('copy', g / 'dinput8.dll', g / 'dinput8.dll.pre_proxy'),
                                       ('copy', self.source, g / 'dinput8.dll')])

    def test_already_installed(self):
        self.put('dinput8.dll', SHARED)
        self.put('dinput8_rome.dll', ROME)
        self.assertEqual(self.plan(check_processes=running), [])

    def test_refusals(self):
        with self.assertRaisesRegex(RuntimeError, 'Build the shared proxy first.*missing'):
            self.plan(source=self.game / 'absent.dll')
        self.source.write_bytes(PROXY)
        with self.assertRaisesRegex(RuntimeError, 'not the shared proxy'):
            self.plan()
        self.source.write_bytes(SHARED)
        with self.assertRaisesRegex(RuntimeError, 'Game folder not found'):
            ci.plan_install(self.game / 'nope', self.source, no_procs, fake_exports)
        with self.assertRaisesRegex(RuntimeError, 'install Project Rome first'):
            self.plan()
        self.put('dinput8.dll', OTHER)
        with self.assertRaisesRegex(RuntimeError, 'not touched'):
            self.plan()
        self.put('dinput8.dll', PROXY)
        with self.assertRaisesRegex(RuntimeError, 'dinput8_rome.dll is missing'):
            self.plan()
        self.put('dinput8.dll', ROME)
        self.put('msimg32.dll', OTHER)
        with self.assertRaises(RuntimeError) as cm:
            self.plan()
        self.assertEqual(str(cm.exception), ci.INSTALL_NOTE)
        (self.game / 'msimg32.dll').unlink()
        with self.assertRaisesRegex(RuntimeError, 'BFBC2Game.exe is running'):
            self.plan(check_processes=running)

    def test_default_process_check(self):
        self.put('dinput8.dll', ROME)
        with mock.patch('replay.client.client_pids', return_value=[1234]):
            with self.assertRaisesRegex(RuntimeError, 'Close Battlefield'):
                ci.plan_install(self.game, self.source, exports=fake_exports)
        with mock.patch('replay.client.client_pids', return_value=[]):
            self.assertEqual(len(ci.plan_install(self.game, self.source, exports=fake_exports)), 2)


class PlanUninstall(Base):
    def plan(self, check=no_procs):
        return ci.plan_uninstall(self.game, check, exports=fake_exports)

    def test_success_order(self):
        self.put('dinput8.dll', SHARED)
        self.put('dinput8_rome.dll', ROME)
        self.put('dinput8.dll.pre_proxy', PROXY)
        g = self.game
        self.assertEqual(self.plan(), [('copy', g / 'dinput8_rome.dll', g / 'dinput8.dll'),
                                       ('verify', g / 'dinput8.dll', g / 'dinput8_rome.dll'),
                                       ('delete', g / 'dinput8_rome.dll'),
                                       ('delete', g / 'dinput8.dll.pre_proxy')])

    def test_nothing_to_restore(self):
        self.put('dinput8.dll', ROME)
        self.assertEqual(self.plan(check=running), [])

    def test_refusals(self):
        self.put('dinput8.dll', SHARED)
        with self.assertRaisesRegex(RuntimeError, "Rome's dinput8_rome.dll is missing"):
            self.plan()
        self.put('dinput8_rome.dll', OTHER)
        with self.assertRaisesRegex(RuntimeError, 'not Project Rome'):
            self.plan()
        self.put('dinput8_rome.dll', ROME)
        self.put('dinput8.dll', OTHER)
        with self.assertRaisesRegex(RuntimeError, 'not overwritten'):
            self.plan()
        self.put('dinput8.dll', PROXY)
        with self.assertRaisesRegex(RuntimeError, 'running'):
            self.plan(check=running)
        with self.assertRaisesRegex(RuntimeError, 'Game folder not found'):
            ci.plan_uninstall(self.game / 'nope', no_procs, fake_exports)


class Apply(Base):
    def test_copy_rename_verify_delete(self):
        g = self.game
        self.put('a', b'A')
        ci.apply([('copy', self.source, g / 'b'), ('verify', g / 'b', self.source),
                  ('rename', g / 'a', g / 'c'), ('delete', g / 'b')])
        self.assertEqual(self.snapshot(), {'c': b'A'})

    def test_rename_refuses_existing_target(self):
        self.put('a', b'A')
        self.put('c', b'C')
        with self.assertRaisesRegex(RuntimeError, 'already exists'):
            ci.apply([('rename', self.game / 'a', self.game / 'c')])
        self.assertEqual(self.snapshot(), {'a': b'A', 'c': b'C'})

    def test_verify_mismatch(self):
        self.put('a', b'A')
        self.put('b', b'B')
        with self.assertRaisesRegex(RuntimeError, 'Verification failed'):
            ci.apply([('verify', self.game / 'a', self.game / 'b'), ('delete', self.game / 'b')])
        self.assertEqual(self.get('b'), b'B')

    def test_permission_error_passthrough(self):
        self.put('dinput8.dll', ROME)
        err = PermissionError(13, 'Access is denied')
        with mock.patch.object(ci.os, 'replace', side_effect=err):
            with self.assertRaises(PermissionError) as cm:
                ci.apply([('copy', self.source, self.game / 'dinput8.dll')])
        self.assertIs(cm.exception, err)
        self.assertEqual(self.snapshot(), {'dinput8.dll': ROME})

    def test_writable(self):
        self.assertTrue(ci.writable(self.game))
        self.assertFalse((self.game / ci.WRITE_PROBE).exists())
        self.assertFalse(ci.writable(self.game / 'nope'))
        with mock.patch('builtins.open', side_effect=PermissionError('denied')):
            self.assertFalse(ci.writable(self.game))


class ScriptText(Base):
    def test_content(self):
        g = self.game
        ops = [('rename', g / 'dinput8.dll', g / 'dinput8_rome.dll'), ('copy', self.source, g / 'dinput8.dll'),
               ('verify', g / 'dinput8.dll', self.source), ('delete', g / 'dinput8.dll.pre_proxy')]
        text = ci.script_text(ops)
        self.assertIn("$ErrorActionPreference = 'Stop'", text)
        self.assertIn('Start-Process powershell.exe -Verb RunAs', text)
        self.assertIn('-Wait -PassThru', text)
        self.assertIn('exit $child.ExitCode', text)
        self.assertIn('exit 1\n', text)
        quoted = "'" + str(g).replace("'", "''")
        self.assertIn("O''Brien", text)
        self.assertNotIn("O'Brien", text)
        steps = ['Test-Path -LiteralPath ' + quoted + "\\dinput8_rome.dll'",
                 'Rename-Item -LiteralPath ' + quoted + "\\dinput8.dll' -NewName 'dinput8_rome.dll'",
                 "Copy-Item -LiteralPath '" + str(self.source) + "' -Destination " + quoted + "\\dinput8.dll.tmp' -Force",
                 'Move-Item -LiteralPath ' + quoted + "\\dinput8.dll.tmp' -Destination " + quoted + "\\dinput8.dll' -Force",
                 'Get-FileHash -LiteralPath ' + quoted + "\\dinput8.dll'",
                 'Remove-Item -LiteralPath ' + quoted + "\\dinput8.dll.pre_proxy' -Force"]
        positions = [text.find(s) for s in steps]
        self.assertNotIn(-1, positions, steps)
        self.assertEqual(positions, sorted(positions))


class InstallFlow(Base):
    def install(self, **kw):
        return ci.install(self.game, self.source, no_procs, runtime_dir=self.runtime, exports=fake_exports, **kw)

    def uninstall(self, **kw):
        return ci.uninstall(self.game, self.source, no_procs, runtime_dir=self.runtime, exports=fake_exports, **kw)

    def test_install_direct(self):
        self.put('dinput8.dll', ROME)
        self.put('dinput8.dll.old_r259', PROXY)
        r = self.install()
        self.assertEqual((r['installed'], r['elevated'], r['backup']), (True, False, None))
        self.assertEqual(r['status']['state'], 'installed')
        self.assertEqual(self.snapshot(), {'dinput8.dll': SHARED, 'dinput8_rome.dll': ROME,
                                           'dinput8.dll.old_r259': PROXY})
        r = self.install()
        self.assertEqual((r['installed'], r['reason']), (False, 'shared proxy already installed'))

    def test_upgrade_direct(self):
        self.put('dinput8.dll', SHARED_OLD)
        self.put('dinput8_rome.dll', ROME)
        r = self.install()
        self.assertTrue(r['installed'])
        self.assertEqual(r['backup'], str(self.game / 'dinput8.dll.pre_proxy'))
        self.assertEqual(self.get('dinput8.dll.pre_proxy'), SHARED_OLD)
        self.assertEqual(self.get('dinput8.dll'), SHARED)

    def test_install_elevated(self):
        self.put('dinput8.dll', ROME)
        seen = []

        def run(args):
            seen.append(args)
            self.assertTrue(Path(args[-1]).is_file())
            ci.apply(ci.plan_install(self.game, self.source, no_procs, fake_exports))
            return 0

        with mock.patch.object(ci, 'writable', return_value=False):
            r = self.install(run=run)
        self.assertEqual((r['installed'], r['elevated']), (True, True))
        self.assertEqual(seen[0][:5], ['powershell.exe', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File'])
        self.assertTrue(seen[0][-1].endswith('.private.ps1'))
        self.assertTrue(Path(seen[0][-1]).name.startswith('client_install_'))
        self.assertTrue(Path(seen[0][-1]).is_file())
        self.assertEqual(self.get('dinput8.dll'), SHARED)

    def test_elevated_cancelled(self):
        self.put('dinput8.dll', ROME)
        before = self.snapshot()
        with mock.patch.object(ci, 'writable', return_value=False):
            with self.assertRaisesRegex(RuntimeError, r'cancelled.*code 1223'):
                self.install(run=lambda args: 1223)
        self.assertEqual(self.snapshot(), before)

    def test_elevated_claims_success_but_did_nothing(self):
        self.put('dinput8.dll', ROME)
        with mock.patch.object(ci, 'writable', return_value=False):
            with self.assertRaisesRegex(RuntimeError, 'state is rome'):
                self.install(run=lambda args: 0)

    def test_uninstall_direct(self):
        self.put('dinput8.dll', ROME)
        self.install()
        self.put('dinput8.dll.pre_proxy', PROXY)
        r = self.uninstall()
        self.assertEqual((r['restored'], r['elevated']), (True, False))
        self.assertEqual(r['status']['state'], 'rome')
        self.assertEqual(self.snapshot(), {'dinput8.dll': ROME})
        r = self.uninstall()
        self.assertEqual((r['restored'], r['reason']), (False, 'nothing to restore'))

    def test_uninstall_never_without_dll(self):
        self.put('dinput8.dll', SHARED)
        self.put('dinput8_rome.dll', ROME)
        calls = []
        real = ci.apply

        def spy(ops):
            calls.append(list(ops))
            # dinput8.dll is overwritten before anything is deleted.
            self.assertEqual([op[0] for op in ops], ['copy', 'verify', 'delete'])
            self.assertEqual(ops[0][2], self.game / 'dinput8.dll')
            real(ops)

        with mock.patch.object(ci, 'apply', side_effect=spy):
            self.uninstall()
        self.assertEqual(len(calls), 1)
        self.assertEqual(self.snapshot(), {'dinput8.dll': ROME})

    def test_uninstall_with_msimg32(self):
        self.put('dinput8.dll', SHARED)
        self.put('dinput8_rome.dll', ROME)
        self.put('msimg32.dll', OTHER)
        r = self.uninstall()
        self.assertTrue(r['restored'])
        self.assertEqual(r['status']['state'], 'mute')
        self.assertEqual(self.get('dinput8.dll'), ROME)


class Paths(unittest.TestCase):
    def test_proxy_source_order(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            release = root / 'proxy' / 'dinput8.dll'
            dev = root / 'rome_proxy' / 'out_shared' / 'dinput8.dll'
            self.assertEqual(ci.proxy_source(root), release)
            dev.parent.mkdir(parents=True)
            dev.write_bytes(SHARED)
            self.assertEqual(ci.proxy_source(root), dev)
            release.parent.mkdir()
            release.write_bytes(SHARED)
            self.assertEqual(ci.proxy_source(root), release)

    def test_game_dir_of(self):
        exe = Path('X:/Games/BC2/BFBC2Game.exe')
        self.assertEqual(ci.game_dir_of(exe), Path('X:/Games/BC2'))


if __name__ == '__main__':
    unittest.main()
