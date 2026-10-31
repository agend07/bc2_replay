"""Unified local replay controls (Setup + Recording Selector + Timeline seek + Camera Free/Focus/Follow).
Opening this window makes no server or camera writes. Play/Pause/step live on the in-game hotkeys (poll()).
"""
import ctypes as c
import datetime
import json
import queue
import threading
import time  # r222
import tkinter as tk
from tkinter import ttk, messagebox

from replay.client import Client, client_pids
from preview_config import APP_ROOT as ROOT
from replay.camera_native import ControlClient
from replay.players import roster
from replay.camera import (
    STATE, attach, session,
    status, native_command, focus_command, target_position,
    TargetUnavailable, probe,  # r222
)
from replay.follow import run_follow
from replay import seek_guard as seek_view_guard
from replay import clean_screen  # r219: HUD/blood off in free cam
from replay.tooltip import tip  # r286: hover help
from replay import destruction_reset  # r286: repair walls/houses on backward seeks
from replay.timeline import (
    operate as timeline_operate, limit_frame, FPS,
    list_recordings, active_demo, switch_recording, key_frames,
    seek_keeping_destruction,  # r286
)
from replay.ui_logic import (format_mmss, parse_time_entry, should_auto_launch, cycle_player,
                             follow_button_text, FOLLOW_TEXT, AutoPlay, PlayWatch, PLAY_REJECTED_TEXT)
from replay import vcr_overlay  # VCR time label over the game window
from replay.look_target import LookTarget, clamp_distance, DEFAULT_DISTANCE_M, DISTANCE_RANGE_M  # FREE view: looked-at player name on the VCR label
from replay import owned  # client/server this panel started: stopped when it closes
from replay import load_gap  # r242 C: skip the empty load gap after a join
from replay import pov as pov_mod  # r234: players.json (names, viewer, game start) when present; mmss / stem_of
from replay import recordings  # step 0: Recordings window (client captures -> published demo)
from preview_config import server_dir as cfg_server_dir, settings as cfg_settings  # r234
import preview_config  # Recordings window: save_settings (raw capture retention)

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
VK_F = 0x46  # r238: toggle free camera
VK_OEM_COMMA = 0xBC  # ',' / '<': Focus the previous player
VK_OEM_PERIOD = 0xBE  # '.' / '>': Focus the next player
STEP_SMALL_FRAMES = 3 * FPS
STEP_LARGE_FRAMES = 10 * FPS
ROSTER_RETRY_S = 10  # retry reading the player roster from client memory while it is empty
TIME_HINT = 'Enter = seek'
ROSTER_REFRESH_S = 120  # re-read it this often while a viewer is connected (late joiners)
LOOK_PERIOD_S = 0.22  # looked-at player: ~4.5 Hz, only in FREE view with the option on


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


def format_recording_label(item):
    lvl_short = item['level'].replace('Levels/', '')
    return f"{item['name']}  [{lvl_short} | {format_mmss(item['seconds'])}]"


HELP_TEXT = (
    'How to use\n'
    '  1. Choose a Demo, click ▶ (play). When the server is ready BC2 starts and joins\n'
    '     Local Replay automatically (a running BC2 is closed and started again).\n'
    '  2. Playback starts by itself once BC2 is in the map (SPACE in the game pauses). Demos with an empty\n'
    '     load gap (e.g. ~1:07 before anything moves) are skipped to ~3 s before the game starts once\n'
    '     the game accepts seeks (once per join; not if you seek first).\n'
    '  3. Type a time (seconds, mm:ss or hh:mm:ss) and press Enter to seek; seeking leaves playback paused.\n'
    '     A plain seek does not move destruction with it (going back repairs walls and houses).\n'
    '     Keep destruction: seeks (also the in-game arrows) play there fast instead, up to 50x; going\n'
    '     back repairs first and replays from 0:00 (about 1.3 s per minute of recording).\n'
    '     Hover over any control for a short explanation.\n'
    '\n'
    'In-game keys (BC2 window in front, server ready)\n'
    '  F                     free camera on/off (off = back to the normal player view)\n'
    '  SPACE                 play / pause\n'
    '  LEFT / RIGHT          -3 s / +3 s\n'
    '  SHIFT+LEFT / RIGHT    -10 s / +10 s\n'
    '  < / >  (, / .)        Focus the previous / next player (alive ones, Player list order)\n'
    '\n'
    'Free camera: WASD move, mouse look, hold SHIFT to move faster. HUD and blood are hidden.\n'
    '             Left / right mouse button = up / down.\n'
    '\n'
    'Focus: one-shot camera move to the chosen player (starts the free camera if it is off).\n'
    'Follow (experimental): camera tracks the player at a fixed offset; click Stop follow to end.\n'
    'Show looked-at player: name of the player at the screen centre within max m (10-300).\n'
    '\n'
    'Recordings...: one row per round of each client capture (grey = under 30 s). ▶ Play plays\n'
    'the round\'s demo, or builds it first (a few minutes) and then plays it; Rebuild builds it\n'
    'again. One build at a time, refused while the replay server or BC2 started here runs. New raw\n'
    'recorder files are extracted automatically about a minute after recording stops; the list\n'
    'refreshes every 5 s. Captures contain every player name and chat; conversion never emits chat.\n'
    '\n'
    'Log in to BC2 with any dummy email/password, never your real account.\n'
    'Closing this window stops the replay server and the BC2 it launched (a BC2 you started\n'
    'yourself keeps running, its camera restored).'
)


def show_help(window):
    top = tk.Toplevel(window)
    top.title('Help')
    top.transient(window)
    top.resizable(False, False)
    ttk.Label(top, text=HELP_TEXT, justify='left', font=('Consolas', 9)).pack(padx=14, pady=(12, 6))
    ttk.Button(top, text='Close', command=top.destroy).pack(pady=(0, 10))
    top.bind('<Escape>', lambda _e: top.destroy())
    top.focus_set()


_recordings_win = {'top': None, 'busy': False}  # step 0: one Recordings window, one build/extract at a time
RECORDINGS_POLL_MS = 5000  # r287: list refresh while the window is shown and idle


