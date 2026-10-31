"""Setup section (paths, Prepare, Clean up) and local-backend lifecycle for the controller.
Opening the Setup section never starts or stops services."""
import atexit
import datetime
import os
from pathlib import Path
import queue
import socket
import subprocess
import sys
import threading
import time
from tkinter import ttk, filedialog, messagebox
import tkinter as tk
from preview_config import APP_ROOT, raw_config, runtime_dir, save_settings


def command(action):
    if getattr(sys, 'frozen', False):
        return [sys.executable, action]
    return [sys.executable, str(APP_ROOT / 'preview.py'), action]


_backend = None
_backend_log = None


def stop_backend():
    """Stop only the backend launched by this controller."""
    global _backend
    from backend_lifecycle import stop_owned_backend
    stop_owned_backend(_backend)
    _backend = None


atexit.register(stop_backend)


def start_backend():
    """Start one backend owned by this controller; wait for its readiness marker."""
    global _backend, _backend_log
    if _backend is None or _backend.poll() is not None:
        from backend_lifecycle import cleanup_orphan_backends
        cleanup_orphan_backends(command('backend'))
        # Never reuse or kill an unrelated service or a live controller's backend.
        for kind, ports in ((socket.SOCK_STREAM, (80, 18390, 19021, 18395, 19026)),
                            (socket.SOCK_DGRAM, (18100, 18395, 19026))):
            for port in ports:
                with socket.socket(socket.AF_INET, kind) as sock:
                    sock.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
                    try:
                        sock.bind(('127.0.0.1', port))
                    except OSError as exc:
                        raise RuntimeError(f'Local backend port {port} is occupied. Close the older backend first.') from exc
        stamp = datetime.datetime.now().strftime('%Y%m%d_%H%M%S_%f')
        _backend_log = runtime_dir() / ('backend_' + stamp + '.private.log')
        with _backend_log.open('w', encoding='utf-8') as output:
            _backend = subprocess.Popen(command('backend'), cwd=APP_ROOT,
                stdout=output, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL,
                creationflags=subprocess.CREATE_NO_WINDOW)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if _backend.poll() is not None:
            raise RuntimeError('Backend exited. See runtime/' + _backend_log.name)
        if 'LOCAL ROME BACKEND READY' in _backend_log.read_text(encoding='utf-8', errors='replace'):
            return
        time.sleep(0.1)
    raise RuntimeError('Backend readiness timed out. Inspect runtime/' + _backend_log.name + ' before retrying.')


def wrap_to_width(label, minimum=200):
    """Re-wrap a label at its current width, so a long text never sets the window's minimum width.
    wraplength only follows the width Tk gave the label, so it cannot shrink the window by itself."""
    def on_configure(event):
        width = max(minimum, event.width)
        if int(str(label.cget('wraplength')) or 0) != width:
            label.config(wraplength=width)
    label.bind('<Configure>', on_configure)
    return label


def setup_needed():
    """True when the saved paths are missing/invalid or Prepare has not created the preview instance yet."""
    data = raw_config()  # missing/unreadable -> {} -> paths invalid -> True
    for key in ('server_dir', 'client_exe'):
        p = Path(data.get(key) or '')
        if not data.get(key) or not p.is_absolute() or not (p.is_dir() if key == 'server_dir' else p.is_file()):
            return True
    if not (APP_ROOT / 'runtime/Instance-local-preview/ServerOptions.ini').is_file():
        return True
    try:
        from preview_config import timeline_exe
        return not timeline_exe().is_file()  # built ReplayTimeline.exe
    except Exception:
        return True


