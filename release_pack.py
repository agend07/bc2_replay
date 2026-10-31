"""r266: assemble the release zip of the panel from built parts (binaries are gitignored; CI runs this and its
tag workflow uploads the zip as the GitHub Release asset; a local run is for checking). Stdlib only.

  python release_pack.py [--builder-exe builder.exe] [--datapack DIR] [--dist dist\\onedir\\BC2LocalReplay]
                         [--proxy rome_proxy\\out_shared\\dinput8.dll] [--out dist\\release]
                         [--skip-exe-version] [--dry-run]
  python release_pack.py --check ZIP

Every provenance / content check runs before anything is written; any failure prints REFUSED and
returns rc 1 with nothing written. The zip holds one top-level folder bc2-local-replay-<VERSION>/
with SHA256.txt (sha256sum format) listing every other file; <zip>.sha256 sits next to the zip.

r278: --datapack defaults to the committed datapack\\ next to this script.
P5 (Go port): the recording builder is the Go builder.exe. Without --builder-exe it is built from builder_go\\ with
builder_go\\build.ps1 -Version <VERSION> (reproducible: trimpath, no VCS stamp, empty build id); --builder-exe takes a
prebuilt one (e.g. the CI artifact). The zip ships builder\\builder.exe + builder\\builder.json (format 2: runtime
"go", go version, version, commit, exe sha256) - no Python runtime. --check also verifies the exe sha against builder.json and runs 'builder.exe version --json'.
"""
import argparse
import fnmatch
import hashlib
import json
import shutil
import subprocess
import sys
import tempfile
import zipfile
from pathlib import Path, PurePosixPath

APP_ROOT = Path(__file__).resolve().parent
if str(APP_ROOT) not in sys.path:
    sys.path.insert(0, str(APP_ROOT))

import preview_config  # noqa: E402
from replay import client_install  # noqa: E402

VERSION = preview_config.VERSION
EXE_NAME = 'BC2LocalReplay.exe'
SUMS_NAME = 'SHA256.txt'
EXE_TIMEOUT = 60
BUILD_TIMEOUT = 900
APP_FILES = ('USER_GUIDE.md', 'docs/tester_checklist.md')
GO_DIR = 'builder_go'                 # Go builder source; build.ps1 there makes builder.exe
BUILDER_EXE = 'builder/builder.exe'   # in the zip
BUILDER_JSON = 'builder/builder.json'
BUILDER_FILES = (BUILDER_EXE, BUILDER_JSON)  # the only files under builder/ in the zip

# Content guard, matched case-insensitively against each staged relative path.
REFUSE_NAMES = ('*.private.*', 'preview.json', '*.bc2raw', 'm-log.bin', 'soldier_rows.pkl', 'capture.json',
                '*.log', '*.dmp', '*.pre_proxy','.bc2replay_write_test', '*.pcap*', '.env*')
REFUSE_DIRS = ('runtime', 'builder_src')
SKIP_NAMES = ('*.pyc', '*.bak_*', '*.tmp', 'desktop.ini', 'thumbs.db')
SKIP_DIRS = ('__pycache__',)


def release_name(version=VERSION):
    return 'bc2-local-replay-' + version


def sha256(path):
    h = hashlib.sha256()
    with open(path, 'rb') as f:
        for chunk in iter(lambda: f.read(1 << 20), b''):
            h.update(chunk)
    return h.hexdigest()


def guard(rel):
    """None (keep), ('skip', reason) or ('refuse', reason) for one relative posix path."""
    parts = [p.lower() for p in PurePosixPath(rel).parts]
    name, dirs = parts[-1], parts[:-1]
    for d in dirs:
        if d in SKIP_DIRS:
            return 'skip', d + ' directory'
    for pat in SKIP_NAMES:
        if fnmatch.fnmatchcase(name, pat):
            return 'skip', 'matches ' + pat
    for d in dirs:
        if d in REFUSE_DIRS:
            return 'refuse', d + ' directory component'
    for pat in REFUSE_NAMES:
        if fnmatch.fnmatchcase(name, pat):
            return 'refuse', 'matches ' + pat
    return None


