"""r264 (R3): install / upgrade / uninstall the shared dinput8.dll proxy in the GAME folder.

Project Rome ships its own dinput8.dll beside BFBC2Game.exe. The proxy renames it to
dinput8_rome.dll and takes its place; uninstall copies Rome back over dinput8.dll, verifies the
bytes and only then deletes dinput8_rome.dll. The old bc2_mute msimg32.dll must never coexist
with the proxy (same chat commands).

DLLs are classified by byte markers only (Rome builds differ between users, so never sha-pinned).
Planning is pure; apply() runs the ops directly and apply_elevated() runs the same ops through a
self-elevating PowerShell script when the game folder (usually under Program Files) is not
writable. install()/uninstall() always re-check status() afterwards instead of trusting exit codes.
"""
import datetime
import hashlib
import os
import shutil
import struct
import subprocess
from pathlib import Path

import preview_config

DLL_NAME = 'dinput8.dll'
ROME_DLL_NAME = 'dinput8_rome.dll'
BACKUP_NAME = 'dinput8.dll.pre_proxy'
MUTE_DLL_NAME = 'msimg32.dll'
INSTALL_NOTE = ('msimg32.dll (bc2-chat-smoke or the older bc2_mute) is in the game folder. Remove it first: the proxy '
                'contains the same chat commands, and both together must never be loaded.')

# Identity of the shared build (rome_proxy\version.h BC2_SHARED_MARKER; the literal dates from r231
# builds, so already installed proxies still classify as 'shared'). The plain proxy never had it.
SHARED_MARKER = b'BC2_REPLAY_PATCH'
PROXY_MARKERS = (b'dinput8_rome.dll', b'BC2_ROME_HOST')
ROME_MARKER = b'bfbc2.emulatornexus.com'
# r266: the shared build embeds "BC2_PROXY_VERSION=<version>" + NUL (rome_proxy\version.h BC2_VERSION_MARKER).
VERSION_MARKER = b'BC2_PROXY_VERSION='
ROME_EXPORT = 'DirectInput8Create'
WRITE_PROBE = '.bc2replay_write_test'
STATES = ('no-folder', 'missing', 'rome', 'installed', 'upgrade', 'broken', 'mute', 'unknown')


# ---------------------------------------------------------------- classification

def exports(path):
    """Exported names of a PE32/PE32+ file; empty set on any malformed input. Never raises."""
    try:
        data = Path(path).read_bytes()
        if data[:2] != b'MZ':
            return set()
        pe = struct.unpack_from('<I', data, 0x3C)[0]
        if data[pe:pe + 4] != b'PE\0\0':
            return set()
        nsect, = struct.unpack_from('<H', data, pe + 6)
        opt_size, = struct.unpack_from('<H', data, pe + 20)
        opt = pe + 24
        magic, = struct.unpack_from('<H', data, opt)
        if magic == 0x10B:
            count_off, dir_off = 92, 96
        elif magic == 0x20B:
            count_off, dir_off = 108, 112
        else:
            return set()
        if opt_size < dir_off + 8 or struct.unpack_from('<I', data, opt + count_off)[0] < 1:
            return set()
        exp_rva, exp_size = struct.unpack_from('<II', data, opt + dir_off)
        if not exp_rva:
            return set()
        sections = []
        sect = opt + opt_size
        for i in range(min(nsect, 96)):
            vsize, va, rsize, rptr = struct.unpack_from('<IIII', data, sect + 40 * i + 8)
            sections.append((va, max(vsize, rsize), rsize, rptr))

        def offset(rva):
            for va, span, rsize, rptr in sections:
                if va <= rva < va + span and rva - va < rsize:
                    return rptr + rva - va
            raise ValueError('rva outside sections')

        exp = offset(exp_rva)
        nnames, = struct.unpack_from('<I', data, exp + 24)
        names_rva, = struct.unpack_from('<I', data, exp + 32)
        if not nnames or not names_rva:
            return set()
        table = offset(names_rva)
        names = set()
        for i in range(min(nnames, 65536)):
            start = offset(struct.unpack_from('<I', data, table + 4 * i)[0])
            end = data.find(b'\0', start, start + 512)
            if end < 0:
                return set()
            names.add(data[start:end].decode('ascii'))
        return names
    except Exception:
        return set()


