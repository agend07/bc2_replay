"""Hash-checked playback server builders. Never overwrite the original server."""
import hashlib
import struct
from preview_config import (ORIGINAL_HASH, TRACE_HASH, TIMELINE_HASH,
                            server_dir, trace_exe, timeline_exe)
BASE = 0x400000
CALLSITE = 0x647995
CAVE = 0x165DB40
HOOK = 0x76A797
CODE = 0x165DC20
MAILBOX = 0x20BF300
MAGIC = b'BC2TLN1\0'

def patch_settings(original):
    if hashlib.sha256(original).hexdigest() != ORIGINAL_HASH:
        raise ValueError("Original executable does not match the inspected build.")
    b = bytearray(original)
    pe = struct.unpack_from("<I", b, 0x3c)[0]
    optional = pe + 24
    nsections = struct.unpack_from("<H", b, pe + 6)[0]
    optional_size = struct.unpack_from("<H", b, pe + 20)[0]
    headers = optional + optional_size
    if struct.unpack_from("<H", b, optional)[0] != 0x10b:
        raise ValueError("Expected PE32.")
    if struct.unpack_from("<I", b, optional + 28)[0] != BASE:
        raise ValueError("Unexpected image base.")
    if struct.unpack_from("<H", b, optional + 70)[0] & 0x40:
        raise ValueError("This adapter requires the inspected fixed-base build.")
    text = None
    for i in range(nsections):
        off = headers + i * 40
        if b[off:off+8].rstrip(b"\0") == b".text":
            text = off
    if text is None:
        raise ValueError("Missing text section.")
    vs, rva, raw_size, raw = struct.unpack_from("<IIII", b, text+8)
    if rva != raw:
        raise ValueError("Unexpected text file mapping.")
    cave_off = CAVE - BASE
    call_off = CALLSITE - BASE
    if bytes(b[call_off:call_off+5]) != bytes.fromhex("e8e6cdf0ff"):
        raise ValueError("Unexpected recording getter call.")
    code = bytearray()
    def emit(hexbytes):
        code.extend(bytes.fromhex(hexbytes))
    def call(target):
        here = CAVE + len(code)
        emit("e8")
        code.extend(struct.pack("<i", target - here - 5))
    # thiscall wrapper: output string* at [ebp+8], key string* at [ebp+12].
    # Keep the normal getter to initialize the output and retain its fallback.
    emit("55 8bec 56 8b7508 ff750c 56")
    call(0x554780)
    # ExecutionContext lookup(name, default=null, index=null), cdecl.
    emit("8b450c 6a00 6a00 ff30")
    call(0x54cd20)
    emit("83c40c 85c0")
    emit("74"); absent_branch = len(code); emit("00")
    # Find end of returned C string; assign via the engine's string routine.
    emit("8bd0")
    scan = len(code)
    emit("803a00 74"); end_branch = len(code); emit("00")
    emit("42 eb"); loop_branch = len(code); emit("00")
    assign = len(code)
    emit("52 50 8bce")
    call(0x436170)
    done = len(code)
    emit("8bc6 5e 5d c20800")
    def rel8(at, target):
        delta = target - at - 1
        if not -128 <= delta <= 127:
            raise ValueError("Branch out of range.")
        code[at] = delta & 255
    rel8(absent_branch, done)
    rel8(end_branch, assign)
    rel8(loop_branch, scan)
    if cave_off < raw + vs or cave_off + len(code) > raw + raw_size:
        raise ValueError("Adapter does not fit in unused text padding.")
    if any(b[cave_off:cave_off+len(code)]):
        raise ValueError("Expected zero padding.")
    b[cave_off:cave_off+len(code)] = code
    b[call_off:call_off+5] = b"\xe8" + struct.pack("<i", CAVE - CALLSITE - 5)
    struct.pack_into("<I", b, text+8, cave_off + len(code) - raw)
    if struct.unpack_from("<I", b, optional+64)[0] != 0:
        raise ValueError("Expected the inspected build's zero PE checksum.")
    allowed = set(range(cave_off,cave_off+len(code))) | set(range(call_off,call_off+5)) | set(range(text+8,text+12))
    changed = {i for i,(a,z) in enumerate(zip(original,b)) if a != z}
    if len(b) != len(original) or not changed <= allowed:
        raise ValueError("Unexpected modification outside adapter, call, or text size.")
    return bytes(b), bytes(code), changed


