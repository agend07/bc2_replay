"""r232: VCR-style on-screen label ('▶ PLAY 04:07' / '❚❚ PAUSE 04:07 FPV') over the BC2 client window.

No game hooking: a borderless, colour-keyed (transparent), click-through, always-on-top
tkinter Toplevel is positioned over a corner of the BFBC2Game.exe client area and tracks
it on move/resize. Hidden while BC2 is minimized, missing, or (by default) when neither
BC2 nor this panel is the foreground window. A second label in the mirrored corner (top-right
for the default top-left) names the demo being played, coloured like the VCR label. Works with windowed /
borderless BC2 only;
in exclusive fullscreen the overlay cannot appear above the game. preview.json "client_borderless": true makes
a windowed BC2 borderless over its whole monitor (make_borderless) so the label stays visible (r291).

Time/state source:
  * panel mode   -> VcrOverlay.feed(state) with the dict ui.py already gets from
                    timeline_operate('status') (no extra server queries);
  * self-polling -> VcrOverlay(..., self_poll=True) queries timeline_operate('status')
                    itself in a worker thread at ~4 Hz (standalone mode).
Seconds are interpolated between polls while playing.

Standalone test:  python -m replay.vcr_overlay [--corner tl|tr|bl|br] [--size 20] [--demo]
"""
import ctypes as c
from ctypes import wintypes as w
import math
import os
import threading
import time

FPS_DEFAULT = 30
GREEN = '#33ff33'
RED = '#ff3333'  # r240: paused label
SHADOW = '#000000'
KEY = '#ff00fe'          # colour key -> fully transparent pixels
CORNERS = ('tl', 'tr', 'bl', 'br')
PLAY_GLYPH = '▶'
PAUSE_GLYPH = '❚❚'
PAUSE_HINT = 'SPACE to play'
PLAY_HINT = 'SPACE to pause'
YELLOW = '#ffd633'  # r286: centre banner (Keep destruction seek in progress)
BANNER_SCALE = 1.6  # banner font size relative to the VCR label
MAX_EXTRAPOLATE_S = 1.5  # never run further ahead of the last poll than this


# ---------------------------------------------------------------- pure helpers (tested)

def label_color(text):
    """r240: red while paused, green otherwise."""
    return RED if 'PAUSE' in (text or '') else GREEN


def format_clock(seconds):
    """Demo clock: mm:ss below an hour, h:mm:ss above. Truncates (VCR counters don't round up)."""
    if seconds is None or not math.isfinite(seconds):
        return '--:--'
    total = max(0, int(seconds))
    m, s = divmod(total, 60)
    h, m = divmod(m, 60)
    return f'{h}:{m:02d}:{s:02d}' if h else f'{m:02d}:{s:02d}'


def view_suffix(view, target=None, ascii_only=False):
    """view: 'free' | 'fpv' | None (unknown) -> ' FREE' / ' FPV' / ''. In FREE, target (the
    looked-at player) adds ' · <name>'."""
    text = {'free': ' FREE', 'fpv': ' FPV'}.get(view, '')
    if view == 'free' and target:
        text += (' - ' if ascii_only else ' · ') + target
    return text


def format_label(mode, seconds, ascii_only=False, speed=1.0, view=None, target=None):
    """mode: 'play' | 'pause' | 'init' | None (no server). r233: speed != 1 while playing
    shows e.g. '▶▶ 2x 04:07' (fast) / '▶ 0.5x 04:07' (slow). view: 'free' / 'fpv' appends
    ' FREE' (free camera) / ' FPV' (player view); not shown while initializing. target: name of
    the looked-at player, shown in FREE only ('⏸ PAUSE 03:45 FREE · Davorich')."""
    if mode in ('play', 'pause'):
        text = _format_label(mode, seconds, ascii_only, speed) + view_suffix(view, target, ascii_only)
        return text + '\n' + (PAUSE_HINT if mode == 'pause' else PLAY_HINT)  # r286: second line: the SPACE key
    return _format_label(mode, seconds, ascii_only, speed)


def _format_label(mode, seconds, ascii_only, speed):
    clock = format_clock(seconds)
    if mode == 'play' and speed and speed != 1.0:
        glyph = ('>>' if ascii_only else PLAY_GLYPH * 2) if speed > 1 else ('>' if ascii_only else PLAY_GLYPH)
        return f'{glyph} {speed:g}x {clock}'
    if mode == 'play':
        return ('> PLAY ' if ascii_only else PLAY_GLYPH + ' PLAY ') + clock
    if mode == 'pause':
        return ('|| PAUSE ' if ascii_only else PAUSE_GLYPH + ' PAUSE ') + clock
    if mode == 'init':
        return 'INIT ' + clock
    return ''


