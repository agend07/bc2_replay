"""Skip the empty load gap at the start of our demos (r242 option C).

Our demos (SHIFTF recipe) are key0 + a short join-batch tail + a long run of EMPTY deltas until the
first game content (cl04_1088: frames 20..2007, content at 2008 = 66.9 s). The seek guard refuses
until the client HUD exists, so after a join the panel retries a seek to (first content - LEAD_S),
snapped to the key at or before it, until it is accepted.

First content per demo: demos/<stem>.players.json 'game_start_frame' when present, else a header-
only scan of the .fbgd: frame = u8 marker (1 key, 2 delta, 0 end) | u32 byteSize | u32 bitCount |
payload; an empty delta is exactly 64 bits (f31 dt, f31 sim, no-deleted bit, no-dirty bit; the
reader in bc2-client-recorder work/tools/fbgd_read.py). Panel frame = 1-based delta number.
Only a run of >= MIN_GAP_FRAMES empty deltas that starts within GAP_START_MAX frames counts; server
demos (ghost 51 updated every ~2nd delta) have none, so nothing is done for them.
Pure module except scan_fbgd (reads the file); tested offline.
"""
import struct
import time

EMPTY_DELTA_BITS = 64
MIN_GAP_FRAMES = 300  # 10 s of empty deltas
GAP_START_MAX = 120   # the gap must begin within the first 4 s (after the join-batch tail)
SCAN_MAX_DELTAS = 20000
LEAD_S = 3.0          # land this long before the first content (forward-play entry stays)
MIN_TARGET_S = 10.0   # not worth a seek below this
RETRY_S = 2.0
GIVE_UP_S = 60.0


def first_content(delta_bits):
    """delta_bits: bit counts of deltas 1, 2, ... -> panel frame of the first non-empty delta after
    a qualifying empty run, or None."""
    run_start = None
    for frame, bits in enumerate(delta_bits, start=1):
        if bits == EMPTY_DELTA_BITS:
            if run_start is None:
                run_start = frame
            continue
        if run_start is not None and run_start <= GAP_START_MAX and frame - run_start >= MIN_GAP_FRAMES:
            return frame
        run_start = None
        if frame > GAP_START_MAX:
            return None
    return None


def iter_delta_bits(f, max_deltas=SCAN_MAX_DELTAS):
    """Yield the bit count of each delta record of an open .fbgd (headers only, payloads skipped)."""
    prolog = f.read(256)
    try:
        end = prolog.index(b'\0', prolog.index(b'\0') + 1) + 1
    except ValueError:
        raise ValueError('Unexpected .fbgd prolog') from None
    f.seek(end)
    n = 0
    while n < max_deltas:
        head = f.read(9)
        if len(head) < 9 or head[0] == 0:
            return
        marker, size, bits = head[0], *struct.unpack_from('<II', head, 1)
        if marker not in (1, 2) or bits > size * 8:
            raise ValueError('Unexpected .fbgd record header')
        if marker == 2:
            n += 1
            yield bits
        f.seek(size, 1)


_CACHE = {}


def scan_fbgd(path):
    """First-content frame of a .fbgd (None if no load gap), cached by path/mtime/size."""
    st = path.stat()
    sig = (str(path), st.st_mtime_ns, st.st_size)
    if sig not in _CACHE:
        with open(path, 'rb') as f:
            _CACHE[sig] = first_content(iter_delta_bits(f))
    return _CACHE[sig]


def content_frame(players_data, fbgd_path):
    """players.json game_start_frame when present, else the .fbgd scan; None if unknown."""
    gs = (players_data or {}).get('game_start_frame')
    if isinstance(gs, int) and gs > 0:
        return gs
    try:
        return scan_fbgd(fbgd_path)
    except (OSError, ValueError):
        return None


def seek_target(content, keys=None, fps=30):
    """Frame to seek to: (content - LEAD_S) snapped to the key at or before it; None if <= MIN_TARGET_S."""
    if not content:
        return None
    target = int(content - LEAD_S * fps)
    if keys:
        floor = [k for k in keys if k <= target]
        target = floor[-1] if floor else None
    if target is None or target <= MIN_TARGET_S * fps:
        return None
    return target


class GapSeek:
    """Once per join: retry the seek every RETRY_S until accepted, give up after GIVE_UP_S; cancelled
    by any user seek or when playback is already at/after the target."""

    def __init__(self, clock=time.monotonic):
        self.clock = clock
        self.target = None
        self.armed_at = None
        self.last_try = None

    def arm(self, target):
        self.target, self.armed_at, self.last_try = target, self.clock(), None
        return target is not None

    def cancel(self):
        self.target = None

    def action(self, frame):
        """'idle' (nothing armed), 'wait', 'seek' (caller tries now) or 'give_up'."""
        if self.target is None:
            return 'idle'
        now = self.clock()
        if frame is not None and frame >= self.target:
            self.cancel()  # already past it (played or seeked through)
            return 'idle'
        if now - self.armed_at > GIVE_UP_S:
            self.cancel()
            return 'give_up'
        if self.last_try is not None and now - self.last_try < RETRY_S:
            return 'wait'
        self.last_try = now
        return 'seek'

    def accepted(self):
        self.cancel()
