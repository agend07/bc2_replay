"""Exact-build instance-vtable seek view-readiness guard.

Instead of patching executing .text bytes at 0x7813B6, this attaches an
instance-vtable wrapper on the single validated HUD/UI object (primary table
0x142B7A0, secondary tables 0x142B794 / 0x142B780 / 0x142B764, singleton
0x15704A8 and [0x19A4CA8]+0x14).

Virtual slot +0x18 (0x785140) drives per-frame HUD/minimap updates that call
0x784B90 -> 0x781370 / 0x77F2E0 / 0x74A5D0. During timeline seek resync, the
selected ClPlayer ([[0x15713D8]+0x2C]+0xB4) briefly has a soldier entity before
its ClPlayerView (+0xC8C) is bound. When the wrapper observes a selected player
with a NULL view pointer (+0xC8C == 0), it increments a private skip counter,
restores registers/flags, and returns stdcall (ret 4) for that single UI tick.
Otherwise it restores all registers and flags (popad + popfd) and tail-jumps to
unchanged native 0x785140.

Publication and detach are single 4-byte aligned instance-vtable pointer writes
after RX page protection and instruction-cache flush. No .text bytes, server
state, demo state, player selection, or camera state are modified.
"""
import ctypes as c
from ctypes import wintypes as w
import hashlib
import json
import struct
import time

from replay.client import Client, k
from preview_config import APP_ROOT as ROOT, client_exe
from replay.timeline_guard import check as check_timeline

UI_VTABLE = 0x142B7A0
UI_SUBTABLE_2C = 0x142B794
UI_SUBTABLE_30 = 0x142B780
UI_SUBTABLE_94 = 0x142B764
UI_VTABLE_BYTES = 0x40  # covers RTTI at -4 through 15 virtual entries (+0x38)
UI_SLOT_OFFSET = 0x18
NATIVE_UI_UPDATE = 0x785140

ROOT_GLOBAL = 0x19A4CA8
UI_SINGLETON = 0x15704A8
CLIENT_CONTEXT = 0x15713D8
PLAYER_MANAGER_PTR = 0x1571404  # CLIENT_CONTEXT + 0x2C
MANAGER_VTABLE = 0x14320F0
MANAGER_SUBTABLE_10 = 0x14320D4
PLAYER_VTABLE = 0x1431D3C
VIEW_VTABLE = 0x1431E34

STATE = ROOT / 'runtime' / 'private_seek_guard_session.json'
MAGIC = b'BC2SVG2\0'
VERSION = 2

# Mailbox layout (within second 4096-byte page of allocation):
MB_MAGIC = 0x00
MB_HEARTBEAT = 0x08
MB_SKIPPED = 0x0C
MB_ENABLED = 0x10
MB_OWNER = 0x14
MB_LAST_PLAYER = 0x18
MB_LAST_VIEW = 0x1C
CLONE_VTABLE_OFFSET = 0x104  # clone[-4] at 0x100 holds RTTI pointer

k.VirtualAllocEx.argtypes = [w.HANDLE, c.c_void_p, c.c_size_t, w.DWORD, w.DWORD]
k.VirtualAllocEx.restype = c.c_void_p
k.VirtualProtectEx.argtypes = [w.HANDLE, c.c_void_p, c.c_size_t, w.DWORD, c.POINTER(w.DWORD)]
k.VirtualProtectEx.restype = w.BOOL
k.FlushInstructionCache.argtypes = [w.HANDLE, c.c_void_p, c.c_size_t]
k.FlushInstructionCache.restype = w.BOOL


from replay.camera_native import ControlClient as GuardClient


def build(code_address, mailbox):
    """Build x86 instance-vtable wrapper for UI update 0x785140 (thiscall(float dt))."""
    code = bytearray()
    labels = {}
    fixups = []

    def emit(hex_bytes):
        code.extend(bytes.fromhex(hex_bytes))

    def word(value):
        code.extend(struct.pack('<I', value & 0xFFFFFFFF))

    def label(name):
        labels[name] = len(code)

    def jump(opcode_hex, target):
        emit(opcode_hex)
        fixups.append((len(code), target))
        word(0)

    # Save flags and all general-purpose registers so native path receives
    # bit-identical CPU state.
    emit('9c 60 bb')
    word(mailbox)

    # Verify ECX is our exact hooked UI owner and increment heartbeat.
    emit('3b 4b 14')
    jump('0f85', 'native')
    emit('ff 43 08')

    # Check if guard policy is enabled.
    emit('83 7b 10 01')
    jump('0f85', 'native')

    # Check client context manager pointer at 0x1571404 (0x15713D8 + 0x2C).
    emit('a1')
    word(PLAYER_MANAGER_PTR)
    emit('85 c0')
    jump('0f84', 'native')

    # Verify manager primary/secondary vtables before reading selected player.
    emit('81 38')
    word(MANAGER_VTABLE)
    jump('0f85', 'native')
    emit('81 78 10')
    word(MANAGER_SUBTABLE_10)
    jump('0f85', 'native')

    # Read selected player at manager + 0xB4 (same as native 0x7EDA00).
    emit('8b 80 b4 00 00 00')
    emit('89 43 18')  # record last observed selected player
    emit('85 c0')
    jump('0f84', 'native')

    # Verify selected player vtable (0x1431D3C). If not a standard ClPlayer,
    # skip this UI tick rather than letting 0x785140 dereference it.
    emit('81 38')
    word(PLAYER_VTABLE)
    jump('0f85', 'skip')

    # Read player view at player + 0xC8C (same as native 0x7ED850).
    emit('8b 90 8c 0c 00 00')
    emit('89 53 1c')  # record last observed player view
    emit('85 d2')
    jump('0f84', 'skip')

    # Verify view vtable (0x1431E34) and reciprocal owner link (view + 0x8C == player).
    emit('81 3a')
    word(VIEW_VTABLE)
    jump('0f85', 'skip')
    emit('3b 82 8c 00 00 00')
    jump('0f85', 'skip')

    label('native')
    emit('61 9d e9')
    word(NATIVE_UI_UPDATE - (code_address + len(code) + 4))

    label('skip')
    emit('ff 43 0c')  # increment skipped null/unready view update counter
    emit('61 9d c2 04 00')  # restore registers/flags and return stdcall (pops float dt)

    for offset, target in fixups:
        struct.pack_into('<i', code, offset, labels[target] - (offset + 4))

    if len(code) > 4096:
        raise RuntimeError('Guard wrapper exceeds RX page')
    return bytes(code)