def mode_from_state(state):
    """Map a timeline status dict (replay.server.Server.status) to 'play'/'pause'/'init'/None."""
    if not state:
        return None
    if not state.get('ready'):
        return 'init'
    return 'pause' if state.get('paused') else 'play'


class Clock:
    """Holds the last polled state and interpolates demo seconds while playing."""

    def __init__(self, fps=FPS_DEFAULT, now=time.monotonic):
        self.fps = fps
        self.now = now
        self.mode = None
        self.base = None
        self.t0 = 0.0
        self.speed = 1.0  # r233
        self.view = None  # 'free' | 'fpv' | None
        self.target = None  # looked-at player name (FREE only)

    def update(self, state):
        self.speed = float((state or {}).get('speed') or 1.0)
        self.set(mode_from_state(state), (state['frame'] / self.fps) if state else None)

    def set(self, mode, seconds):
        self.mode, self.base, self.t0 = mode, seconds, self.now()

    def clear(self):
        self.set(None, None)

    def seconds(self):
        if self.base is None:
            return None
        if self.mode != 'play':
            return self.base
        return self.base + min(max(0.0, self.now() - self.t0), MAX_EXTRAPOLATE_S) * self.speed

    def label(self, ascii_only=False):
        return format_label(self.mode, self.seconds(), ascii_only, self.speed, self.view, self.target)


def pause_bars(height, x=0, y=0):
    """Pause icon as two vertical bars (like U+23F8): bar width == gap, about a third of the
    icon width. Returns ([(x0, y0, x1, y1), (x0, y0, x1, y1)], icon_width) for a canvas
    whose text line is `height` px tall, starting at (x, y)."""
    bar = max(2, round(height * 0.16))
    top = y + round(height * 0.18)
    bottom = y + height - round(height * 0.18)
    return [(x, top, x + bar, bottom), (x + 2 * bar, top, x + 3 * bar, bottom)], 3 * bar


def recording_name(base):
    """Demo stem from a recording base such as 'demos/cl04_1088' or a path ending in .fbgd."""
    if not base:
        return ''
    name = str(base).replace('\\', '/').rstrip('/').rsplit('/', 1)[-1]
    return name[:-5] if name.lower().endswith('.fbgd') else name


def name_label(vcr_text, recording):
    """Recording-name label next to the VCR label: (text, colour), or ('', None) when the VCR
    label is empty (no replay) or no recording is known. Colour follows label_color(vcr_text)."""
    name = recording_name(recording)
    if not vcr_text or not name:
        return '', None
    return name, label_color(vcr_text)