def dll_version(source):
    """Version string embedded in a proxy dll (bytes or path): the text after VERSION_MARKER up to NUL,
    or None (older builds, Rome, unreadable). Never raises."""
    data = source
    if not isinstance(source, (bytes, bytearray)):
        try:
            data = Path(source).read_bytes()
        except (OSError, TypeError, ValueError):
            return None
    pos = data.find(VERSION_MARKER)
    if pos < 0:
        return None
    start = pos + len(VERSION_MARKER)
    end = data.find(b'\0', start, start + 64)
    text = data[start:end if end >= 0 else start + 64].decode('ascii', 'replace').strip()
    return text or None


def classify(path, exports=exports):
    """'missing' | 'shared' | 'proxy' | 'rome' | 'unknown' for one file. Never raises."""
    path = Path(path)
    try:
        if not path.is_file():
            return 'missing'
        data = path.read_bytes()
    except OSError:
        return 'unknown'
    if data[:2] != b'MZ':
        return 'unknown'
    if SHARED_MARKER in data:
        return 'shared'
    if all(m in data for m in PROXY_MARKERS):
        return 'proxy'
    if ROME_MARKER in data and PROXY_MARKERS[0] not in data:
        try:
            if ROME_EXPORT in exports(path):
                return 'rome'
        except Exception:
            pass
    return 'unknown'


# ---------------------------------------------------------------- paths

def proxy_source(app_root=None):
    """First existing of <app>/proxy/dinput8.dll (release) and <app>/rome_proxy/out_shared/dinput8.dll."""
    root = Path(app_root or preview_config.APP_ROOT)
    candidates = (root / 'proxy' / DLL_NAME, root / 'rome_proxy' / 'out_shared' / DLL_NAME)
    for candidate in candidates:
        if candidate.is_file():
            return candidate
    return candidates[0]


def game_dir_of(client_exe):
    return Path(client_exe).parent


def _paths(game_dir):
    g = Path(game_dir)
    return g / DLL_NAME, g / ROME_DLL_NAME, g / BACKUP_NAME, g / MUTE_DLL_NAME


def _sha(path):
    try:
        return hashlib.sha256(Path(path).read_bytes()).hexdigest()
    except OSError:
        return None


def _read(path):
    try:
        return Path(path).read_bytes()
    except OSError:
        return None


def _is_file(path):
    try:
        return Path(path).is_file()
    except OSError:
        return False


# ---------------------------------------------------------------- status

