"""Clean screen for free camera: hide the 2D UI and the blood/damage overlay.

Exact-build (CLIENT_HASH) instance-vtable gates, ported from the live-proven
private diagnostics (`private_hud_callback_gate_native.py`,
`private_hud_damage_gate_native.py`, `private_hud_gate_installer.py`):

1. UI batch callback owner `[root+0x824F0]` (native vtable 0x14AE9F4, six
   category slots used by UiRenderBuffer_ExecuteBatch 0xF84110). Each cloned
   slot skips its category (`ret N`) while clean screen is enabled and a level
   HUD exists; otherwise it tail-jumps to the unchanged native function.
   Hides crosshair, minimap, markers/name tags, chat, kill feed, hit-direction
   arcs, TAB/M screens. SCOPE_ALL_IN_LEVEL also hides non-HUD UI states drawn
   in a level (deploy/respawn screen, ESC menu, loading screen).
2. DynamicPostProcess `[0x156D4F8]` (native vtable 0x141B460): slots +0x78
   AddDirectionalBlood -> ret 8, +0x7C EnableLowHealthBlood -> ResetAllBlood,
   +0x94 ComputeBloodWeights -> ResetAllBlood (every frame). Removes red
   blood/low-health overlay.

3. Always on (replay/clean_vignette.py): data-only neutralization of the HUD
   Vignettes table (death grey/blur, low-health desaturation), and
   (replay/free_cam_fov.py) a fixed free-camera FOV. Both are applied on every
   enable; a failure never blocks the UI/blood gates. Never writes
   `+0x108C` (diagnostics/mem_findings.md 9a). Not touched: root vtable
(bc2_mute), HUD vtable (seek guard), camera owner, any .text/.rdata byte.

Publication = one aligned 4-byte vtable-pointer write per object, recovery JSON
written first; restore only overwrites our own clone. Native rebuild of an
object simply drops the gate (fail-safe: UI returns).
"""
import ctypes as c
from ctypes import wintypes as w
import json
import struct

from replay.client import Client, k
from replay import clean_vignette
from replay import free_cam_fov
from preview_config import APP_ROOT as ROOT

STATE = ROOT / 'runtime' / 'private_clean_screen_session.json'
MAGIC = b'BC2CLN1\0'

ROOT_GLOBAL = 0x19A4CA8
ROOT_VTABLE = 0x1440830
ROOT_ACTIVE_STATE = 0x14
ROOT_HUD_POINTER = 0x82B20
ROOT_OWNER_FIELD = 0x824F0
ROOT_BUFFER_FIELD = 0x824F4
BUFFER_OWNER_PUBLISH = 0x170
OWNER_BACK_REFERENCE = 0x04

OWNER_VTABLE = 0x14AE9F4
OWNER_SLOTS = (0xF8A1E0, 0xF9E070, 0xF8A670, 0xF91F40, 0xF83430, 0xF92080)
OWNER_SLOT_RET = (16, 12, 12, 12, 12, 12)

PP_GLOBAL = 0x156D4F8
PP_VTABLE = 0x141B460
PP_HEADER = 0x5CA810
PP_SLOTS = 58
FN_ADD_BLOOD = 0x5C9690        # +0x78, ret 8
FN_ENABLE_LOWHEALTH = 0x5BEC30  # +0x7C, ret
FN_RESET_BLOOD = 0x5BEC70       # +0x84, ret
FN_COMPUTE_BLOOD = 0x5C99E0     # +0x94, ret
PP_EXPECTED = {0x78: FN_ADD_BLOOD, 0x7C: FN_ENABLE_LOWHEALTH,
               0x84: FN_RESET_BLOOD, 0x94: FN_COMPUTE_BLOOD}

SCOPE_HUD_ONLY = 0        # proven live: only while the in-game HUD is the active UI state
SCOPE_ALL_IN_LEVEL = 1    # any UI state while a level HUD exists (deploy screen, menus)
DEFAULT_SCOPE = SCOPE_ALL_IN_LEVEL

# Allocation: page 0 = code (RX), page 1 = mailbox + clone tables (RW).
REGION = 0x2000
MB_MAGIC, MB_ENABLED, MB_SCOPE, MB_UI_SKIPS, MB_BLOOD_RESETS = 0x00, 0x08, 0x0C, 0x10, 0x14
MB_OWNER_CLONE = 0x104     # header at 0x100
MB_PP_CLONE = 0x204        # header at 0x200
STUB_STRIDE = 0x60

