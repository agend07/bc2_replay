"""Unified local replay controls (Recording Selector + Timeline Play/Pause/Seek + Camera Free/Focus/Follow).
Opening this window makes no server or camera writes.
"""
import ctypes as c
import datetime
import json
import queue
import threading
import tkinter as tk
from tkinter import ttk, messagebox

from replay.client import Client, client_pids
from preview_config import APP_ROOT as ROOT
from replay.camera_native import ControlClient
from replay.players import roster
from replay.camera import (
    STATE, attach, session,
    status, native_command, focus_command, target_position,
)
from replay.follow import run_follow
from replay import seek_guard as seek_view_guard
from replay.timeline import (
    operate as timeline_operate, seek_frame, limit_frame, FPS,
    list_recordings, active_demo, switch_recording,
)

UI_LOG = ROOT / 'runtime' / 'private_ui_events.jsonl'
u32_dll = c.WinDLL('user32', use_last_error=True)
u32_dll.GetForegroundWindow.argtypes = []
u32_dll.GetForegroundWindow.restype = c.wintypes.HWND
u32_dll.GetWindowThreadProcessId.argtypes = [c.wintypes.HWND, c.POINTER(c.wintypes.DWORD)]
u32_dll.GetWindowThreadProcessId.restype = c.wintypes.DWORD
u32_dll.GetAsyncKeyState.argtypes = [c.c_int]
u32_dll.GetAsyncKeyState.restype = c.c_short
VK_SPACE = 0x20
VK_SHIFT = 0x10
VK_LEFT = 0x25
VK_RIGHT = 0x27
STEP_SMALL_FRAMES = 1 * FPS
STEP_LARGE_FRAMES = 5 * FPS


def log_ui(kind, detail, error=False):
    try:
        UI_LOG.parent.mkdir(parents=True, exist_ok=True)
        with UI_LOG.open('a', encoding='utf-8') as f:
            f.write(json.dumps({
                'utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                'kind': kind,
                'error': bool(error),
                'detail': detail,
            }) + '\n')
    except OSError:
        pass


def format_mmss(seconds):
    total = max(0, int(round(seconds)))
    m, s = divmod(total, 60)
    h, m = divmod(m, 60)
    return f'{h}:{m:02d}:{s:02d}' if h else f'{m:02d}:{s:02d}'


def format_recording_label(item):
    lvl_short = item['level'].replace('Levels/', '')
    return f"{item['name']}  [{lvl_short} | {format_mmss(item['seconds'])}]"