def status(game_dir, source=None, exports=exports):
    """State of the game folder for the panel. Never raises."""
    source = Path(source) if source is not None else proxy_source()
    source_ok = classify(source, exports) == 'shared'
    result = {'state': 'no-folder', 'dll': 'missing', 'chain_ok': False, 'rome_present': False,
              'msimg32': False, 'backup': False, 'sha256': None, 'size': None, 'source': str(source),
              'source_ok': source_ok, 'source_sha256': _sha(source) if _is_file(source) else None,
              'version': None, 'source_version': dll_version(source), 'text': ''}
    try:
        folder_ok = Path(game_dir).is_dir()
    except OSError:
        folder_ok = False
    if not folder_ok:
        result['text'] = 'Game folder not found: ' + str(game_dir) + '.'
        return result
    dll, rome_dll, backup, mute = _paths(game_dir)
    kind = classify(dll, exports)
    rome_present = _is_file(rome_dll)
    chain_ok = rome_present and classify(rome_dll, exports) == 'rome'
    result.update(dll=kind, chain_ok=chain_ok, rome_present=rome_present, msimg32=_is_file(mute),
                  backup=_is_file(backup))
    if kind != 'missing':
        data = _read(dll)
        if data is not None:
            result['sha256'] = hashlib.sha256(data).hexdigest()
            result['size'] = len(data)
            if kind in ('shared', 'proxy'):
                result['version'] = dll_version(data)
    build = ('build ' + result['version'] + ', ') if result['version'] else ''
    sha8 = (result['sha256'] or '?')[:8]
    if result['msimg32']:
        state, text = 'mute', 'msimg32.dll present: remove it first.'
    elif kind == 'missing':
        state, text = 'missing', 'No dinput8.dll in the game folder: install Project Rome first.'
    elif kind == 'unknown':
        state, text = 'unknown', "dinput8.dll is not Project Rome or this project's proxy; not touched."
    elif rome_present and not chain_ok:
        state, text = 'broken', 'dinput8_rome.dll is not Project Rome; the proxy chain is broken. Restore it by hand.'
    elif kind in ('shared', 'proxy') and not rome_present:
        state, text = 'broken', ("Proxy installed but Rome's dinput8_rome.dll is missing; restore it by hand "
                                 'from your Project Rome download.')
    elif kind == 'rome' and rome_present:
        state, text = 'broken', 'dinput8.dll is Project Rome but a leftover dinput8_rome.dll is present; run uninstall.'
    elif kind == 'rome':
        state, text = 'rome', 'Project Rome dinput8.dll found; proxy not installed.'
    elif kind == 'shared' and (not source_ok or _read(dll) == _read(source)):
        state = 'installed'
        text = ('Client proxy installed (' + build + 'sha ' + sha8 + ', ' + str(result['size']) + ' B), Rome chain ok.')
    else:
        state = 'upgrade'
        old = 'Older proxy build' if kind == 'proxy' else 'Older shared proxy build'
        text = old + ' installed (' + build + 'sha ' + sha8 + '); upgrade available.'
    result['state'], result['text'] = state, text
    return result


# ---------------------------------------------------------------- planning

def _no_game_running():
    from replay.client import client_pids
    if client_pids():
        raise RuntimeError('Close Battlefield: Bad Company 2 first (BFBC2Game.exe is running).')


def plan_install(game_dir, source=None, check_processes=None, exports=exports):
    """Ops installing/upgrading the shared proxy; [] when already installed. RuntimeError on refusal."""
    source = Path(source) if source is not None else proxy_source()
    if not _is_file(source):
        raise RuntimeError('Build the shared proxy first: rome_proxy\\build_shared.cmd (missing ' + str(source) + ')')
    if classify(source, exports) != 'shared':
        raise RuntimeError(str(source) + ' is not the shared proxy build.')
    st = status(game_dir, source, exports)
    state = st['state']
    if state == 'no-folder':
        raise RuntimeError('Game folder not found: ' + str(game_dir))
    if state == 'mute':
        raise RuntimeError(INSTALL_NOTE)
    if state in ('missing', 'unknown', 'broken'):
        raise RuntimeError(st['text'])
    if state == 'installed':
        return []
    dll, rome_dll, backup, _ = _paths(game_dir)
    if state == 'rome':
        ops = [('rename', dll, rome_dll), ('copy', source, dll)]
    elif state == 'upgrade':
        ops = [('copy', dll, backup), ('copy', source, dll)]
    else:
        raise RuntimeError('Unexpected state ' + state + '; nothing changed.')
    (check_processes or _no_game_running)()
    return ops


