"""Build an experimental BC2 recorder activation test from one verified server build.
Does not launch the server or alter the original executable.
"""
from pathlib import Path
import hashlib
import struct

ROOT = Path(__file__).resolve().parent
ORIGINAL = ROOT / "Frost.Game.Main_Win32_Final.exe"
OUTPUT = ROOT / "Frost.Game.Main_Win32_Final.RecordingTest.exe"
SHA256 = "ca8169fa03a29e02af1bf8db8770ab0e68f13a95b010c52eca23733068298347"
BASE = 0x400000
CALLSITE = 0x647995
CAVE = 0x165DB40

def make_patch(original):
    if hashlib.sha256(original).hexdigest() != SHA256:
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

def main():
    original = ORIGINAL.read_bytes()
    patched, code, changed = make_patch(original)
    if OUTPUT.exists():
        if OUTPUT.read_bytes() != patched:
            raise SystemExit("Test output already exists with different contents; no file changed.")
        print("Existing test copy matches this adapter.")
    else:
        with OUTPUT.open("xb") as f:
            f.write(patched)
        print("Created", OUTPUT.name)
    print("Original SHA256:", hashlib.sha256(original).hexdigest())
    print("Test SHA256:", hashlib.sha256(patched).hexdigest())
    print("Adapter:", len(code), "bytes; changed byte positions:", len(changed))
    print("This only changes recording filename activation. Recording/playback are unverified.")
if __name__ == "__main__":
    main()
