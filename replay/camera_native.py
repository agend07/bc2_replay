"""Experimental native free-camera toggle for the local BC2 replay session."""
import ctypes as c
from ctypes import wintypes as w
import struct
from replay.client import Client, k
VTABLE = 21174840
UPDATE = 8478272
# r218: input-mapper stack 0x7E1610 push / 0x7DBB40 remove; 0x7C3350 makes the
# highest signed priority (ties: latest push) the only mapper fed by input ([0x1570E98]).
# UI/menu mappers push 0x40 (0x5807BF, 0x59B2A6), so the old 0x20 free-camera mapper
# lost input once the deploy screen opened. 0x41 beats them; signed, keep < 0x80.
FREE_INPUT_PRIORITY = 0x41
MAGIC = b'BC2CAM2\x00'
k.VirtualAllocEx.argtypes = [w.HANDLE, c.c_void_p, c.c_size_t, w.DWORD, w.DWORD]
k.VirtualAllocEx.restype = c.c_void_p
k.VirtualProtectEx.argtypes = [w.HANDLE, c.c_void_p, c.c_size_t, w.DWORD, c.POINTER(w.DWORD)]
k.FlushInstructionCache.argtypes = [w.HANDLE, c.c_void_p, c.c_size_t]

class ControlClient(Client):

    def __init__(self):
        super().__init__()
        k.CloseHandle(self.handle)
        self.handle = k.OpenProcess(1080, False, self.pid)
        if not self.handle:
            raise c.WinError(c.get_last_error())

    def write(self, address, data):
        data = bytes(data)
        count = c.c_size_t()
        buffer = c.create_string_buffer(data)
        if not k.WriteProcessMemory(self.handle, address, buffer, len(data), c.byref(count)) or count.value != len(data):
            raise c.WinError(c.get_last_error())