def plan_uninstall(game_dir, check_processes=None, exports=exports):
    """Ops restoring Rome as dinput8.dll; [] when nothing to restore. RuntimeError on refusal."""
    try:
        folder_ok = Path(game_dir).is_dir()
    except OSError:
        folder_ok = False
    if not folder_ok:
        raise RuntimeError('Game folder not found: ' + str(game_dir))
    dll, rome_dll, backup, _ = _paths(game_dir)
    kind = classify(dll, exports)
    if _is_file(rome_dll):
        if classify(rome_dll, exports) != 'rome':
            raise RuntimeError('dinput8_rome.dll is not Project Rome; nothing changed. Restore it by hand.')
        if kind == 'unknown':
            raise RuntimeError("dinput8.dll is not Project Rome or this project's proxy; not overwritten.")
        ops = [('copy', rome_dll, dll), ('verify', dll, rome_dll), ('delete', rome_dll)]
        if _is_file(backup):
            ops.append(('delete', backup))
    elif kind == 'rome':
        return []
    elif kind in ('shared', 'proxy'):
        raise RuntimeError("Rome's dinput8_rome.dll is missing; restore it by hand from your Project Rome download.")
    elif kind == 'missing':
        raise RuntimeError('No dinput8.dll or dinput8_rome.dll in the game folder; nothing to restore.')
    else:
        raise RuntimeError("dinput8.dll is not Project Rome or this project's proxy; not touched.")
    (check_processes or _no_game_running)()
    return ops


# ---------------------------------------------------------------- execution

def apply(ops):
    """Run ops in order in this process. PermissionError passes through unchanged."""
    for op in ops:
        kind = op[0]
        if kind == 'copy':
            src, dst = Path(op[1]), Path(op[2])
            tmp = dst.with_name(dst.name + '.tmp')
            try:
                shutil.copy2(src, tmp)
                os.replace(tmp, dst)
            except BaseException:
                try:
                    tmp.unlink()
                except OSError:
                    pass
                raise
        elif kind == 'rename':
            src, dst = Path(op[1]), Path(op[2])
            if dst.exists():
                raise RuntimeError('Refusing to rename ' + src.name + ': ' + str(dst) + ' already exists.')
            os.replace(src, dst)
        elif kind == 'verify':
            if Path(op[1]).read_bytes() != Path(op[2]).read_bytes():
                raise RuntimeError('Verification failed: ' + str(op[1]) + ' differs from ' + str(op[2]) + '.')
        elif kind == 'delete':
            Path(op[1]).unlink()
        else:
            raise RuntimeError('Unknown op ' + repr(kind))


def writable(game_dir):
    probe = Path(game_dir) / WRITE_PROBE
    try:
        with open(probe, 'w') as f:
            f.write('x')
        probe.unlink()
        return True
    except OSError:
        return False


def _q(path):
    """PowerShell single-quoted literal of an absolute path (embedded quotes doubled)."""
    text = str(Path(path).absolute())
    for ch in ("'", '‘', '’', '‚', '‛'):
        text = text.replace(ch, ch * 2)
    return "'" + text + "'"


_HEADER = r"""$ErrorActionPreference = 'Stop'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    $arguments = @('-NoProfile','-ExecutionPolicy','Bypass','-File',('"'+$PSCommandPath+'"'))
    try { $child = Start-Process powershell.exe -Verb RunAs -ArgumentList $arguments -Wait -PassThru }
    catch { exit 1223 }
    exit $child.ExitCode
}
try {
"""

_FOOTER = """    exit 0
} catch {
    [Console]::Error.WriteLine($_)
    exit 1
}
"""


