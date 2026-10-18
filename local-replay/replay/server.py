"""Local controls for the separately built BC2 replay timeline server (Windows)."""
from pathlib import Path
import ctypes as c
from ctypes import wintypes as w
import hashlib
import json
import math
import struct
import time
from datetime import datetime, timezone
from preview_config import APP_ROOT as ROOT, server_dir
from server_build import MAILBOX, MAGIC
from preview_config import timeline_exe, TIMELINE_HASH, TIMELINE_NAME
FPS = 30
END_MARGIN_FRAMES = FPS * 5
EXE_NAME = TIMELINE_NAME.lower()
k = c.WinDLL('kernel32', use_last_error=True)

class ProcessEntry(c.Structure):
    _fields_ = [('dwSize', w.DWORD), ('cntUsage', w.DWORD), ('th32ProcessID', w.DWORD), ('th32DefaultHeapID', c.c_size_t), ('th32ModuleID', w.DWORD), ('cntThreads', w.DWORD), ('th32ParentProcessID', w.DWORD), ('pcPriClassBase', w.LONG), ('dwFlags', w.DWORD), ('szExeFile', w.WCHAR * 260)]
k.CreateToolhelp32Snapshot.argtypes = [w.DWORD, w.DWORD]
k.CreateToolhelp32Snapshot.restype = w.HANDLE
k.Process32FirstW.argtypes = [w.HANDLE, c.POINTER(ProcessEntry)]
k.Process32NextW.argtypes = [w.HANDLE, c.POINTER(ProcessEntry)]
k.CloseHandle.argtypes = [w.HANDLE]
k.OpenProcess.argtypes = [w.DWORD, w.BOOL, w.DWORD]
k.OpenProcess.restype = w.HANDLE
k.ReadProcessMemory.argtypes = [w.HANDLE, c.c_void_p, c.c_void_p, c.c_size_t, c.POINTER(c.c_size_t)]
k.WriteProcessMemory.argtypes = [w.HANDLE, c.c_void_p, c.c_void_p, c.c_size_t, c.POINTER(c.c_size_t)]
k.QueryFullProcessImageNameW.argtypes = [w.HANDLE, w.DWORD, w.LPWSTR, c.POINTER(w.DWORD)]
k.TerminateProcess.argtypes = [w.HANDLE, w.UINT]

def processes():
    snap = k.CreateToolhelp32Snapshot(2, 0)
    if snap == c.c_void_p(-1).value:
        raise c.WinError(c.get_last_error())
    result = []
    try:
        entry = ProcessEntry()
        entry.dwSize = c.sizeof(entry)
        ok = k.Process32FirstW(snap, c.byref(entry))
        while ok:
            if entry.szExeFile.lower().startswith('frost.game.main_win32_final'):
                result.append((entry.th32ProcessID, entry.szExeFile.lower()))
            ok = k.Process32NextW(snap, c.byref(entry))
    finally:
        k.CloseHandle(snap)
    return result