def validate_ui_owner(client, owner):
    if not owner or owner % 4 != 0:
        return False
    try:
        vtable = client.u32(owner)
        if vtable != UI_VTABLE:
            return False
        if (client.u32(owner + 0x2C) != UI_SUBTABLE_2C
                or client.u32(owner + 0x30) != UI_SUBTABLE_30
                or client.u32(owner + 0x94) != UI_SUBTABLE_94):
            return False
        if client.u32(owner + 0x27460) != owner:
            return False
        return True
    except OSError:
        return False


def recover_installed_session(client, owner):
    """If our own BC2SVG2 clone vtable is already active on owner, recover its mailbox."""
    if not owner or owner % 4 != 0:
        return None
    try:
        current_vtable = client.u32(owner)
        if current_vtable == UI_VTABLE or current_vtable < CLONE_VTABLE_OFFSET + 4096:
            return None
        if (client.u32(owner + 0x2C) != UI_SUBTABLE_2C
                or client.u32(owner + 0x30) != UI_SUBTABLE_30
                or client.u32(owner + 0x94) != UI_SUBTABLE_94
                or client.u32(owner + 0x27460) != owner):
            return None
        mailbox = current_vtable - CLONE_VTABLE_OFFSET
        code_addr = mailbox - 4096
        if client.read(mailbox, 8) != MAGIC or client.u32(mailbox + MB_OWNER) != owner:
            return None
        data = {
            'version': VERSION,
            'pid': client.pid,
            'owner': owner,
            'code': code_addr,
            'mailbox': mailbox,
            'clone_vtable': current_vtable,
            'original_vtable': UI_VTABLE,
            'slot_offset': UI_SLOT_OFFSET,
            'native_target': NATIVE_UI_UPDATE,
            'bytes': len(build(code_addr, mailbox)),
            'exe_sha256': hashlib.sha256(client_exe().read_bytes()).hexdigest(),
            'timeline_verified': True,
        }
        STATE.parent.mkdir(parents=True, exist_ok=True)
        STATE.write_text(json.dumps(data, indent=2), encoding='utf-8')
        return data
    except OSError:
        return None


def locate_ui_owner(client):
    root = client.u32(ROOT_GLOBAL)
    singleton = client.u32(UI_SINGLETON)
    from_root = client.u32(root + 0x14) if root else 0
    if not singleton or singleton != from_root:
        raise RuntimeError('HUD/UI singleton and root+0x14 do not match; join replay first')
    if not validate_ui_owner(client, singleton):
        raise RuntimeError('HUD/UI object failed vtable/subobject validation')
    if client.u32(UI_VTABLE + UI_SLOT_OFFSET) != NATIVE_UI_UPDATE:
        raise RuntimeError('Unexpected native UI vtable +0x18 entry')
    return singleton


def session(client):
    if not STATE.exists():
        root = client.u32(ROOT_GLOBAL)
        singleton = client.u32(UI_SINGLETON)
        from_root = client.u32(root + 0x14) if root else 0
        if singleton and singleton == from_root:
            recovered = recover_installed_session(client, singleton)
            if recovered is not None:
                return recovered
    data = json.loads(STATE.read_text(encoding='utf-8'))
    if data['pid'] != client.pid or client.read(data['mailbox'], 8) != MAGIC:
        root = client.u32(ROOT_GLOBAL)
        singleton = client.u32(UI_SINGLETON)
        from_root = client.u32(root + 0x14) if root else 0
        if singleton and singleton == from_root:
            recovered = recover_installed_session(client, singleton)
            if recovered is not None:
                return recovered
        raise RuntimeError('Guard session differs from running client')
    owner = data['owner']
    current_vtable = client.u32(owner)
    if current_vtable not in (data['clone_vtable'], UI_VTABLE):
        raise RuntimeError('UI owner vtable changed unexpectedly; restart client for a clean reset')
    if (client.u32(owner + 0x2C) != UI_SUBTABLE_2C
            or client.u32(owner + 0x30) != UI_SUBTABLE_30
            or client.u32(owner + 0x94) != UI_SUBTABLE_94
            or client.u32(owner + 0x27460) != owner):
        raise RuntimeError('UI owner subobjects changed; no writes made')
    return data


