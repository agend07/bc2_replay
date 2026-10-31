"""r286: put composite destruction (walls, fences, house parts) back to its level-start state in a running client.

A seek never repairs what the client has already destroyed (live 2026-10-30). Static RE (private r286 report):
composite destruction reaches each part through ClientPartComponent::setHealthState 0x7EAAA0 (vtable 0x1430A28
slot +0x150, __thiscall (state, a, b), ret 0xC); its only early-out is "state == [part+0x40]" (signed byte), so the
engine swaps mesh and physics both ways. Live cl04_1084: parts start at 1 and a destroyed part reads 0.
Group destruction (instanced props) only steps down and is not touched here.

Mechanism: the client's ClientConnection ([0x1571430], vtable 0x14182A8, 36 slots) gets an instance vtable
clone whose slot +0x7C (UpdateTick 0x566C90, per frame on the game thread that also applies destruction
deltas) runs one pending batch from the mailbox, then tail-jumps to the native UpdateTick. A batch is a list of
(part, state) pairs; each part's vtable is checked in the stub. With QUIET the part flag [part+0x60] bit 0 is set
for the call (skips transition effects 7E9D50) and restored after it.
Snapshot = every part's state at join (read-only); reset = every part whose state differs, back to the snapshot.
"""
import struct
import time

from replay.client import Client
from replay.clean_screen import WriteClient, k, c, w

CONNECTION_GLOBAL = 0x1571430
CONNECTION_VTABLE = 0x14182A8
CONNECTION_SLOTS = 36
UPDATE_TICK_SLOT = 0x7C
UPDATE_TICK = 0x566C90
COMPOSITE_MANAGER = 0x1768  # ClientConnection field; level target T = [manager+8]
PART_VTABLE = 0x1430A28
SET_HEALTH_STATE_SLOT = 0x150
MAGIC = b'BC2DRS1\0'

REGION = 0x11000  # page 0 code, then mailbox (header, clone, list)
MB_MAGIC, MB_REQUEST, MB_ACK, MB_COUNT, MB_QUIET, MB_CALLS, MB_SKIPPED = 0x00, 0x08, 0x0C, 0x10, 0x14, 0x18, 0x1C
MB_CLONE = 0x100  # [MB_CLONE] = RTTI locator copy, slots from MB_CLONE + 4
MB_LIST = 0x400
MAX_PARTS = (REGION - 0x1000 - MB_LIST) // 8
MAX_ENTITIES = 1 << 16
MAX_PARTS_PER_ENTITY = 512


def stub(code, mailbox):
    """Clone slot +0x7C (thiscall, ecx = connection, one stack arg): run a pending batch, then native."""
    b = bytearray()
    b += b'\x9c\x60' + b'\xbb' + struct.pack('<I', mailbox)          # pushfd; pushad; mov ebx, mailbox
    b += b'\x8b\x43' + bytes([MB_REQUEST]) + b'\x3b\x43' + bytes([MB_ACK])
    je_done = len(b); b += b'\x74\x00'
    b += b'\x8b\x73' + bytes([MB_COUNT])                                # mov esi, [ebx+count]
    b += b'\x8d\xbb' + struct.pack('<I', MB_LIST)                      # lea edi, [ebx+list]
    loop = len(b)
    b += b'\x85\xf6'; jz_fin = len(b); b += b'\x74\x00'                # test esi, esi; jz fin
    b += b'\x8b\x0f'                                                    # mov ecx, [edi]
    b += b'\x81\x39' + struct.pack('<I', PART_VTABLE)                  # cmp dword [ecx], PART_VTABLE
    jne_skip = len(b); b += b'\x75\x00'
    b += b'\x0f\xb6\x51\x60\x52\x51'                                    # movzx edx, byte [ecx+0x60]; push edx; push ecx
    b += b'\xf6\x43' + bytes([MB_QUIET]) + b'\x01\x74\x04\x80\x49\x60\x01'  # quiet ? or byte [ecx+0x60], 1
    b += b'\x6a\x00\x6a\x00\xff\x77\x04'                                # push 0; push 0; push [edi+4]
    b += b'\x8b\x01\xff\x90' + struct.pack('<I', SET_HEALTH_STATE_SLOT)  # mov eax, [ecx]; call [eax+0x150]
    b += b'\x59\x5a\x88\x51\x60'                                        # pop ecx; pop edx; mov [ecx+0x60], dl
    b += b'\xff\x43' + bytes([MB_CALLS]) + b'\xeb\x03'                  # inc calls; jmp next
    skip = len(b); b += b'\xff\x43' + bytes([MB_SKIPPED])               # inc skipped
    b += b'\x83\xc7\x08\x4e\xeb' + bytes([(loop - (len(b) + 6)) & 0xFF])  # add edi, 8; dec esi; jmp loop
    fin = len(b)
    b += b'\x8b\x43' + bytes([MB_REQUEST]) + b'\x89\x43' + bytes([MB_ACK])  # ack = request
    done = len(b)
    b += b'\x61\x9d\xe9' + struct.pack('<I', (UPDATE_TICK - (code + len(b) + 7)) & 0xFFFFFFFF)  # popad; popfd; jmp
    for at, target in ((je_done, done), (jz_fin, fin), (jne_skip, skip)):
        b[at + 1] = target - (at + 2)
    return bytes(b)


def connection(client):
    conn = client.u32(CONNECTION_GLOBAL)
    if not conn:
        raise RuntimeError('No client connection (not joined)')
    return conn