def _tree(src, prefix):
    """(source file, relative posix path) for every file under src, sorted."""
    src = Path(src)
    out = []
    for p in sorted(src.rglob('*')):
        if p.is_file():
            out.append((p, (PurePosixPath(prefix) / p.relative_to(src).as_posix()).as_posix()))
    return out


def default_run(args, cwd, timeout):
    """Run args; return (returncode, stdout text)."""
    r = subprocess.run(args, cwd=cwd, timeout=timeout, capture_output=True, text=True,
                       encoding='utf-8', errors='replace', stdin=subprocess.DEVNULL)
    return r.returncode, r.stdout


def powershell():
    """pwsh when installed (PowerShell 7), else Windows PowerShell."""
    return shutil.which('pwsh') or 'powershell'


def build_builder(app_root, out_exe, run, say):
    """builder_go\\build.ps1 -Version VERSION -Out out_exe; None or a problem text."""
    script = Path(app_root) / GO_DIR / 'build.ps1'
    if not script.is_file():
        return 'no %s (pass --builder-exe)' % script
    args = [powershell(), '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', str(script),
            '-Version', VERSION, '-Out', str(out_exe)]
    try:
        rc, stdout = run(args, str(script.parent), BUILD_TIMEOUT)
    except Exception as exc:  # timeout, no powershell, ...
        return 'build.ps1 failed: %s' % exc
    if rc != 0 or not Path(out_exe).is_file():
        tail = (stdout or '').strip().splitlines()[-1:] or ['']
        return 'build.ps1 rc %s, no builder.exe (%s)' % (rc, tail[0][:120])
    say('OK built builder.exe with %s/build.ps1 -Version %s' % (GO_DIR, VERSION))
    return None


def builder_version(exe, run, cwd=None):
    """('version --json' dict, None) or (None, problem text)."""
    try:
        rc, stdout = run([str(exe), 'version', '--json'], str(cwd or Path(exe).parent), EXE_TIMEOUT)
    except Exception as exc:
        return None, 'builder.exe version --json failed: %s' % exc
    try:
        data = json.loads(stdout or '')
    except ValueError:
        data = None
    if rc != 0 or not isinstance(data, dict):
        return None, 'builder.exe version --json printed %r (rc %s)' % ((stdout or '').strip()[:80], rc)
    return data, None


def git_commit(app_root, run):
    """HEAD of the repo holding app_root, + '-dirty' when any tracked file has uncommitted changes (untracked files
    are ignored); 'unknown' without git."""
    try:
        rc, head = run(['git', 'rev-parse', 'HEAD'], str(app_root), EXE_TIMEOUT)
        if rc != 0 or not (head or '').strip():
            return 'unknown'
        rc2, dirty = run(['git', 'status', '--porcelain', '--untracked-files=no'], str(app_root), EXE_TIMEOUT)
        return head.strip() + ('-dirty' if rc2 == 0 and (dirty or '').strip() else '')
    except Exception:
        return 'unknown'


def builder_meta(exe, info, commit):
    """builder.json format 2."""
    return {'format': 2, 'runtime': 'go', 'go': info.get('go'), 'version': info.get('version'), 'commit': commit,
            'exe_sha256': sha256(exe)}


