"""Player list of a recording (demos/<stem>.players.json) and builder resolution / child environment.

The player list is demos/<stem>.players.json next to the selected recording (older builds wrote it; the current
builder does not): player names, the recording viewer and the game start frame, read when present. The builder
(used by replay/recordings.py) is the release
zip's <app root>\\builder\\builder.exe (the Go builder, source builder_go/), auto-detected, or preview.json
"pov_builder": {"exe": ...}; builder_env() is the child's environment (data pack, demos, work dirs).

Pure module (no tkinter / ctypes imports; preview_config only lazily for the app root) so it can be tested offline.
"""
import json
import os
import subprocess
from pathlib import Path


def stem_of(base):
    """'demos/cl04_1097' -> 'cl04_1097'."""
    return str(base).replace('\\', '/').rsplit('/', 1)[-1]


def _read(path):
    try:
        data = json.loads(Path(path).read_text(encoding='utf-8'))
    except (OSError, ValueError):
        return None
    if not isinstance(data, dict) or not isinstance(data.get('players'), list):
        return None
    return data


def load_players(server_dir, base):
    """Player list for recording base ('demos/<stem>') or None."""
    return _read(Path(server_dir) / 'demos' / (stem_of(base) + '.players.json'))


def mmss(seconds):
    if seconds is None:
        return 'end'
    total = max(0, int(round(seconds)))
    m, s = divmod(total, 60)
    return f'{m}:{s:02d}'


BUILDER_DIR = 'builder'      # release zip: <app root>\builder\ (builder.exe, builder.json)
BUILDER_EXE = 'builder.exe'  # the Go builder (builder_go/)
DATAPACK_DIR = 'datapack'    # release zip: <app root>\datapack\ (marker: baselines\baselines.json)
DATAPACK_MARKER = Path('baselines') / 'baselines.json'
MISSING_BUILDER = ('Builder folder not found: expected builder\\ with builder.exe next to '
                   'BC2LocalReplay.exe (or set pov_builder.exe in preview.json).')
VERSION_TIMEOUT_S = 10
MISSING_DATAPACK = 'Data pack not found: expected datapack\\baselines\\baselines.json next to the panel.'


def _app_root(app_root=None):
    if app_root is not None:
        return Path(app_root)
    import preview_config  # lazy: it only imports json/pathlib/sys
    return Path(preview_config.get_app_root())


def resolve_builder(cfg=None, app_root=None):
    """{'repo': Path or None, 'exe': Path or None, 'flat': bool, 'configured': bool}.
    preview.json pov_builder.exe (a builder.exe; repo = its folder) wins; otherwise <app root>/builder/builder.exe is
    auto-detected ('flat'). The caller runs [exe] + args. Never raises."""
    b = (cfg or {}).get('pov_builder') or {}
    if b.get('exe'):
        exe = Path(b['exe'])
        return {'repo': exe.parent, 'exe': exe, 'flat': False, 'configured': True}
    flat = _app_root(app_root) / BUILDER_DIR
    if (flat / BUILDER_EXE).is_file():
        return {'repo': flat, 'exe': flat / BUILDER_EXE, 'flat': True, 'configured': False}
    return {'repo': None, 'exe': None, 'flat': False, 'configured': False}


def _mkdir(path):
    try:
        Path(path).mkdir(parents=True, exist_ok=True)
    except OSError:
        pass  # the builder reports an unwritable work dir itself