def adapter(code_address, mailbox, clone_vtable):
    code = bytearray()
    labels = {}
    fixups = []

    def emit(h):
        code.extend(bytes.fromhex(h))

    def word(n):
        code.extend(struct.pack('<I', n))

    def label(n):
        labels[n] = len(code)

    def jump(op, name):
        emit(op)
        fixups.append((len(code), name))
        word(0)

    def call(target):
        emit('e8')
        word(target - code_address - len(code) - 4 & 4294967295)
    emit('9c 60 8b f1 bb')
    word(mailbox)
    emit('3b 73 24')
    jump('0f85', 'return')
    emit('ff 43 18 8b 86 98 00 00 00 89 43 1c')
    emit('8b 86 a4 00 00 00 89 43 28')
    emit('8b 7b 08 3b 7b 10')
    jump('0f84', 'done')
    emit('c7 43 14 00 00 00 00 8b 43 0c 83 f8 01')
    jump('0f84', 'enable')
    emit('83 f8 02')
    jump('0f84', 'restore')
    emit('83 f8 03')
    jump('0f84', 'restore')
    emit('c7 43 14 01 00 00 00')
    jump('e9', 'ack')
    label('enable')
    emit('83 7b 2c 00')
    jump('0f85', 'ack')
    # r222b: enable from player view (4) or the pre-spawn/deploy mode 3 (owner+0xD0 object camera;
    # leave(3) 0x80776C is null-safe). Save the real mode for restore instead of a constant 4.
    emit('8b 86 98 00 00 00 83 f8 04')
    jump('0f84', 'mode_ok')
    emit('83 f8 03')
    jump('0f85', 'invalid')
    label('mode_ok')
    emit('89 43 20')
    emit('8b 86 a4 00 00 00 8b 80 80 00 00 00 8b 40 3c 89 43 3c')
    emit('6a 00 6a 00 8b ce')
    call(7126592)
    emit('8b 86 a4 00 00 00 8b 80 80 00 00 00 6a %02x 50' % FREE_INPUT_PRIORITY)
    call(8263184)
    emit('83 c4 08 c7 43 2c 01 00 00 00')
    jump('e9', 'ack')
    label('restore')
    emit('83 7b 2c 00')
    jump('0f84', 'maybe_detach')
    # r218: remove every stack entry of the free mapper (AL=1 while one was removed).
    label('pop_mapper')
    emit('8b 86 a4 00 00 00 ff b0 80 00 00 00')
    call(8239936)
    emit('83 c4 04 84 c0')
    jump('0f85', 'pop_mapper')
    # r222b: restore the saved mode, but player view (4) once a target soldier exists (+0xC4).
    emit('8b 43 20 83 be c4 00 00 00 00 74 05 b8 04 00 00 00 6a 00 50 8b ce')
    call(7126592)
    emit('8b 86 a4 00 00 00 8b 80 80 00 00 00 8b 53 3c 89 50 3c')
    emit('c7 43 2c 00 00 00 00')
    label('maybe_detach')
    emit('83 7b 0c 03')
    jump('0f85', 'ack')
    emit('c7 06')
    word(VTABLE)
    emit('c7 43 30 01 00 00 00')
    jump('e9', 'ack')
    label('invalid')
    emit('c7 43 14 02 00 00 00')
    label('ack')
    emit('8b 86 98 00 00 00 89 43 1c 89 7b 10')
    label('done')
    emit('83 7b 2c 00')
    jump('0f84', 'return')
    # r222b: keep one (free mapper, 0x41) stack entry while enabled. Native code pushes the same
    # mapper at 0x20 on entry changes (0x7EEF20) and 0x7DBB40 removes the FIRST entry of a mapper,
    # which deleted our 0x41 entry live (cl04_1095 16:48:38); a 0x40 deploy/menu mapper would then
    # win. Re-push only when missing (bounded); count re-pushes at mailbox+0x58.
    emit('8b 86 a4 00 00 00 8b 80 80 00 00 00 8b 0d d0 0e 57 01 8b 15 d4 0e 57 01')
    label('mapper_scan')
    emit('3b ca')
    jump('0f83', 'mapper_push')
    emit('39 01')
    jump('0f85', 'mapper_next')
    emit('83 79 04 %02x' % FREE_INPUT_PRIORITY)
    jump('0f84', 'mapper_ok')
    label('mapper_next')
    emit('83 c1 08')
    jump('e9', 'mapper_scan')
    label('mapper_push')
    emit('6a %02x 50' % FREE_INPUT_PRIORITY)
    call(8263184)
    emit('83 c4 08 ff 43 58')
    label('mapper_ok')
    emit('c7 43 40 00 00 00 00')
    emit('83 7b 54 00')
    jump('0f85', 'key_function_ready')
    emit('68')
    word(mailbox + 128)
    emit('ff 15 7c 53 40 01 85 c0')
    jump('0f84', 'base_speed')
    emit('68')
    word(mailbox + 144)
    emit('50 ff 15 14 53 40 01 89 43 54 85 c0')
    jump('0f84', 'base_speed')
    label('key_function_ready')
    emit('ff 15 f4 57 40 01 89 43 44 85 c0')
    jump('0f84', 'base_speed')
    emit('8d 53 4c 52 50 ff 15 74 58 40 01 8b 43 4c 3b 43 50')
    jump('0f85', 'base_speed')
    # r222c: deploy-screen input gate (live cl04_1096): mapper current and ticking, FreeCamera consuming,
    # yet the input device returns 0 for every action before spawn and after death. While BC2 is the
    # foreground process and all three move axes are 0, feed W/S (+0x18) and D/A (+0x10) ourselves.
    # Harmless when the device works (axes non-zero -> skipped). Count at mailbox+0x5C.
    emit('8b be a4 00 00 00 8b bf 80 00 00 00')
    emit('8b 47 10 0b 47 14 0b 47 18 25 ff ff ff 7f')
    jump('0f85', 'inject_done')
    emit('33 ed')
    # r293: + the vertical axis (+0x14) from the mouse buttons (VK_LBUTTON up, VK_RBUTTON down), which the native
    # free camera binds to up/down but the gated input device never delivers (live cl04_ccap202610302r2_1: deploy
    # screen up for the whole free-camera session, buttons dead while WASD / look came from this fallback).
    for vk, offset, value in ((0x57, 0x18, 0x3f800000), (0x53, 0x18, 0xbf800000),
                              (0x44, 0x10, 0x3f800000), (0x41, 0x10, 0xbf800000),
                              (0x01, 0x14, 0x3f800000), (0x02, 0x14, 0xbf800000)):
        emit('6a %02x ff 53 54 66 a9 00 80 74 08 c7 47 %02x' % (vk, offset))
        word(value)
        emit('45')
    emit('01 6b 5c')
    label('inject_done')
    # r222d: mouse-look fallback. Live cl04_1096 v7: look axes stay 0 while the deploy screen is up
    # (owner+0xD0 = deploy object, cursor shown); device binding map/flags unchanged. While the cursor
    # is SHOWING and both look axes are 0, turn the cursor delta from an anchor into look axes
    # (+0x24 = dx*[mb+0x60], +0x20 = dy*[mb+0x64]) and put the cursor back on the anchor.
    # [mb+0x60]==0 disables it. User32 functions resolved once into mb+0xD4/D8/DC.
    emit('83 7b 60 00')
    jump('0f84', 'look_done')
    emit('8b 47 20 0b 47 24 25 ff ff ff 7f')
    jump('0f85', 'look_done')
    emit('83 bb dc 00 00 00 00')
    jump('0f85', 'look_ready')
    emit('8d 83 80 00 00 00 50 ff 15 7c 53 40 01 85 c0')  # lea eax,[ebx+0x80] (disp32)
    jump('0f84', 'look_done')
    emit('8b e8')
    for name_off, slot in ((0xa4, 0xd4), (0xb4, 0xd8), (0xc4, 0xdc)):
        emit('8d 8b'); word(name_off); emit('51 55 ff 15 14 53 40 01 89 83'); word(slot)
    emit('83 bb d4 00 00 00 00')
    jump('0f84', 'look_done')
    emit('83 bb d8 00 00 00 00')
    jump('0f84', 'look_done')
    emit('83 bb dc 00 00 00 00')
    jump('0f84', 'look_done')
    label('look_ready')
    emit('c7 83 e0 00 00 00 14 00 00 00 8d 83 e0 00 00 00 50 ff 93 d4 00 00 00 85 c0')
    jump('0f84', 'look_done')
    emit('f6 83 e4 00 00 00 01')
    jump('0f85', 'look_showing')
    emit('c7 83 f4 00 00 00 00 00 00 00')
    jump('e9', 'look_done')
    label('look_showing')
    emit('83 bb f4 00 00 00 00')
    jump('0f85', 'look_anchor')
    emit('8b 83 ec 00 00 00 89 83 f8 00 00 00 8b 83 f0 00 00 00 89 83 fc 00 00 00')
    emit('c7 83 f4 00 00 00 01 00 00 00')
    jump('e9', 'look_done')
    label('look_anchor')
    emit('8b 83 ec 00 00 00 2b 83 f8 00 00 00 8b 8b f0 00 00 00 2b 8b fc 00 00 00')
    emit('8b d0 0b d1')
    jump('0f84', 'look_done')
    emit('89 83 e8 00 00 00 db 83 e8 00 00 00 d8 4b 60 d9 5f 24')
    emit('89 8b e8 00 00 00 db 83 e8 00 00 00 d8 4b 64 d9 5f 20')
    emit('ff b3 fc 00 00 00 ff b3 f8 00 00 00 ff 93 dc 00 00 00 ff 43 68')
    label('look_done')
    emit('6a 10 ff 53 54 66 a9 00 80')
    jump('0f84', 'base_speed')
    emit('c7 43 40 01 00 00 00 8b 43 38')
    jump('e9', 'apply_speed')
    label('base_speed')
    emit('8b 43 34')
    label('apply_speed')
    emit('83 f8 03')
    jump('0f87', 'return')
    emit('8b 96 a4 00 00 00 8b 92 80 00 00 00 89 42 3c')
    label('return')
    emit('61 9d e9')
    word(UPDATE - code_address - len(code) - 4 & 4294967295)
    for offset, name in fixups:
        struct.pack_into('<i', code, offset, labels[name] - offset - 4)
    return bytes(code)

