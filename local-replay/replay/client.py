"""Exact-build client access and bounded read-only object discovery."""
import ctypes as c
from ctypes import wintypes as w
from pathlib import Path
import hashlib
import struct
from replay.server import k, ProcessEntry
from preview_config import client_exe, CLIENT_HASH

class MemoryInfo(c.Structure):
    _fields_ = [('BaseAddress', c.c_void_p), ('AllocationBase', c.c_void_p), ('AllocationProtect', w.DWORD), ('PartitionId', w.WORD), ('RegionSize', c.c_size_t), ('State', w.DWORD), ('Protect', w.DWORD), ('Type', w.DWORD)]
k.VirtualQueryEx.argtypes = [w.HANDLE, c.c_void_p, c.POINTER(MemoryInfo), c.c_size_t]
k.VirtualQueryEx.restype = c.c_size_t

def client_pids():
    """Enumerate clients without opening them or requiring saved configuration."""
    snap = k.CreateToolhelp32Snapshot(2, 0)
    if snap == c.c_void_p(-1).value:
        raise c.WinError(c.get_last_error())
    ids = []
    try:
        e = ProcessEntry()
        e.dwSize = c.sizeof(e)
        ok = k.Process32FirstW(snap, c.byref(e))
        while ok:
            if e.szExeFile.lower() == 'bfbc2game.exe':
                ids.append(e.th32ProcessID)
            ok = k.Process32NextW(snap, c.byref(e))
    finally:
        k.CloseHandle(snap)
    return ids


class Client:

    def __init__(self):
        CLIENT = client_exe()
        if hashlib.sha256(CLIENT.read_bytes()).hexdigest() != CLIENT_HASH:
            raise RuntimeError('Unrecognized installed client build.')
        ids = client_pids()
        if len(ids) != 1:
            raise RuntimeError('Expected one running BC2 client.')
        self.pid = ids[0]
        self.handle = k.OpenProcess(1040, False, self.pid)
        if not self.handle:
            raise c.WinError(c.get_last_error())
        path = c.create_unicode_buffer(32768)
        length = w.DWORD(len(path))
        if not k.QueryFullProcessImageNameW(self.handle, 0, path, c.byref(length)) or Path(path.value).resolve() != CLIENT.resolve():
            self.close()
            raise RuntimeError('Client executable path differs.')
        if self.read(4194304, 2) != b'MZ':
            self.close()
            raise RuntimeError('Unexpected image base.')

    def close(self):
        if self.handle:
            k.CloseHandle(self.handle)
            self.handle = None

    def read(self, address, size):
        buffer = c.create_string_buffer(size)
        count = c.c_size_t()
        if not k.ReadProcessMemory(self.handle, address, buffer, size, c.byref(count)) or count.value != size:
            raise c.WinError(c.get_last_error())
        return buffer.raw

    def u32(self, address):
        return struct.unpack('<I', self.read(address, 4))[0]

    def scan(self, values, byte_budget=768 * 1024 * 1024):
        needles = {name: struct.pack('<I', value) for name, value in values.items()}
        hits = {name: [] for name in needles}
        address = 65536
        scanned = 0
        while address < 2147418112:
            info = MemoryInfo()
            if not k.VirtualQueryEx(self.handle, address, c.byref(info), c.sizeof(info)):
                break
            start, size = (info.BaseAddress, info.RegionSize)
            if not size:
                break
            if info.State == 4096 and info.Type == 131072 and (info.Protect & 255 in (4, 8, 64, 128)) and (not info.Protect & 256):
                for pos in range(start, start + size, 1024 * 1024):
                    length = min(1024 * 1024, start + size - pos)
                    if scanned + length > byte_budget:
                        raise RuntimeError('Read-only scan exceeded byte budget.')
                    try:
                        chunk = self.read(pos, length)
                    except OSError:
                        continue
                    scanned += length
                    for name, needle in needles.items():
                        offset = 0
                        while True:
                            offset = chunk.find(needle, offset)
                            if offset < 0:
                                break
                            if (pos + offset) % 4 == 0:
                                hits[name].append(pos + offset)
                            offset += 1
            address = start + size
        return (hits, scanned)
