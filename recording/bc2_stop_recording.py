"""Force the BC2 server recorder to finalize and flush to disk.

Uses the inspected native stop path: the ServerPeer update
loop detects that DemoControl recording-enabled (+0x24) has been cleared while
a recorder exists, and then finalizes the ghost + destruction streams, writing
the in-RAM buffer to disk.

This performs a single 4-byte write to the running server process. Run with
--confirm to actually write; without it, only reads and reports.
"""
import argparse
import ctypes as c
from ctypes import wintypes as w
import struct
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent
DEMOS = ROOT / 'demos'
CONTEXT = 0x19C4CC0
OFF_DEMO = 0x20
DC_RECORDING = 0x24
TARGET = 'frost.game.main_win32_final.recordingtest.exe'

k = c.WinDLL('kernel32', use_last_error=True)


class ProcessEntry(c.Structure):
    _fields_ = [('dwSize', w.DWORD), ('cntUsage', w.DWORD),
                ('th32ProcessID', w.DWORD), ('th32DefaultHeapID', c.c_size_t),
                ('th32ModuleID', w.DWORD), ('cntThreads', w.DWORD),
                ('th32ParentProcessID', w.DWORD), ('pcPriClassBase', w.LONG),
                ('dwFlags', w.DWORD), ('szExeFile', w.WCHAR * 260)]


k.CreateToolhelp32Snapshot.argtypes = [w.DWORD, w.DWORD]
k.CreateToolhelp32Snapshot.restype = w.HANDLE
k.Process32FirstW.argtypes = [w.HANDLE, c.POINTER(ProcessEntry)]
k.Process32NextW.argtypes = [w.HANDLE, c.POINTER(ProcessEntry)]
k.CloseHandle.argtypes = [w.HANDLE]
k.OpenProcess.argtypes = [w.DWORD, w.BOOL, w.DWORD]
k.OpenProcess.restype = w.HANDLE
k.ReadProcessMemory.argtypes = [w.HANDLE, c.c_void_p, c.c_void_p, c.c_size_t, c.POINTER(c.c_size_t)]
k.WriteProcessMemory.argtypes = [w.HANDLE, c.c_void_p, c.c_void_p, c.c_size_t, c.POINTER(c.c_size_t)]


def find_pid():
    snap = k.CreateToolhelp32Snapshot(2, 0)
    try:
        e = ProcessEntry(); e.dwSize = c.sizeof(e)
        ok = k.Process32FirstW(snap, c.byref(e))
        while ok:
            if e.szExeFile.lower() == TARGET:
                return e.th32ProcessID
            ok = k.Process32NextW(snap, c.byref(e))
    finally:
        k.CloseHandle(snap)
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--confirm', action='store_true')
    args = ap.parse_args()

    pid = find_pid()
    if not pid:
        print('RecordingTest.exe is not running.')
        return 1
    handle = k.OpenProcess(0x438, False, pid)  # VM_OPERATION|VM_READ|VM_WRITE
    if not handle:
        print('OpenProcess failed:', c.WinError(c.get_last_error()))
        return 2

    def read(addr, size):
        buf = c.create_string_buffer(size)
        got = c.c_size_t()
        if not k.ReadProcessMemory(handle, c.c_void_p(addr), buf, size, c.byref(got)) or got.value != size:
            raise c.WinError(c.get_last_error())
        return buf.raw

    demo = struct.unpack('<I', read(CONTEXT + OFF_DEMO, 4))[0]
    if not demo:
        print('DemoControl not found.')
        return 3
    flag = struct.unpack('<I', read(demo + DC_RECORDING, 4))[0]
    print(f'PID {pid}  DemoControl 0x{demo:X}  recording_enabled={flag}')
    if flag != 1:
        print('Recording is not enabled; nothing to stop.')
        return 4

    # Snapshot current nonempty byte total.
    def total():
        if not DEMOS.is_dir():
            return 0
        return sum(p.stat().st_size for p in DEMOS.iterdir() if p.is_file() and p.suffix.startswith('.fb'))
    before = total()
    print(f'Binary demo bytes on disk before: {before}')

    if not args.confirm:
        print('Dry run. Re-run with --confirm to clear the flag and force finalization.')
        k.CloseHandle(handle)
        return 0

    zero = c.create_string_buffer(struct.pack('<I', 0))
    got = c.c_size_t()
    if not k.WriteProcessMemory(handle, c.c_void_p(demo + DC_RECORDING), zero, 4, c.byref(got)) or got.value != 4:
        print('WriteProcessMemory failed:', c.WinError(c.get_last_error()))
        k.CloseHandle(handle)
        return 5
    print('Cleared recording flag; waiting for finalization...')

    for _ in range(30):
        time.sleep(0.5)
        now = total()
        if now > before:
            print(f'Disk bytes now: {now}  (delta +{now - before})  -> SAVED')
            break
    else:
        print(f'Disk bytes now: {total()} (no growth yet)')
    k.CloseHandle(handle)
    return 0


if __name__ == '__main__':
    sys.exit(main())