def status_from_session(client, data):
    owner = data['owner']
    mailbox = data['mailbox']
    current_vtable = client.u32(owner)
    last_player = client.u32(mailbox + MB_LAST_PLAYER)
    last_view = client.u32(mailbox + MB_LAST_VIEW)
    return {
        'pid': client.pid,
        'version': data.get('version', VERSION),
        'installed': current_vtable == data['clone_vtable'],
        'enabled': bool(client.u32(mailbox + MB_ENABLED)),
        'heartbeat': client.u32(mailbox + MB_HEARTBEAT),
        'null_view_updates_skipped': client.u32(mailbox + MB_SKIPPED),
        'owner': hex(owner),
        'current_vtable': hex(current_vtable),
        'clone_vtable': hex(data['clone_vtable']),
        'original_vtable': hex(UI_VTABLE),
        'guard_entry': hex(data['code']),
        'native_target': hex(NATIVE_UI_UPDATE),
        'last_player': hex(last_player) if last_player else None,
        'last_view': hex(last_view) if last_view else None,
    }


def install():
    timeline_info = check_timeline()
    client = GuardClient()
    try:
        try:
            existing = session(client)
            current = status_from_session(client, existing)
            if current['installed']:
                if not current['enabled']:
                    client.write(existing['mailbox'] + MB_ENABLED, struct.pack('<I', 1))
                    current = status_from_session(client, existing)
                return current
        except (OSError, ValueError, KeyError, RuntimeError):
            pass

        owner = locate_ui_owner(client)
        allocation = k.VirtualAllocEx(client.handle, None, 8192, 0x3000, 4)
        if not allocation or allocation > 0xFFFFFFFF - 8192:
            raise RuntimeError('Cannot allocate x86 guard pages')
        code_addr = allocation
        mailbox = allocation + 4096
        clone_vtable = mailbox + CLONE_VTABLE_OFFSET

        payload = build(code_addr, mailbox)
        table = bytearray(client.read(UI_VTABLE - 4, UI_VTABLE_BYTES))
        if struct.unpack_from('<I', table, 4 + UI_SLOT_OFFSET)[0] != NATIVE_UI_UPDATE:
            raise RuntimeError('Native UI vtable slot +0x18 mismatch')
        struct.pack_into('<I', table, 4 + UI_SLOT_OFFSET, code_addr)

        initial = bytearray(4096)
        initial[:8] = MAGIC
        struct.pack_into('<I', initial, MB_ENABLED, 1)
        struct.pack_into('<I', initial, MB_OWNER, owner)
        initial[CLONE_VTABLE_OFFSET - 4:CLONE_VTABLE_OFFSET - 4 + len(table)] = table

        client.write(code_addr, payload)
        client.write(mailbox, initial)

        old_protect = w.DWORD()
        if not k.VirtualProtectEx(client.handle, code_addr, 4096, 0x20, c.byref(old_protect)):
            raise c.WinError(c.get_last_error())
        if not k.FlushInstructionCache(client.handle, code_addr, len(payload)):
            raise c.WinError(c.get_last_error())

        data = {
            'version': VERSION,
            'pid': client.pid,
            'owner': owner,
            'code': code_addr,
            'mailbox': mailbox,
            'clone_vtable': clone_vtable,
            'original_vtable': UI_VTABLE,
            'slot_offset': UI_SLOT_OFFSET,
            'native_target': NATIVE_UI_UPDATE,
            'bytes': len(payload),
            'exe_sha256': hashlib.sha256(client_exe().read_bytes()).hexdigest(),
            'timeline_verified': bool(timeline_info),
        }
        # Persist recovery metadata before atomic 4-byte vtable pointer publication.
        STATE.write_text(json.dumps(data, indent=2), encoding='utf-8')

        check_timeline()
        if locate_ui_owner(client) != owner:
            raise RuntimeError('UI owner changed before hook publication; no hook installed')

        client.write(owner, struct.pack('<I', clone_vtable))
        deadline = time.monotonic() + 5.0
        while time.monotonic() < deadline:
            current = status_from_session(client, data)
            if current['heartbeat'] >= 2:
                return current
            time.sleep(0.05)

        if client.u32(owner) == clone_vtable:
            client.write(owner, struct.pack('<I', UI_VTABLE))
        raise RuntimeError('Guard vtable hook did not tick; original vtable restored')
    finally:
        client.close()


def status():
    client = Client()
    try:
        data = session(client)
        return status_from_session(client, data)
    finally:
        client.close()