k.VirtualAllocEx.argtypes = [w.HANDLE, c.c_void_p, c.c_size_t, w.DWORD, w.DWORD]
k.VirtualAllocEx.restype = c.c_void_p
k.VirtualProtectEx.argtypes = [w.HANDLE, c.c_void_p, c.c_size_t, w.DWORD, c.POINTER(w.DWORD)]
k.VirtualProtectEx.restype = w.BOOL
k.FlushInstructionCache.argtypes = [w.HANDLE, c.c_void_p, c.c_size_t]
k.FlushInstructionCache.restype = w.BOOL
k.GetProcessTimes.argtypes = [w.HANDLE] + [c.POINTER(w.FILETIME)] * 4
k.GetProcessTimes.restype = w.BOOL


class WriteClient(Client):
    """Same exact-build/path checks as Client, reopened with VM write/operation rights."""

    def __init__(self):
        super().__init__()
        k.CloseHandle(self.handle)
        self.handle = k.OpenProcess(0x438, False, self.pid)
        if not self.handle:
            raise c.WinError(c.get_last_error())

    def write(self, address, data):
        data = bytes(data)
        count = c.c_size_t()
        buffer = c.create_string_buffer(data)
        if not k.WriteProcessMemory(self.handle, address, buffer, len(data), c.byref(count)) or count.value != len(data):
            raise c.WinError(c.get_last_error())
        if self.read(address, len(data)) != data:
            raise RuntimeError(f'Write readback mismatch at {address:#x}')


def created(client):
    times = [w.FILETIME() for _ in range(4)]
    if not k.GetProcessTimes(client.handle, *(c.byref(t) for t in times)):
        raise c.WinError(c.get_last_error())
    return (times[0].dwHighDateTime << 32) | times[0].dwLowDateTime


# ---------------------------------------------------------------------------
# Offline stub builders (pure bytes)
# ---------------------------------------------------------------------------
def gate_stub(address, mailbox, original, active, ui_scope):
    """pushfd/eax/edx saved; ECX and stack args untouched.

    Active when [mailbox+ENABLED] != 0 and, for ui_scope stubs, a level HUD
    exists and (scope != 0 or HUD is the active UI state).
    active = ('ret', N) or ('jmp', target).
    """
    p = lambda v: struct.pack('<I', v & 0xFFFFFFFF)
    code, fix = bytearray(), []

    def br(op, label):
        code.extend(op)
        code.append(0)
        fix.append((len(code) - 1, label))

    code += b'\x9c\x50\x52'                                   # pushfd; push eax; push edx
    code += b'\x83\x3d' + p(mailbox + MB_ENABLED) + b'\x00'   # cmp dword [enabled], 0
    br(b'\x74', 'pass')
    if ui_scope:
        code += b'\x8b\x15' + p(ROOT_GLOBAL)                  # mov edx, [root global]
        code += b'\x85\xd2'; br(b'\x74', 'pass')
        code += b'\x8b\x82' + p(ROOT_HUD_POINTER)             # mov eax, [edx+0x82B20]
        code += b'\x85\xc0'; br(b'\x74', 'pass')
        code += b'\x83\x3d' + p(mailbox + MB_SCOPE) + b'\x00' # cmp dword [scope], 0
        br(b'\x75', 'active')
        code += b'\x3b\x42' + bytes([ROOT_ACTIVE_STATE])      # cmp eax, [edx+0x14]
        br(b'\x75', 'pass')
        counter = MB_UI_SKIPS
    else:
        counter = MB_BLOOD_RESETS
    labels = {'active': len(code)}
    code += b'\xf0\xff\x05' + p(mailbox + counter)            # lock inc dword [counter]
    code += b'\x5a\x58\x9d'                                   # pop edx; pop eax; popfd
    if active[0] == 'ret':
        code += b'\xc2' + struct.pack('<H', active[1])
    else:
        code += b'\xe9' + p(active[1] - (address + len(code) + 5))
    labels['pass'] = len(code)
    code += b'\x5a\x58\x9d'
    code += b'\xe9' + p(original - (address + len(code) + 5))
    for offset, label in fix:
        delta = labels[label] - (offset + 1)
        if not 0 <= delta <= 127:
            raise ValueError('rel8 out of range')
        code[offset] = delta
    if len(code) > STUB_STRIDE:
        raise ValueError('stub too large')
    return bytes(code)


