"""Explicit setup/launcher UI. Opening it never starts or stops services."""
import datetime
import json
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
from preview_config import APP_ROOT, CONFIG, runtime_dir


def command(action):
    if getattr(sys, 'frozen', False):
        return [sys.executable, action]
    return [sys.executable, str(APP_ROOT / 'preview.py'), action]


_backend = None
_backend_log = None


def start_backend():
    """Start one backend owned by this controller; wait for its readiness marker."""
    global _backend, _backend_log
    if _backend is None or _backend.poll() is not None:
        # Do not silently reuse an older/unrelated service that happens to answer.
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


def redirect_status(path=None):
    """Inspect hosts only; never resolve a hostname, elevate or change a file."""
    path = path or Path(os.environ.get('SystemRoot', r'C:\Windows')) / 'System32/drivers/etc/hosts'
    try:
        lines = path.read_text(encoding='utf-8-sig').splitlines()
    except (OSError, UnicodeError):
        return 'Redirect: cannot read hosts; check permissions/configuration manually.', False, False
    begin = '# === BC2-VIEWERSET-STUB-BEGIN ==='
    end = '# === BC2-VIEWERSET-STUB-END ==='
    entry = '127.0.0.1 bfbc2.emulatornexus.com'
    owned = begin in lines or end in lines
    if owned:
        if lines.count(begin) != 1 or lines.count(end) != 1:
            return 'Redirect: malformed preview markers; repair hosts manually.', False, False
        index = lines.index(begin)
        if lines[index:index + 3] != [begin, entry, end]:
            return 'Redirect: unexpected preview block; repair hosts manually.', False, False
        lines = lines[:index] + lines[index + 3:]
    external = any('bfbc2.emulatornexus.com' in line.split('#', 1)[0].lower().split()
                   for line in lines)
    if owned:
        text = 'Redirect: LOCAL (preview block). Restart BC2 after changing modes.'
        if external:
            text = 'Redirect: preview block plus another mapping. Off removes only our block; review the other mapping manually.'
        return text, False, True
    if external:
        return 'Redirect: externally managed mapping; this application will not modify it.', False, False
    return 'Redirect: OFF (no hosts override). Enable On for local playback.', True, False