def collect(builder_exe, datapack, dist, proxy, app_root, skip_exe_version, run, say, work):
    """Run every check; return (files, problems). files = sorted [(src, rel)] after the content guard.
    builder_exe None = build it from builder_go into work; builder.json is written into work."""
    problems = []
    run = run or default_run
    datapack, dist, proxy, app_root, work = map(Path, (datapack, dist, proxy, app_root, work))

    # proxy
    kind = client_install.classify(proxy)
    ver = client_install.dll_version(proxy)
    if kind != 'shared':
        problems.append('proxy %s classifies %r, need shared' % (proxy, kind))
    elif ver != VERSION:
        problems.append('proxy %s build %s, need %s' % (proxy, ver or '(no version marker)', VERSION))
    else:
        say('OK proxy %s build %s sha %s' % (proxy, ver, sha256(proxy)[:8]))

    # builder: the Go builder.exe (built here or given) + a generated builder.json
    exe = None
    if builder_exe is None:
        exe = work / 'builder.exe'
        problem = build_builder(app_root, exe, run, say)
        if problem:
            problems.append(problem)
            exe = None
    elif not Path(builder_exe).is_file():
        problems.append('builder exe %s not found' % builder_exe)
    else:
        exe = Path(builder_exe)
    bjson = work / 'builder.json'
    if exe is not None:
        info, problem = builder_version(exe, run)
        if problem:
            problems.append(problem)
        elif info.get('engine') != 'go' or info.get('version') != VERSION:
            problems.append('builder.exe version --json says engine %r version %r, need go %s' % (
                info.get('engine'), info.get('version'), VERSION))
        else:
            meta = builder_meta(exe, info, git_commit(app_root, run))
            bjson.write_text(json.dumps(meta, indent=1) + '\n', encoding='utf-8', newline='\n')
            say('OK builder.exe version %s %s sha %s commit %s' % (meta['version'], meta['go'], meta['exe_sha256'][:8],
                                                                   meta['commit'][:12]))

    # datapack
    missing =[r for r in ('baselines/baselines.json', 'manifest.json') if not (datapack / r).is_file()]
    if missing:
        problems.append('datapack %s missing %s' % (datapack, ', '.join(missing)))

    # dist
    pexe = dist / EXE_NAME
    internal = dist / '_internal'
    dist_ok = pexe.is_file() and internal.is_dir()
    if not dist_ok:
        problems.append('dist %s needs %s and _internal\\' % (dist, EXE_NAME))

    # app files
    for rel in APP_FILES:
        if not (app_root / rel).is_file():
            problems.append('missing %s' % (app_root / rel))

    # gather + content guard
    files = []
    if pexe.is_file():
        files.append((pexe, EXE_NAME))
    if internal.is_dir():
        files += _tree(internal, '_internal')
    if proxy.is_file():
        files.append((proxy, 'proxy/dinput8.dll'))
    if exe is not None and bjson.is_file():
        files += [(exe, BUILDER_EXE), (bjson, BUILDER_JSON)]
    if datapack.is_dir():
        files += _tree(datapack, 'datapack')
    for rel in APP_FILES:
        if (app_root / rel).is_file():
            files.append((app_root / rel, rel))
    kept = []
    for src, rel in files:
        verdict = guard(rel)
        if verdict is None:
            kept.append((src, rel))
        elif verdict[0] == 'skip':
            say('SKIP %s %s' % (rel, verdict[1]))
        else:
            problems.append('%s %s (private/runtime content)' % (rel, verdict[1]))
    kept.sort(key=lambda t: t[1])
    if not missing and datapack.is_dir():
        say('OK datapack %d files' % sum(1 for _, r in kept if r.startswith('datapack/')))

    # exe --version last, and only when everything else passed (never run a build we will refuse anyway)
    if dist_ok:
        if skip_exe_version:
            say('WARN exe --version check skipped (--skip-exe-version)')
        elif problems:
            say('WARN exe --version not run: earlier checks failed')
        else:
            try:
                rc, stdout = run([str(pexe), '--version'], str(dist), EXE_TIMEOUT)
                got = (stdout or '').strip()
                if rc != 0 or got != VERSION:
                    problems.append('exe --version printed %r (rc %s), need %s' % (got[:80], rc, VERSION))
                else:
                    say('OK exe version %s' % got)
            except Exception as exc:  # timeout, missing exe, ...
                problems.append('exe --version failed: %s' % exc)
    return kept, problems


