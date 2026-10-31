"""First-run setup check (pure functions, no tkinter): client EXE, client proxy, server folder, builder,
%LOCALAPPDATA%\\BC2Replay. Used after the steps of the panel's Prepare button (run_prepare) and by `preview.py check-setup`.
Every check returns {'name', 'ok', 'text'}; ok None = not applicable / not configured. Never raises."""
import hashlib
import os
from pathlib import Path

import preview_config

NAMES = ('client', 'proxy', 'server', 'server_proxy', 'builder', 'bc2replay')


def folder_proxy_status(folder, app_root=None):
    """client_install.status for one folder (game or server; thin wrapper; tests patch this)."""
    from replay import client_install
    return client_install.status(Path(folder), client_install.proxy_source(app_root))


def proxy_status(client_exe, app_root=None):
    """client_install.status for the game folder of client_exe (thin wrapper; tests patch this)."""
    return folder_proxy_status(Path(client_exe).parent, app_root)


SERVER_PROXY_PREFIX = 'Server folder proxy: '


def server_proxy_text(text):
    """client_install.status text reworded for the server folder, prefixed so both proxy lines differ."""
    text = str(text or '')
    text = (text.replace('Client proxy installed', 'Proxy installed').replace('the game folder', 'the server folder')
            .replace('Game folder not found', 'Server folder not found'))
    return SERVER_PROXY_PREFIX + text


def version_mismatch(st):
    """r266: 'proxy build X, panel Y: ...' when an installed dll embeds a version other than the panel's, else None.
    Report only: Prepare treats 'installed' as done (the zip's source dll is that build; reinstalling can't help)."""
    version = st.get('version') if st.get('state') == 'installed' else None
    if version and version != preview_config.VERSION:
        return f'proxy build {version}, panel {preview_config.VERSION}: reinstall from this zip'
    return None


def _item(name, ok, text):
    return {'name': name, 'ok': ok, 'text': text}