def show_recordings(window, events):
    """'Recordings (client captures)': one row per round of each capture (v3_captures.py list, r287 rounds).
    ▶ Play plays the round's published demo, or builds & publishes it (--round K) and then plays it; Rebuild builds
    again. Settled raw .bc2raw files are extracted automatically, one at a time; the list refreshes every 5 s while
    idle. Builds and extracts are refused while the panel runs a server/client. A published stem goes to the main
    window as ('recordings_demo', stem) (select only); play posts ('recordings_play', stem) (select + start the
    replay without the dialog) and hides this window. A progress bar follows the builder's PROGRESS / ESTIMATE lines."""
    top = _recordings_win['top']
    if top is not None and top.winfo_exists():
        top.deiconify()
        top.lift()
        top.focus_set()
        return
    top = tk.Toplevel(window)
    _recordings_win['top'] = top
    top.title('Recordings (client captures)')
    top.minsize(640, 380)
    log_ui('recordings_open', '')
    lines = queue.Queue()
    rows = {}  # tree iid (recordings.row_key) -> round row
    # just_built: row key -> new stem; tried: raw paths auto-extracted in this window (never retried)
    state = {'listing': False, 'started': None, 'key': None, 'just_built': {}, 'action': None, 'play_after': False,
             'tried': set(), 'sig': None, 'sel': None, 'list_err': None, 'wait_msg': None, 'select_dir': None,
             'raw_sel': None}
    # progress bar: last PROGRESS (phase i/k, its start), ESTIMATE, bar value (never goes back), end state
    prog = {'phase': None, 'i': 0, 'k': 0, 'since': None, 'estimate': None, 'phase_est': {}, 'frac': 0.0,
            'end': None}
    status = tk.StringVar(value='Reading captures…')
    elapsed = tk.StringVar(value='')
    prog_text = tk.StringVar(value='')

    ttk.Label(top, text=recordings.PRIVACY_NOTE, foreground='gray').pack(padx=10, pady=(8, 2), anchor='w')
    cols = (('name', 'Name', 220), ('recorded', 'Recorded', 110), ('level', 'Level', 90), ('length', 'Length', 55),
            ('packets', 'Packets', 60), ('drops', 'Drops', 45), ('status', 'Status', 200))
    tree_frame = ttk.Frame(top)
    tree_frame.pack(padx=10, pady=4, fill='both', expand=True)
    tree = ttk.Treeview(tree_frame, columns=[c_[0] for c_ in cols], show='headings', height=7, selectmode='browse')
    for key, text, width in cols:
        tree.heading(key, text=text, anchor='w')
        tree.column(key, width=width, anchor='w', stretch=key in ('name', 'status'))
    tree.tag_configure('short', foreground='gray')  # rounds under 30 s
    tree_sb = ttk.Scrollbar(tree_frame, orient='vertical', command=tree.yview)
    tree.config(yscrollcommand=tree_sb.set)
    tree.pack(side='left', fill='both', expand=True)
    tree_sb.pack(side='right', fill='y')

    row = ttk.Frame(top)
    row.pack(padx=10, pady=4, fill='x')
    play_btn = ttk.Button(row, text='▶ Play')
    rebuild_btn = ttk.Button(row, text='Rebuild')
    for b in (play_btn, rebuild_btn):
        b.pack(side='left', padx=(0, 6))
    tip(play_btn, 'Play the selected round in the main window. Without a demo yet it is built first (a few '
                  'minutes) and then played.')
    tip(rebuild_btn, 'Build the selected round again and publish the new demo to demos/ (a few minutes). '
                     'Nothing is played.')
    ttk.Label(row, textvariable=elapsed).pack(side='right')
    # raw capture retention (preview.json "captures"); nothing is deleted before Clean up… and its confirmation
    try:
        ret = recordings.retention(cfg_settings())
    except Exception:
        ret = recordings.retention({})
    keep_var = tk.StringVar(value=str(ret['keep_raws']))
    extracted_var = tk.BooleanVar(value=ret['delete_extracted'])
    ret_row = ttk.Frame(top)
    ret_row.pack(padx=10, pady=(0, 4), fill='x')
    ttk.Label(ret_row, text='Raw files: keep newest').pack(side='left')
    keep_spin = ttk.Spinbox(ret_row, from_=0, to=recordings.KEEP_MAX, width=4, textvariable=keep_var)
    keep_spin.pack(side='left', padx=(4, 4))
    tip(keep_spin, 'How many of the newest raw files Clean up… keeps (0 = keep all). Raw files take about 6.7 MB '
                   'per minute.')
    ttk.Label(ret_row, text='(0 = all)').pack(side='left', padx=(0, 12))
    extracted_chk = ttk.Checkbutton(ret_row, text='delete raws already extracted', variable=extracted_var)
    extracted_chk.pack(side='left', padx=(0, 12))
    tip(extracted_chk, 'Clean up… may also delete raw files that were already extracted into a capture folder.')
    cleanup_btn = ttk.Button(ret_row, text='Clean up…')
    cleanup_btn.pack(side='left')
    tip(cleanup_btn, 'List the raw files the setting on the left would remove, with sizes. Nothing is deleted '
                     'before you confirm.')
    prog_row = ttk.Frame(top)
    prog_row.pack(padx=10, pady=(0, 4), fill='x')
    prog_bar = ttk.Progressbar(prog_row, mode='determinate', maximum=1000)
    prog_bar.pack(side='left', fill='x', expand=True)
    ttk.Label(prog_row, textvariable=prog_text, width=30).pack(side='left', padx=(8, 0))
    ttk.Label(top, textvariable=status, wraplength=620, justify='left').pack(padx=10, pady=(0, 2), fill='x')

    log_frame = ttk.Frame(top)
    log_frame.pack(padx=10, pady=(2, 10), fill='both', expand=True)
    log_box = tk.Text(log_frame, height=10, wrap='none', font=('Consolas', 9), state='disabled')
    log_sb = ttk.Scrollbar(log_frame, orient='vertical', command=log_box.yview)
    log_box.config(yscrollcommand=log_sb.set)
    log_box.pack(side='left', fill='both', expand=True)
    log_sb.pack(side='right', fill='y')

    def append(text):
        log_box.config(state='normal')
        log_box.insert('end', text + '\n')
        log_box.see('end')
        log_box.config(state='disabled')

    def selected():
        sel = tree.selection()
        return rows.get(sel[0]) if sel else None

    def set_buttons(*_):
        busy = _recordings_win['busy']
        r = selected()
        play_ok = bool(r) and (recordings.row_play_stem(r, state['just_built']) or recordings.can_build(r))
        play_btn.config(state='normal' if not busy and play_ok else 'disabled')
        rebuild_btn.config(state='normal' if not busy and recordings.can_build(r) else 'disabled')
        cleanup_btn.config(state='disabled' if busy else 'normal')

    def describe(r):
        """status line for a selected row"""
        if recordings.is_raw(r['item']):
            return f"{r['name']}: raw capture, extracted automatically about a minute after recording stops."
        if r['short']:
            return f"{r['name']}: shorter than {recordings.MIN_ROUND_S} s, not worth a demo."
        stem = recordings.row_play_stem(r, state['just_built'])
        if stem:
            return f'▶ Play plays {stem}.'
        if recordings.can_build(r):
            return f"{r['name']}: no demo yet; ▶ Play builds it (a few minutes) and then plays it."
        return f"{r['name']}: {recordings.row_status(r)}"

    def on_select(*_):
        set_buttons()
        sel = tree.selection()
        key = sel[0] if sel else None
        if key == state['sel']:  # same row re-selected by a list refresh: keep the status line
            return
        state['sel'] = key
        r = selected()
        if r and not _recordings_win['busy']:
            status.set(describe(r))

    def play(stem):
        if not stem:
            return
        log_ui('recordings_play', stem)
        events.put(recordings.play_event(stem))  # main window: select it and start the replay (no dialog)
        top.withdraw()  # Recordings... shows it again (deiconify)
        try:
            window.deiconify()
            window.lift()
            window.focus_force()
        except tk.TclError:
            pass

    def play_clicked():
        r = selected()
        if not r or _recordings_win['busy']:
            return
        stem = recordings.row_play_stem(r, state['just_built'])
        if stem:
            play(stem)
        elif recordings.can_build(r):
            start_build(r, play_after=True)

    def progress_reset():
        prog.update(phase=None, i=0, k=0, since=None, estimate=None, phase_est={}, frac=0.0, end=None)
        prog_bar['value'] = 0
        prog_text.set('starting')

    def progress_tick():
        """every 500 ms: bar from the last PROGRESS event, its elapsed time and the ESTIMATE (recordings helper)."""
        if not top.winfo_exists():
            return
        if state['started'] is not None and prog['end'] is None:
            now = time.monotonic()
            if prog['i']:
                names = recordings.phase_plan(prog['k'])
                weights, total = recordings.run_weights(names, prog['phase_est'])  # last build's phase times if any
                prog['frac'] = recordings.progress_fraction(prog['i'], prog['k'], now - prog['since'],
                                                            total or prog['estimate'], weights, floor=prog['frac'])
            prog_bar['value'] = int(prog['frac'] * 1000)
            prog_text.set(recordings.progress_label(prog['phase'], now - state['started'], prog['estimate']))
        top.after(500, progress_tick)

    def list_worker(quiet):
        try:
            items = recordings.list_captures(cfg_settings())
            items = recordings.visible_captures(items, preview_config.raw_config())  # preview.json "recordings_hide"
            lines.put(('list', items, None, quiet))
        except Exception as exc:
            lines.put(('list', None, str(exc), quiet))

    def refresh(quiet=True):
        """quiet: keep the status line (5 s poll, after a build/extract); the first list sets a summary."""
        if state['listing'] or _recordings_win['busy']:
            return
        state['listing'] = True
        if not quiet:
            status.set('Reading captures…')
        threading.Thread(target=list_worker, args=(quiet,), daemon=True).start()

    def poll():
        """every RECORDINGS_POLL_MS while the window exists: refresh when shown and idle (refresh skips otherwise)."""
        if not top.winfo_exists():
            return
        try:
            shown = top.state() != 'withdrawn'
        except tk.TclError:
            shown = False
        if shown:
            refresh()
        top.after(RECORDINGS_POLL_MS, poll)

    def show_list(items, err, quiet):
        if err:
            if err != state['list_err']:  # the 5 s poll logs a lasting failure once
                status.set('Capture list failed: ' + err)
                log_ui('recordings_list', err, error=True)
            state['list_err'] = err
            return
        state['list_err'] = None
        sig = json.dumps(items, sort_keys=True, default=str)
        if sig != state['sig']:  # unchanged list: no redraw (selection and scroll stay put)
            state['sig'] = sig
            keep = tree.selection()
            y = tree.yview()[0]
            tree.delete(*tree.get_children())
            rows.clear()
            for r in recordings.rows_of(items):  # newest recording first, its rounds in order
                rows[r['key']] = r
                tree.insert('', 'end', iid=r['key'], values=recordings.format_row(r, state['just_built']),
                            tags=('short',) if r['short'] else ())
            pick = keep[0] if keep and keep[0] in rows else None
            if pick is None and state['select_dir']:  # the selected raw was extracted: select its first round
                pick = next((k for k, r in rows.items() if str(r['item'].get('dir')) == state['select_dir']), None)
            state['select_dir'] = None
            if pick:
                tree.selection_set(pick)
            tree.yview_moveto(y)
        if not quiet:
            caps = sum(1 for i in items if not recordings.is_raw(i))
            raw = sum(1 for i in items if recordings.is_raw(i))
            status.set(f'{len(rows) - raw} round(s) from {caps} capture(s)' +
                       (f', {raw} raw file(s) to extract' if raw else '') + '. Select a round and click ▶ Play.')
            r = selected()
            if r:
                status.set(status.get() + '  ' + describe(r))
        auto_extract(items)

    def refusal():
        """the live-test refusal text, or None (read-only query of the processes this panel started)"""
        try:
            owned_state = {kind: owned.alive(rec) for kind, rec in owned.load_all().items()}
            return recordings.busy_reason(owned_state)
        except Exception as exc:
            return f'Could not check the replay server / BC2 client started by the panel: {exc}'

    def start_build(r, play_after=False):
        item = r['item'] if r else None
        if not item or _recordings_win['busy']:
            return
        reason = refusal()
        if reason:
            append('Refused: ' + reason)
            log_ui('recordings_refused', reason, error=True)
            messagebox.showwarning('Build refused', reason, parent=top)
            return
        if play_after:
            what = f"{r['name']} has no demo yet. Build it now and play it when the build finishes?"
        else:
            what = f"Build {r['name']} again and publish the new demo to demos/?"
        if not messagebox.askyesno('Recordings', what + '\nA build takes a few minutes.', parent=top):
            return
        if _recordings_win['busy']:  # an auto-extract started while the dialog was open
            status.set('A raw file is being extracted; try again in a moment.')
            return
        round_k = r['round']
        _recordings_win['busy'] = True
        state.update(started=time.monotonic(), key=r['key'], action='build', play_after=play_after)
        progress_reset()
        set_buttons()
        append('Build & publish: ' + str(item['dir']) + (f' (round {round_k})' if round_k is not None else ''))
        status.set('Building…' + (' The demo plays when the build finishes.' if state['play_after'] else ''))
        log_ui('recordings_build', json.dumps({'dir': item['dir'], 'round': round_k}))

        def worker():
            try:
                def on_line(p):
                    lines.put(('line', p))
                    if p[0] == 'demo':
                        events.put(('recordings_demo', p[1]))  # main window: refresh the Demo list, select it
                res = tuple(recordings.run_build(cfg_settings(), item['dir'], on_line, round_k=round_k))
            except Exception as exc:
                res = (False, None, str(exc))
            _recordings_win['busy'] = False  # before 'done', so its list refresh is not skipped
            lines.put(('done',) + res)

        threading.Thread(target=worker, daemon=True).start()

    def auto_extract(items):
        """main thread, after a list: extract the oldest settled raw (raws_to_extract), one at a time, never during a
        build or a live test; a failed raw is not retried in this window."""
        if _recordings_win['busy'] or state['listing']:
            return
        try:
            if top.state() == 'withdrawn':  # hidden after play: the replay may be starting
                return
        except tk.TclError:
            return
        todo = recordings.raws_to_extract(items, time.time(), state['tried'])
        if not todo:
            return
        reason = refusal()
        if reason:
            msg = 'Raw files wait for extraction: ' + reason
            if msg != state['wait_msg']:
                state['wait_msg'] = msg
                append(msg)
            return
        state['wait_msg'] = None
        item = todo[0]
        state['tried'].add(item['path'])
        sel = selected()
        state['raw_sel'] = item['path'] if sel and sel['item'].get('path') == item['path'] else None
        _recordings_win['busy'] = True
        state.update(started=time.monotonic(), key=None, action='extract', play_after=False)
        progress_reset()
        set_buttons()
        append('Extract: ' + str(item['path']))
        status.set(f"Extracting {item.get('name')}…")
        log_ui('recordings_extract', json.dumps({'path': item['path']}))

        def worker():
            try:
                ok, out, text = recordings.run_extract(cfg_settings(), item['path'], lambda p: lines.put(('line', p)))
                res = (ok, out, text)
            except Exception as exc:
                res = (False, None, str(exc))
            _recordings_win['busy'] = False
            lines.put(('done',) + res)

        threading.Thread(target=worker, daemon=True).start()

    # ---- raw capture retention: setting row + Clean up… (dry run, confirmation, then the real prune) ----
    def current_retention():
        return recordings.retention({recordings.RETENTION_KEY: {'keep_raws': keep_var.get(),
                                                                'delete_extracted': bool(extracted_var.get())}})

    state['ret_saved'] = dict(ret)

    def save_retention(*_):
        ret_now = current_retention()
        if keep_var.get() != str(ret_now['keep_raws']):
            keep_var.set(str(ret_now['keep_raws']))  # junk / out of range -> the clamped value
        if ret_now == state['ret_saved']:
            return
        try:
            try:
                cfg = cfg_settings()
            except Exception:
                cfg = {}
            preview_config.save_settings(recordings.retention_update(cfg, ret_now['keep_raws'],
                                                                     ret_now['delete_extracted']))
            state['ret_saved'] = ret_now
            log_ui('recordings_retention', json.dumps(ret_now))
        except Exception as exc:
            status.set(f'Could not save the raw file setting to preview.json: {exc}')
            log_ui('recordings_retention', str(exc), error=True)

    def start_cleanup():
        """'v3_captures.py prune --dry-run' first; the user confirms the exact list before anything is deleted."""
        if _recordings_win['busy'] or state['listing']:
            status.set('Clean up waits until the current operation finishes.')
            return
        save_retention()
        ret_now = current_retention()
        if not recordings.prune_enabled(ret_now):
            messagebox.showinfo('Clean up raw captures',
                                'The current setting keeps every raw file, so there is nothing to clean up.\n\n'
                                'Set "keep newest" to 1 or more and/or tick "delete raws already extracted", then '
                                'click Clean up… again. You will see exactly which files would be deleted first.',
                                parent=top)
            return
        _recordings_win['busy'] = True
        try:
            set_buttons()
            policy = recordings.policy_text(ret_now['keep_raws'], ret_now['delete_extracted'])
            append('Clean up (dry run): ' + policy)
            status.set('Checking which raw files would be deleted…')
            log_ui('recordings_prune', json.dumps(dict(ret_now, dry_run=True)))

            def worker():
                try:
                    res = tuple(recordings.run_prune(cfg_settings(), ret_now['keep_raws'], ret_now['delete_extracted'],
                                                     True, lambda p: lines.put(('line', p))))
                except Exception as exc:
                    res = (False, [], str(exc))
                lines.put(('prune_dry', ret_now) + res)

            threading.Thread(target=worker, daemon=True).start()
        except Exception as exc:
            _recordings_win['busy'] = False
            status.set(f'Clean up failed: {exc}')
            log_ui('recordings_prune', str(exc), error=True)

    def prune_checked(ret_now, ok, entries, text):
        """main thread, after the dry run: nothing / confirm -> real prune; busy is cleared unless the real run starts."""
        started = False
        try:
            if not ok:
                msg = f'Clean up check failed: {text}'
                status.set(msg)
                append(msg)
                log_ui('recordings_prune', msg, error=True)
                return
            confirm = recordings.prune_confirm_text(entries, ret_now['keep_raws'], ret_now['delete_extracted'])
            if confirm is None:
                s = recordings.prune_summary(entries)
                msg = f"Nothing to delete ({s['keep']} kept, {s['skip']} skipped; {text})"
                status.set(msg)
                append(msg)
                log_ui('recordings_prune', msg)
                return
            if not messagebox.askyesno('Clean up raw captures', confirm, parent=top):
                status.set('Clean up cancelled; nothing was deleted.')
                append('Clean up cancelled; nothing was deleted.')
                log_ui('recordings_prune', 'cancelled')
                return
            append('Clean up: deleting')
            status.set('Deleting raw files…')
            log_ui('recordings_prune', json.dumps(dict(ret_now, dry_run=False)))

            def worker():
                try:
                    res = tuple(recordings.run_prune(cfg_settings(), ret_now['keep_raws'], ret_now['delete_extracted'],
                                                     False, lambda p: lines.put(('line', p))))
                except Exception as exc:
                    res = (False, [], str(exc))
                _recordings_win['busy'] = False  # before 'prune_done', so its list refresh is not skipped
                lines.put(('prune_done',) + res)

            threading.Thread(target=worker, daemon=True).start()
            started = True
        except Exception as exc:
            status.set(f'Clean up failed: {exc}')
            log_ui('recordings_prune', str(exc), error=True)
        finally:
            if not started:
                _recordings_win['busy'] = False
                set_buttons()

    def prune_done(ok, entries, text):
        msg = f'Clean up done: {text}' if ok else f'Clean up failed: {text}'
        status.set(msg)
        append(msg)
        log_ui('recordings_prune', msg, error=not ok)
        refresh()

    def done(ok, out, text):
        """main thread: a build / extract finished (out = published stem or extracted dir)."""
        action, play_after = state['action'], state['play_after']
        state.update(action=None, play_after=False)
        secs = int(time.monotonic() - (state['started'] or time.monotonic()))
        if action == 'extract':
            msg = f'Extracted in {secs} s: {text}' if ok else f'Extract failed after {secs} s: {text} (not retried)'
            if ok and out and state['raw_sel']:
                state['select_dir'] = str(out)
        else:
            stem = out if action == 'build' else None
            if ok and stem and state['key']:
                state['just_built'][state['key']] = stem
            msg = (f'Done in {secs} s: {text}' + (f' (new demo {stem})' if stem else '') if ok
                   else f'Build failed after {secs} s: {text}')
        state['raw_sel'] = None
        status.set(msg)
        append(msg)
        log_ui('recordings_done', msg, error=not ok)
        prog['end'] = 'ok' if ok else 'fail'
        if ok:
            prog['frac'] = 1.0
            prog_bar['value'] = 1000
            prog_text.set(f'done in {secs} s')
        else:  # the bar stays where it stopped
            prog_text.set(recordings.progress_label(prog['phase'], secs, failed=True))
        state['started'] = None
        state['sig'] = None  # redraw: the just-built stem shows in the row status
        refresh()
        if ok and play_after and out:
            play(out)

    def pump():
        if not top.winfo_exists():
            return
        try:
            while True:
                ev = lines.get_nowait()
                if ev[0] == 'list':
                    state['listing'] = False
                    show_list(ev[1], ev[2], ev[3])
                elif ev[0] == 'line':
                    p = ev[1]
                    if p[0] == 'step':
                        status.set('Step ' + p[1])
                        append('STEP ' + p[1])
                    elif p[0] == 'progress':  # bar only (progress_tick), not the log
                        prog.update(phase=p[1], i=p[2], k=p[3], since=time.monotonic())
                    elif p[0] == 'estimate':
                        prog['estimate'], prog['phase_est'] = p[1], p[2]
                        append(f'expected ~{int(p[1])} s')
                    elif p[0] == 'demo':
                        append('DEMO ' + p[1])
                        if state['key'] and state['action'] == 'build':
                            state['just_built'][state['key']] = p[1]
                    elif p[0] == 'result':
                        append('RESULT: ' + p[2])
                    elif p[0] == 'prune':
                        append(f'{p[1]} {p[2]} ({recordings.format_size(p[3])}) {p[4]}'.rstrip())
                    elif p[1]:
                        append(p[1])
                elif ev[0] == 'done':
                    done(*ev[1:])
                elif ev[0] == 'prune_dry':
                    prune_checked(*ev[1:])
                elif ev[0] == 'prune_done':
                    prune_done(*ev[1:])
        except queue.Empty:
            pass
        if state['started'] is not None:
            elapsed.set(f'elapsed {pov_mod.mmss(time.monotonic() - state["started"])}')
        set_buttons()
        top.after(150, pump)

    def on_close():
        if _recordings_win['busy']:
            messagebox.showinfo('Recordings', 'A build or extract is running. Wait for it to finish before closing '
                                              'this window.', parent=top)
            return
        _recordings_win['top'] = None
        top.destroy()

    play_btn.config(command=play_clicked)
    rebuild_btn.config(command=lambda: start_build(selected()))
    cleanup_btn.config(command=start_cleanup)
    keep_spin.config(command=save_retention)
    keep_spin.bind('<FocusOut>', save_retention)
    keep_spin.bind('<Return>', save_retention)
    extracted_chk.config(command=save_retention)
    tree.bind('<<TreeviewSelect>>', on_select)
    top.protocol('WM_DELETE_WINDOW', on_close)
    top.bind('<Escape>', lambda _e: on_close())
    refresh(quiet=False)
    top.after(150, pump)
    top.after(500, progress_tick)
    top.after(RECORDINGS_POLL_MS, poll)