def write_release(files, out, say):
    name = release_name()
    out = Path(out)
    stage = out / name
    zpath = out / (name + '.zip')
    if stage.exists():
        shutil.rmtree(stage)
    stage.mkdir(parents=True)
    lines = []
    for src, rel in files:
        dst = stage / rel
        dst.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(src, dst)
        lines.append('%s  %s' % (sha256(dst), rel))
    (stage / SUMS_NAME).write_text('\n'.join(sorted(lines, key=lambda s: s[66:])) + '\n', encoding='ascii',
                                   newline='\n')
    entries = sorted([rel for _, rel in files] + [SUMS_NAME])
    tmp = zpath.with_name(zpath.name + '.partial')
    with zipfile.ZipFile(tmp, 'w', zipfile.ZIP_DEFLATED) as z:
        for rel in entries:
            z.write(stage / rel, name + '/' + rel)
    tmp.replace(zpath)
    digest = sha256(zpath)
    Path(str(zpath) + '.sha256').write_text('%s  %s\n' % (digest, zpath.name), encoding='ascii', newline='\n')
    return zpath, zpath.stat().st_size, len(entries), digest


def pack(builder_exe, datapack, dist=None, proxy=None, out=None, skip_exe_version=False, dry_run=False, run=None,
         app_root=None, say=print):
    app_root = Path(app_root or APP_ROOT)
    dist = Path(dist) if dist else app_root / 'dist' / 'onedir' / 'BC2LocalReplay'
    proxy = Path(proxy) if proxy else app_root / 'rome_proxy' / 'out_shared' / 'dinput8.dll'
    out = Path(out) if out else app_root / 'dist' / 'release'
    work = Path(tempfile.mkdtemp(prefix='release_pack_'))
    try:
        files, problems = collect(builder_exe, datapack, dist, proxy, app_root, skip_exe_version, run, say, work)
        if problems:
            for p in problems:
                say('REFUSED ' + p)
            say('REFUSED %d check(s) failed; nothing written' % len(problems))
            return 1
        name = release_name()
        if dry_run:
            for _, rel in files:
                say('FILE %s/%s' % (name, rel))
            say('RESULT dry-run %s %d files (+%s); nothing written' % (out / (name + '.zip'), len(files), SUMS_NAME))
            return 0
        zpath, size, n, digest = write_release(files, out, say)
    finally:
        shutil.rmtree(work, ignore_errors=True)
    say('OK zip sha256 %s' % digest)
    say('RESULT %s %d B %d files' % (zpath, size, n))
    return 0


def check_builder(z, present, run, say):
    """builder/ holds exactly builder.exe + builder.json (format 2), no Python builder anywhere; the exe sha matches
    builder.json and 'version --json' answers with its version. Returns the problem count."""
    bad = 0
    for rel in sorted(present):
        name = PurePosixPath(rel).name.lower()
        if rel.startswith('builder/') and rel not in BUILDER_FILES:
            say('UNEXPECTED %s (the builder folder holds only builder.exe + builder.json)' % rel)
            bad += 1
        elif rel.startswith('builder_src/') or (name.startswith('python') and name.endswith('.exe')):
            say('UNEXPECTED %s (no Python builder in the release)' % rel)
            bad += 1
    for rel in BUILDER_FILES:
        if rel not in present:
            say('MISSING %s' % rel)
            return bad + 1
    try:
        meta = json.loads(z.read(present[BUILDER_JSON]).decode('utf-8'))
        if not isinstance(meta, dict):
            raise ValueError('not an object')
    except ValueError as exc:
        say('BAD %s: %s' % (BUILDER_JSON, exc))
        return bad + 1
    if meta.get('format') != 2 or meta.get('runtime') != 'go':
        say('BAD %s: format %r runtime %r, need 2 / go' % (BUILDER_JSON, meta.get('format'), meta.get('runtime')))
        bad += 1
    tmp = Path(tempfile.mkdtemp(prefix='release_check_'))
    try:
        exe = tmp / 'builder.exe'
        with z.open(present[BUILDER_EXE]) as src, open(exe, 'wb') as dst:
            shutil.copyfileobj(src, dst)
        got = sha256(exe)
        if got != meta.get('exe_sha256'):
            say('MISMATCH %s sha %s, builder.json says %s' % (BUILDER_EXE, got, meta.get('exe_sha256')))
            bad += 1
        else:
            say('OK %s sha %s = builder.json' % (BUILDER_EXE, got[:8]))
        info, problem = builder_version(exe, run or default_run, tmp)
        if problem:
            say('BAD ' + problem)
            bad += 1
        elif info.get('version') != meta.get('version') or info.get('engine') != 'go':
            say('MISMATCH builder.exe version --json %r/%r, builder.json version %r' % (
                info.get('engine'), info.get('version'), meta.get('version')))
            bad += 1
        else:
            say('OK builder.exe version --json: %s %s' % (info.get('version'), info.get('go')))
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    return bad