def _sha256(path):
    with open(path, 'rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()


def check_client(cfg):
    value = (cfg.get('client_exe') or '').strip() if isinstance(cfg.get('client_exe'), str) else ''
    want = preview_config.CLIENT_HASH[:8]
    if not value:
        return _item('client', False, 'BC2 client EXE not set (Setup: BC2 client EXE).')
    path = Path(value)
    try:
        if not path.is_file():
            return _item('client', False, f'BC2 client EXE file not found: {path}')
        digest = _sha256(path)
    except OSError as exc:
        return _item('client', False, f'BC2 client EXE not readable: {exc}')
    if digest != preview_config.CLIENT_HASH:
        return _item('client', False, f'BC2 client EXE: unsupported build (sha {digest[:8]}); '
                                      f'this preview supports only the retail BFBC2Game.exe sha {want}')
    return _item('client', True, f'BC2 client EXE verified ({path.name}, sha {want})')


def check_proxy(cfg, app_root=None):
    value = cfg.get('client_exe')
    if not isinstance(value, str) or not value.strip():
        return _item('proxy', None, 'Set the BC2 client EXE to check the proxy.')
    try:
        st = proxy_status(value.strip(), app_root)
        mismatch = version_mismatch(st)
        if mismatch:
            return _item('proxy', False, mismatch)
        return _item('proxy', st.get('state') == 'installed', str(st.get('text') or st.get('state')))
    except Exception as exc:
        return _item('proxy', False, f'Proxy status unavailable: {exc}')


def check_server(cfg):
    value = cfg.get('server_dir')
    name = preview_config.ORIGINAL_NAME
    if not isinstance(value, str) or not value.strip():
        return _item('server', False, 'Server folder not set (Setup: Server folder).')
    folder = Path(value.strip())
    try:
        if not folder.is_dir():
            return _item('server', False, f'Server folder not found: {folder}')
        if not (folder / name).is_file():
            return _item('server', False, f'Server folder missing {name} '
                                          '(select the dedicated server folder, not the client)')
    except OSError as exc:
        return _item('server', False, f'Server folder not readable: {exc}')
    return _item('server', True, f'Server folder ok: {name} found')


def check_server_proxy(cfg, app_root=None):
    value = cfg.get('server_dir')
    if not isinstance(value, str) or not value.strip():
        return _item('server_proxy', None, SERVER_PROXY_PREFIX + 'set the Server folder to check it.')
    try:
        st = folder_proxy_status(value.strip(), app_root)
        mismatch = version_mismatch(st)
        if mismatch:
            return _item('server_proxy', False, SERVER_PROXY_PREFIX + mismatch)
        return _item('server_proxy', st.get('state') == 'installed', server_proxy_text(st.get('text') or st.get('state')))
    except Exception as exc:
        return _item('server_proxy', False, f'{SERVER_PROXY_PREFIX}status unavailable: {exc}')


def check_builder(cfg, app_root=None):
    try:
        from replay import pov
        ok, text = pov.builder_status(cfg, app_root)
        return _item('builder', bool(ok), text)
    except Exception as exc:
        return _item('builder', False, f'Builder status unavailable: {exc}')


def check_bc2replay(local_app_data=None):
    label = '%LOCALAPPDATA%\\BC2Replay'
    base = local_app_data if local_app_data is not None else os.environ.get('LOCALAPPDATA')
    if not base:
        return _item('bc2replay', False, f'{label} unavailable: LOCALAPPDATA is not set')
    try:
        folder = Path(base) / 'BC2Replay'
        folder.mkdir(parents=True, exist_ok=True)
        probe = folder / '.write_test'
        probe.write_bytes(b'ok')
        probe.unlink()
    except Exception as exc:  # OSError normally; never raise from a check
        return _item('bc2replay', False, f'{label} not writable: {exc}')
    return _item('bc2replay', True, f'{label} writable (captures, logs and crash dumps go there)')


def checks(cfg, app_root=None, local_app_data=None):
    """All six checks in order: client, proxy, server, server_proxy, builder, bc2replay."""
    cfg = cfg if isinstance(cfg, dict) else {}
    return [check_client(cfg), check_proxy(cfg, app_root), check_server(cfg), check_server_proxy(cfg, app_root),
            check_builder(cfg, app_root), check_bc2replay(local_app_data)]


def summary(results):
    """One line: 'Setup check: all 6 ok.' or 'Setup check: 5 of 6 ok - proxy: <text>' (first failure)."""
    counted = [r for r in results if r.get('ok') is not None]
    good = sum(1 for r in counted if r['ok'])
    failed = [r for r in counted if not r['ok']]
    if not failed:
        return f'Setup check: all {len(counted)} ok.'
    first = failed[0]
    return f'Setup check: {good} of {len(counted)} ok - {first["name"]}: {first["text"]}'


def format_report(results):
    """Multi-line 'OK / FAIL / --  name: text' report for the messagebox and the CLI."""
    lines = []
    for r in results:
        mark = '--' if r.get('ok') is None else ('OK' if r['ok'] else 'FAIL')
        lines.append(f'{mark:<4}  {r["name"]}: {r["text"]}')
    return '\n'.join(lines)


# ---------------------------------------------------------------- Prepare (panel button + `preview.py prepare`)

PREPARE_STEPS = ('client_proxy', 'server_proxy', 'build', 'instance')
PROXY_WORK_STATES = ('rome', 'upgrade')          # Prepare installs / upgrades
PROXY_SKIP_STATES = ('installed',)               # already done (a version mismatch is check-only: see version_mismatch)
# Any other state (mute, unknown, broken, missing, no-folder) stops Prepare before any step.
INSTANCE_INI = ('runtime', 'Instance-local-preview', 'ServerOptions.ini')
INSTANCE_LABEL = 'runtime\\Instance-local-preview'
SERVER_RUNNING = 'Stop every BC2 dedicated server first (it has dinput8.dll loaded).'

STEP_LABELS = {'client_proxy': 'client proxy', 'server_proxy': 'server folder proxy', 'build': 'build',
               'instance': 'instance', 'check': 'check'}
PROGRESS = {
    'client_proxy': 'Prepare: installing client proxy… (answer the Windows permission prompt)',
    'server_proxy': 'Prepare: installing server folder proxy…',
    'build': 'Prepare: building the playback server executable…',
    'instance': 'Prepare: creating the local preview instance…',
    'check': 'Prepare: checking setup…',
}


class PrepareStopped(RuntimeError):
    """Prepare refused before any step; str() is the user line."""
    def __init__(self, text):
        super().__init__('Prepare stopped: ' + str(text))


class PrepareFailed(RuntimeError):
    """A Prepare step raised; str() is the user line naming the step."""
    def __init__(self, step, message):
        self.step = step
        super().__init__(f'Prepare failed at {STEP_LABELS.get(step, step)}: {message}')


def _path_value(cfg, key):
    value = cfg.get(key) if isinstance(cfg, dict) else None
    return value.strip() if isinstance(value, str) and value.strip() else None


def _is_file(path):
    try:
        return Path(path).is_file()
    except OSError:
        return False


def _exists(path):
    try:
        return Path(path).exists()
    except OSError:
        return False


def no_server_running():
    """check_processes for the SERVER folder: refuse while any BC2 dedicated server runs."""
    from replay.server import processes
    if processes():
        raise RuntimeError(SERVER_RUNNING)


def prepare_facts(cfg, app_root=None, status=None, is_file=None):
    """Everything prepare_plan needs (reads the folders; status/is_file injectable). Never raises."""
    status = status or folder_proxy_status
    is_file = is_file or _is_file
    root = Path(app_root or preview_config.APP_ROOT)
    client = _path_value(cfg, 'client_exe')
    server = _path_value(cfg, 'server_dir')
    game_dir = str(Path(client).parent) if client else None

    def safe_status(folder):
        try:
            return status(folder, app_root)
        except Exception as exc:
            return {'state': 'unknown', 'text': f'status unavailable: {exc}'}

    return {
        'game_dir': game_dir,
        'server_dir': server,
        'client_proxy': safe_status(game_dir) if game_dir else None,
        'server_proxy': safe_status(server) if server else None,
        'timeline_exe': bool(server) and is_file(Path(server) / preview_config.TIMELINE_NAME),
        'instance': is_file(root.joinpath(*INSTANCE_INI)),
    }


def _proxy_line(step, text):
    return server_proxy_text(text) if step == 'server_proxy' else 'Client proxy: ' + str(text or '')


def prepare_plan(facts):
    """Pure: facts (prepare_facts) -> {'stop': text|None, 'steps': [{'step', 'needed', 'reason', ...}]}.
    Steps are always the four in PREPARE_STEPS order; only needed ones run. A stop is decided before any step."""
    steps = []
    stop = None
    folders = {'client_proxy': facts.get('game_dir'), 'server_proxy': facts.get('server_dir')}
    for step in ('client_proxy', 'server_proxy'):
        folder = folders[step]
        st = facts.get(step) or {}
        state = st.get('state')
        if not folder:
            reason = ('BC2 client EXE not set (Setup: BC2 client EXE).' if step == 'client_proxy'
                      else 'Server folder not set (Setup: Server folder).')
            stop = stop or reason
            steps.append({'step': step, 'needed': False, 'reason': reason, 'folder': None, 'state': None})
            continue
        reason = _proxy_line(step, st.get('text') or state)
        needed = state in PROXY_WORK_STATES
        if state not in PROXY_WORK_STATES + PROXY_SKIP_STATES:
            stop = stop or reason
        elif needed and st.get('source_ok', True) is False:
            stop = stop or (f'the proxy build {st.get("source") or "dinput8.dll"} is missing or not the shared '
                            'build. Build it first: rome_proxy\\build_shared.cmd')
        steps.append({'step': step, 'needed': needed, 'reason': reason, 'folder': folder, 'state': state})
    missing = [name for name, key in ((preview_config.TIMELINE_NAME, 'timeline_exe'),) if not facts.get(key)]
    steps.append({'step': 'build', 'needed': bool(missing), 'folder': facts.get('server_dir'),
                  'reason': ('missing ' + ', '.join(missing)) if missing else 'playback executable present'})
    has_instance = bool(facts.get('instance'))
    steps.append({'step': 'instance', 'needed': not has_instance, 'folder': None,
                  'reason': (INSTANCE_LABEL + ' present') if has_instance
                  else (INSTANCE_LABEL + '\\ServerOptions.ini missing')})
    return {'stop': stop, 'steps': steps}


def needed_steps(plan):
    return [s for s in plan['steps'] if s['needed']]


def format_plan(plan):
    """Multi-line 'RUN / skip  step: reason' listing for the CLI."""
    lines = [f'{"RUN" if s["needed"] else "skip":<4}  {s["step"]}: {s["reason"]}' for s in plan['steps']]
    return '\n'.join(lines)  # a stop is reported by run_prepare (PrepareStopped)


def prepare_button_text(results):
    """'Prepare' until a check ran with no failure (ok None does not count), then 'Check'."""
    if not results or any(r.get('ok') is False for r in results):
        return 'Prepare'
    return 'Check'


def prepare_confirm_text(plan, game_dir, server_dir):
    """askyesno text listing the needed steps; None when nothing is needed (no confirmation)."""
    lines = []
    for s in needed_steps(plan):
        upgrade = s.get('state') == 'upgrade'
        if s['step'] == 'client_proxy':
            lines.append((f'Upgrade the dinput8.dll proxy in {game_dir}' if upgrade else
                          f'Install the dinput8.dll proxy into {game_dir}') +
                         ' (Windows will ask for administrator permission)')
        elif s['step'] == 'server_proxy':
            lines.append(f'Upgrade the dinput8.dll proxy in {server_dir}' if upgrade else
                         f'Install the dinput8.dll proxy into {server_dir}')
        elif s['step'] == 'build':
            lines.append(f'Build the playback server executable in {server_dir}')
        elif s['step'] == 'instance':
            lines.append('Create the local preview instance under runtime\\')
    if not lines:
        return None
    return 'Prepare will:\n' + ''.join('- ' + line + '\n' for line in lines) + 'Nothing else is changed. Continue?'


def run_prepare(cfg, app_root, run_command, install, progress=None, runtime_dir=None, plan=None, check=None,
                source=None):
    """Run the needed Prepare steps in order, then the checks. Shared by the panel worker and the CLI.
    run_command(action): runs `preview.py build|init-instance`, raises on failure.
    install(folder, source, check_processes=?, runtime_dir=?): client_install.install (fakes in tests).
    Returns the check results. Raises PrepareStopped (before any step) or PrepareFailed (names the step)."""
    progress = progress or (lambda text: None)
    if plan is None:
        plan = prepare_plan(prepare_facts(cfg, app_root))
    if plan.get('stop'):
        raise PrepareStopped(plan['stop'])
    for s in needed_steps(plan):
        step = s['step']
        progress(PROGRESS[step])
        try:
            if step in ('client_proxy', 'server_proxy'):
                if source is None:
                    from replay import client_install
                    source = client_install.proxy_source(app_root)
                if step == 'client_proxy':
                    install(Path(s['folder']), source, runtime_dir=runtime_dir)
                else:
                    install(Path(s['folder']), source, check_processes=no_server_running, runtime_dir=runtime_dir)
            elif step == 'build':
                run_command('build')
            elif step == 'instance':
                run_command('init-instance')
        except Exception as exc:
            raise PrepareFailed(step, exc) from exc
    progress(PROGRESS['check'])
    try:
        return (check or checks)(cfg if isinstance(cfg, dict) else {}, app_root)
    except Exception as exc:
        raise PrepareFailed('check', exc) from exc


# ---------------------------------------------------------------- Clean up (panel button + `preview.py cleanup`)

CLEANUP_REFUSED = ('Clean up refused: close the game (BFBC2Game.exe) and stop every BC2 dedicated server '
                   '(including the replay server) first.')
UNINSTALL_STATES = ('installed', 'upgrade')        # proxy present -> restore Rome
NOTHING_STATES = ('rome', 'missing', 'no-folder')  # nothing of ours there


def built_names():
    """What Prepare's build step writes into the server folder."""
    return (preview_config.TIMELINE_NAME,)


def cleanup_names():
    """built_names() plus the intermediate trace exe older releases also wrote (deleted when present)."""
    return built_names() + (preview_config.LEGACY_TRACE_NAME,)


def cleanup_confirm_text(game_dir, server_dir):
    return (f"Clean up removes what Prepare created: the proxy in {game_dir} (Project Rome's dinput8.dll "
            f'restored; administrator prompt), the proxy in {server_dir}, the built playback executable '
            f'{preview_config.TIMELINE_NAME} (and {preview_config.LEGACY_TRACE_NAME} from older versions) '
            f'in {server_dir}, and {INSTANCE_LABEL}. Your game, server files and recordings stay. '
            'Close the game and the replay server first. Continue?')


def cleanup_plan(statuses, server_dir, app_root, game_dir=None, exists=None):
    """Pure (exists injectable): ordered actions {'action', 'step', 'path', 'text'}.
    action: 'uninstall' (proxy), 'delete_file' (built exe), 'delete_tree' (preview instance),
    'skip' (proxy in a state Clean up does not touch; reported, nothing done).
    statuses: {'client_proxy': status dict|None, 'server_proxy': status dict|None}."""
    exists = exists or _exists
    statuses = statuses or {}
    actions = []
    for step, folder, label in (('client_proxy', game_dir, 'client proxy'),
                                ('server_proxy', server_dir, 'server folder proxy')):
        st = statuses.get(step)
        if not folder or not st:
            continue
        state = st.get('state')
        if state in UNINSTALL_STATES:
            actions.append({'action': 'uninstall', 'step': step, 'path': str(folder), 'text': label + ' removed'})
        elif state not in NOTHING_STATES:
            actions.append({'action': 'skip', 'step': step, 'path': str(folder),
                            'text': _proxy_line(step, st.get('text') or state) + ' (not touched)'})
    if server_dir:
        for name in cleanup_names():
            path = Path(server_dir) / name
            if exists(path):
                actions.append({'action': 'delete_file', 'step': 'build', 'path': str(path),
                                'text': name + ' deleted'})
    instance = Path(app_root or preview_config.APP_ROOT) / 'runtime' / 'Instance-local-preview'
    if exists(instance):
        actions.append({'action': 'delete_tree', 'step': 'instance', 'path': str(instance),
                        'text': INSTANCE_LABEL + ' deleted'})
    return actions


def cleanup_statuses(game_dir, server_dir, app_root=None, status=None):
    """Both proxy statuses for cleanup_plan (status injectable). Never raises."""
    status = status or folder_proxy_status
    result = {}
    for step, folder in (('client_proxy', game_dir), ('server_proxy', server_dir)):
        try:
            result[step] = status(folder, app_root) if folder else None
        except Exception as exc:
            result[step] = {'state': 'unknown', 'text': f'status unavailable: {exc}'}
    return result


def format_cleanup(actions):
    """Multi-line 'DEL / skip  text: path' listing for the CLI."""
    if not actions:
        return 'Nothing to clean up.'
    return '\n'.join(f'{("skip" if a["action"] == "skip" else "DEL"):<4}  {a["text"]}: {a["path"]}'
                     for a in actions)


def _running_processes():
    from replay.client import client_pids
    from replay.server import processes
    return bool(client_pids()) or bool(processes())


def _remove_tree(path):
    import shutil
    path = Path(path)
    if path.name != 'Instance-local-preview' or path.parent.name != 'runtime':
        raise RuntimeError('Refusing to delete ' + str(path))
    shutil.rmtree(path)


def cleanup_result_text(removed, skipped=()):
    text = 'Clean up finished: ' + (', '.join(removed) if removed else 'nothing to remove') + '.'
    if skipped:
        text += ' ' + ' '.join(skipped)
    return text


def cleanup_progress_text(action):
    step = action['step']
    if step == 'client_proxy':
        return 'Clean up: removing client proxy… (answer the Windows permission prompt)'
    if step == 'server_proxy':
        return 'Clean up: removing server folder proxy…'
    if step == 'instance':
        return 'Clean up: deleting ' + INSTANCE_LABEL + '…'
    return 'Clean up: deleting ' + Path(action['path']).name + '…'


def run_cleanup(actions, uninstall, running=None, remove_file=None, remove_tree=None, runtime_dir=None,
                progress=None):
    """Run cleanup_plan actions in order; refuse first when the game or a dedicated server runs.
    uninstall(folder, check_processes=?, runtime_dir=?): client_install.uninstall (fakes in tests).
    Returns the 'Clean up finished: ...' line; raises RuntimeError with a user line on refusal/failure."""
    progress = progress or (lambda text: None)
    if (running or _running_processes)():
        raise RuntimeError(CLEANUP_REFUSED)
    remove_file = remove_file or (lambda p: Path(p).unlink())
    remove_tree = remove_tree or _remove_tree
    removed, skipped = [], []
    for a in actions:
        if a['action'] == 'skip':
            skipped.append(a['text'] + '.')
            continue
        progress(cleanup_progress_text(a))
        try:
            if a['action'] == 'uninstall':
                if a['step'] == 'server_proxy':
                    result = uninstall(Path(a['path']), check_processes=no_server_running, runtime_dir=runtime_dir)
                else:
                    result = uninstall(Path(a['path']), runtime_dir=runtime_dir)
                if isinstance(result, dict) and result.get('restored') is False:
                    skipped.append(f'{STEP_LABELS[a["step"]]}: nothing to restore.')
                    continue
            elif a['action'] == 'delete_file':
                remove_file(a['path'])
            elif a['action'] == 'delete_tree':
                remove_tree(a['path'])
            else:
                raise RuntimeError('unknown action ' + repr(a['action']))
        except Exception as exc:
            done = (' (already removed: ' + ', '.join(removed) + ')') if removed else ''
            raise RuntimeError(f'Clean up failed at {STEP_LABELS.get(a["step"], a["step"])}: {exc}{done}') from exc
        removed.append(a['text'])
    return cleanup_result_text(removed, skipped)