def make_trace(original):
    patched, code, _ = patch_settings(original)
    result = bytearray(patched)
    extra_sites = [6584952, 9059652]
    allowed = set()
    for callsite in extra_sites:
        offset = callsite - BASE
        expected = b'\xe8' + struct.pack('<i', 5588864 - callsite - 5)
        if original[offset:offset + 5] != expected:
            raise SystemExit('Unexpected settings call; no output written.')
        result[offset:offset + 5] = b'\xe8' + struct.pack('<i', CAVE - callsite - 5)
        allowed.update(range(offset, offset + 5))
    differences = {i for i, (a, b) in enumerate(zip(patched, result)) if a != b}
    if not differences <= allowed:
        raise SystemExit('Unexpected changes outside playback and level calls.')
    branch = 9059293 - BASE
    if original[branch:branch + 2] != bytes.fromhex('7441'):
        raise SystemExit('Unexpected server-mode branch.')
    result[branch] = 235
    for address in [7830886, 7744503, 7712362]:
        mode = address - BASE
        if original[mode:mode + 3] != bytes.fromhex('0f95c0'):
            raise SystemExit('Unexpected replay network-mode calculation.')
        result[mode:mode + 3] = bytes.fromhex('30c090')
    stub = CAVE + 128
    string_object = CAVE + 144
    string_data = CAVE + 160
    prefix = b'diagnostics/playback_net_peerfix'

    def place(address, value):
        offset = address - BASE
        if any(original[offset:offset + len(value)]):
            raise SystemExit('Trace adapter padding is not empty.')
        result[offset:offset + len(value)] = value
    place(stub, b'\xb8' + struct.pack('<I', string_object) + bytes.fromhex('c20800'))
    place(string_object, struct.pack('<III', string_data, string_data + len(prefix), string_data + len(prefix) + 1))
    place(string_data, prefix + b'\x00')
    trace_call = 7736841
    off = trace_call - BASE
    expected = b'\xe8' + struct.pack('<i', 5557360 - trace_call - 5)
    if original[off:off + 5] != expected:
        raise SystemExit('Unexpected debug-prefix getter.')
    result[off:off + 5] = b'\xe8' + struct.pack('<i', stub - trace_call - 5)
    pe = struct.unpack_from('<I', original, 60)[0]
    sections = pe + 24 + struct.unpack_from('<H', original, pe + 20)[0]
    for i in range(struct.unpack_from('<H', original, pe + 6)[0]):
        header = sections + 40 * i
        if original[header:header + 8].rstrip(b'\x00') == b'.text':
            text_rva = struct.unpack_from('<I', original, header + 12)[0]
            struct.pack_into('<I', result, header + 8, string_data + len(prefix) + 1 - BASE - text_rva)
            break
    result = bytes(result)
    return result