def check(zpath, say=print, run=None):
    zpath = Path(zpath)
    bad = 0
    try:
        z = zipfile.ZipFile(zpath)
    except (OSError, zipfile.BadZipFile) as exc:
        say('REFUSED cannot open %s: %s' % (zpath, exc))
        return 1
    with z:
        names = [n for n in z.namelist() if not n.endswith('/')]
        tops = {n.split('/', 1)[0] for n in z.namelist()}
        if len(tops) != 1 or any('/' not in n for n in names):
            say('REFUSED need exactly one top-level folder, found %s' % sorted(tops))
            return 1
        top = tops.pop()
        sums_entry = top + '/' + SUMS_NAME
        if sums_entry not in names:
            say('REFUSED %s missing' % sums_entry)
            return 1
        listed = {}
        for line in z.read(sums_entry).decode('ascii', 'replace').splitlines():
            if line.strip():
                digest, _, rel = line.partition('  ')
                listed[rel] = digest.strip().lower()
        present = {n[len(top) + 1:]: n for n in names if n != sums_entry}
        for rel in sorted(set(listed) | set(present)):
            if rel not in present:
                say('MISSING %s' % rel)
                bad += 1
            elif rel not in listed:
                say('EXTRA %s' % rel)
                bad += 1
            else:
                h = hashlib.sha256()
                with z.open(present[rel]) as f:
                    for chunk in iter(lambda: f.read(1 << 20), b''):
                        h.update(chunk)
                if h.hexdigest() != listed[rel]:
                    say('MISMATCH %s' % rel)
                    bad += 1
        bad += check_builder(z, present, run, say)
    digest = sha256(zpath)
    say('zip sha256 %s' % digest)
    side = Path(str(zpath) + '.sha256')
    if side.is_file():
        want = side.read_text(encoding='ascii', errors='replace').split()
        if want and want[0].lower() == digest:
            say('OK %s matches' % side.name)
        else:
            say('MISMATCH %s says %s' % (side.name, want[0] if want else '(empty)'))
            bad += 1
    else:
        say('WARN no %s next to the zip' % side.name)
    if bad:
        say('REFUSED %d problem(s)' % bad)
        return 1
    say('OK %d files verified' % len(listed))
    return 0


def main(argv=None, run=None, say=print):
    ap = argparse.ArgumentParser(prog='release_pack.py', description='Assemble or verify the BC2 Local Replay '
                                 'release zip (version %s).' % VERSION)
    ap.add_argument('--builder-exe', help='prebuilt Go builder.exe (default: build it with builder_go\\build.ps1)')
    ap.add_argument('--datapack', default=str(APP_ROOT / 'datapack'),
                    help='datapack folder (manifest.json + baselines/baselines.json; default: datapack\\ here)')
    ap.add_argument('--dist', help='PyInstaller onedir output (default dist\\onedir\\BC2LocalReplay)')
    ap.add_argument('--proxy', help='shared proxy dll (default rome_proxy\\out_shared\\dinput8.dll)')
    ap.add_argument('--out', help='output folder (default dist\\release)')
    ap.add_argument('--skip-exe-version', action='store_true', help='do not run BC2LocalReplay.exe --version')
    ap.add_argument('--dry-run', action='store_true', help='run every check, print the file list, write nothing')
    ap.add_argument('--check', metavar='ZIP', help='verify a release zip against its SHA256.txt and builder.json')
    a = ap.parse_args(argv)
    if a.check:
        return check(a.check, say=say, run=run)
    return pack(a.builder_exe, a.datapack, a.dist, a.proxy, a.out, a.skip_exe_version, a.dry_run, run=run,
                app_root=APP_ROOT, say=say)


if __name__ == '__main__':
    sys.exit(main())
