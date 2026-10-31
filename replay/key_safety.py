"""Keyframes a seek must not land on (r283): the viewer's view chain names a ghost absent from the key.

cl04_1084: the viewer dies in a helicopter at frame 4489 (2:29.6); the helicopter (ghost 159) is deleted at 4491,
but the viewer's component (class 0x1431df8) keeps p12 -> 159 until 4550, because the builder dropped the wire
update that moved p12 back to the soldier (r204; the r205 opt-in builder fix was removed in r290). Forward play delivers
that as a delete and works; a seek restores key 4500, which carries the component with p12 -> a ghost that is
not in the key, and the client is removed within 1 s of the seek (3/3 live). Keys 3900/4200 (no such ref) work.

A key is unsafe when a component of the viewer (create = 7-bit class '0001110' + 13-bit ref to the viewer's player)
or its camera (the component's p0 target) has a ref in VIEW_SLOTS naming a ghost that is not in the key.
Other dangling refs (e.g. the native death object p5 -> deleted soldier) are not judged here; ALL_REFS flips the
rule to "any dangling ref in the key" if a live test shows those disconnect too.

.fbgd layout (bc2-client-recorder work/tools/fbgd_read.py, builder_src/fbgd_read.py): prolog = two C strings, then
records u8 marker (1 key, 2 delta, 0 end) | u32 byteSize | u32 bitCount | payload. Key payload (MSB-first bits):
bit nonEmpty; u16 count-1; per ghost: u16 id, u16 sectionCount, chunk(create), per section: u32 header
(count << 16 | bitOffset), mask[count], count chunks. Chunk: u6 index, u16 bits, payload, u6 nrefs, nrefs x (u16 id, u1).
A key's panel frame = number of delta records before it. Pure module except unsafe_keys_cached (reads the file).
"""
import struct

COMP_CLASS = '0001110'   # 0x1431df8 component create prefix (wire / viewer form: class + ref13 player)
COMP_CREATE_BITS = 20
VIEW_SLOTS = {'comp': (5, 12), 'cam': (3, 5)}  # comp p5 use target, p12 entity; camera p3 target, p5 death/spawn entity
ALL_REFS = False


class Bits:
    def __init__(self, data, nbits):
        self.v = int.from_bytes(data, 'big')
        self.total = len(data) * 8
        self.n = nbits
        self.p = 0

    def get(self, w):
        if w == 0:
            return 0
        if self.p + w > self.n:
            raise ValueError('key record read past its bit count')
        val = (self.v >> (self.total - self.p - w)) & ((1 << w) - 1)
        self.p += w
        return val


def _chunk_refs(b):
    idx, nbits = b.get(6), b.get(16)
    value = b.get(nbits)
    refs = []
    for _ in range(b.get(6)):
        refs.append(b.get(16))
        b.get(1)
    return idx, nbits, value, refs


def parse_key(payload, nbits):
    """Key payload -> {ghost id: (create (bits, value, refs), {chunk index: refs})}."""
    b = Bits(payload, nbits)
    ghosts = {}
    if not b.get(1):
        return ghosts
    for _ in range(b.get(16) + 1):
        gid, nsec = b.get(16), b.get(16)
        _i, cbits, cval, crefs = _chunk_refs(b)
        refs = {}
        for _s in range(nsec):
            count = b.get(32) >> 16
            b.get(count)  # mask (metadata; every chunk is written)
            for _c in range(count):
                idx, _n, _v, r = _chunk_refs(b)
                if r:
                    refs.setdefault(idx, []).extend(r)
        ghosts[gid] = ((cbits, cval, crefs), refs)
    return ghosts


def iter_keys(data):
    """Yield (panel frame, key payload, bit count) for every key record of .fbgd bytes."""
    try:
        p = data.index(b'\0', data.index(b'\0') + 1) + 1
    except ValueError:
        raise ValueError('Unexpected .fbgd prolog') from None
    deltas = 0
    while p + 9 <= len(data):
        marker = data[p]
        if marker == 0:
            return
        size, bits = struct.unpack_from('<II', data, p + 1)
        if marker not in (1, 2) or bits > size * 8 or p + 9 + size > len(data):
            raise ValueError('Unexpected .fbgd record header')
        if marker == 1:
            yield deltas, data[p + 9:p + 9 + size], bits
        else:
            deltas += 1
        p += 9 + size


def is_component_of(create, players):
    cbits, cval, crefs = create
    if cbits != COMP_CREATE_BITS or format(cval, '0%db' % cbits)[:len(COMP_CLASS)] != COMP_CLASS:
        return False
    return any(r in players for r in crefs)


def dangling(ghosts, players, all_refs=None):
    """['comp134.p12->159', ...]: view-chain refs of the viewer naming ghosts absent from the key."""
    all_refs = ALL_REFS if all_refs is None else all_refs
    out = []
    if all_refs:
        for gid, (_c, refs) in ghosts.items():
            out += ['g%d.p%d->%d' % (gid, i, r) for i, rs in sorted(refs.items()) for r in rs if r not in ghosts]
        return out
    for gid, (create, refs) in ghosts.items():
        if not is_component_of(create, players):
            continue
        out += ['comp%d.p%d->%d' % (gid, i, r) for i in VIEW_SLOTS['comp'] for r in refs.get(i, ()) if r not in ghosts]
        for cam in refs.get(0, ()):
            if cam in ghosts:
                crefs = ghosts[cam][1]
                out += ['cam%d.p%d->%d' % (cam, i, r) for i in VIEW_SLOTS['cam'] for r in crefs.get(i, ()) if r not in ghosts]
    return out


def unsafe_keys(data, players, all_refs=None):
    """{key panel frame: [dangling refs]} for the keys of .fbgd bytes that a seek must avoid."""
    players = set(players or ())
    if not players and not (ALL_REFS if all_refs is None else all_refs):
        return {}
    out = {}
    for frame, payload, bits in iter_keys(data):
        bad = dangling(parse_key(payload, bits), players, all_refs)
        if bad:
            out[frame] = bad
    return out


def viewer_ids(players_data):
    """Demo player ids whose component is the client's view: players.json viewer.demo_id."""
    vid = ((players_data or {}).get('viewer') or {}).get('demo_id')
    return {vid} if isinstance(vid, int) else set()


_CACHE = {}


def unsafe_keys_cached(path, players):
    st = path.stat()
    sig = (str(path), st.st_mtime_ns, st.st_size, tuple(sorted(players)), ALL_REFS)
    if sig not in _CACHE:
        _CACHE.clear()
        _CACHE[sig] = unsafe_keys(path.read_bytes(), players)
    return _CACHE[sig]


def redirect(keys, unsafe, frame):
    """(target frame, None) if the seek is fine, else (safe key, unsafe key it avoided).

    A seek restores the key at or before frame. If that key is unsafe: the largest safe key before it, else the
    smallest safe key after it, else frame unchanged."""
    if not unsafe or not keys:
        return frame, None
    floor = None
    for k in keys:
        if k <= frame:
            floor = k
        else:
            break
    if floor is None or floor not in unsafe:
        return frame, None
    before = [k for k in keys if k < floor and k not in unsafe]
    if before:
        return before[-1], floor
    after = [k for k in keys if k > floor and k not in unsafe]
    if after:
        return after[0], floor
    return frame, None