def add_launcher(window, on_paths_changed=None, on_status=None):
    """Collapsible Setup section (paths + Prepare / Clean up…). Returns launch_game(): the Launch client action (preview.py client)."""
    header = ttk.Frame(window)
    header.pack(padx=16, pady=(5, 0), fill='x')
    frame = ttk.LabelFrame(window, text='Setup — paths, Prepare and Clean up')
    expanded = tk.BooleanVar(value=False)
    toggle = ttk.Button(header, width=14)
    toggle.pack(side='left')
    from replay.tooltip import tip  # r286
    tip(toggle, 'Show or hide the setup: server and game paths, Prepare and Clean up.')

    def show(on):
        expanded.set(on)
        toggle.config(text=('▾ Setup' if on else '▸ Setup'))
        if on:
            frame.pack(padx=16, pady=5, fill='x', after=header)
        else:
            frame.pack_forget()

    toggle.config(command=lambda: show(not expanded.get()))
    data = raw_config()
    fields = {}
    for row, (key, title) in enumerate((('server_dir', 'Server folder'), ('client_exe', 'BC2 client EXE'))):
        ttk.Label(frame, text=title).grid(row=row, column=0, padx=5)
        value = tk.StringVar(value=data.get(key, ''))
        fields[key] = value
        ttk.Entry(frame, textvariable=value, width=30).grid(row=row, column=1, sticky='ew')
        def browse(k=key, v=value):
            path = (filedialog.askdirectory(parent=window, title='Dedicated server folder') if k == 'server_dir'
                    else filedialog.askopenfilename(parent=window, title='Select BFBC2Game.exe', filetypes=[('Executable', '*.exe')]))
            if path:
                v.set(path)
        tip(ttk.Button(frame, text='Browse…', command=browse),
            'Choose the dedicated server folder.' if key == 'server_dir' else 'Choose BFBC2Game.exe of Project Rome.'
            ).grid(row=row, column=2, padx=5, pady=2)
    frame.columnconfigure(1, weight=1)
    status = tk.StringVar(value='Paths save automatically when valid. Then click Prepare once. Dummy login only.')
    wrap_to_width(ttk.Label(frame, textvariable=status, wraplength=440)).grid(row=3, column=0, columnspan=3, sticky='ew', padx=5)
    summary = tk.StringVar(value='')
    ttk.Label(header, textvariable=summary).pack(side='left', padx=8)
    # str (Launch client) | ('progress', text) (busy stays) | ('done', text) | ('check', results)
    # | ('cleaned', text) from worker threads
    events = queue.Queue()
    busy = False
    buttons = {}
    # Last check summary / Prepare step in progress. Own StringVar: autosave owns `status` and would overwrite it.
    proxy_text = tk.StringVar(value='Click Prepare to check and set up everything (it asks before changing anything).')
    last = {'results': None, 'paths': None}

    def report(text):
        status.set(text)
        if on_status:
            on_status(text)

    def update_buttons():
        for button in buttons.values():
            button.config(state='disabled' if busy else 'normal')

    def refresh_summary(first=False):
        text = 'Setup needed: set both paths, then click Prepare.' if setup_needed() else 'Paths and setup OK.'
        try:  # r262: builder\ + datapack\ status (informational: playback works without the builder)
            from replay import pov
            ok, detail = pov.builder_status(raw_config(), APP_ROOT)
            text += '  Builder found.' if ok else '  Builder missing (see Setup).'
            if not ok and first:  # once at startup; autosave owns the status text afterwards
                status.set(detail)
        except Exception:
            pass
        summary.set(text)

    def save(strict=True):
        values = {k: v.get().strip() for k, v in fields.items()}
        valid = {}
        invalid = []
        for key, value in values.items():
            p = Path(value)
            if not value or not p.is_absolute() or not (p.is_dir() if key == 'server_dir' else p.is_file()):
                invalid.append(key)
            else:
                valid[key] = value
        if strict and invalid:
            raise RuntimeError('Select an existing absolute path for ' + ', '.join(invalid))
        current = raw_config()
        changed = any(current.get(k) != v for k, v in valid.items())
        if changed:
            save_settings(valid)
            if on_paths_changed:
                on_paths_changed()
        return invalid

    def refresh_prepare(results=None):
        """Prepare label from the last check ('Prepare' until a clean check, then 'Check'). Never raises."""
        try:
            from replay import first_run
            last['results'] = results
            if 'prepare' in buttons:
                buttons['prepare'].config(text=first_run.prepare_button_text(results))
        except Exception:
            pass

    def autosave(*_):
        if busy:
            return  # Poll retries after the active launcher operation finishes.
        try:
            invalid = save(strict=False)
            status.set('Not saved (incomplete/invalid): ' + ', '.join(invalid)
                       if invalid else 'Paths saved automatically. Recording list refreshed.')
            if invalid:
                show(True)
        except Exception as exc:
            status.set('Paths not saved: ' + str(exc))
        refresh_summary()
        cfg = raw_config()
        paths = (cfg.get('client_exe'), cfg.get('server_dir'))
        if last['paths'] is not None and paths != last['paths'] and last['results'] is not None:
            refresh_prepare(None)  # an old check says nothing about new paths
        last['paths'] = paths

    for value in fields.values():
        value.trace_add('write', autosave)

    def start(action):
        """Launch client (preview.py client) on a worker thread; the only subprocess action left here."""
        nonlocal busy
        if busy:
            return
        try:
            save()
        except Exception as exc:
            show(True)
            messagebox.showerror('Setup', str(exc), parent=window)
            return
        busy = True
        update_buttons()
        report('Working: ' + ('Launch client' if action == 'client' else action) + '…')

        def worker():
            try:
                stamp = datetime.datetime.now().strftime('%Y%m%d_%H%M%S_%f')
                log = runtime_dir() / ('launcher_' + stamp + '.private.log')
                with log.open('w', encoding='utf-8') as output:
                    env = None
                    if action == 'client':  # the client it starts is recorded; closing the panel stops it
                        from replay.owned import CLIENT_ENV
                        env = dict(os.environ, **{CLIENT_ENV: '1'})
                    proc = subprocess.Popen(command(action), cwd=APP_ROOT, stdout=output, stderr=subprocess.STDOUT,
                                            env=env)
                    try:
                        result = proc.wait(timeout=2)
                    except subprocess.TimeoutExpired:
                        result = None
                    if result not in (None, 0):
                        raise RuntimeError('Process exited with code ' + str(result))
                events.put('Started/completed: ' + action + '. Log: ' + log.name + ' (startup is not proof of readiness).')
            except Exception as exc:
                events.put('Failed: ' + str(exc) + '. Check the latest launcher log in runtime/.')
        threading.Thread(target=worker, daemon=True).start()

    def logged_run(output, log):
        """run_command for first_run.run_prepare: `preview.py build|init-instance` into the launcher log."""
        def run(action):
            output.flush()
            proc = subprocess.run(command(action), cwd=APP_ROOT, stdout=output, stderr=subprocess.STDOUT,
                                  creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
            if proc.returncode:
                output.flush()
                try:
                    tail = [line.strip() for line in log.read_text(encoding='utf-8', errors='replace').splitlines()
                            if line.strip()]
                except OSError:
                    tail = []
                detail = (tail[-1] + ' ') if tail else ''
                raise RuntimeError(detail + '(' + action + ' exit code ' + str(proc.returncode) + '; log runtime/'
                                   + log.name + ')')
        return run

    def prepare():
        """Prepare: plan on the Tk thread, confirm when anything changes, run the steps + checks on a worker."""
        nonlocal busy
        if busy:
            return
        try:
            save()
        except Exception as exc:
            show(True)
            messagebox.showerror('Prepare', str(exc), parent=window)
            return
        from replay import first_run
        cfg = raw_config()
        try:
            plan = first_run.prepare_plan(first_run.prepare_facts(cfg, APP_ROOT))
        except Exception as exc:
            report('Prepare failed at plan: ' + str(exc))
            return
        if plan['stop']:
            line = str(first_run.PrepareStopped(plan['stop']))
            report(line)
            proxy_text.set(line)
            return
        game_dir = next((s['folder'] for s in plan['steps'] if s['step'] == 'client_proxy'), None)
        text = first_run.prepare_confirm_text(plan, game_dir, cfg.get('server_dir'))
        if text and not messagebox.askyesno('Prepare', text, parent=window):
            return
        busy = True
        update_buttons()
        report('Working: Prepare…')

        def progress(line):
            events.put(('progress', line))

        def worker():
            try:
                from replay import client_install
                stamp = datetime.datetime.now().strftime('%Y%m%d_%H%M%S_%f')
                log = runtime_dir() / ('launcher_' + stamp + '.private.log')
                with log.open('w', encoding='utf-8') as output:
                    results = first_run.run_prepare(cfg, APP_ROOT, logged_run(output, log), client_install.install,
                                                    progress, runtime_dir=runtime_dir(), plan=plan)
                events.put(('check', results))
            except (first_run.PrepareStopped, first_run.PrepareFailed) as exc:
                events.put(('done', str(exc)))
            except Exception as exc:
                events.put(('done', 'Prepare failed: ' + str(exc)))
        threading.Thread(target=worker, daemon=True).start()

    def cleanup():
        """Clean up…: confirmed; removes only what Prepare created (first_run.cleanup_plan)."""
        nonlocal busy
        if busy:
            return
        from replay import first_run
        cfg = raw_config()
        client = cfg.get('client_exe') if isinstance(cfg.get('client_exe'), str) and cfg.get('client_exe').strip() else None
        server = cfg.get('server_dir') if isinstance(cfg.get('server_dir'), str) and cfg.get('server_dir').strip() else None
        game_dir = str(Path(client.strip()).parent) if client else None
        server = server.strip() if server else None
        if not messagebox.askyesno('Clean up', first_run.cleanup_confirm_text(game_dir or '(BC2 client EXE not set)',
                                                                             server or '(Server folder not set)'),
                                   parent=window):
            return
        busy = True
        update_buttons()
        report('Working: Clean up…')

        def worker():
            try:
                from replay import client_install
                statuses = first_run.cleanup_statuses(game_dir, server, APP_ROOT)
                actions = first_run.cleanup_plan(statuses, server, APP_ROOT, game_dir=game_dir)
                line = first_run.run_cleanup(actions, client_install.uninstall, runtime_dir=runtime_dir(),
                                             progress=lambda text: events.put(('progress', text)))
                events.put(('cleaned', line))
            except Exception as exc:
                events.put(('done', str(exc)))
        threading.Thread(target=worker, daemon=True).start()

    row = ttk.Frame(frame)
    row.grid(row=2, column=0, columnspan=3, sticky='ew', pady=5)
    b = ttk.Button(row, text='Prepare', command=prepare)
    b.pack(side='left', padx=3, pady=4)
    buttons['prepare'] = b
    tip(b, 'One-time setup: installs the proxy in the game and server folders and builds the replay server '
           'executable. Close the game and the server first.')
    b = ttk.Button(row, text='Clean up…', command=cleanup)
    b.pack(side='right', padx=3, pady=4)
    buttons['cleanup'] = b
    tip(b, 'Undo Prepare: removes the proxies (restores the Rome dinput8.dll), the built replay executable and the '
           'local instance. Your game, server files and recordings stay. Asks first.')
    wrap_to_width(ttk.Label(frame, textvariable=proxy_text, wraplength=440)).grid(
        row=5, column=0, columnspan=3, sticky='ew', padx=5, pady=(0, 4))

    def handle(item):
        """One worker event. Returns True when the operation finished (busy cleared)."""
        nonlocal busy
        if not isinstance(item, str) and item[0] == 'progress':  # a Prepare / Clean up step started
            report(item[1])
            proxy_text.set(item[1])
            return False
        busy = False
        autosave()
        if isinstance(item, str):  # Launch client
            report(item)
        elif item[0] == 'check':
            from replay import first_run
            line = first_run.summary(item[1])
            report(line)
            proxy_text.set(line)
            refresh_prepare(item[1])
            if any(r.get('ok') is False for r in item[1]):
                messagebox.showinfo('Setup check', first_run.format_report(item[1]), parent=window)
        elif item[0] == 'cleaned':
            report(item[1])
            proxy_text.set(item[1])
            refresh_prepare(None)
        else:  # ('done', text): Prepare stopped/failed, Clean up refused/failed
            report(item[1])
            proxy_text.set(item[1])
        return True

    def poll():
        try:
            while True:
                if handle(events.get_nowait()):
                    break
        except queue.Empty:
            pass
        update_buttons()
        window.after(200, poll)
    show(setup_needed())
    refresh_summary(first=True)
    refresh_prepare(None)
    poll()
    return lambda: start('client')