def add_launcher(window, on_start_replay):
    frame = ttk.LabelFrame(window, text='Setup / Launcher — explicit actions only')
    frame.pack(padx=16, pady=5, fill='x')
    try:
        data = json.loads(CONFIG.read_text(encoding='utf-8')) if CONFIG.exists() else {}
    except (OSError, ValueError):
        data = {}
    fields = {}
    for row, (key, title) in enumerate((('server_dir', 'Server folder'), ('client_exe', 'BC2 client EXE'))):
        ttk.Label(frame, text=title).grid(row=row, column=0, padx=5)
        value = tk.StringVar(value=data.get(key, ''))
        fields[key] = value
        ttk.Entry(frame, textvariable=value, width=65).grid(row=row, column=1, sticky='ew')
        def browse(k=key, v=value):
            path = (filedialog.askdirectory(parent=window, title='Dedicated server folder') if k == 'server_dir'
                    else filedialog.askopenfilename(parent=window, title='Select BFBC2Game.exe', filetypes=[('Executable', '*.exe')]))
            if path:
                v.set(path)
        ttk.Button(frame, text='Browse…', command=browse).grid(row=row, column=2, padx=5, pady=2)
    frame.columnconfigure(1, weight=1)
    status = tk.StringVar(value='Save paths, Setup / Build, choose Demo, Redirect On, Start Replay, Launch Game. Dummy login only.')
    ttk.Label(frame, textvariable=status, wraplength=750).grid(row=3, column=0, columnspan=3, sticky='w', padx=5)
    events = queue.Queue()
    busy = False
    buttons = {}
    redirect_text = tk.StringVar()
    ttk.Label(frame, textvariable=redirect_text, wraplength=750).grid(row=4, column=0, columnspan=3, sticky='w', padx=5)
    redirect_state = ('', False, False)
    next_redirect_check = 0

    def update_buttons():
        for action, button in buttons.items():
            enabled = not busy
            if action.startswith('redirect-'):
                enabled = enabled and redirect_state[1 if action == 'redirect-on' else 2]
            button.config(state='normal' if enabled else 'disabled')

    def save():
        values = {k: v.get().strip() for k, v in fields.items()}
        for key, value in values.items():
            p = Path(value)
            if not p.is_absolute() or not (p.is_dir() if key == 'server_dir' else p.is_file()):
                raise RuntimeError('Select an existing absolute path for ' + key)
        current = json.loads(CONFIG.read_text(encoding='utf-8')) if CONFIG.exists() else {}
        current.update(values)
        temp = CONFIG.with_suffix('.json.tmp')
        temp.write_text(json.dumps(current, indent=2) + '\n', encoding='utf-8')
        temp.replace(CONFIG)

    def start(action):
        nonlocal busy
        if busy:
            return
        try:
            if action.startswith('redirect'):
                current = redirect_status()
                if not current[1 if action == 'redirect-on' else 2]:
                    status.set(current[0])
                    return
                if not messagebox.askyesno('Local redirect', 'Change the local hosts redirect? Windows will request administrator permission. Restart BC2 after changing modes.', parent=window):
                    return
            else:
                save()
            if action == 'save':
                status.set('Paths saved. Click Refresh list and choose a Demo below.')
                return
            if action == 'replay':
                on_start_replay()
                return
        except Exception as exc:
            messagebox.showerror('Setup', str(exc), parent=window)
            return
        busy = True
        update_buttons()
        status.set('Working: ' + action + '…')

        def worker():
            try:
                stamp = datetime.datetime.now().strftime('%Y%m%d_%H%M%S_%f')
                log = runtime_dir() / ('launcher_' + stamp + '.private.log')
                with log.open('w', encoding='utf-8') as output:
                    if action.startswith('redirect'):
                        args = ['powershell.exe', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', str(APP_ROOT / 'Redirect.ps1'), '-Mode', action.split('-')[1]]
                        subprocess.run(args, cwd=APP_ROOT, stdout=output, stderr=subprocess.STDOUT, check=True)
                    elif action == 'setup':
                        for step in ('build', 'init-instance'):
                            if step == 'init-instance' and (APP_ROOT / 'runtime/Instance-local-preview/ServerOptions.ini').is_file():
                                continue
                            subprocess.run(command(step), cwd=APP_ROOT, stdout=output, stderr=subprocess.STDOUT, check=True)
                    else:
                        proc = subprocess.Popen(command(action), cwd=APP_ROOT, stdout=output, stderr=subprocess.STDOUT)
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

    row = ttk.Frame(frame)
    row.grid(row=2, column=0, columnspan=3, pady=5)
    for title, actions in (
        ('Setup', [('Save paths', 'save'), ('Setup / Build', 'setup')]),
        ('Redirect', [('Redirect On', 'redirect-on'), ('Redirect Off', 'redirect-off')]),
        ('Start', [('Start Replay', 'replay'), ('Launch Game', 'client')]),
    ):
        group = ttk.LabelFrame(row, text=title)
        group.pack(side='left', padx=6)
        for label, action in actions:
            b = ttk.Button(group, text=label, command=lambda a=action: start(a))
            b.pack(side='left', padx=3, pady=4)
            buttons[action] = b

    def poll():
        nonlocal busy, redirect_state, next_redirect_check
        try:
            status.set(events.get_nowait())
            busy = False
            next_redirect_check = 0
        except queue.Empty:
            pass
        if time.monotonic() >= next_redirect_check:
            redirect_state = redirect_status()
            redirect_text.set(redirect_state[0])
            next_redirect_check = time.monotonic() + 2
        update_buttons()
        window.after(200, poll)
    poll()