def make_timeline(source):
    if hashlib.sha256(source).hexdigest() != TRACE_HASH:
        raise ValueError('Expected the exact user-confirmed playback trace build.')
    result = bytearray(source)
    pe = struct.unpack_from('<I', source, 0x3C)[0]
    headers = pe + 24 + struct.unpack_from('<H', source, pe + 20)[0]
    sections = {}
    for i in range(struct.unpack_from('<H', source, pe + 6)[0]):
        h = headers + i * 40
        sections[source[h:h+8].rstrip(b'\0')] = h

    def place(va, data, name):
        h = sections[name]
        vs, rva, size, raw = struct.unpack_from('<IIII', source, h + 8)
        delta = va - BASE - rva
        if delta < vs or delta + len(data) > size:
            raise ValueError('Adapter exceeds verified section padding.')
        off = raw + delta
        if any(source[off:off+len(data)]):
            raise ValueError('Adapter padding is not zero.')
        result[off:off+len(data)] = data
        struct.pack_into('<I', result, h + 8, delta + len(data))
        return set(range(off, off+len(data))) | set(range(h+8, h+12))

    code = bytearray()
    labels, fixups = {}, []

    def emit(value):
        code.extend(bytes.fromhex(value))

    def u32(value):
        code.extend(struct.pack('<I', value))

    def label(name):
        labels[name] = len(code)

    def jump(opcode, target):
        emit(opcode)
        fixups.append((len(code), target))
        u32(0)

    # Preserve the original call's result and all registers/flags while the
    # adapter operates on the main thread, before native demo flags are read.
    emit('e8'); u32((0x6663D0 - (CODE + len(code) + 4)) & 0xFFFFFFFF)
    emit('9c 60 bb'); u32(MAILBOX)
    emit('ff 43 1c c7 43 34 00 00 00 00')  # heartbeat; ready = false
    emit('8b 48 20 85 c9'); jump('0f84', 'done')
    emit('83 79 1c 01'); jump('0f85', 'done')  # playback only
    emit('83 79 24 00'); jump('0f85', 'done')  # never recording
    emit('80 bf 05 03 00 00 00'); jump('0f84', 'done')
    emit('8b b7 d4 02 00 00 85 f6'); jump('0f84', 'done')
    emit('83 7e 04 00'); jump('0f84', 'done')  # input stream exists
    emit('c7 43 34 01 00 00 00')
    emit('83 7b 30 00'); jump('0f85', 'status')
    emit('c7 41 20 01 00 00 00 c7 43 30 01 00 00 00')
    label('status')
    emit('8b 86 c0 00 00 00 89 43 20')
    emit('8b 41 20 89 43 24 8b 86 d8 00 00 00 89 43 28')
    emit('8b 87 0c 02 00 00 2b 87 08 02 00 00 c1 e8 02 89 43 2c')
    emit('8b 53 08 3b 53 14'); jump('0f84', 'done')
    emit('c7 43 18 00 00 00 00 8b 43 0c')
    emit('83 f8 01'); jump('0f84', 'pause')
    emit('83 f8 02'); jump('0f84', 'resume')
    emit('83 f8 03'); jump('0f84', 'seek')
    emit('c7 43 18 01 00 00 00'); jump('e9', 'ack')
    label('pause')
    emit('c7 41 20 01 00 00 00'); jump('e9', 'ack')
    label('resume')
    emit('c7 41 20 00 00 00 00'); jump('e9', 'ack')
    label('seek')
    emit('8b 43 10 85 c0'); jump('0f88', 'invalid')
    # Seek while paused. Native ServerPeer update consumes flag 0x20 and
    # invokes all destruction seeks, ghost seek and viewer reset itself.
    emit('c7 41 20 01 00 00 00 89 41 34 83 49 18 20')
    jump('e9', 'ack')
    label('invalid')
    emit('c7 43 18 02 00 00 00')
    label('ack')
    emit('89 53 14')
    label('done')
    emit('61 9d c3')
    for offset, target in fixups:
        struct.pack_into('<i', code, offset, labels[target] - offset - 4)

    allowed = place(CODE, code, b'.text')
    allowed |= place(MAILBOX, MAGIC + bytes(56), b'typeinfo')
    off = HOOK - BASE
    expected = b'\xe8' + struct.pack('<i', 0x6663D0 - HOOK - 5)
    if source[off:off+5] != expected:
        raise ValueError('Unexpected ServerPeer update call.')
    result[off:off+5] = b'\xe8' + struct.pack('<i', CODE - HOOK - 5)
    allowed.update(range(off, off+5))
    changed = {i for i, (a, b) in enumerate(zip(source, result)) if a != b}
    if len(source) != len(result) or not changed <= allowed:
        raise ValueError('Unexpected change outside adapter and mailbox.')
    return bytes(result), bytes(code)


def build():
    original = (server_dir() / 'Frost.Game.Main_Win32_Final.exe').read_bytes()
    trace = make_trace(original)
    timeline, _ = make_timeline(trace)
    outputs = ((trace_exe(), trace, TRACE_HASH), (timeline_exe(), timeline, TIMELINE_HASH))
    # Validate all outputs before writing either file.
    for path, data, expected in outputs:
        if hashlib.sha256(data).hexdigest() != expected:
            raise RuntimeError('Generated server hash differs: ' + path.name)
        if path.exists() and path.read_bytes() != data:
            raise RuntimeError('Different server output exists; not overwritten: ' + path.name)
    for path, data, _ in outputs:
        if not path.exists():
            with path.open('xb') as out:
                out.write(data)
        print('Verified playback build:', path.name)