def builder_env(cfg=None, app_root=None, environ=None):
    """Environment for a builder child: environ (default os.environ) plus
      PYTHONIOENCODING  = utf-8
      BC2_CLIENT_EXE    = cfg client_exe (when set)
      BC2_DEMOS_DIR     = <cfg server_dir>/demos (when set)
      BC2_DATAPACK_DIR  = pov_builder.datapack, else <app root>/datapack when datapack/baselines/baselines.json exists
      BC2_WORK_DIR      = pov_builder.work_dir, else <app root>/runtime/builder only for the auto-detected flat builder
      BC2_CACHE_DIR     = pov_builder.cache_dir, else <app root>/runtime/builder/cache only for the flat builder
    A configured pov_builder.exe keeps its own default work dir (no BC2_WORK_DIR / BC2_CACHE_DIR unless configured).
    The panel's values override the same keys in environ; the work / cache dirs are created."""
    cfg = cfg or {}
    b = cfg.get('pov_builder') or {}
    root = _app_root(app_root)
    env = dict(os.environ if environ is None else environ)
    env['PYTHONIOENCODING'] = 'utf-8'
    if cfg.get('client_exe'):
        env['BC2_CLIENT_EXE'] = str(Path(cfg['client_exe']))
    if cfg.get('server_dir'):
        env['BC2_DEMOS_DIR'] = str(Path(cfg['server_dir']) / 'demos')
    if b.get('datapack'):
        env['BC2_DATAPACK_DIR'] = str(Path(b['datapack']))
    elif (root / DATAPACK_DIR / DATAPACK_MARKER).is_file():
        env['BC2_DATAPACK_DIR'] = str(root / DATAPACK_DIR)
    flat = resolve_builder(cfg, root)['flat']
    work = b.get('work_dir') or (root / 'runtime' / 'builder' if flat else None)
    cache = b.get('cache_dir') or (root / 'runtime' / 'builder' / 'cache' if flat else None)
    for key, path in (('BC2_WORK_DIR', work), ('BC2_CACHE_DIR', cache)):
        if path:
            _mkdir(path)
            env[key] = str(Path(path))
    return env


_VERSION_CACHE = {}


def go_builder_version(exe, run=subprocess.run):
    """'version --json' of a builder.exe as a dict ({'name', 'engine', 'version', 'go', ...}) or None; cached per
    (path, size, mtime), short timeout, never raises."""
    exe = Path(exe)
    try:
        st = exe.stat()
    except OSError:
        return None
    key = (str(exe), st.st_size, st.st_mtime_ns)
    if key not in _VERSION_CACHE:
        data = None
        try:
            res = run([str(exe), 'version', '--json'], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                      stdin=subprocess.DEVNULL, text=True, encoding='utf-8', errors='replace',
                      timeout=VERSION_TIMEOUT_S, creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
            if res.returncode == 0:
                data = json.loads(res.stdout)
        except (OSError, ValueError, subprocess.SubprocessError):
            data = None
        _VERSION_CACHE[key] = data if isinstance(data, dict) else None
    return _VERSION_CACHE[key]


def _exe_versions(exe, run=subprocess.run):
    """' (Go builder 0.1.0, go1.27.3)' from 'builder.exe version --json', '' when it does not answer."""
    data = go_builder_version(exe, run) or {}
    parts = [str(data[k]).strip() for k in ('version', 'go')
             if isinstance(data.get(k), (str, int, float)) and str(data[k]).strip()]
    return f' (Go builder {", ".join(parts)})' if parts else ''


def builder_status(cfg=None, app_root=None, run=subprocess.run):
    """(ok, one-line text) about the builder for first-run messages (Setup summary, Recordings window)."""
    b = (cfg or {}).get('pov_builder') or {}
    root = _app_root(app_root)
    r = resolve_builder(cfg, root)
    if r['repo'] is None:
        return False, MISSING_BUILDER
    if r['configured']:
        if not r['exe'].is_file():
            return False, f'Builder not found: {r["exe"]} (preview.json pov_builder.exe)'
        return True, f'Builder: {r["exe"]}{_exe_versions(r["exe"], run)} (configured in preview.json)'
    if b.get('datapack'):
        if not (Path(b['datapack']) / DATAPACK_MARKER).is_file():
            return False, ('Data pack not found: no baselines\\baselines.json in '
                           f'{b["datapack"]} (preview.json pov_builder.datapack)')
        pack = 'data pack (preview.json)'
    elif (root / DATAPACK_DIR / DATAPACK_MARKER).is_file():
        pack = 'datapack\\'
    else:
        return False, MISSING_DATAPACK
    return True, f'Builder: builder\\{_exe_versions(r["exe"], run)} + {pack} found next to the panel.'