def composite_target(client):
    mgr = client.u32(connection(client) + COMPOSITE_MANAGER)
    target = client.u32(mgr + 8) if mgr else 0
    if not target:
        raise RuntimeError('Composite destruction not bound to a level')
    return target


def part_states(client):
    """{part address: state} of every ClientPartComponent of the level's composite entities (read-only)."""
    target = composite_target(client)
    begin, end = client.u32(target + 0x74), client.u32(target + 0x78)
    if not (begin <= end and (end - begin) // 4 <= MAX_ENTITIES):
        raise RuntimeError('Unrecognized composite entity table')
    states = {}
    table = client.read(begin, end - begin) if end > begin else b''
    for (entity,) in struct.iter_unpack('<I', table):
        if not entity:
            continue
        try:
            pb, pe = client.u32(entity + 0x5C), client.u32(entity + 0x60)
            if not (pb <= pe and pe - pb <= 4 * MAX_PARTS_PER_ENTITY):
                continue
            for (part,) in struct.iter_unpack('<I', client.read(pb, pe - pb) if pe > pb else b''):
                if part and client.u32(part) == PART_VTABLE:
                    states[part] = struct.unpack('<b', client.read(part + 0x40, 1))[0]
        except OSError:
            continue
    return target, states


class Snapshot:
    """Level-start part states of one client process and level target."""

    def __init__(self, pid, target, states):
        self.pid, self.target, self.states = pid, target, states

    @classmethod
    def take(cls, client=None):
        own = client is None
        client = client or Client()
        try:
            target, states = part_states(client)
            return cls(client.pid, target, states)
        finally:
            if own:
                client.close()


def _mailbox(client):
    """Mailbox of our installed clone, or None. Never trusts a foreign vtable."""
    conn = connection(client)
    current = client.u32(conn)
    if current == CONNECTION_VTABLE:
        return None
    mailbox = current - 4 - MB_CLONE
    try:
        if client.read(mailbox, 8) == MAGIC:
            return mailbox
    except OSError:
        pass
    raise RuntimeError(f'ClientConnection vtable {current:#x} is neither native nor ours; nothing written')


def install(client):
    mailbox = _mailbox(client)
    if mailbox is not None:
        return mailbox
    conn = connection(client)
    base = k.VirtualAllocEx(client.handle, None, REGION, 0x3000, 0x04)
    if not base or base > 0xFFFFFFFF - REGION:
        raise RuntimeError('Cannot allocate destruction-reset pages')
    mailbox = base + 0x1000
    code = stub(base, mailbox)
    native = client.read(CONNECTION_VTABLE - 4, 4 + 4 * CONNECTION_SLOTS)
    clone = bytearray(native)
    struct.pack_into('<I', clone, 4 + UPDATE_TICK_SLOT, base)
    client.write(base, code)
    client.write(mailbox, MAGIC + bytes(MB_CLONE - len(MAGIC)))
    client.write(mailbox + MB_CLONE, bytes(clone))
    old = w.DWORD()
    if not k.VirtualProtectEx(client.handle, base, 0x1000, 0x20, c.byref(old)) \
            or not k.FlushInstructionCache(client.handle, base, 0x1000):
        raise c.WinError(c.get_last_error())
    if client.u32(conn) != CONNECTION_VTABLE or connection(client) != conn:
        raise RuntimeError('Connection changed before publication; nothing published')
    client.write(conn, struct.pack('<I', mailbox + MB_CLONE + 4))
    return mailbox


def run_batch(client, pairs, quiet=True, timeout=5.0):
    """Apply [(part, state)] on the game thread; returns {'calls', 'skipped'} for this batch."""
    if len(pairs) > MAX_PARTS:
        raise ValueError(f'At most {MAX_PARTS} parts per batch')
    mailbox = install(client)
    request, ack = struct.unpack('<II', client.read(mailbox + MB_REQUEST, 8))
    if request != ack:
        raise RuntimeError('A destruction-reset batch is still pending')
    calls0, skipped0 = struct.unpack('<II', client.read(mailbox + MB_CALLS, 8))
    client.write(mailbox + MB_LIST, b''.join(struct.pack('<Ii', p, s) for p, s in pairs))
    client.write(mailbox + MB_COUNT, struct.pack('<II', len(pairs), 1 if quiet else 0))
    client.write(mailbox + MB_REQUEST, struct.pack('<I', (request + 1) & 0xFFFFFFFF))
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if client.u32(mailbox + MB_ACK) == (request + 1) & 0xFFFFFFFF:
            calls, skipped = struct.unpack('<II', client.read(mailbox + MB_CALLS, 8))
            return {'calls': calls - calls0, 'skipped': skipped - skipped0}
        time.sleep(0.02)
    raise RuntimeError('Destruction reset not serviced (game loop not ticking?)')


def reset(snapshot, client=None, quiet=True):
    """Every part whose state differs from the snapshot goes back to it. Returns a summary dict."""
    own = client is None
    client = client or WriteClient()
    try:
        if client.pid != snapshot.pid:
            raise RuntimeError('Snapshot belongs to another client process')
        target, states = part_states(client)
        if target != snapshot.target:
            raise RuntimeError('Level changed since the snapshot')
        pairs = [(p, s) for p, s in snapshot.states.items() if p in states and states[p] != s]
        result = run_batch(client, pairs, quiet=quiet) if pairs else {'calls': 0, 'skipped': 0}
        result['changed'] = len(pairs)
        return result
    finally:
        if own:
            client.close()