def script_text(ops):
    """Self-elevating PowerShell script performing ops exactly like apply()."""
    lines = []
    for op in ops:
        kind = op[0]
        if kind == 'copy':
            src, dst = Path(op[1]), Path(op[2])
            tmp = dst.with_name(dst.name + '.tmp')
            lines.append('Copy-Item -LiteralPath ' + _q(src) + ' -Destination ' + _q(tmp) + ' -Force')
            lines.append('Move-Item -LiteralPath ' + _q(tmp) + ' -Destination ' + _q(dst) + ' -Force')
        elif kind == 'rename':
            src, dst = Path(op[1]), Path(op[2])
            if src.parent != dst.parent:
                raise RuntimeError('rename must stay in one folder: ' + str(src) + ' -> ' + str(dst))
            lines.append('if (Test-Path -LiteralPath ' + _q(dst) + ") { throw 'Refusing to rename: target exists.' }")
            name = dst.name.replace("'", "''")
            lines.append('Rename-Item -LiteralPath ' + _q(src) + " -NewName '" + name + "'")
        elif kind == 'verify':
            lines.append('if ((Get-FileHash -LiteralPath ' + _q(op[1]) + ' -Algorithm SHA256).Hash -ne '
                         '(Get-FileHash -LiteralPath ' + _q(op[2]) + " -Algorithm SHA256).Hash) "
                         "{ throw 'Verification failed: copied file differs.' }")
        elif kind == 'delete':
            lines.append('Remove-Item -LiteralPath ' + _q(op[1]) + ' -Force')
        else:
            raise RuntimeError('Unknown op ' + repr(kind))
    return _HEADER + ''.join('    ' + line + '\n' for line in lines) + _FOOTER


def _run_powershell(args):
    return subprocess.run(args).returncode


def apply_elevated(ops, runtime_dir, run=None, stamp=None):
    """Write <runtime_dir>/client_install_<stamp>.private.ps1 and run it (UAC prompt). Returns the script path."""
    stamp = stamp or datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%d-%H%M%S-%f')
    folder = Path(runtime_dir)
    folder.mkdir(parents=True, exist_ok=True)
    script = folder / ('client_install_' + stamp + '.private.ps1')
    script.write_text(script_text(ops), encoding='utf-8-sig', newline='\r\n')
    args = ['powershell.exe', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', str(script)]
    code = (run or _run_powershell)(args)
    if code:
        raise RuntimeError('Elevation was cancelled or the elevated step failed (code ' + str(code) +
                           '). Nothing verified; check the folder.')
    return script


def _execute(ops, game_dir, runtime_dir, run):
    if writable(game_dir):
        apply(ops)
        return False
    apply_elevated(ops, runtime_dir if runtime_dir is not None else preview_config.runtime_dir(), run)
    return True


def install(game_dir, source=None, check_processes=None, runtime_dir=None, run=None, exports=exports):
    source = Path(source) if source is not None else proxy_source()
    ops = plan_install(game_dir, source, check_processes, exports)
    if not ops:
        st = status(game_dir, source, exports)
        return {'installed': False, 'reason': 'shared proxy already installed', 'sha256': st['sha256'],
                'status': st}
    elevated = _execute(ops, game_dir, runtime_dir, run)
    st = status(game_dir, source, exports)
    if st['state'] != 'installed':
        raise RuntimeError('Install finished but the folder state is ' + st['state'] + ': ' + st['text'])
    backup = _paths(game_dir)[2]
    wrote_backup = any(op[0] == 'copy' and Path(op[2]) == backup for op in ops)
    return {'installed': True, 'elevated': elevated, 'sha256': st['sha256'],
            'backup': str(backup) if wrote_backup else None, 'status': st}


def _restored(st):
    return st['state'] != 'no-folder' and st['dll'] == 'rome' and not st['rome_present']


def uninstall(game_dir, source=None, check_processes=None, runtime_dir=None, run=None, exports=exports):
    ops = plan_uninstall(game_dir, check_processes, exports)
    if not ops:
        return {'restored': False, 'elevated': False, 'reason': 'nothing to restore',
                'status': status(game_dir, source, exports)}
    elevated = _execute(ops, game_dir, runtime_dir, run)
    st = status(game_dir, source, exports)
    # msimg32.dll makes the state 'mute' regardless; the restore itself is judged structurally.
    if not _restored(st) or st['backup']:
        raise RuntimeError('Uninstall finished but the folder state is ' + st['state'] + ': ' + st['text'])
    return {'restored': True, 'elevated': elevated, 'status': st}