def status(client, data):
    values = struct.unpack('<14I', client.read(data['mailbox'] + 8, 56))
    names = ('sequence', 'command', 'ack', 'result', 'heartbeat', 'mode', 'previous_mode', 'owner', 'free_camera', 'enabled', 'detached', 'base_tier', 'shift_tier', 'original_tier')
    result = dict(zip(names, values))
    result['pid'] = client.pid
    result['position'] = list(struct.unpack('<3f', client.read(data['free_camera'] + 64, 12)))
    result['hook_installed'] = client.u32(data['owner']) == data['clone_vtable']
    mapper = client.u32(data['free_camera'] + 128)
    result['current_speed_tier'] = client.u32(mapper + 60)
    result['shift_held_in_game'] = client.u32(data['mailbox'] + 64)
    result['foreground_window'] = client.u32(data['mailbox'] + 68)
    result['foreground_process_id'] = client.u32(data['mailbox'] + 76)
    result['key_function_resolved'] = bool(client.u32(data['mailbox'] + 84))
    result['mapper_repush_count'] = client.u32(data['mailbox'] + 88)  # r222b
    result['key_inject_count'] = client.u32(data['mailbox'] + 92)  # r222c
    result['look_inject_count'] = client.u32(data['mailbox'] + 0x68)  # r222d
    return result