def build(base, owner_table, pp_table):
    """Return {address: bytes} for code page and mailbox page.

    owner_table: 6 native dwords at 0x14AE9F4 (+ header dword first).
    pp_table: header + 58 native dwords at 0x141B460-4.
    """
    code_page, mailbox = base, base + 0x1000
    images = {}
    owner_clone = list(owner_table)
    for index, original in enumerate(OWNER_SLOTS):
        addr = code_page + index * STUB_STRIDE
        images[addr] = gate_stub(addr, mailbox, original, ('ret', OWNER_SLOT_RET[index]), True)
        owner_clone[1 + index] = addr
    pp_clone = list(pp_table)
    blood = ((0x78, FN_ADD_BLOOD, ('ret', 8)),
             (0x7C, FN_ENABLE_LOWHEALTH, ('jmp', FN_RESET_BLOOD)),
             (0x94, FN_COMPUTE_BLOOD, ('jmp', FN_RESET_BLOOD)))
    for index, (slot, original, active) in enumerate(blood):
        addr = code_page + (6 + index) * STUB_STRIDE
        images[addr] = gate_stub(addr, mailbox, original, active, False)
        pp_clone[1 + slot // 4] = addr
    page = bytearray(0x1000)
    page[:8] = MAGIC
    blob = struct.pack(f'<{len(owner_clone)}I', *owner_clone)
    page[MB_OWNER_CLONE - 4:MB_OWNER_CLONE - 4 + len(blob)] = blob
    blob = struct.pack(f'<{len(pp_clone)}I', *pp_clone)
    page[MB_PP_CLONE - 4:MB_PP_CLONE - 4 + len(blob)] = blob
    images[mailbox] = bytes(page)
    return images


# ---------------------------------------------------------------------------
# Live discovery (read-only)
# ---------------------------------------------------------------------------
def native_tables(client):
    owner = struct.unpack('<7I', client.read(OWNER_VTABLE - 4, 28))
    if owner[1:] != OWNER_SLOTS:
        raise RuntimeError('UI callback vtable differs from the known build')
    pp = struct.unpack(f'<{PP_SLOTS + 1}I', client.read(PP_VTABLE - 4, 4 * (PP_SLOTS + 1)))
    if pp[0] != PP_HEADER or any(pp[1 + s // 4] != f for s, f in PP_EXPECTED.items()):
        raise RuntimeError('Post-process vtable differs from the known build')
    return owner, pp


def locate(client):
    root = client.u32(ROOT_GLOBAL)
    if not root or client.u32(root) != ROOT_VTABLE:
        raise RuntimeError('UI root missing or not native')
    if not client.u32(root + ROOT_HUD_POINTER):
        raise RuntimeError('No in-level HUD; join the replay first')
    owner = client.u32(root + ROOT_OWNER_FIELD)
    buffer = client.u32(root + ROOT_BUFFER_FIELD)
    if not owner or not buffer or client.u32(buffer + BUFFER_OWNER_PUBLISH) != owner \
            or client.u32(owner + OWNER_BACK_REFERENCE) != root:
        raise RuntimeError('UI callback owner failed validation')
    pp = client.u32(PP_GLOBAL)
    if pp and client.u32(client.u32(pp) - 4) != PP_HEADER:
        raise RuntimeError('Post-process object failed validation')
    return root, owner, pp


def load_session(client):
    if not STATE.exists():
        return None
    data = json.loads(STATE.read_text(encoding='utf-8'))
    if data.get('pid') != client.pid or data.get('created') != created(client):
        STATE.unlink()          # stale: that process is gone, nothing to restore
        return None
    try:
        ok = client.read(data['mailbox'], 8) == MAGIC
    except OSError:
        ok = False
    if not ok:
        raise RuntimeError('Clean-screen session mailbox mismatch; restart client for a clean reset')
    return data


def _publish(client, obj, native, clone):
    current = client.u32(obj)
    if current == clone:
        return 'already'
    if current != native:
        raise RuntimeError(f'Foreign vtable {current:#x} on {obj:#x}; not overwritten')
    client.write(obj, struct.pack('<I', clone))
    return 'published'


def _unpublish(client, obj, native, clone):
    try:
        current = client.u32(obj)
    except OSError:
        return 'gone'
    if current == clone:
        client.write(obj, struct.pack('<I', native))
        return 'restored'
    return 'native' if current == native else f'foreign {current:#x} (left)'


# ---------------------------------------------------------------------------
# Public API (panel)
# ---------------------------------------------------------------------------
def enable(client=None, scope=DEFAULT_SCOPE):
    own = client is None
    client = client or WriteClient()
    try:
        data = load_session(client)
        root, owner, pp = locate(client)
        if data is None:
            owner_table, pp_table = native_tables(client)
            base = k.VirtualAllocEx(client.handle, None, REGION, 0x3000, 0x04)
            if not base or base > 0xFFFFFFFF - REGION:
                raise RuntimeError('Cannot allocate x86 clean-screen pages')
            images = build(base, owner_table, pp_table)
            for address in sorted(images):
                client.write(address, images[address])
            old = w.DWORD()
            if not k.VirtualProtectEx(client.handle, base, 0x1000, 0x20, c.byref(old)) \
                    or not k.FlushInstructionCache(client.handle, base, 0x1000):
                raise c.WinError(c.get_last_error())
            data = dict(pid=client.pid, created=created(client), mailbox=base + 0x1000, code=base,
                        owner_clone=base + 0x1000 + MB_OWNER_CLONE, pp_clone=base + 0x1000 + MB_PP_CLONE)
        mailbox = data['mailbox']
        client.write(mailbox + MB_SCOPE, struct.pack('<I', scope))
        client.write(mailbox + MB_ENABLED, struct.pack('<I', 1))
        data.update(root=root, owner=owner, pp=pp)
        STATE.parent.mkdir(parents=True, exist_ok=True)
        STATE.write_text(json.dumps(data, indent=2), encoding='utf-8')   # recovery before publication
        if locate(client)[1:] != (owner, pp):
            raise RuntimeError('UI objects changed before publication; nothing published')
        result = {'ui': _publish(client, owner, OWNER_VTABLE, data['owner_clone'])}
        try:
            result['blood'] = _publish(client, pp, PP_VTABLE, data['pp_clone']) if pp else 'no post-process object'
        except RuntimeError as exc:
            result['blood'] = str(exc)
        apply_extras(client, result)
        return result
    finally:
        if own:
            client.close()


def apply_extras(client, result):
    """Always on: no vignette (r226) and fixed free-camera FOV (r227).
    A failure never blocks the UI/blood gates; it is reported in `result`."""
    try:
        result['vignette'] = clean_vignette.enable(client)
    except Exception as exc:
        result['vignette'] = f'not applied: {exc}'
    try:
        result['fov'] = free_cam_fov.enable(client)
    except Exception as exc:
        result['fov'] = f'not applied: {exc}'
    return result


def disable(client=None):
    own = client is None
    client = client or WriteClient()
    try:
        try:   # r226: always undo the vignette data
            vignette = clean_vignette.disable(client)
        except Exception as exc:
            vignette = f'restore failed: {exc}'
        try:   # r227: always undo the FOV lock
            vignette = {'vignette': vignette, 'fov': free_cam_fov.disable(client)}
        except Exception as exc:
            vignette = {'vignette': vignette, 'fov': f'restore failed: {exc}'}
        data = load_session(client)
        if data is None:
            return {'clean_screen': 'not installed', 'vignette': vignette}
        client.write(data['mailbox'] + MB_ENABLED, struct.pack('<I', 0))   # stubs pass through now
        root = client.u32(ROOT_GLOBAL)
        # Never restore through stale pointers: only objects that are still the live ones.
        live_owner = client.u32(root + ROOT_OWNER_FIELD) if root else 0
        result = {'ui': _unpublish(client, data['owner'], OWNER_VTABLE, data['owner_clone'])
                  if live_owner == data['owner'] else 'replaced natively; nothing to restore'}
        if data.get('pp'):
            result['blood'] = (_unpublish(client, data['pp'], PP_VTABLE, data['pp_clone'])
                               if client.u32(PP_GLOBAL) == data['pp'] else 'replaced natively; nothing to restore')
        result['vignette'] = vignette
        return result     # session kept: region is reused on next enable in this process
    finally:
        if own:
            client.close()


def status(client=None):
    own = client is None
    client = client or Client()
    try:
        try:
            vignette = clean_vignette.status(client)
        except Exception as exc:
            vignette = str(exc)
        try:
            vignette = {'vignette': vignette, 'fov': free_cam_fov.status(client)}
        except Exception as exc:
            vignette = {'vignette': vignette, 'fov': str(exc)}
        if not STATE.exists():
            return {'installed': False, 'vignette': vignette}
        data = json.loads(STATE.read_text(encoding='utf-8'))
        if data.get('pid') != client.pid:
            return {'installed': False, 'note': 'stale session from another client process', 'vignette': vignette}
        mb = data['mailbox']
        enabled, scope, skips, resets = struct.unpack('<4I', client.read(mb + MB_ENABLED, 16))
        root = client.u32(ROOT_GLOBAL)
        return {'enabled': bool(enabled), 'scope': scope,
                'ui_gated': bool(root) and client.u32(root + ROOT_OWNER_FIELD) == data['owner']
                and client.u32(data['owner']) == data['owner_clone'],
                'blood_gated': bool(data.get('pp')) and client.u32(PP_GLOBAL) == data['pp']
                and client.u32(data['pp']) == data['pp_clone'],
                'ui_calls_skipped': skips, 'blood_resets': resets, 'vignette': vignette}
    finally:
        if own:
            client.close()


if __name__ == '__main__':
    import sys
    # Usage (from the repo root): python -m replay.clean_screen status|enable [0|1]|disable
    action = sys.argv[1] if len(sys.argv) > 1 else 'status'
    if action == 'enable':
        out = enable(scope=int(sys.argv[2]) if len(sys.argv) > 2 else DEFAULT_SCOPE)
    else:
        out = {'disable': disable, 'status': status}[action]()
    print(json.dumps(out, indent=2))
