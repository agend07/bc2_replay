"""Safe seek (r224): snap timeline seeks to .fbgk keyframes and rate-limit them.

r223: the replay server crashed after BURSTS of seeks to mid-interval frames
(key restore + fast-forward through deltas); single exact-key seeks survived.

Frame space: the panel/server seek frame == the .fbgk frameCounter (logical
frame, 30 fps). .fbgk = u32 key interval (150) + 12-byte entries
<u32 frameCounter, f32 time (0.0), u32 .fbgd byte offset>; keys at 0,150,300...
The .fbgd physical record index of key k is 151*k (key records are extra
records); that numbering is NOT the seek frame and is not used here.

Pure module (no ctypes / config imports) so it can be tested offline.
"""
import struct
import time

MIN_INTERVAL_S = 2.0
# Server lands one frame past the seek target (key K reads back as K+1); a
# frame within this many frames above a key counts as "at" that key.
AT_KEY_TOLERANCE = 3


def parse_fbgk(data):
    """Return sorted logical key frames from .fbgk bytes."""
    if len(data) < 16 or (len(data) - 4) % 12:
        raise ValueError('Unexpected keyframe index layout.')
    frames = [struct.unpack_from('<I', data, i)[0] for i in range(4, len(data), 12)]
    if frames != sorted(set(frames)):
        raise ValueError('Unexpected keyframe order.')
    return frames


def usable_keys(keys, limit):
    return [k for k in keys if 0 <= k <= limit]


def snap_floor(keys, frame):
    """Nearest key at or before frame (first key if frame precedes all)."""
    best = None
    for k in keys:
        if k <= frame:
            best = k
        else:
            break
    return keys[0] if best is None and keys else best


def at_key(keys, frame, tol=AT_KEY_TOLERANCE):
    """Return frame's key if frame is within tol above a key, else frame."""
    k = snap_floor(keys, frame)
    if k is not None and 0 <= frame - k <= tol:
        return k
    return frame


def step_key(keys, cur, delta):
    """Key to move to for a relative step; always >= one key in delta's sign.

    Forward: smallest key >= cur+delta that is > cur, else the next key.
    Backward: largest key <= cur+delta that is < cur, else the previous key.
    Returns None at the edge (no key in that direction)."""
    if not keys or delta == 0:
        return None
    ref = at_key(keys, cur)
    target = ref + delta
    if delta > 0:
        ahead = [k for k in keys if k > ref]
        if not ahead:
            return None
        hit = [k for k in ahead if k >= target]
        return hit[0] if hit else ahead[-1]
    behind = [k for k in keys if k < ref]
    if not behind:
        return None
    hit = [k for k in behind if k <= target]
    return hit[-1] if hit else behind[0]


def resolve(keys, cur, request):
    """Apply a (possibly coalesced) request; returns target key or None."""
    ref = request.get('base')
    if ref is None:
        ref = cur
    moved = request.get('base') is not None
    for d in request.get('steps', ()):
        nxt = step_key(keys, ref, d)
        if nxt is None:
            break
        ref, moved = nxt, True
    return ref if moved else None


class SeekGate:
    """One seek at a time, >= MIN_INTERVAL_S after the previous completed.

    Requests while not ready are coalesced: an absolute request (base) replaces
    the pending one, step requests append their delta."""

    def __init__(self, min_interval=MIN_INTERVAL_S, clock=time.monotonic):
        self.min_interval = min_interval
        self.clock = clock
        self.in_flight = False
        self.last_done = None
        self.pending = None

    def remaining(self):
        if self.in_flight:
            return self.min_interval
        if self.last_done is None:
            return 0.0
        return max(0.0, self.min_interval - (self.clock() - self.last_done))

    def ready(self):
        return not self.in_flight and self.remaining() <= 0.0

    def queue(self, request):
        if request.get('base') is not None or self.pending is None:
            self.pending = dict(request)
            self.pending['steps'] = list(request.get('steps', ()))
        else:
            self.pending['steps'].extend(request.get('steps', ()))
        return self.pending

    def take(self):
        """Pending request if it may be sent now (marks in flight), else None."""
        if self.pending is None or not self.ready():
            return None
        req, self.pending = self.pending, None
        self.in_flight = True
        return req

    def start(self):
        self.in_flight = True

    def done(self):
        self.in_flight = False
        self.last_done = self.clock()

    def clear(self):
        self.pending = None