class Server:

    def __init__(self, write=False, stop=False):
        candidates = [pid for pid, name in processes() if name == EXE_NAME]
        if len(candidates) != 1:
            raise RuntimeError('Start the timeline server first.' if not candidates else 'Multiple timeline servers found.')
        self.pid = candidates[0]
        self.handle = k.OpenProcess(4112 | (40 if write else 0) | (1 if stop else 0), False, self.pid)
        if not self.handle:
            raise c.WinError(c.get_last_error())
        try:
            path = c.create_unicode_buffer(32768)
            length = w.DWORD(len(path))
            if not k.QueryFullProcessImageNameW(self.handle, 0, path, c.byref(length)):
                raise c.WinError(c.get_last_error())
            if Path(path.value).resolve() != timeline_exe().resolve():
                raise RuntimeError('Server path differs from this workspace.')
            if hashlib.sha256(timeline_exe().read_bytes()).hexdigest() != TIMELINE_HASH:
                raise RuntimeError('Timeline executable does not match the builder.')
            if self.read(MAILBOX, 8) != MAGIC:
                raise RuntimeError('Timeline mailbox missing; no changes made.')
        except Exception:
            self.close()
            raise

    def close(self):
        if self.handle:
            k.CloseHandle(self.handle)
            self.handle = None

    def read(self, address, size):
        buffer = c.create_string_buffer(size)
        got = c.c_size_t()
        if not k.ReadProcessMemory(self.handle, address, buffer, size, c.byref(got)) or got.value != size:
            raise c.WinError(c.get_last_error())
        return buffer.raw

    def write(self, offset, data):
        if offset < 8 or offset + len(data) > 20:
            raise ValueError('Only the mailbox request fields may be written.')
        buffer = c.create_string_buffer(data)
        got = c.c_size_t()
        if not k.WriteProcessMemory(self.handle, MAILBOX + offset, buffer, len(data), c.byref(got)) or got.value != len(data):
            raise c.WinError(c.get_last_error())

    def status(self):
        data = struct.unpack('<14I', self.read(MAILBOX + 8, 56))
        names = ('sequence', 'command', 'argument', 'ack', 'result', 'heartbeat', 'frame', 'paused', 'active', 'connections', 'initialized', 'ready', 'reserved1', 'reserved2')
        result = dict(zip(names, data))
        result['pid'] = self.pid
        result['seconds_at_30hz'] = round(result['frame'] / FPS, 3)
        return result

    def command(self, command, argument=0):
        state = self.status()
        if not state['ready']:
            raise RuntimeError('Replay is still initializing. Try again shortly.')
        if state['sequence'] != state['ack']:
            raise RuntimeError('A previous command is pending. Use one controller at a time.')
        sequence = state['sequence'] + 1 & 4294967295
        self.write(12, struct.pack('<II', command, argument))
        self.write(8, struct.pack('<I', sequence))
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            state = self.status()
            if state['ack'] == sequence:
                if state['result']:
                    raise RuntimeError(f"Server rejected command: {state['result']}")
                heartbeat = state['heartbeat']
                while time.monotonic() < deadline:
                    time.sleep(0.05)
                    state = self.status()
                    if state['heartbeat'] - heartbeat & 4294967295 >= 2:
                        log = ROOT / 'runtime' / 'timeline_commands.jsonl'
                        log.parent.mkdir(exist_ok=True)
                        try:
                            with log.open('a', encoding='utf-8') as out:
                                out.write(json.dumps({'utc': datetime.now(timezone.utc).isoformat(), 'command': command, 'target_frame': argument, 'status': state}) + '\n')
                        except OSError:
                            pass
                        return state
                raise RuntimeError('Command acknowledged, but update did not complete. Inspect server log.')
            time.sleep(0.03)
        raise RuntimeError('Command timed out. Inspect server status and log before retrying.')

    def resume(self):
        paused = self.command(1)
        if not paused['paused'] or not paused['ready'] or (not paused['active']):
            raise RuntimeError('Replay is not ready to resume. Seek earlier if it has ended.')

        def u32(address):
            return struct.unpack('<I', self.read(address, 4))[0]
        demo = u32(27020480 + 32)
        peer = u32(27020480 + 56)
        playback = u32(peer + 724) if peer else 0
        if not demo or not playback or u32(demo + 28) != 1 or (u32(demo + 36) != 0) or (u32(demo + 32) != 1):
            raise RuntimeError('Expected paused playback-only state; no timing field changed.')
        if u32(demo + 24) & 544 or not u32(playback + 4) or (not u32(playback + 216)):
            raise RuntimeError('Seek is pending or replay input is inactive; no timing field changed.')
        wait = struct.unpack('<f', self.read(playback + 208, 4))[0]
        if not math.isfinite(wait):
            raise RuntimeError('Invalid replay timing field; no timing field changed.')
        zero = c.create_string_buffer(struct.pack('<f', 0.0))
        count = c.c_size_t()
        if not k.WriteProcessMemory(self.handle, playback + 208, zero, 4, c.byref(count)) or count.value != 4:
            raise c.WinError(c.get_last_error())
        result = self.command(2)
        result['cleared_seek_wait_seconds'] = wait
        return result

def recording(base):
    srv = server_dir().resolve()
    path = (srv / base).resolve()
    if not path.is_relative_to(srv):
        raise ValueError('Select a recording within the server folder.')
    for ext in ('.fbgd', '.fbgk', '.fbidd', '.fbidk', '.fbcdd', '.fbcdk', '.fbgdd', '.fbgdk', '.txt'):
        if not Path(str(path) + ext).is_file():
            raise ValueError('Missing replay companion: ' + ext)
    metadata = Path(str(path) + '.txt').read_text(encoding='utf-8-sig')
    level = next((line[6:].strip() for line in metadata.splitlines() if line.startswith('Level:')), '')
    if not level.startswith('Levels/') or any((x in level for x in '\r\n"')):
        raise ValueError('Unsupported level metadata.')
    index = Path(str(path) + '.fbgk').read_bytes()
    if len(index) < 16 or (len(index) - 4) % 12:
        raise ValueError('Unexpected keyframe index layout.')
    frames = [struct.unpack_from('<I', index, i)[0] for i in range(4, len(index), 12)]
    if frames != sorted(set(frames)):
        raise ValueError('Unexpected keyframe order.')
    return (str(path.relative_to(srv)).replace('\\', '/'), level, frames[-1])