def main():
    try:
        limit = limit_frame()
    except Exception:
        limit = 0

    window = tk.Tk()
    window.title('BC2 Local Replay Preview 0.1 — controls')
    window.geometry('900x800')
    from launcher_panel import add_launcher
    add_launcher(window, on_start_replay=lambda: start_switch_recording())

    playback_status = tk.StringVar(value='Reading server status…')
    message = tk.StringVar(
        value='Ready. Opening this window makes no server or camera writes.\n'
              'Timeline starts paused: join Local Replay, then click Play. Seek automatically installs the view-readiness guard.'
    )
    player = tk.StringVar(value='')
    target_time = tk.StringVar(value='0:00')
    selected_recording = tk.StringVar(value='')
    help_text = tk.StringVar()

    def update_help_text():
        help_text.set(
            f'Seek time accepts seconds, mm:ss or hh:mm:ss (max {limit / FPS:g}s / {format_mmss(limit / FPS)}). '
            'Seek leaves playback paused; click Play to resume.\n'
            'In-game Free Camera hotkeys: SPACE = Play/Pause | LEFT/RIGHT = -1s/+1s | SHIFT+LEFT/RIGHT = -5s/+5s.\n'
            'Follow uses a fixed world-space offset, not possession or native killcam. Closing restores camera and leaves server running.'
        )

    update_help_text()

    ttk.Label(window, text='BC2 Local Replay — Timeline & Camera Controller', font=('', 12, 'bold')).pack(pady=8)
    ttk.Label(window, textvariable=playback_status, wraplength=760, font=('', 10, 'bold')).pack(pady=3)

    # Recording Selector Frame
    rec_frame = ttk.LabelFrame(window, text='Server Recording (demos/)')
    rec_frame.pack(padx=16, pady=5, fill='x')
    rec_row = ttk.Frame(rec_frame)
    rec_row.pack(padx=8, pady=6, fill='x')
    ttk.Label(rec_row, text='Demo:').pack(side='left', padx=(2, 4))
    rec_selector = ttk.Combobox(rec_row, textvariable=selected_recording, state='readonly', width=48)
    rec_selector.pack(side='left', padx=4)

    # Timeline row
    tl_frame = ttk.LabelFrame(window, text='Timeline Playback (Play / Pause / Seek)')
    tl_frame.pack(padx=16, pady=5, fill='x')
    tl_row = ttk.Frame(tl_frame)
    tl_row.pack(padx=8, pady=7)

    # Camera row
    cam_frame = ttk.LabelFrame(window, text='Camera Controls (Free Camera / Focus / Follow)')
    cam_frame.pack(padx=16, pady=5, fill='x')
    cam_top = ttk.Frame(cam_frame)
    cam_top.pack(padx=8, pady=4)
    ttk.Label(cam_top, text='Target player:').pack(side='left', padx=4)
    selector = ttk.Combobox(cam_top, textvariable=player, values=[], state='readonly', width=22)
    selector.pack(side='left', padx=4)

    cam_row1 = ttk.Frame(cam_frame)
    cam_row1.pack(padx=8, pady=4)
    cam_row2 = ttk.Frame(cam_frame)
    cam_row2.pack(padx=8, pady=4)

    ttk.Label(window, textvariable=message, wraplength=760, justify='left').pack(padx=18, pady=8, fill='x')
    ttk.Label(window, textvariable=help_text, wraplength=760, justify='left').pack(padx=18, pady=2, fill='x')

    buttons = {}
    rec_map = {}
    events = queue.Queue()
    camera_busy = False
    aux_busy = False
    status_pending = False
    last_status_tick = 0
    follow_thread = None
    stop_event = threading.Event()
    pending_restore = False
    close_after = False
    free_cam_active = False
    client_pid = None
    replay_paused = False
    current_frame = 0
    server_ready = False
    active_recording = None
    space_was_down = False
    left_was_down = False
    right_was_down = False

    def refresh_recordings_list():
        nonlocal limit, active_recording, server_ready
        try:
            items = list_recordings()
        except Exception as exc:
            rec_map.clear()
            active_recording = None
            server_ready = False
            rec_selector.config(values=[])
            selected_recording.set('')
            message.set('Configure and save paths in Setup / Launcher first. ' + str(exc))
            return
        rec_map.clear()
        labels = []
        current_base = active_demo()
        active_recording = current_base
        chosen_label = None
        for item in items:
            lbl = format_recording_label(item)
            rec_map[lbl] = item
            labels.append(lbl)
            if item['base'] == current_base:
                chosen_label = lbl
                limit = item['limit_frame']
        rec_selector.config(values=labels)
        if chosen_label:
            selected_recording.set(chosen_label)
        elif labels and selected_recording.get() not in rec_map:
            selected_recording.set(labels[0])
            limit = rec_map[labels[0]]['limit_frame']
        update_help_text()

    def can_play():
        item = rec_map.get(selected_recording.get())
        return bool(item and server_ready and item['base'] == active_recording)

    def controls():
        following = follow_thread is not None
        selector.config(state='disabled' if following or camera_busy else 'readonly')
        rec_selector.config(state='disabled' if following or camera_busy or aux_busy else 'readonly')
        for action, button in buttons.items():
            if action in ('pause', 'play', 'seek', 'rewind', 'step_back_5', 'step_back_1', 'step_fwd_1', 'step_fwd_5'):
                enabled = not aux_busy and not camera_busy
                if action == 'play':
                    enabled = enabled and can_play()
            elif action in ('switch_rec', 'refresh_rec', 'refresh_players'):
                enabled = not aux_busy and not camera_busy and not following
            elif action == 'stop':
                enabled = following
            elif action == 'restore':
                enabled = not camera_busy
            else:
                enabled = not camera_busy and not following
            button.config(state='normal' if enabled else 'disabled')

    def refresh_players_action():
        if camera_busy or aux_busy or follow_thread is not None:
            return
        try:
            c_ro = Client()
            try:
                data = roster(c_ro)
            finally:
                c_ro.close()
            names = [p['name'] for p in data.get('players', []) if p.get('name')]
            if names:
                selector.config(values=names)
                if player.get() not in names:
                    player.set(names[0])
                message.set('Players in current replay: ' + ', '.join(names))
            else:
                message.set('No players currently spawned in client memory.')
        except Exception as exc:
            message.set('Could not read player roster: ' + str(exc))

    def start_switch_recording():
        nonlocal aux_busy, server_ready, active_recording
        if aux_busy or camera_busy or follow_thread is not None:
            return
        lbl = selected_recording.get()
        item = rec_map.get(lbl)
        if not item:
            messagebox.showerror('Recording', 'Select a recording from the list first.')
            return
        if not messagebox.askyesno(
            'Switch Recording',
            f"Switch server to {item['name']} ({item['level']}, {format_mmss(item['seconds'])})?\n\n"
            "If you are connected in BC2, you will be disconnected and can rejoin 'Local Replay' from the server browser."
        ):
            return
        aux_busy = True
        server_ready = False
        active_recording = None
        controls()
        message.set(f"Switching server to {item['name']}…")

        def worker():
            try:
                # Do not switch servers if recovery of a live camera hook fails.
                # A stale session file after BC2 exits needs no restoration.
                if STATE.exists() and client_pids():
                    c_ctl = ControlClient()
                    try:
                        data = session(c_ctl)
                        st = status(c_ctl, data)
                        if st.get('hook_installed'):
                            native_command(c_ctl, data, 'detach', replay_kind=data.get('replay_kind', 'timeline'))
                    finally:
                        c_ctl.close()
                from launcher_panel import start_backend
                start_backend()
                res = switch_recording(item['base'])
                events.put(('switch_done', res, None))
            except Exception as exc:
                events.put(('switch_done', None, str(exc)))

        threading.Thread(target=worker, daemon=True).start()

    def start_camera(action, closing=False):
        nonlocal camera_busy, pending_restore, close_after
        if action == 'restore' and follow_thread is not None:
            pending_restore = True
            close_after = close_after or closing
            stop_event.set()
            message.set('Stopping Follow before restoring camera…')
            return
        if camera_busy:
            if closing:
                pending_restore = True
                close_after = True
            return
        if action == 'restore':
            pending_restore = False
            close_after = close_after or closing
        camera_busy = True
        controls()
        name = player.get()
        message.set('Working… do not use another controller.')

        def worker():
            client = None
            try:
                kind = 'timeline'
                if action == 'status':
                    summary = {}
                    if STATE.exists():
                        client = Client()
                        data = session(client)
                        result = status(client, data)
                        summary['camera'] = {
                            k: result[k]
                            for k in ('actual_mode', 'adapter_version', 'free_camera_is_global_active', 'hook_installed', 'saved_transform')
                        }
                    else:
                        summary['camera'] = 'not attached'
                    try:
                        svg = seek_view_guard.status()
                        summary['seek_guard'] = {
                            k: svg[k] for k in ('installed', 'enabled', 'null_view_updates_skipped', 'heartbeat')
                        }
                    except Exception:
                        summary['seek_guard'] = 'not installed'
                    text = json.dumps(summary)
                elif action == 'restore':
                    if not STATE.exists():
                        text = 'No camera session; no camera changes made.'
                    elif not client_pids():
                        text = 'BC2 is closed; camera hook ended with the game. No camera writes needed.'
                    else:
                        client = ControlClient()
                        data = session(client)
                        sess_kind = data.get('replay_kind', 'timeline')
                        result = status(client, data)
                        if result['hook_installed']:
                            native_command(client, data, 'detach', replay_kind=sess_kind)
                            text = 'Saved camera transform restored; original player-view mode and vtable restored.'
                        else:
                            text = 'Already detached; no camera writes made.'
                    events.put(('camera_mode', False, None))
                else:
                    seek_view_guard.install()
                    client = ControlClient()
                    if action == 'focus':
                        target_position(client, name)
                    attach(client, replay_kind=kind)
                    data = session(client)
                    native_command(client, data, 'free', replay_kind=kind)
                    events.put(('camera_mode', True, client.pid))
                    if action == 'focus':
                        focus_command(client, data, 'focus', name, replay_kind=kind)
                        text = (
                            f'Focused on {name}. One-shot move; not following.\n'
                            'You can choose another player and Focus again, or use Seek. Press SPACE in-game to toggle Play/Pause.'
                        )
                    else:
                        text = (
                            'Free camera is active. Return to the game for mouse/WASD (press SPACE in-game to toggle Play/Pause).\n'
                            'Focus moves once; Follow overrides camera pose until Stop Follow.'
                        )
                events.put(('camera_done', text, False))
            except Exception as exc:
                events.put((
                    'camera_done',
                    'Stopped: ' + str(exc) + '\nDo not repeat timed-out commands. Use Restore player view or leave the window open for diagnosis.',
                    True,
                ))
            finally:
                if client:
                    client.close()

        threading.Thread(target=worker, daemon=True).start()

    def start_follow():
        nonlocal follow_thread
        if camera_busy or follow_thread is not None:
            return
        name = player.get()
        stop_event.clear()
        message.set(f'Starting Follow: {name}…')

        def worker():
            try:
                kind = 'timeline'
                seek_view_guard.install()
                run_follow(name, stop_event, lambda event: events.put(('follow_event', event)), replay_kind=kind)
            except Exception as exc:
                events.put(('follow_error', str(exc)))
            finally:
                events.put(('follow_finished',))

        follow_thread = threading.Thread(target=worker, daemon=True)
        follow_thread.start()
        controls()

    def stop_follow():
        if follow_thread is not None:
            stop_event.set()
            message.set('Stopping Follow after the current acknowledged camera command…')

    def playback(action, frame=0, resume_after=False):
        nonlocal aux_busy
        if aux_busy or camera_busy:
            return
        if action == 'play' and not can_play():
            message.set('Choose a Demo, click Start Replay and wait for the server to be ready before Play.')
            return
        aux_busy = True
        controls()
        message.set('Working… do not repeat commands or use another controller.')

        def worker():
            try:
                target_f = frame
                if action == 'step':
                    cur = timeline_operate('status')
                    was_playing = not bool(cur['paused'])
                    target_f = max(0, min(limit, cur['frame'] + frame))
                    state = timeline_operate('seek', target_f)
                    if was_playing and target_f < limit:
                        state = timeline_operate('play')
                else:
                    state = timeline_operate(action, target_f)
                    if action == 'seek' and resume_after and target_f < limit:
                        state = timeline_operate('play')
                sec = state['frame'] / FPS
                if action in ('seek', 'step'):
                    skipped = state.get('seek_guard', {}).get('null_view_updates_skipped', 0)
                    mode_txt = 'Paused' if state['paused'] else 'Playing'
                    text = f"Seek -> {format_mmss(sec)} ({sec:.1f}s, frame {state['frame']}). {mode_txt}."
                    if skipped:
                        text += f" (guard skipped={skipped})"
                else:
                    mode = 'Paused' if state['paused'] else 'Playing'
                    text = f"{mode} — {format_mmss(sec)} ({sec:.1f}s, frame {state['frame']})"
                events.put(('playback_done', text, state))
            except Exception as exc:
                events.put(('playback_done', 'Playback command stopped: ' + str(exc), None))

        threading.Thread(target=worker, daemon=True).start()

    def seek_to_entry():
        try:
            frame = seek_frame(target_time.get(), limit)
        except ValueError as exc:
            messagebox.showerror('Seek', str(exc))
            return
        playback('seek', frame)

    def poll_status_worker():
        try:
            kind = 'timeline'
            cam_active = False
            cam_pid = None
            if STATE.exists():
                try:
                    data = json.loads(STATE.read_text(encoding='utf-8'))
                    cam_pid = data.get('pid')
                    c_ro = Client()
                    try:
                        if c_ro.pid == cam_pid:
                            st = status(c_ro, data)
                            cam_active = bool(st.get('hook_installed') and st.get('enabled') and st.get('actual_mode') == 0 and st.get('free_camera_is_global_active'))
                    finally:
                        c_ro.close()
                except Exception:
                    pass
            state = timeline_operate('status')
            events.put(('status_poll', kind, state, None, cam_active, cam_pid))
        except Exception as exc:
            events.put(('status_poll', 'unknown', None, str(exc), False, None))

    def poll():
        nonlocal camera_busy, aux_busy, status_pending, last_status_tick, follow_thread, pending_restore, close_after
        nonlocal free_cam_active, client_pid, replay_paused, current_frame, limit, space_was_down, left_was_down, right_was_down
        nonlocal server_ready, active_recording
        try:
            while True:
                event = events.get_nowait()
                kind = event[0]
                if kind == 'camera_mode':
                    free_cam_active = bool(event[1])
                    if event[2] is not None:
                        client_pid = event[2]
                elif kind == 'switch_done':
                    aux_busy = False
                    free_cam_active = False
                    server_ready = False
                    res, err = event[1], event[2]
                    if err:
                        text = 'Recording switch failed: ' + err
                        message.set(text)
                        log_ui('switch_error', text, error=True)
                    else:
                        limit = res['limit_frame']
                        active_recording = res['recording_base']
                        update_help_text()
                        text = (
                            f"Server started {res['recording_base']} ({res['level']}, PID {res['pid']}).\n"
                            "Join 'Local Replay' in BC2 server browser, then click Play."
                        )
                        message.set(text)
                        log_ui('switch_done', text, error=False)
                elif kind == 'camera_done':
                    camera_busy = False
                    message.set(event[1])
                    log_ui('camera_done', event[1], error=event[2])
                    if event[2]:
                        if close_after and messagebox.askyesno(
                            'Camera recovery could not be confirmed',
                            event[1] + '\n\nClose the controller anyway? If BC2 is still running, '
                            'restart it to clear any remaining camera hook. Backend/server stay running.',
                            parent=window,
                        ):
                            stop_event.set()
                            window.destroy()
                            return
                        pending_restore = False
                        close_after = False
                    elif close_after and not pending_restore:
                        window.destroy()
                        return
                elif kind == 'follow_event':
                    state = event[1]
                    mode = state['state']
                    name = state['player']
                    free_cam_active = True
                    if mode == 'following':
                        message.set(
                            f"Following {name} — {state['updates']} camera updates.\n"
                            "Stop Follow to move manually. SPACE = Play/Pause, Arrows = -1s/+1s, Shift+Arrows = -5s/+5s."
                        )
                    elif mode == 'stopped':
                        message.set(f"Follow stopped after {state['updates']} updates. Free camera stays at its last position.")
                    elif mode == 'waiting_for_target':
                        message.set(
                            f'Waiting for {name} soldier link (death/respawn/seek resync may cause this).\n'
                            'No pose writes while unavailable. Will retry automatically. Stop Follow remains available.'
                        )
                    else:
                        message.set('Waiting for native camera transition; no pose writes.')
                elif kind == 'follow_error':
                    text = 'Follow stopped: ' + event[1] + '\nCamera stays where it is. Use Restore player view for recovery.'
                    message.set(text)
                    log_ui('follow_error', text, error=True)
                elif kind == 'follow_finished':
                    follow_thread = None
                elif kind == 'playback_done':
                    aux_busy = False
                    text, tl_state = event[1], event[2]
                    log_ui('playback_done', text, error=text.startswith('Playback command stopped:'))
                    if tl_state is not None:
                        replay_paused = bool(tl_state['paused'])
                        current_frame = tl_state['frame']
                        mode = 'Initializing' if not tl_state['ready'] else ('Paused' if tl_state['paused'] else 'Playing')
                        sec = tl_state['frame'] / FPS
                        playback_status.set(
                            f"Timeline: {mode} | {format_mmss(sec)} ({sec:.1f}s) | frame {tl_state['frame']} | {tl_state['connections']} viewer(s)"
                        )
                    else:
                        if text.startswith('Paused'):
                            replay_paused = True
                        elif text.startswith('Playing'):
                            replay_paused = False
                        playback_status.set(text)
                    if follow_thread is None:
                        message.set(text)
                elif kind == 'status_poll':
                    status_pending = False
                    srv_kind, tl_state, err, cam_act, cam_p = event[1], event[2], event[3], event[4], event[5]
                    free_cam_active = cam_act
                    if cam_p is not None:
                        client_pid = cam_p
                    if err:
                        server_ready = False
                        playback_status.set('Server status unavailable: ' + err)
                        log_ui('status_poll_error', err, error=True)
                    elif srv_kind == 'timeline' and tl_state is not None:
                        server_ready = bool(tl_state['ready'] and tl_state['active'] and tl_state['initialized'])
                        replay_paused = bool(tl_state['paused'])
                        current_frame = tl_state['frame']
                        mode = 'Initializing' if not tl_state['ready'] else ('Paused' if tl_state['paused'] else 'Playing')
                        sec = tl_state['frame'] / FPS
                        playback_status.set(
                            f"Timeline: {mode} | {format_mmss(sec)} ({sec:.1f}s) | frame {tl_state['frame']} | {tl_state['connections']} viewer(s)"
                        )
                    else:
                        server_ready = False
                        playback_status.set('Timeline status unavailable.')
                controls()
        except queue.Empty:
            pass

        # In-game hotkeys while Free Camera is active:
        # SPACE = Play/Pause, LEFT/RIGHT = -1s/+1s, SHIFT+LEFT/RIGHT = -5s/+5s
        space_down = bool(u32_dll.GetAsyncKeyState(VK_SPACE) & 0x8000)
        shift_down = bool(u32_dll.GetAsyncKeyState(VK_SHIFT) & 0x8000)
        left_down = bool(u32_dll.GetAsyncKeyState(VK_LEFT) & 0x8000)
        right_down = bool(u32_dll.GetAsyncKeyState(VK_RIGHT) & 0x8000)
        step_frames = STEP_LARGE_FRAMES if shift_down else STEP_SMALL_FRAMES
        if free_cam_active and client_pid and not aux_busy and not camera_busy:
            hwnd = u32_dll.GetForegroundWindow()
            if hwnd:
                fg_pid = c.wintypes.DWORD(0)
                u32_dll.GetWindowThreadProcessId(hwnd, c.byref(fg_pid))
                if fg_pid.value == client_pid:
                    if space_down and not space_was_down:
                        playback('play' if replay_paused else 'pause')
                    elif left_down and not left_was_down:
                        playback('step', -step_frames)
                    elif right_down and not right_was_down:
                        playback('step', step_frames)
        space_was_down = space_down
        left_was_down = left_down
        right_was_down = right_down

        last_status_tick += 1
        if not aux_busy and not camera_busy and not status_pending and last_status_tick >= 8:
            last_status_tick = 0
            status_pending = True
            threading.Thread(target=poll_status_worker, daemon=True).start()

        if pending_restore and follow_thread is None and not camera_busy and not aux_busy:
            start_camera('restore', closing=close_after)
        window.after(60, poll)

    # Populate Recording Selector buttons
    switch_btn = ttk.Button(rec_row, text='Switch Recording', command=start_switch_recording)
    switch_btn.pack(side='left', padx=4)
    buttons['switch_rec'] = switch_btn

    refresh_rec_btn = ttk.Button(rec_row, text='Refresh list', command=refresh_recordings_list)
    refresh_rec_btn.pack(side='left', padx=4)
    buttons['refresh_rec'] = refresh_rec_btn

    refresh_players_btn = ttk.Button(cam_top, text='Refresh players', command=refresh_players_action)
    refresh_players_btn.pack(side='left', padx=6)
    buttons['refresh_players'] = refresh_players_btn

    # Populate Timeline buttons
    for label, action in [('Play', 'play'), ('Pause', 'pause')]:
        btn = ttk.Button(tl_row, text=label, command=lambda a=action: playback(a))
        btn.pack(side='left', padx=2)
        buttons[action] = btn

    for key, label, delta in [
        ('step_back_5', '-5s', -STEP_LARGE_FRAMES),
        ('step_back_1', '-1s', -STEP_SMALL_FRAMES),
        ('step_fwd_1', '+1s', STEP_SMALL_FRAMES),
        ('step_fwd_5', '+5s', STEP_LARGE_FRAMES),
    ]:
        btn = ttk.Button(tl_row, text=label, width=4, command=lambda d=delta: playback('step', d))
        btn.pack(side='left', padx=2)
        buttons[key] = btn

    ttk.Label(tl_row, text='Time:').pack(side='left', padx=(6, 2))
    ttk.Entry(tl_row, textvariable=target_time, width=9).pack(side='left', padx=2)

    seek_btn = ttk.Button(tl_row, text='Seek and pause', command=seek_to_entry)
    seek_btn.pack(side='left', padx=2)
    buttons['seek'] = seek_btn

    rewind_btn = ttk.Button(tl_row, text='Rewind to start', command=lambda: playback('seek', 0))
    rewind_btn.pack(side='left', padx=2)
    buttons['rewind'] = rewind_btn

    # Populate Camera buttons
    for row, label, action in [
        (cam_row1, 'Enable free camera', 'free'),
        (cam_row1, 'Focus', 'focus'),
        (cam_row1, 'Follow (experimental)', 'follow'),
        (cam_row1, 'Stop Follow', 'stop'),
        (cam_row2, 'Restore player view', 'restore'),
        (cam_row2, 'Status', 'status'),
    ]:
        cmd = (
            start_follow if action == 'follow'
            else stop_follow if action == 'stop'
            else lambda a=action: start_camera(a)
        )
        btn = ttk.Button(row, text=label, command=cmd)
        btn.pack(side='left', padx=4)
        buttons[action] = btn

    def on_close():
        nonlocal pending_restore, close_after
        if (aux_busy or camera_busy) and not messagebox.askyesno(
            'Command pending', 'A command may still be in flight. Close and restore camera?'
        ):
            return
        pending_restore = True
        close_after = True
        if follow_thread is not None:
            stop_follow()
        elif not camera_busy and not aux_busy:
            start_camera('restore', closing=True)

    window.protocol('WM_DELETE_WINDOW', on_close)
    selected_recording.trace_add('write', lambda *_: controls())
    refresh_recordings_list()
    controls()
    window.after(60, poll)
    window.mainloop()