def line_offsets(widths, align='left'):
    """x offset of each line inside a block as wide as the widest line ('right' aligns right edges,
    'center' centres them)."""
    width = max(widths, default=0)
    return [width - lw if align == 'right' else ((width - lw) // 2 if align == 'center' else 0) for lw in widths]


def name_corner(corner):
    """Corner for the recording-name label: the VCR label's corner mirrored left/right."""
    return corner[0] + ('r' if corner[1] == 'l' else 'l')


def banner_text(back, ascii_only=False):
    """r286: centre banner while a Keep destruction seek replays: back (rewind) or forward."""
    dots = '...' if ascii_only else '\u2026'
    return ('REWINDING' if back else 'FAST FORWARD') + dots + '\nplease wait'


def center_position(rect, size):
    """Top-left that centres size=(w, h) in rect=(left, top, right, bottom)."""
    left, top, right, bottom = rect
    return int((left + right - size[0]) / 2), int((top + bottom - size[1]) / 2)


def corner_position(rect, size, corner='tl', margin=12):
    """rect=(left, top, right, bottom) of the client area in screen coords, size=(w, h).
    Returns (x, y) for the overlay's top-left."""
    left, top, right, bottom = rect
    ow, oh = size
    x = left + margin if corner[1] == 'l' else right - margin - ow
    y = top + margin if corner[0] == 't' else bottom - margin - oh
    return int(x), int(y)


# ---------------------------------------------------------------- win32 window lookup

u32 = c.WinDLL('user32', use_last_error=True)
GWL_EXSTYLE = -20
WS_EX_LAYERED, WS_EX_TRANSPARENT = 0x00080000, 0x00000020
WS_EX_TOOLWINDOW, WS_EX_NOACTIVATE = 0x00000080, 0x08000000
WS_EX_APPWINDOW = 0x00040000
_ENUM = c.WINFUNCTYPE(w.BOOL, w.HWND, w.LPARAM)
u32.EnumWindows.argtypes = [_ENUM, w.LPARAM]
u32.GetWindowThreadProcessId.argtypes = [w.HWND, c.POINTER(w.DWORD)]
u32.GetWindowThreadProcessId.restype = w.DWORD
u32.IsWindowVisible.argtypes = [w.HWND]
u32.IsIconic.argtypes = [w.HWND]
u32.IsWindow.argtypes = [w.HWND]
u32.GetClientRect.argtypes = [w.HWND, c.POINTER(w.RECT)]
u32.ClientToScreen.argtypes = [w.HWND, c.POINTER(w.POINT)]
u32.GetForegroundWindow.restype = w.HWND
u32.GetParent.argtypes = [w.HWND]
u32.GetParent.restype = w.HWND
u32.GetWindowLongW.argtypes = [w.HWND, c.c_int]
u32.GetWindowLongW.restype = c.c_long
u32.SetWindowLongW.argtypes = [w.HWND, c.c_int, c.c_long]
u32.SetWindowLongW.restype = c.c_long
u32.GetWindow.argtypes = [w.HWND, w.UINT]
u32.GetWindow.restype = w.HWND
u32.SetWindowPos.argtypes = [w.HWND, w.HWND, c.c_int, c.c_int, c.c_int, c.c_int, w.UINT]
u32.SetWindowPos.restype = w.BOOL
GW_OWNER = 4
HWND_TOPMOST = w.HWND(-1)
SWP_NOSIZE, SWP_NOACTIVATE = 0x0001, 0x0010
# r291: borderless window (preview.json "client_borderless"): the game's own caption/frame styles stripped
GWL_STYLE = -16
WS_CAPTION, WS_THICKFRAME, WS_SYSMENU = 0x00C00000, 0x00040000, 0x00080000
WS_MINIMIZEBOX, WS_MAXIMIZEBOX = 0x00020000, 0x00010000
WS_FRAME_STYLES = WS_CAPTION | WS_THICKFRAME | WS_SYSMENU | WS_MINIMIZEBOX | WS_MAXIMIZEBOX
SWP_NOZORDER, SWP_FRAMECHANGED = 0x0004, 0x0020
MONITOR_DEFAULTTONEAREST = 2


class _MONITORINFO(c.Structure):
    _fields_ = [('cbSize', w.DWORD), ('rcMonitor', w.RECT), ('rcWork', w.RECT), ('dwFlags', w.DWORD)]


u32.MonitorFromWindow.argtypes = [w.HWND, w.DWORD]
u32.MonitorFromWindow.restype = w.HANDLE
u32.GetMonitorInfoW.argtypes = [w.HANDLE, c.POINTER(_MONITORINFO)]
u32.GetMonitorInfoW.restype = w.BOOL


def borderless_style(style):
    """Window style without caption / sizing frame / system menu buttons; unchanged when it has none (pure)."""
    return style & ~WS_FRAME_STYLES


def monitor_rect(hwnd):
    """(left, top, right, bottom) of the monitor showing hwnd, or None."""
    mon = u32.MonitorFromWindow(hwnd, MONITOR_DEFAULTTONEAREST)
    mi = _MONITORINFO(cbSize=c.sizeof(_MONITORINFO))
    if not mon or not u32.GetMonitorInfoW(mon, c.byref(mi)):
        return None
    r = mi.rcMonitor
    return (r.left, r.top, r.right, r.bottom)


def make_borderless(hwnd):
    """Strip the frame of a windowed BC2 and stretch it over its monitor (the cheap 'borderless fullscreen', so the
    overlay stays visible; exclusive fullscreen hides every other window). No-op (False) when the window already has
    no frame or the monitor is unknown. The game keeps rendering at its own resolution: set it to the desktop size."""
    style = u32.GetWindowLongW(hwnd, GWL_STYLE)
    new = borderless_style(style)
    rect = monitor_rect(hwnd)
    if new == style or rect is None:
        return False
    u32.SetWindowLongW(hwnd, GWL_STYLE, new)
    x, y, r, b = rect
    return bool(u32.SetWindowPos(hwnd, None, x, y, r - x, b - y, SWP_NOZORDER | SWP_NOACTIVATE | SWP_FRAMECHANGED))


def _window_pid(hwnd):
    pid = w.DWORD()
    u32.GetWindowThreadProcessId(hwnd, c.byref(pid))
    return pid.value


def client_rect(hwnd):
    """Client area in screen coords, or None."""
    r = w.RECT()
    if not u32.GetClientRect(hwnd, c.byref(r)):
        return None
    p = w.POINT(0, 0)
    if not u32.ClientToScreen(hwnd, c.byref(p)):
        return None
    return (p.x, p.y, p.x + r.right, p.y + r.bottom)


def find_bc2_window(pids=None):
    """Largest visible unowned top-level window of a BFBC2Game.exe process."""
    if pids is None:
        from replay.client import client_pids
        pids = client_pids()
    pids = set(pids)
    if not pids:
        return None
    best = []

    def cb(hwnd, _):
        if _window_pid(hwnd) in pids and u32.IsWindowVisible(hwnd) and not u32.GetWindow(hwnd, GW_OWNER):
            rc = client_rect(hwnd)
            area = (rc[2] - rc[0]) * (rc[3] - rc[1]) if rc else 0
            if area > 0 or u32.IsIconic(hwnd):
                best.append((area, hwnd))
        return True
    u32.EnumWindows(_ENUM(cb), 0)
    return max(best)[1] if best else None


# ---------------------------------------------------------------- overlay window

class _LabelWindow:
    """One borderless, colour-keyed, click-through, always-on-top text label."""

    def __init__(self, master, font):
        import tkinter as tk
        self.font = font
        self.shown = False
        self.text = None
        self.color = None
        self.align = None
        self.pos = None  # last requested top-left
        self.hwnd = None  # native top-level handle, set on first show (make_clickthrough)
        self.top = tk.Toplevel(master)
        self.top.withdraw()
        self.top.overrideredirect(True)
        self.top.attributes('-topmost', True)
        self.top.configure(bg=KEY)
        try:
            self.top.attributes('-transparentcolor', KEY)
        except tk.TclError:
            pass
        self.canvas = tk.Canvas(self.top, bg=KEY, highlightthickness=0, bd=0)
        self.canvas.pack()
        self.made_clickthrough = False

    def make_clickthrough(self):
        hwnd = u32.GetParent(int(self.top.winfo_id())) or int(self.top.winfo_id())
        ex = u32.GetWindowLongW(hwnd, GWL_EXSTYLE)
        ex = (ex | WS_EX_LAYERED | WS_EX_TRANSPARENT | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE) & ~WS_EX_APPWINDOW
        u32.SetWindowLongW(hwnd, GWL_EXSTYLE, ex)
        self.hwnd = hwnd
        self.made_clickthrough = True

    def hide(self):
        if self.shown:
            self.top.withdraw()
            self.shown = False

    def draw(self, text, color, align='left'):
        """text may hold several lines ('\n'); align='right' keeps their right edges aligned."""
        if text == self.text and color == self.color and align == self.align:
            return
        self.text, self.color, self.align = text, color, align
        cv = self.canvas
        cv.delete('all')
        o = max(1, self.font.metrics('linespace') // 14)  # outline thickness
        th = self.font.metrics('linespace')
        pad = o + 2
        bars, icon_w = [], 0
        if text.startswith(PAUSE_GLYPH + ' '):
            # Consolas has no U+275A: the fallback font draws two wide-spaced blocks. Draw the icon.
            text = text[len(PAUSE_GLYPH):]
            bars, icon_w = pause_bars(th, pad, pad)
        lines = text.split('\n')
        widths = [self.font.measure(line) for line in lines]
        offsets = line_offsets(widths, align)
        cv.configure(width=icon_w + max(widths) + 2 * pad, height=th * len(lines) + 2 * pad)
        for x0, y0, x1, y1 in bars:
            cv.create_rectangle(x0 - o, y0 - o, x1 + o, y1 + o + 1, fill=SHADOW, outline='')
        for i, line in enumerate(lines):
            tx, ty = pad + icon_w + offsets[i], pad + i * th
            for dx in (-o, 0, o):
                for dy in (-o, 0, o):
                    if dx or dy:
                        cv.create_text(tx + dx, ty + dy, text=line, anchor='nw', font=self.font, fill=SHADOW)
            cv.create_text(tx + o, ty + o + 1, text=line, anchor='nw', font=self.font, fill=SHADOW)
        for x0, y0, x1, y1 in bars:
            cv.create_rectangle(x0, y0, x1, y1, fill=color, outline='')
        for i, line in enumerate(lines):
            cv.create_text(pad + icon_w + offsets[i], pad + i * th, text=line, anchor='nw', font=self.font, fill=color)

    def show_at(self, text, color, rect, corner, margin):
        """corner 'c' centres the label (centre-aligned lines)."""
        self.draw(text, color, 'center' if corner == 'c' else ('right' if corner[1] == 'r' else 'left'))
        self.top.update_idletasks()
        size = (self.canvas.winfo_reqwidth(), self.canvas.winfo_reqheight())
        x, y = center_position(rect, size) if corner == 'c' else corner_position(rect, size, corner, margin)
        if (x, y) != self.pos:
            self.pos = (x, y)
            self.top.geometry(f'+{x}+{y}')  # Tk's own idea of the position (a later redraw re-applies it)
        if not self.shown:
            self.top.deiconify()
            self.shown = True
            if not self.made_clickthrough:
                self.make_clickthrough()
        # Tk applies `wm geometry` from an idle handler and the old per-tick `-topmost` re-assert in
        # between cancelled the pending move (the label stayed put when the game window moved). Move the
        # native window and keep it topmost in one call instead; cheap, and idempotent when nothing changed.
        u32.SetWindowPos(self.hwnd, HWND_TOPMOST, x, y, 0, 0, SWP_NOSIZE | SWP_NOACTIVATE)

    def destroy(self):
        try:
            self.top.destroy()
        except Exception:
            pass


class VcrOverlay:
    def __init__(self, master, corner='tl', size=20, margin=12, fps=FPS_DEFAULT,
                 self_poll=False, poll_hz=4.0, foreground_only=True, ascii_only=False, borderless=False):
        import tkinter.font as tkfont
        if corner not in CORNERS:
            raise ValueError('corner must be one of ' + ', '.join(CORNERS))
        self.master, self.corner, self.margin = master, corner, margin
        self.self_poll, self.poll_hz = self_poll, poll_hz
        self.foreground_only, self.ascii_only = foreground_only, ascii_only
        self.borderless = borderless  # r291: strip the BC2 window frame once per window (preview.json client_borderless)
        self._borderless_hwnd = None
        self.clock = Clock(fps)
        self._lock = threading.Lock()
        self._running = False
        self._after = None
        self._hwnd = None
        self._hwnd_check = 0.0
        self._recording = None  # recording base of the demo being played (name label)
        self._own_pid = os.getpid()
        self.font = tkfont.Font(family='Consolas', size=-abs(int(size)), weight='bold')
        self._main = _LabelWindow(master, self.font)
        self._name = _LabelWindow(master, self.font)  # demo name, mirrored corner
        self._banner_text = None  # r286: centre banner (None = hidden)
        self.banner_font = tkfont.Font(family='Consolas', size=-abs(int(size * BANNER_SCALE)), weight='bold')
        self._banner = _LabelWindow(master, self.banner_font)
        self.top, self.canvas = self._main.top, self._main.canvas

    # -- public API
    def start(self):
        if self._running:
            return
        self._running = True
        if self.self_poll:
            threading.Thread(target=self._poll_loop, daemon=True).start()
        self._tick()

    def stop(self):
        self._running = False
        if self._after is not None:
            try:
                self.top.after_cancel(self._after)
            except Exception:
                pass
            self._after = None
        self._hide()

    def destroy(self):
        self.stop()
        self._banner.destroy()
        self._name.destroy()
        self._main.destroy()

    @property
    def running(self):
        return self._running

    def feed(self, state):
        """Give the latest timeline status dict (or None when unavailable). Thread-safe."""
        with self._lock:
            if state is None:
                self.clock.clear()
            else:
                self.clock.update(state)

    def set_view(self, free):
        """True -> FREE (free camera), False -> FPV (player view), None -> unknown. Thread-safe."""
        with self._lock:
            self.clock.view = None if free is None else ('free' if free else 'fpv')

    def set_target(self, name):
        """Looked-at player name (or None); shown only in FREE view. Thread-safe."""
        with self._lock:
            self.clock.target = name or None

    def set_recording(self, base):
        """Recording base (e.g. 'demos/cl04_1088') of the demo being played, or None. Thread-safe."""
        with self._lock:
            self._recording = base or None

    def set_banner(self, text):
        """Centre banner text (or None to hide). Thread-safe."""
        with self._lock:
            self._banner_text = text or None

    # -- internals
    def _poll_loop(self):
        from replay.timeline import operate
        period = 1.0 / max(0.5, self.poll_hz)
        while self._running:
            try:
                state = operate('status')
            except Exception:
                state = None
            self.feed(state)
            time.sleep(period)

    def _hide(self):
        self._main.hide()
        self._name.hide()
        self._banner.hide()

    def _visible_target(self):
        now = time.monotonic()
        if self._hwnd is None or not u32.IsWindow(self._hwnd) or now - self._hwnd_check > 3.0:
            self._hwnd_check = now
            try:
                self._hwnd = find_bc2_window()
            except Exception:
                self._hwnd = None
        hwnd = self._hwnd
        if not hwnd or u32.IsIconic(hwnd) or not u32.IsWindowVisible(hwnd):
            return None
        if self.borderless and hwnd != self._borderless_hwnd:
            self._borderless_hwnd = hwnd
            try:
                make_borderless(hwnd)
            except Exception:
                pass
        if self.foreground_only:
            fg = u32.GetForegroundWindow()
            fg_pid = _window_pid(fg) if fg else 0
            if fg_pid not in (_window_pid(hwnd), self._own_pid):
                return None
        rect = client_rect(hwnd)
        if not rect or rect[2] - rect[0] < 64 or rect[3] - rect[1] < 32:
            return None
        return rect

    def _tick(self):
        if not self._running:
            return
        try:
            with self._lock:
                text = self.clock.label(self.ascii_only)
                recording = self._recording
                banner = self._banner_text
            rect = self._visible_target() if text or banner else None
            if rect is None:
                self._hide()
            else:
                if text:
                    self._main.show_at(text, label_color(text), rect, self.corner, self.margin)
                else:
                    self._main.hide()
                name, color = name_label(text, recording)
                if name:
                    self._name.show_at(name, color, rect, name_corner(self.corner), self.margin)
                else:
                    self._name.hide()
                if banner:
                    self._banner.show_at(banner, YELLOW, rect, 'c', 0)
                else:
                    self._banner.hide()
        except Exception:
            self._hide()
        self._after = self.top.after(100, self._tick)


# ---------------------------------------------------------------- module-level API for the panel

_instance = None


def start(master, **kw):
    """Create (once) and start the overlay on the panel's Tk root. Returns the instance."""
    global _instance
    if _instance is None:
        _instance = VcrOverlay(master, **kw)
    _instance.start()
    return _instance


def stop():
    if _instance is not None:
        _instance.stop()


def feed(state):
    if _instance is not None:
        _instance.feed(state)


def set_view(free):
    if _instance is not None:
        _instance.set_view(free)


def set_target(name):
    if _instance is not None:
        _instance.set_target(name)


def set_recording(base):
    if _instance is not None:
        _instance.set_recording(base)


def set_banner(text):
    if _instance is not None:
        _instance.set_banner(text)


def _main(argv=None):
    import argparse
    import tkinter as tk
    ap = argparse.ArgumentParser(description='VCR overlay over the BC2 client window (standalone test).')
    ap.add_argument('--corner', default='tl', choices=CORNERS)
    ap.add_argument('--size', type=int, default=20)
    ap.add_argument('--margin', type=int, default=12)
    ap.add_argument('--ascii', action='store_true', help='use > and || instead of Unicode glyphs')
    ap.add_argument('--any-foreground', action='store_true', help='show even when BC2 is not foreground')
    ap.add_argument('--demo', action='store_true', help='fake clock (no server needed); toggles PLAY/PAUSE every 5 s, FPV/FREE every 10 s')
    a = ap.parse_args(argv)
    root = tk.Tk()
    root.title('VCR overlay test')
    tk.Label(root, text='VCR overlay running. Close this window to stop.').pack(padx=12, pady=8)
    ov = VcrOverlay(root, corner=a.corner, size=a.size, margin=a.margin, self_poll=not a.demo,
                    foreground_only=not a.any_foreground, ascii_only=a.ascii)
    if a.demo:
        t0 = time.monotonic()

        def fake():
            el = time.monotonic() - t0
            paused = int(el // 5) % 2 == 1
            ov.feed({'ready': 1, 'paused': int(paused), 'frame': int((245 + el) * FPS_DEFAULT)})
            ov.set_view(int(el // 10) % 2 == 1)  # FPV / FREE every 10 s
            ov.set_recording('demos/cl04_1088')
            root.after(250, fake)
        fake()
    ov.start()
    root.protocol('WM_DELETE_WINDOW', lambda: (ov.destroy(), root.destroy()))
    root.mainloop()


if __name__ == '__main__':
    _main()
