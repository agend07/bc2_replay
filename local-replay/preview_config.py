"""Portable preview configuration. Reading config never starts a process."""
import json
from pathlib import Path
import sys

def get_app_root():
    if getattr(sys, 'frozen', False):
        return Path(sys.executable).resolve().parent
    return Path(__file__).resolve().parent

APP_ROOT = get_app_root()
CONFIG = APP_ROOT / 'preview.json'
SERVER_NAME = 'Frost.Game.Main_Win32_Final.PlaybackOnlineTrace.exe'
TIMELINE_NAME = 'Frost.Game.Main_Win32_Final.ReplayTimeline.exe'
ORIGINAL_HASH = 'ca8169fa03a29e02af1bf8db8770ab0e68f13a95b010c52eca23733068298347'
TRACE_HASH = 'b95bdeaaee6994f838a433efa5ff037ce76a24c403eae488c8f8a22d764fe99e'
TIMELINE_HASH = 'ccf9420ebbe64a782a08f9067fa8e234a2e0d0b9800e34047bd2472f338b7638'
CLIENT_HASH = '3911fcc8914b0158c434cee36f19295316e715d4c9a0e70c857e8c39e3d54258'


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


def server_dir(): return Path(settings()['server_dir']).resolve()
def client_exe(): return Path(settings()['client_exe']).resolve()
def trace_exe(): return server_dir()/SERVER_NAME
def timeline_exe(): return server_dir()/TIMELINE_NAME


def runtime_dir():
    path = APP_ROOT/'runtime'
    path.mkdir(exist_ok=True)
    return path


def recording():
    data = settings()
    value = data.get('recording_base', 'demos/patched_20261017_125023_912')
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