def main():
    try:
        stale = owned.sweep_stale()  # left by a force-killed panel
        if stale:
            log_ui('owned_stale_sweep', json.dumps(stale))
    except Exception as exc:
        log_ui('owned_stale_sweep', str(exc), error=True)
    try:
        limit = limit_frame()
    except Exception:
        limit = 0

    window = tk.Tk()
    window.title('BC2 Local Replay ' + preview_config.VERSION + ' — controls')
    window.minsize(520, 0)  # height follows the content (Setup section expands/collapses)
    vcr_on = tk.BooleanVar(value=True)
    look_on = tk.BooleanVar(value=True)  # VCR label: looked-at player name in FREE view
    look_dist = tk.StringVar(value=str(int(DEFAULT_DISTANCE_M)))  # its max range in metres (this session only)
    vcr_overlay.start(window, borderless=preview_config.raw_config().get('client_borderless') is True)  # r291
    message = tk.StringVar(
        value='Ready. Opening this window makes no server or camera writes.\n'
              'Choose a Demo and click ▶ (play); BC2 starts and joins when the server is ready.'
    )
    from launcher_panel import add_launcher, wrap_to_width
    launch_game = add_launcher(window, on_paths_changed=lambda: refresh_recordings_list(),
                               on_status=message.set)

    playback_status = tk.StringVar(value='Reading server status…')
    player = tk.StringVar(value='')
    target_time = tk.StringVar(value='0:00')
    time_error = tk.StringVar(value='')
    keep_destruction = tk.BooleanVar(value=False)  # r286: seeks replay fast from 0 so earlier destruction shows
    selected_recording = tk.StringVar(value='')

    head = ttk.Frame(window)
    head.pack(fill='x', padx=16, pady=(8, 0))
    ttk.Label(head, text='BC2 Local Replay — Controller', font=('', 12, 'bold')).pack(side='left')
    tip(ttk.Button(head, text='\u24d8 Help', command=lambda: show_help(window)), 'Short how-to and the in-game keys.'
        ).pack(side='right')
    tip(ttk.Button(head, text='Recordings\u2026', command=lambda: show_recordings(window, events)),
        'Your own client captures: extract, build into demos, play.').pack(side='right', padx=(0, 6))
    wrap_to_width(ttk.Label(window, textvariable=playback_status, wraplength=480, justify='center',
                            anchor='center', font=('', 10, 'bold'))).pack(padx=16, pady=3, fill='x')

    # Recording Selector Frame
    rec_frame = ttk.LabelFrame(window, text='Server Recording (demos/)')
    rec_frame.pack(padx=16, pady=5, fill='x')
    rec_row = ttk.Frame(rec_frame)
    rec_row.pack(padx=8, pady=6, fill='x')
    ttk.Label(rec_row, text='Demo:').pack(side='left', padx=(2, 4))
    rec_selector = ttk.Combobox(rec_row, textvariable=selected_recording, state='readonly', width=28)
    rec_selector.pack(side='left', padx=4, fill='x', expand=True)

    # Timeline: typed seek (Enter), Launch client, VCR label
    tl_frame = ttk.LabelFrame(window, text='Timeline (in game: SPACE play/pause, arrows step)')
    tl_frame.pack(padx=16, pady=5, fill='x')
    tl_row = ttk.Frame(tl_frame)
    tl_row.pack(padx=8, pady=(7, 4), fill='x')
    tl_row2 = ttk.Frame(tl_frame)  # VCR label options
    tl_row2.pack(padx=8, pady=(0, 4), fill='x')

    # Camera row
    cam_frame = ttk.LabelFrame(window, text='Camera (in game: F free camera, < > previous / next player)')
    cam_frame.pack(padx=16, pady=5, fill='x')
    cam_row1 = ttk.Frame(cam_frame)
    cam_row1.pack(padx=8, pady=4, fill='x')
    ttk.Label(cam_row1, text='Player:').pack(side='left', padx=4)
    selector = ttk.Combobox(cam_row1, textvariable=player, values=[], state='readonly', width=14)
    selector.pack(side='left', padx=4, fill='x', expand=True)

    # r286: messages under a ▾/▸ toggle like Setup; the choice is kept in preview.json ('show_messages')
    msg_head = ttk.Frame(window)
    msg_head.pack(padx=16, pady=(5, 0), fill='x')
    msg_toggle = ttk.Button(msg_head, width=14)
    msg_toggle.pack(side='left')
    tip(msg_toggle, 'Show or hide the messages below (what the controller is doing, errors).')
    msg_label = wrap_to_width(ttk.Label(window, textvariable=message, wraplength=480, justify='left'))
    try:
        msg_shown = bool(json.loads(preview_config.CONFIG.read_text(encoding='utf-8')).get('show_messages', True))
    except Exception:
        msg_shown = True

    def show_messages(on, save=True):
        nonlocal msg_shown
        msg_shown = on
        msg_toggle.config(text='▾ Messages' if on else '▸ Messages')
        if on:
            msg_label.pack(padx=18, pady=(2, 8), fill='x', after=msg_head)
        else:
            msg_label.pack_forget()
        if save:
            try:
                preview_config.save_settings({'show_messages': on})
            except Exception:
                pass

    msg_toggle.config(command=lambda: show_messages(not msg_shown))
    show_messages(msg_shown, save=False)

    buttons = {}
    rec_map = {}
    events = queue.Queue()
    camera_busy = False
    aux_busy = False
    pending_step = 0  # r285: arrow steps pressed while a seek runs, sent as one seek when it ends
    destruction_ok = True  # r286: the client shows all destruction up to now (no plain seek since the join)
    destr_snapshot = None  # r286: level-start part states of the joined client (destruction_reset.Snapshot)
    snapshot_busy = False
    snapshot_next = 0.0  # monotonic time of the next snapshot try
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
    f_was_down = False  # r238
    bc2_pids = set()  # r233: running BC2 clients (hotkeys work in any view, not only free cam)
    auto_launch = None  # recording base: launch BC2 once this server is ready (Replay button)
    roster_busy = False
    roster_for = None  # recording base whose client roster filled the player list
    roster_next = 0.0  # monotonic time of the next roster try
    look_reader = LookTarget()  # used only by look_worker (one at a time)
    look_busy = False
    look_next = 0.0  # monotonic time of the next looked-at read
    look_name = None  # name shown on the VCR label
    cycle_reader = LookTarget()  # '<' / '>': own read-only handle, used only by cycle_worker
    cycle_busy = False  # '<' / '>' roster read in flight
    gap = load_gap.GapSeek()  # armed once per join (0 -> 1 viewer); any user seek cancels it
    gap_conns = 0  # viewer count at the previous status poll
    auto_play = AutoPlay()  # r286: armed once per join at the demo start; plays when the client is in the map
    play_watch = PlayWatch()  # viewer gone within 10 s of Play = the client rejected the demo data
    prev_was_down = False
    next_was_down = False

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
            message.set('Configure and save paths in Setup first. ' + str(exc))
            return
        rec_map.clear()
        labels = []
        try:
            current_base = active_demo()
        except Exception:
            # No configured/valid recording yet: the selector still lists what
            # exists and nothing is applied until the user picks and starts one.
            current_base = None
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

    def can_play():
        item = rec_map.get(selected_recording.get())
        return bool(item and server_ready and item['base'] == active_recording)

    def controls():
        following = follow_thread is not None
        selector.config(state='disabled' if following or camera_busy else 'readonly')
        rec_selector.config(state='disabled' if following or camera_busy or aux_busy else 'readonly')
        for action, button in buttons.items():
            if action in ('switch_rec', 'refresh_rec'):
                enabled = not aux_busy and not camera_busy and not following
            elif action == 'follow':  # one toggle: Follow / Stop follow
                button.config(text=follow_button_text(following))
                enabled = following or not camera_busy
            else:
                enabled = not camera_busy and not following
            button.config(state='normal' if enabled else 'disabled')

    def set_players(names, source):
        selector.config(values=names)
        if player.get() not in names:
            player.set(names[0] if names else '')
        if names and follow_thread is None and not camera_busy:
            log_ui('players', f'{source}: ' + ', '.join(names))

    def look_worker(max_distance):
        """Read-only: player nearest the free camera's screen centre (off the UI thread)."""
        try:
            data = json.loads(STATE.read_text(encoding='utf-8'))
            events.put(('looked_at', look_reader.read(data, max_distance=max_distance), None))
        except Exception as exc:
            events.put(('looked_at', None, str(exc)))

    def read_roster_worker():
        """Read-only: player names of the joined replay from client memory (memory scan; run off the UI thread)."""
        try:
            c_ro = Client()
            try:
                data = roster(c_ro)
                try:
                    from replay.camera import warm_owner
                    warm_owner(c_ro)  # r285: the first Focus / free camera then skips a ~2.5 s scan
                except Exception:
                    pass
            finally:
                c_ro.close()
            events.put(('roster', [p['name'] for p in data.get('players', []) if p.get('name')], None))
        except Exception as exc:
            events.put(('roster', [], str(exc)))

    def snapshot_worker(recording):
        """r286: read-only level-start part states, only while the replay is still before the game start."""
        try:
            data = pov_mod.load_players(cfg_server_dir(), recording)
        except Exception:
            data = None
        try:
            start = load_gap.content_frame(data, cfg_server_dir() / (recording + '.fbgd'))
            frame = timeline_operate('status')['frame']
            if frame > (start if start is not None else 60 * FPS):
                events.put(('destr_snapshot', None, f'too late for a level-start snapshot (frame {frame})'))
                return
            events.put(('destr_snapshot', destruction_reset.Snapshot.take(), None))
        except Exception as exc:
            events.put(('destr_snapshot', None, str(exc)))

    def gap_target():
        """Seek frame past the empty load gap of the running demo (players.json / .fbgd scan), or None."""
        try:
            data = pov_mod.load_players(cfg_server_dir(), active_recording)
        except Exception:
            data = None
        try:
            content = load_gap.content_frame(data, cfg_server_dir() / (active_recording + '.fbgd'))
            return load_gap.seek_target(content, key_frames(active_recording), FPS)
        except Exception as exc:
            log_ui('gap_seek', f'no target: {exc}', error=True)
            return None

    def gap_seek_worker(target, resume):
        """Refused (seek guard: no client HUD yet) until the client has entered the map; retried by poll()."""
        try:
            state = timeline_operate('seek', target)
            if resume and target < limit:
                state = timeline_operate('play')
            events.put(('gap_seek', state, None))
        except Exception as exc:
            events.put(('gap_seek', None, str(exc)))

    def cycle_worker(pid, names, current, step):
        """Read-only: '<' / '>' pick the previous / next player with a live soldier (Player list order)."""
        try:
            roster_names, valid = cycle_reader.alive_players(pid)
            events.put(('cycle', cycle_player(names or roster_names, current, valid, step), None))
        except Exception as exc:
            events.put(('cycle', None, str(exc)))

    def start_cycle(step, pid):
        nonlocal cycle_busy
        if cycle_busy or camera_busy or follow_thread is not None:
            return
        cycle_busy = True
        threading.Thread(target=cycle_worker, args=(pid, list(selector.cget('values') or ()), player.get(), step),
                         daemon=True).start()

    def on_recording_selected(*_):
        """Fill the camera player list (players.json names when present, until the client roster is read)."""
        nonlocal roster_for, roster_next
        item = rec_map.get(selected_recording.get())
        data = None
        try:
            if item:
                data = pov_mod.load_players(cfg_server_dir(), item['base'])
        except Exception:
            data = None
        if roster_for is None or not item or item['base'] != active_recording:
            roster_for, roster_next = None, 0.0
            set_players([p['name'] for p in (data or {}).get('players', []) if p.get('name')], 'players.json')
        controls()

    def start_switch_recording(item=None):
        nonlocal aux_busy, server_ready, active_recording
        if aux_busy or camera_busy or follow_thread is not None:
            return False
        if item is None:
            item = rec_map.get(selected_recording.get())
        if not item:
            messagebox.showerror('Recording', 'Select a recording from the list first.')
            return False
        aux_busy = True
        server_ready = False
        active_recording = None
        play_watch.cancel()  # the switch stops the running client on purpose
        controls()
        message.set(f"Starting the replay server for {item['name']}…")

        def worker():
            try:
                # Do not switch servers if recovery of a live camera hook fails.
                # A stale session file after BC2 exits needs no restoration.
                pids = client_pids()
                if pids:  # r292: never kill a game the panel did not start (the user may be playing / recording)
                    rec = owned.load('client')
                    foreign = [p for p in pids if not (rec and rec.get('pid') == p and owned.alive(rec))]
                    if foreign:
                        raise RuntimeError(f'BC2 is running (pid {foreign[0]}) but was not started by this panel: '
                                           'close the game first, then click ▶ again')
                if STATE.exists() and pids:
                    sess_pid = json.loads(STATE.read_text(encoding='utf-8')).get('pid')
                    if sess_pid in pids:
                        c_ctl = ControlClient()
                        try:
                            data = session(c_ctl)
                            st = status(c_ctl, data)
                            if st.get('hook_installed'):
                                native_command(c_ctl, data, 'detach', replay_kind=data.get('replay_kind', 'timeline'))
                        finally:
                            c_ctl.close()
                    else:  # r292: the session file belongs to an earlier client: nothing to restore
                        log_ui('switch_stale_session', str(sess_pid))
                if client_pids():  # r285: a running client gets confused by the switch; a fresh one is launched when ready
                    log_ui('switch_stop_clients', json.dumps(owned.stop_clients(client_pids())))
                from launcher_panel import start_backend
                start_backend()
                res = switch_recording(item['base'])
                def warm():  # r285: the first seek parsed the demo's keyframes (~2 s); do it now in the background
                    try:
                        from replay.timeline import seek_redirect
                        seek_redirect(0, res['recording_base'])
                    except Exception:
                        pass
                threading.Thread(target=warm, daemon=True).start()
                try:
                    owned.record('server', res['pid'])  # stopped by PID when the panel closes
                except Exception as exc:
                    log_ui('owned_record', f'server: {exc}', error=True)
                events.put(('switch_done', res, None))
            except Exception as exc:
                events.put(('switch_done', None, str(exc)))

        threading.Thread(target=worker, daemon=True).start()
        return True

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
                if action == 'restore':
                    if not STATE.exists():
                        text = 'No camera session; no camera changes made.'
                    elif not client_pids():
                        text = 'BC2 is closed; camera hook ended with the game. No camera writes needed.'
                    else:
                        client = ControlClient()
                        stale_pid = json.loads(STATE.read_text(encoding='utf-8')).get('pid')
                        if stale_pid != client.pid:  # r222: session file from an earlier BC2 run
                            text = 'Camera session belongs to an earlier BC2 run; nothing to restore.'
                        else:
                            data = session(client)
                            sess_kind = data.get('replay_kind', 'timeline')
                            if status(client, data)['hook_installed']:
                                native_command(client, data, 'detach', replay_kind=sess_kind)
                                text = 'Saved camera transform restored; original player-view mode and vtable restored.'
                            else:
                                text = 'Already detached; no camera writes made.'
                    if client_pids():  # r219
                        try:
                            text += '\nClean screen: ' + json.dumps(clean_screen.disable())
                        except Exception as exc:
                            text += f'\nClean screen restore failed: {exc}'
                    events.put(('camera_mode', False, None))
                else:
                    seek_view_guard.install()
                    client = ControlClient()
                    effective, fallback_note = action, ''
                    if action == 'focus':
                        try:
                            target_position(client, name)
                        except TargetUnavailable as exc:  # r222: no soldier yet -> plain free cam
                            effective = 'free'
                            fallback_note = f'{name}: no live soldier or vehicle ({exc}); free camera started instead.' + chr(10)
                    attach(client, replay_kind=kind)
                    data = session(client)
                    native_command(client, data, 'free', replay_kind=kind)
                    events.put(('camera_mode', True, client.pid))
                    try:  # r219
                        clean_note = '\nClean screen: ' + json.dumps(clean_screen.enable(scope=1))
                    except Exception as exc:
                        clean_note = f'\nClean screen not applied: {exc}'
                    if effective == 'free':  # camera_home: start free cam at a findable spot (soldier / remembered / map spawn)
                        try:
                            from replay.camera_home import home_position, home_command
                            _src, _pos = home_position(client)
                            if _pos:
                                home_command(client, data, _pos, replay_kind=kind)
                        except Exception as exc:
                            events.put(('camera_done', f'camera home skipped: {exc}', False))
                    if effective == 'focus':
                        focus_command(client, data, 'focus', name, replay_kind=kind)
                        text = (
                            f'Focused on {name}. One-shot move; not following.\n'
                            'You can choose another player and Focus again, or seek. Press SPACE in-game to toggle Play/Pause.'
                        )
                    else:
                        text = (
                            'Free camera is active. Return to the game for mouse/WASD (press SPACE in-game to toggle Play/Pause).\n'
                            'Focus moves once; Follow overrides camera pose until Stop follow. F in game: back to the player view.'
                        )
                    text = fallback_note + text + clean_note
                events.put(('camera_done', text, False))
            except Exception as exc:
                events.put((
                    'camera_done',
                    'Stopped: ' + str(exc) + '\nDo not repeat timed-out commands. Press F in game (player view) or leave the window open for diagnosis.',
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

    def toggle_follow():
        if follow_thread is None:
            start_follow()
        else:
            stop_follow()

    def stop_follow():
        if follow_thread is not None:
            stop_event.set()
            message.set('Stopping Follow after the current acknowledged camera command…')

    def playback(action, frame=0, resume_after=False):
        nonlocal aux_busy
        if action in ('seek', 'step'):
            gap.cancel()  # the user seeked: never skip the load gap over that
            play_watch.cancel()  # a seek can drop the client for other reasons
        if aux_busy or camera_busy:
            return
        if action == 'play' and not can_play():
            message.set('Choose a Demo, click ▶ (play) and wait for the server to be ready before Play.')
            return
        aux_busy = True
        controls()
        keep = action in ('seek', 'step') and keep_destruction.get()
        message.set('Replaying fast to the target (destruction kept)…' if keep else
                    'Working… do not repeat commands or use another controller.')
        consistent = destruction_ok
        snap = destr_snapshot

        def repair():
            """r286: composite destruction (walls, houses, fences) back to level start; a short note."""
            if snap is None:
                return 'walls not repaired (no level-start snapshot: joined mid-demo)'
            try:
                r = destruction_reset.reset(snap)
                return f"{r['changed']} destroyed parts repaired" if r['changed'] else None
            except Exception as exc:
                return f'walls not repaired: {exc}'

        def worker():
            try:
                target_f = frame
                if keep:  # r286
                    cur = timeline_operate('status')
                    was_playing = not bool(cur['paused'])
                    if action == 'step':
                        target_f = cur['frame'] + frame
                    target_f = max(0, min(limit, target_f))
                    vcr_overlay.set_banner(vcr_overlay.banner_text(target_f < cur['frame']))  # r286: game window centre
                    try:
                        state = seek_keeping_destruction(target_f, cur['frame'], consistent, after_seek_zero=repair)
                    finally:
                        vcr_overlay.set_banner(None)
                    if (was_playing if action == 'step' else resume_after) and target_f < limit:
                        state = dict(state, **timeline_operate('play'), replayed_from=state['replayed_from'],
                                     elapsed_s=state['elapsed_s'], reset=state['reset'])
                    sec = state['frame'] / FPS
                    mode_txt = 'Paused' if state['paused'] else 'Playing'
                    origin = 'from 0:00' if state['replayed_from'] == 0 and cur['frame'] != 0 else 'forward'
                    text = (f"Seek -> {format_mmss(sec)} ({sec:.1f}s, frame {state['frame']}), destruction kept "
                            f"(replayed {origin} in {state['elapsed_s']:.1f} s). {mode_txt}.")
                    if state.get('reset'):
                        text += f" ({state['reset']})"
                    state['destruction_ok'] = True
                    events.put(('playback_done', text, state))
                    return
                if action == 'step':
                    cur = timeline_operate('status')
                    was_playing = not bool(cur['paused'])
                    target_f = max(0, min(limit, cur['frame'] + frame))
                    state = timeline_operate('seek', target_f)
                    if was_playing and target_f < limit:
                        state = timeline_operate('play')
                else:
                    cur = timeline_operate('status') if action == 'seek' else None
                    state = timeline_operate(action, target_f)
                    if action == 'seek' and resume_after and target_f < limit:
                        state = timeline_operate('play')
                repaired = None
                if action in ('seek', 'step') and state['frame'] < cur['frame']:
                    repaired = repair()  # r286: going back shows the map as a fresh join would
                sec = state['frame'] / FPS
                if action in ('seek', 'step'):
                    skipped = state.get('seek_guard', {}).get('null_view_updates_skipped', 0)
                    mode_txt = 'Paused' if state['paused'] else 'Playing'
                    text = f"Seek -> {format_mmss(sec)} ({sec:.1f}s, frame {state['frame']}). {mode_txt}."
                    if skipped:
                        text += f" (guard skipped={skipped})"
                    if state.get('avoided_key') is not None:  # r283
                        text += (f" Moved from {format_mmss(state['requested_frame'] / FPS)}: the keyframe at "
                                 f"{format_mmss(state['avoided_key'] / FPS)} names a missing ghost in the viewer's view "
                                 "(a seek there disconnects or crashes the client); press Play to watch through it.")
                    if state.get('key_safety_error'):
                        text += f" (keyframe check failed: {state['key_safety_error']})"
                    if repaired:
                        text += f" ({repaired})"
                    state['destruction_ok'] = False  # r285 §6: a seek drops the destruction before its target
                else:
                    mode = 'Paused' if state['paused'] else 'Playing'
                    text = f"{mode} — {format_mmss(sec)} ({sec:.1f}s, frame {state['frame']})"
                events.put(('playback_done', text, state))
            except Exception as exc:
                events.put(('playback_done', 'Playback command stopped: ' + str(exc), None))

        threading.Thread(target=worker, daemon=True).start()

    def seek_to_entry(*_):
        """Enter in the Time field: validate (seconds, mm:ss, hh:mm:ss within the demo) and seek; playback stays paused."""
        frame, err = parse_time_entry(target_time.get(), limit)
        if err:
            time_error.set(err)
            return
        if not server_ready:
            time_error.set('Server not ready: click ▶ (play) first.')
            return
        if aux_busy or camera_busy:
            time_error.set('Busy: press Enter again in a moment.')
            return
        time_error.set(TIME_HINT)
        playback('seek', frame)

    cam_diag_last = {}  # r218: log camera/input transitions (e.g. deploy screen)

    def poll_status_worker():
        try:
            pids = set(client_pids())  # r233: hotkey target, independent of the camera hook
        except Exception:
            pids = set()
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
                            # r218: hotkeys stay on while our hook holds free mode, even if a
                            # native transition (killcam/deploy) briefly changes mode/active camera.
                            cam_active = bool(st.get('hook_installed') and st.get('enabled') and not st.get('detached'))
                            diag = {k: st.get(k) for k in ('actual_mode', 'free_camera_is_global_active', 'global_active_table',
                                                           'free_mapper_is_current', 'current_input_mapper', 'mode_reset_count')}
                            key = tuple(diag[k] for k in ('actual_mode', 'free_camera_is_global_active', 'free_mapper_is_current'))
                            if cam_active and cam_diag_last.get('key') != key:
                                cam_diag_last['key'] = key
                                log_ui('camera_transition', json.dumps(diag))
                            # r222: while free cam is on, log owner/target/axes/position whenever they
                            # change (or every 10 s) so 'cannot move after death' is pinpointed.
                            if cam_active:
                                try:
                                    pr = probe(c_ro, data)
                                except Exception as exc:
                                    pr = {'probe_error': str(exc)}
                                pr.update(diag, heartbeat=st.get('heartbeat'), hold_free=st.get('hold_free'),
                                          enabled=st.get('enabled'), global_active_camera=st.get('global_active_camera'),
                                          mapper_repush_count=st.get('mapper_repush_count'),
                                          key_inject_count=st.get('key_inject_count'),
                                          look_inject_count=st.get('look_inject_count'),
                                          # r222c: physical keys at poll time (proves 'held but axes 0')
                                          keys_down=''.join(ch for ch, vk in (('W', 0x57), ('A', 0x41), ('S', 0x53), ('D', 0x44), ('M', 0x01))
                                                            if u32_dll.GetAsyncKeyState(vk) & 0x8000))
                                sig = json.dumps({k: v for k, v in pr.items() if k not in ('heartbeat', 'mapper_ticks_30', 'key_inject_count', 'look_inject_count')}, sort_keys=True)
                                now = time.monotonic()
                                if sig != cam_diag_last.get('probe') or now - cam_diag_last.get('probe_t', 0) >= 10:
                                    cam_diag_last['probe'], cam_diag_last['probe_t'] = sig, now
                                    log_ui('camera_probe', json.dumps(pr))
                    finally:
                        c_ro.close()
                except Exception:
                    pass
            state = timeline_operate('status')
            events.put(('status_poll', kind, state, None, cam_active, cam_pid, pids))
        except Exception as exc:
            events.put(('status_poll', 'unknown', None, str(exc), False, None, pids))

    def poll():
        nonlocal camera_busy, aux_busy, status_pending, last_status_tick, follow_thread, pending_restore, close_after
        nonlocal free_cam_active, client_pid, replay_paused, current_frame, limit, space_was_down, left_was_down, right_was_down
        nonlocal server_ready, active_recording, gap_conns, pending_step, destruction_ok, destr_snapshot, snapshot_busy, snapshot_next
        nonlocal bc2_pids  # r233
        nonlocal f_was_down  # r238
        nonlocal auto_launch, roster_busy, roster_for, roster_next
        nonlocal look_busy, look_next, look_name
        nonlocal cycle_busy, prev_was_down, next_was_down
        try:
            while True:
                event = events.get_nowait()
                kind = event[0]
                if kind in ('recordings_demo', 'recordings_play'):  # step 0: Recordings window published / plays a demo
                    stem, play = event[1], kind == 'recordings_play'
                    log_ui(kind, stem)
                    if aux_busy or camera_busy or follow_thread is not None:
                        message.set(f'Demo {stem}: the panel is busy; click ⟳, select it and click ▶ (play).' if play
                                    else f'New demo {stem} published; click ⟳ to list it.')
                        continue
                    refresh_recordings_list()
                    label = next((lbl for lbl, item in rec_map.items() if item['name'] == stem), None)
                    if label:
                        selected_recording.set(label)
                        if play:  # only on the Recordings window's ▶: start it like ▶ here, without the dialog
                            start_switch_recording(rec_map[label])
                        else:
                            message.set(f'New demo {stem} published and selected; click ▶ (play) to watch it.')
                    else:
                        message.set(f'Demo {stem} is not listed yet (companion files?); click ⟳ later.')
                    continue
                if kind == 'camera_mode':
                    free_cam_active = bool(event[1])
                    if event[2] is not None:
                        client_pid = event[2]
                elif kind == 'switch_done':
                    aux_busy = False
                    pending_step = 0
                    destruction_ok = True
                    destr_snapshot = None
                    free_cam_active = False
                    server_ready = False
                    res, err = event[1], event[2]
                    gap.cancel()
                    gap_conns = 0  # new server: the next viewer is a fresh join
                    if err:
                        text = 'Recording switch failed: ' + err
                        message.set(text)
                        log_ui('switch_error', text, error=True)
                        auto_launch = None
                    else:
                        limit = res['limit_frame']
                        active_recording = res['recording_base']
                        auto_launch = res['recording_base']  # launch BC2 once this server reports ready
                        roster_for, roster_next = None, 0.0
                        on_recording_selected()
                        text = (
                            f"Server started {res['recording_base']} ({res['level']}, PID {res['pid']}).\n"
                            'A running BC2 was closed; a fresh BC2 starts when the server is ready; playback starts once it is in the map.'
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
                            'restart it to clear any remaining camera hook. The replay server and backend are stopped.',
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
                elif kind == 'looked_at':
                    look_busy = False
                    if event[2]:  # e.g. roster changed during read, client restarted: back off, keep the name
                        look_next = time.monotonic() + 2.0
                    else:
                        look_name = event[1] if (free_cam_active and look_on.get()) else None
                    vcr_overlay.set_target(look_name)
                elif kind == 'cycle':
                    cycle_busy = False
                    name, err = event[1], event[2]
                    if err:
                        message.set('Player cycle: could not read players: ' + err)
                        log_ui('cycle_error', err, error=True)
                    elif not name:
                        message.set('Player cycle: no player with a live soldier.')
                    elif not camera_busy and follow_thread is None:
                        player.set(name)
                        start_camera('focus')  # attaches the free camera first if it is off
                elif kind == 'destr_snapshot':
                    snapshot_busy = False
                    if event[1] is not None:
                        destr_snapshot = event[1]
                        log_ui('destr_snapshot', f'{len(destr_snapshot.states)} parts, pid {destr_snapshot.pid}')
                    else:
                        snapshot_next = time.monotonic() + 2.0
                        log_ui('destr_snapshot', event[2])
                        if str(event[2]).startswith('too late'):
                            snapshot_next = float('inf')  # joined after the game start: no snapshot this join
                elif kind == 'roster':
                    roster_busy = False
                    names, err = event[1], event[2]
                    if names:
                        roster_for, roster_next = active_recording, time.monotonic() + ROSTER_REFRESH_S
                        set_players(names, 'client roster')
                    else:
                        roster_next = time.monotonic() + ROSTER_RETRY_S
                        if err:
                            log_ui('roster_error', err, error=True)
                elif kind == 'follow_event':
                    state = event[1]
                    mode = state['state']
                    name = state['player']
                    free_cam_active = True
                    if mode == 'following':
                        message.set(
                            f"Following {name} — {state['updates']} camera updates.\n"
                            "Stop follow to move manually. SPACE = Play/Pause, Arrows = -3s/+3s, Shift+Arrows = -10s/+10s."
                        )
                    elif mode == 'stopped':
                        message.set(f"Follow stopped after {state['updates']} updates. Free camera stays at its last position.")
                    elif mode == 'waiting_for_target':
                        message.set(
                            f'Waiting for {name} soldier link (death/respawn/seek resync may cause this).\n'
                            'No pose writes while unavailable. Will retry automatically. Stop follow remains available.'
                        )
                    else:
                        message.set('Waiting for native camera transition; no pose writes.')
                elif kind == 'follow_error':
                    text = 'Follow stopped: ' + event[1] + '\nCamera stays where it is. Press F in game for the player view.'
                    message.set(text)
                    log_ui('follow_error', text, error=True)
                elif kind == 'follow_finished':
                    follow_thread = None
                elif kind == 'gap_seek':
                    aux_busy = False
                    tl_state, err = event[1], event[2]
                    if err:
                        log_ui('gap_seek', f'retry: {err}')
                    else:
                        gap.accepted()
                        vcr_overlay.feed(tl_state)
                        replay_paused = bool(tl_state['paused'])
                        current_frame = tl_state['frame']
                        text = (f"Skipped the empty load gap: seek to {format_mmss(tl_state['frame'] / FPS)}; "
                                'the game starts a few seconds of playback later.')
                        log_ui('gap_seek', text)
                        if follow_thread is None:
                            message.set(text)
                elif kind == 'playback_done':
                    aux_busy = False
                    text, tl_state = event[1], event[2]
                    log_ui('playback_done', text, error=text.startswith('Playback command stopped:'))
                    if pending_step:  # r285: presses during the last seek
                        step, pending_step = pending_step, 0
                        playback('step', step)
                    if tl_state is not None and 'destruction_ok' in tl_state:
                        destruction_ok = tl_state['destruction_ok']
                    if (tl_state is not None and text.startswith('Playing') and not tl_state['paused']
                            and tl_state['connections'] >= 1):
                        play_watch.arm()  # plain Play (button, SPACE or auto-play)
                    if tl_state is not None:
                        vcr_overlay.feed(tl_state)
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
                    bc2_pids = event[6]  # r233
                    free_cam_active = cam_act
                    if cam_p is not None:
                        client_pid = cam_p
                    vcr_overlay.feed(tl_state if (not err and srv_kind == 'timeline') else None)
                    if err:
                        server_ready = False
                        play_watch.cancel()
                        playback_status.set('Server status unavailable: ' + err)
                        log_ui('status_poll_error', err, error=True)
                    elif srv_kind == 'timeline' and tl_state is not None:
                        server_ready = bool(tl_state['ready'] and tl_state['active'] and tl_state['initialized'])
                        conns = tl_state['connections']
                        if server_ready and conns >= 1 and gap_conns == 0 and active_recording:  # a fresh join
                            destruction_ok = tl_state['frame'] <= FPS  # r286: a mid-demo join may miss destruction
                            destr_snapshot, snapshot_next = None, 0.0  # r286: a new client needs its own snapshot
                            if tl_state['frame'] <= FPS and tl_state['paused']:
                                auto_play.arm()  # r286: start playing by itself once in the map
                            else:
                                auto_play.cancel()
                            target = gap_target()
                            if gap.arm(target):
                                log_ui('gap_seek', f'armed: target frame {target}')
                        gap_conns = conns if server_ready else 0  # a viewer before ready still counts as a fresh join
                        if not active_recording:
                            play_watch.cancel()
                        elif play_watch.check(conns) == 'rejected':
                            stem = pov_mod.stem_of(active_recording)
                            message.set(PLAY_REJECTED_TEXT.format(name=stem))
                            log_ui('play_rejected', stem, error=True)
                        decision = should_auto_launch(auto_launch, active_recording, server_ready, bc2_pids)
                        if decision != 'wait':
                            auto_launch = None
                            if decision == 'launch':
                                message.set("Server ready: launching BC2; it logs in and joins 'Local Replay' by itself. "
                                            'Playback starts by itself once BC2 is in the map.')
                                launch_game()
                            else:
                                message.set("Server ready. BC2 is already running: rejoin 'Local Replay' from the server browser.")
                            log_ui('auto_launch', decision)
                        # camera player list from client memory once a viewer is in (memory scan: background, throttled)
                        if (server_ready and bc2_pids and tl_state['connections'] >= 1 and not roster_busy
                                and not camera_busy and follow_thread is None and time.monotonic() >= roster_next):
                            roster_busy = True
                            threading.Thread(target=read_roster_worker, daemon=True).start()
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
        vcr_overlay.set_view(free_cam_active or follow_thread is not None)  # label FREE / FPV
        vcr_overlay.set_recording(active_recording)  # demo name, top-right (None while switching)
        if free_cam_active and look_on.get() and vcr_on.get() and bc2_pids:
            if not look_busy and not camera_busy and time.monotonic() >= look_next:
                look_busy, look_next = True, time.monotonic() + LOOK_PERIOD_S
                threading.Thread(target=look_worker, args=(clamp_distance(look_dist.get()),), daemon=True).start()
        elif look_name is not None:
            look_name = None
            vcr_overlay.set_target(None)

        # In-game hotkeys whenever a BC2 client window is foreground and the timeline server is
        # ready (r233: any view, not only free cam):
        # SPACE = Play/Pause, LEFT/RIGHT = -3s/+3s, SHIFT+LEFT/RIGHT = -10s/+10s,
        # F = free camera on/off, < / > = Focus the previous / next player
        space_down = bool(u32_dll.GetAsyncKeyState(VK_SPACE) & 0x8000)
        shift_down = bool(u32_dll.GetAsyncKeyState(VK_SHIFT) & 0x8000)
        left_down = bool(u32_dll.GetAsyncKeyState(VK_LEFT) & 0x8000)
        right_down = bool(u32_dll.GetAsyncKeyState(VK_RIGHT) & 0x8000)
        f_down = bool(u32_dll.GetAsyncKeyState(VK_F) & 0x8000)  # r238
        prev_down = bool(u32_dll.GetAsyncKeyState(VK_OEM_COMMA) & 0x8000)
        next_down = bool(u32_dll.GetAsyncKeyState(VK_OEM_PERIOD) & 0x8000)
        step_frames = STEP_LARGE_FRAMES if shift_down else STEP_SMALL_FRAMES
        # playback() ignores input while a command runs.
        hotkey_pids = set(bc2_pids)
        if free_cam_active and client_pid:
            hotkey_pids.add(client_pid)
        if server_ready and hotkey_pids and not camera_busy:
            hwnd = u32_dll.GetForegroundWindow()
            if hwnd:
                fg_pid = c.wintypes.DWORD(0)
                u32_dll.GetWindowThreadProcessId(hwnd, c.byref(fg_pid))
                if fg_pid.value in hotkey_pids:
                    if space_down and not space_was_down:
                        playback('play' if replay_paused else 'pause')
                    elif left_down and not left_was_down:
                        if aux_busy:
                            pending_step -= step_frames
                        else:
                            playback('step', -step_frames)
                    elif right_down and not right_was_down:
                        if aux_busy:
                            pending_step += step_frames
                        else:
                            playback('step', step_frames)
                    elif f_down and not f_was_down:  # r238: F toggles the free camera
                        start_camera('restore' if free_cam_active or follow_thread is not None else 'free')
                    elif prev_down and not prev_was_down:  # '<': Focus the previous player
                        start_cycle(-1, fg_pid.value)
                    elif next_down and not next_was_down:  # '>': Focus the next player
                        start_cycle(1, fg_pid.value)
        space_was_down = space_down
        f_was_down = f_down
        prev_was_down = prev_down
        next_was_down = next_down
        left_was_down = left_down
        right_was_down = right_down

        last_status_tick += 1
        if not aux_busy and not camera_busy and not status_pending and last_status_tick >= 8:
            last_status_tick = 0
            status_pending = True
            threading.Thread(target=poll_status_worker, daemon=True).start()

        if (server_ready and bc2_pids and destr_snapshot is None and not snapshot_busy and active_recording
                and time.monotonic() >= snapshot_next):  # r286
            snapshot_busy = True
            threading.Thread(target=snapshot_worker, args=(active_recording,), daemon=True).start()
        if auto_play.armed and server_ready and bc2_pids:  # r286
            act = auto_play.action(replay_paused, destr_snapshot is not None)  # snapshot taken = level bound
            if act == 'play' and not aux_busy and not camera_busy:
                auto_play.cancel()
                log_ui('auto_play', f'frame {current_frame}')
                playback('play')
            elif act == 'give_up':
                log_ui('auto_play', 'gave up: the client did not reach the map in time', error=True)
        if gap.target is not None and server_ready and not aux_busy and not camera_busy:
            act = gap.action(current_frame)
            if act == 'seek':
                aux_busy = True
                play_watch.cancel()  # a seek can drop the client for other reasons
                controls()
                threading.Thread(target=gap_seek_worker, args=(gap.target, not replay_paused), daemon=True).start()
            elif act == 'give_up':
                log_ui('gap_seek', 'gave up: seek still refused after 60 s', error=True)

        if pending_restore and follow_thread is None and not camera_busy and not aux_busy:
            start_camera('restore', closing=close_after)
        window.after(60, poll)

    # Populate Recording Selector buttons
    switch_btn = ttk.Button(rec_row, text='▶', width=3, command=start_switch_recording)  # play the selected Demo
    switch_btn.pack(side='left', padx=4)
    buttons['switch_rec'] = switch_btn
    tip(rec_selector, 'Server demo (demos/ in the server folder) to play.')
    tip(switch_btn, 'Play the selected demo: starts the replay server with it, then BC2 (a running BC2 is closed '
                    'and started again). It joins Local Replay by itself.')

    refresh_rec_btn = ttk.Button(rec_row, text='⟳', width=3, command=refresh_recordings_list)  # refresh the Demo list
    refresh_rec_btn.pack(side='left', padx=4)
    buttons['refresh_rec'] = refresh_rec_btn
    tip(refresh_rec_btn, 'Read the Demo list again (e.g. after a build).')

    # Timeline: typed seek (Enter; the note right of the field shows input errors), Keep destruction;
    # second row: VCR label and its looked-at player option (FREE view). ▶ launches BC2 (no Launch client, r286)
    tip(ttk.Checkbutton(tl_row2, text='VCR label', variable=vcr_on,
                        command=lambda: vcr_overlay.start(window) if vcr_on.get() else vcr_overlay.stop()),
        'Show PLAY/PAUSE, the replay time and the view (FREE / FPV) over the game window, and the demo name '
        'in the top-right corner.').pack(side='left', padx=(2, 12))
    tip(ttk.Checkbutton(tl_row2, text='Show looked-at player', variable=look_on),
        'In the free camera the label also names the player nearest the screen centre, within max metres.'
        ).pack(side='left', padx=4)
    ttk.Label(tl_row2, text='max').pack(side='left', padx=(4, 2))
    tip(ttk.Spinbox(tl_row2, textvariable=look_dist, from_=DISTANCE_RANGE_M[0], to=DISTANCE_RANGE_M[1], increment=10,
                    width=4), 'Farthest looked-at player to name, 10-300 m (this session only).').pack(side='left')
    ttk.Label(tl_row2, text='m').pack(side='left', padx=(2, 0))
    ttk.Label(tl_row, text='Time:').pack(side='left', padx=(2, 2))
    time_entry = ttk.Entry(tl_row, textvariable=target_time, width=10)
    time_entry.pack(side='left', padx=2)
    tip(time_entry, 'Type seconds, mm:ss or hh:mm:ss and press Enter to seek. Playback stays paused; press SPACE '
                    'in the game to continue.')
    time_entry.bind('<Return>', seek_to_entry)
    time_entry.bind('<KP_Enter>', seek_to_entry)
    time_note = ttk.Label(tl_row, textvariable=time_error)
    time_note.pack(side='left', padx=(4, 2))
    tip(ttk.Checkbutton(tl_row, text='Keep destruction (slower seek)', variable=keep_destruction),
        'Off: a seek is instant, but destruction does not follow it: going back repairs walls and houses '
        '(map as at the start), going forward skips what was destroyed on the way.\n'
        'On: seeks (also the in-game arrows) play to the target fast instead, up to 50x; going back first '
        'repairs walls and houses and replays from 0:00, so the map shows exactly the destruction up to that '
        'moment. Takes about 1.3 s per minute of recording (10:00 = ~13 s); lands within about a second.\n'
        'Small props destroyed in steps (some fences, crates) cannot be repaired and may stay broken.').pack(side='right', padx=(4, 2))
    time_error.trace_add('write', lambda *_: time_note.config(foreground='gray' if time_error.get() == TIME_HINT else 'red'))
    target_time.trace_add('write', lambda *_: time_error.set(TIME_HINT))
    time_error.set(TIME_HINT)
    # Camera buttons: Focus and one Follow / Stop follow toggle. Free camera on/off is F in game
    # (start_camera('free' / 'restore')); closing the window still restores the player view.
    focus_btn = ttk.Button(cam_row1, text='Focus', command=lambda: start_camera('focus'))
    focus_btn.pack(side='left', padx=4)
    buttons['focus'] = focus_btn
    tip(selector, 'Players of the demo (from the game once joined). In game < / > Focus the previous / next one.')
    tip(focus_btn, 'Move the free camera behind the chosen player once (starts the free camera if it is off).')
    follow_btn = ttk.Button(cam_row1, text=FOLLOW_TEXT, width=len(FOLLOW_TEXT), command=toggle_follow)
    follow_btn.pack(side='left', padx=4)
    buttons['follow'] = follow_btn
    tip(follow_btn, 'Experimental: the camera keeps tracking the chosen player at a fixed offset (can look jittery). '
                    'Click again (Stop follow) or press F in game to end.')

    def on_close():
        nonlocal pending_restore, close_after
        if (aux_busy or camera_busy) and not messagebox.askyesno(
            'Command pending', 'A command may still be in flight. Close and restore camera?'
        ):
            return
        vcr_overlay.stop()
        if owned.alive(owned.load('client')):
            # The BC2 this panel launched is stopped after the window closes: skip the camera
            # restore (memory writes that would only slow closing).
            stop_event.set()
            window.destroy()
            return
        pending_restore = True
        close_after = True
        if follow_thread is not None:
            stop_follow()
        elif not camera_busy and not aux_busy:
            start_camera('restore', closing=True)

    window.protocol('WM_DELETE_WINDOW', on_close)
    selected_recording.trace_add('write', lambda *_: controls())
    selected_recording.trace_add('write', on_recording_selected)
    refresh_recordings_list()
    on_recording_selected()
    controls()
    window.after(60, poll)
    try:
        window.mainloop()
    finally:
        # Order: (camera restore ran in on_close) client, replay server, then backend; each bounded
        # by owned.STOP_TIMEOUT_S, then forced. The window is already gone.
        try:
            log_ui('owned_close', json.dumps(owned.close_owned()))
        except Exception as exc:
            log_ui('owned_close', str(exc), error=True)
        from launcher_panel import stop_backend
        stop_backend()
