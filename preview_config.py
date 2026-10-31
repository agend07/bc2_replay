"""Portable preview configuration. Reading config never starts a process."""
import hashlib
import json
from pathlib import Path
import sys

def get_app_root():
    if getattr(sys, 'frozen', False):
        return Path(sys.executable).resolve().parent
    return Path(__file__).resolve().parent

APP_ROOT = get_app_root()
# r266: one release version shared with rome_proxy\version.h (BC2_VERSION_STRING) and the builder's
# builder.json "version"; shown in the window title, `preview.py --version`, check-setup and the release zip name.
VERSION = '0.1.0-preview.5'
CONFIG = APP_ROOT / 'preview.json'
# Intermediate server build written by releases up to 0.1.0-preview.4; now derived in memory only
# (server_build.build). Nothing requires it; Clean up still deletes a leftover copy.
LEGACY_TRACE_NAME = 'Frost.Game.Main_Win32_Final.PlaybackOnlineTrace.exe'
TIMELINE_NAME = 'Frost.Game.Main_Win32_Final.ReplayTimeline.exe'
ORIGINAL_HASH = 'ca8169fa03a29e02af1bf8db8770ab0e68f13a95b010c52eca23733068298347'
TRACE_HASH = 'b95bdeaaee6994f838a433efa5ff037ce76a24c403eae488c8f8a22d764fe99e'
TIMELINE_HASH = '035eb1939fccffb9d5b79d685cb1d764cf2645f294e4ab90f5f73ad71e41faa6'  # r233 speed (mailbox+0x38); pre_r233 (r225): fba3efcc...; pre_r225: ccf9420e...
CLIENT_HASH = '3911fcc8914b0158c434cee36f19295316e715d4c9a0e70c857e8c39e3d54258'
ORIGINAL_NAME = 'Frost.Game.Main_Win32_Final.exe'


def settings():
    if not CONFIG.is_file():
        raise RuntimeError('Open the controls window and save your paths in Setup / Launcher first.')
    data = json.loads(CONFIG.read_text(encoding='utf-8'))
    for key in ('server_dir', 'client_exe'):
        value = data.get(key)
        if not isinstance(value, str) or not value.strip():
            raise RuntimeError('Set '+key+' in preview.json')
        if not Path(value).is_absolute():
            raise RuntimeError(key+' must be an absolute path')
    return data


def raw_config():
    """Raw preview.json as a dict (not settings()): missing, unreadable or non-dict -> {}. Never raises.
    A half-configured install still gets a partial report / plan."""
    try:
        cfg = json.loads(CONFIG.read_text(encoding='utf-8')) if CONFIG.is_file() else {}
    except (OSError, ValueError):
        cfg = {}
    return cfg if isinstance(cfg, dict) else {}


def verify(path, expected):
    """Raise unless path exists and its SHA-256 equals expected."""
    if not path.is_file(): raise RuntimeError('Missing file: '+str(path))
    if hashlib.sha256(path.read_bytes()).hexdigest()!=expected:
        raise RuntimeError('Unsupported or changed executable: '+str(path))


def save_settings(updates):
    """Merge updates into preview.json (missing file -> {}), written via .json.tmp + replace."""
    current = json.loads(CONFIG.read_text(encoding='utf-8')) if CONFIG.exists() else {}
    current.update(updates)
    temp = CONFIG.with_suffix('.json.tmp')
    temp.write_text(json.dumps(current, indent=2) + '\n', encoding='utf-8')
    temp.replace(CONFIG)
    return current


def server_dir(): return Path(settings()['server_dir']).resolve()
def client_exe(): return Path(settings()['client_exe']).resolve()
def timeline_exe(): return server_dir()/TIMELINE_NAME


def runtime_dir():
    path = APP_ROOT/'runtime'
    path.mkdir(exist_ok=True)
    return path


COMPANION_EXTENSIONS = ('.fbgd', '.fbgk', '.fbgdd', '.fbgdk',
                        '.fbidd', '.fbidk', '.fbcdd', '.fbcdk', '.txt')


def companions_present(base):
    """A recording is only usable when every companion file exists."""
    return all(Path(str(base) + ext).is_file() for ext in COMPANION_EXTENSIONS)


def missing_companions(base):
    return [str(base) + ext for ext in COMPANION_EXTENSIONS
            if not Path(str(base) + ext).is_file()]


def available_recordings():
    """Validated recordings under server_dir/demos, sorted by name.

    Returns relative 'demos/<name>' bases so a fresh installation can pick any
    recording instead of requiring one hard-coded sample.
    """
    try:
        srv = server_dir()
    except RuntimeError:
        return []
    demos = srv / 'demos'
    if not demos.is_dir():
        return []
    found = []
    for txt in sorted(demos.glob('*.txt')):
        base = 'demos/' + txt.stem
        if companions_present(srv / base):
            found.append(base)
    return found


def recording():
    data = settings()
    value = data.get('recording_base')
    if value is None:
        # No explicit choice: accept any complete recording instead of forcing
        # the bundled sample that may not exist in this installation.
        found = available_recordings()
        if not found:
            raise RuntimeError('No complete recording found in server_dir/demos. '
                               'Download a recording set first.')
        value = found[0]
    if not isinstance(value,str) or not value or '\x00' in value:
        raise RuntimeError('Invalid recording_base')
    path = Path(value)
    return path if path.is_absolute() else server_dir()/path


def recording_argument():
    # Frostbite VFS rejects Windows drive-qualified recording paths.
    try: return recording().resolve().relative_to(server_dir()).as_posix()
    except ValueError as exc:
        raise RuntimeError('Place the recording inside server_dir (normally demos/).') from exc


def level():
    value = settings().get('level','Levels/MP_006CQ')
    if not isinstance(value, str) or not value.startswith('Levels/') or any(c in value for c in '\r\n"'):
        raise RuntimeError('Unsupported level path in preview.json.')
    return value
