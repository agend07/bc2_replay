"""Experimental native free-camera toggle for the local BC2 replay session."""
import ctypes as c
from ctypes import wintypes as w
import struct
from replay.client import Client, k
VTABLE = 21174840
UPDATE = 8478272
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
    emit('83 be 98 00 00 00 04')
    jump('0f85', 'invalid')
    emit('c7 43 20 04 00 00 00')
    emit('8b 86 a4 00 00 00 8b 80 80 00 00 00 8b 40 3c 89 43 3c')
    emit('6a 00 6a 00 8b ce')
    call(7126592)
    emit('8b 86 a4 00 00 00 8b 80 80 00 00 00 6a 20 50')
    call(8263184)
    emit('83 c4 08 c7 43 2c 01 00 00 00')
    jump('e9', 'ack')
    label('restore')
    emit('83 7b 2c 00')
    jump('0f84', 'maybe_detach')
    emit('8b 86 a4 00 00 00 ff b0 80 00 00 00')
    call(8239936)
    emit('83 c4 04 6a 00 ff 73 20 8b ce')
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
    return result
