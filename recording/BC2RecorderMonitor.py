"""Live read-only monitor for the BC2 server-side demo recorder.

Answers "is new data actually being recorded?" without touching the server.

IMPORTANT: while a match runs, the recorder buffers to RAM. The .fbgd/.fbgk
family stays 0 bytes on disk until a level unload/reload (round or map
change). Watching file sizes during play is therefore misleading; this tool
shows the in-process state and the RAM buffer growth instead, and reports the
disk flush when it happens.

No writes, no remote thread, no injected code. Only OpenProcess (read) and
ReadProcessMemory. Addresses match the exact supported fixed-base server build
(global context at 0x19C4CC0).

Usage:
    python BC2RecorderMonitor.py            # live, refresh once per second
    python BC2RecorderMonitor.py --once     # one snapshot, for scripting
"""
from pathlib import Path
import argparse
import ctypes as c
from ctypes import wintypes as w
import os
import struct
import sys
import time
from datetime import datetime

ROOT = Path(__file__).resolve().parent
DEMOS = ROOT / 'demos'

# Fixed-base layout (original server build, image base 0x00400000).
CONTEXT = 0x19C4CC0
OFF_DEMO = 0x20          # global context -> DemoControl*
OFF_PEER = 0x38          # global context -> ServerPeer*

DC_RECORD_NAME = 0x0C    # DemoControl recording filename
DC_FLAGS = 0x18
DC_PLAYBACK = 0x1C
DC_PAUSED = 0x20
DC_RECORDING = 0x24

PEER_RECORDER = 0x2D0    # ServerPeer -> recorder*

REC_FRAME = 0xCC         # recorder update/frame counter
REC_MEMOUT = 0xBC        # 1 = buffer to RAM, flushed at level teardown
REC_STREAM = 0xD8        # output stream (MemoryOutBuffer when REC_MEMOUT)

BUF_START = 0x58
BUF_WRITE = 0x60

TARGET = 'frost.game.main_win32_final.recordingtest.exe'
FPS = 30

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


def processes():
    snap = k.CreateToolhelp32Snapshot(2, 0)
    if snap == c.c_void_p(-1).value:
        raise c.WinError(c.get_last_error())
    found = []
    try:
        entry = ProcessEntry()
        entry.dwSize = c.sizeof(entry)
        ok = k.Process32FirstW(snap, c.byref(entry))
        while ok:
            if entry.szExeFile.lower().startswith('frost.game.main_win32_final'):
                found.append((entry.th32ProcessID, entry.szExeFile.lower()))
            ok = k.Process32NextW(snap, c.byref(entry))
    finally:
        k.CloseHandle(snap)
    return found


class Reader:
    def __init__(self, pid):
        self.pid = pid
        self.handle = k.OpenProcess(0x1010, False, pid)  # VM_READ | QUERY_LIMITED
        if not self.handle:
            raise c.WinError(c.get_last_error())

    def close(self):
        if self.handle:
            k.CloseHandle(self.handle)
            self.handle = None

    def read(self, address, size):
        buf = c.create_string_buffer(size)
        got = c.c_size_t()
        if not k.ReadProcessMemory(self.handle, c.c_void_p(address), buf, size, c.byref(got)) or got.value != size:
            raise c.WinError(c.get_last_error())
        return buf.raw

    def u32(self, address):
        return struct.unpack('<I', self.read(address, 4))[0]

    def cstring(self, address, limit=260):
        out = b''
        while len(out) < limit:
            chunk = self.read(address + len(out), 32)
            z = chunk.find(b'\x00')
            if z >= 0:
                out += chunk[:z]
                break
            out += chunk
        return out.decode('latin-1', 'replace')

    def string_field(self, address, size=0x30):
        """Best-effort read of an EA string member: either a char* or inline."""
        raw = self.read(address, size)
        ptr = struct.unpack_from('<I', raw, 0)[0]
        if 0x10000 <= ptr < 0x7FFFFFFF:
            try:
                s = self.cstring(ptr)
                if s and s.isprintable():
                    return s
            except Exception:
                pass
        z = raw.find(b'\x00')
        return raw[:z if z >= 0 else len(raw)].decode('latin-1', 'replace')


def snapshot(reader):
    info = {'demo': 0, 'peer': 0, 'recording': None, 'playback': None,
            'paused': None, 'flags': None, 'recorder': 0, 'frame': None,
            'memout': None, 'stream': 0, 'buf_bytes': None, 'name': ''}
    demo = reader.u32(CONTEXT + OFF_DEMO)
    peer = reader.u32(CONTEXT + OFF_PEER)
    info['demo'] = demo
    info['peer'] = peer
    if demo:
        info['recording'] = reader.u32(demo + DC_RECORDING)
        info['playback'] = reader.u32(demo + DC_PLAYBACK)
        info['paused'] = reader.u32(demo + DC_PAUSED)
        info['flags'] = reader.u32(demo + DC_FLAGS)
        try:
            info['name'] = reader.string_field(demo + DC_RECORD_NAME)
        except Exception:
            info['name'] = '?'
    if peer:
        rec = reader.u32(peer + PEER_RECORDER)
        info['recorder'] = rec
        if rec:
            info['frame'] = reader.u32(rec + REC_FRAME)
            info['memout'] = reader.u32(rec + REC_MEMOUT)
            stream = reader.u32(rec + REC_STREAM)
            info['stream'] = stream
            if stream:
                try:
                    start = reader.u32(stream + BUF_START)
                    write = reader.u32(stream + BUF_WRITE)
                    if 0 < start <= write < 0x80000000:
                        info['buf_bytes'] = write - start
                except Exception:
                    pass
    return info


def demo_files():
    if not DEMOS.is_dir():
        return []
    rows = []
    for p in DEMOS.iterdir():
        if p.is_file() and p.suffix.startswith('.fb') or (p.is_file() and p.suffix == '.txt'):
            try:
                st = p.stat()
                rows.append((p.name, st.st_size, st.st_mtime))
            except OSError:
                pass
    rows.sort(key=lambda r: (r[2], r[0]))
    return rows


def human(n):
    if n is None:
        return '?'
    for unit in ('B', 'KiB', 'MiB', 'GiB'):
        if n < 1024 or unit == 'GiB':
            return f'{n:.0f} {unit}' if unit == 'B' else f'{n/1:.1f} {unit}'
        n /= 1024


def render(reader, prev):
    now = time.monotonic()
    s = snapshot(reader)
    stamp = datetime.now().strftime('%H:%M:%S')

    lines = []
    lines.append(f'BC2 recorder monitor   {stamp}   PID {reader.pid}')
    lines.append('-' * 66)
    if not s['demo']:
        lines.append('DemoControl not found (server still starting?). Retrying...')
        return '\n'.join(lines), s, prev
    rec = 'ON' if s['recording'] == 1 else 'off'
    lines.append(f"Recording: {rec:>3}   playback={s['playback']} paused={s['paused']} flags=0x{(s['flags'] or 0):X}")
    lines.append(f"Base name: {s['name'] or '(empty)'}")
    if s['recorder']:
        frame = s['frame']
        rate = ''
        if prev.get('frame') is not None and prev.get('t') is not None and frame is not None:
            dt = now - prev['t']
            if dt > 0:
                rate = f"  (+{(frame - prev['frame']) / dt:.1f}/s)"
        secs = '' if frame is None else f'  ~{frame / FPS:.1f}s recorded'
        lines.append(f"Recorder: frame {frame}{rate}{secs}")
        grow = ''
        if prev.get('buf_bytes') is not None and s['buf_bytes'] is not None:
            d = s['buf_bytes'] - prev['buf_bytes']
            grow = f'  (+{human(d)}/poll)' if d >= 0 else '  (shrunk)'
        lines.append(f"RAM buffer: {human(s['buf_bytes'])}{grow}   (memout={s['memout']})")
    else:
        lines.append('Recorder object not created yet.')

    files = demo_files()
    total = sum(r[1] for r in files)
    nonempty = [r for r in files if r[1] > 0]
    lines.append('-' * 66)
    lines.append(f"demos/: {len(files)} plikow, {human(total)} na dysku, {len(nonempty)} niepustych")
    for name, size, mtime in files[-8:]:
        when = datetime.fromtimestamp(mtime).strftime('%H:%M:%S')
        mark = '  <== zapisany' if size > 0 else ''
        lines.append(f"  {when}  {size:>10} B  {name}{mark}")
    if total == 0 and s['recording'] == 1:
        lines.append('')
        lines.append('UWAGA: 0 B podczas gry jest NORMALNE - dane sa w RAM.')
        lines.append('Zrzut na dysk nastapi przy zakonczeniu rundy / zmianie mapy.')

    if s['buf_bytes'] is not None and prev.get('buf_bytes') is not None:
        prev['grew'] = s['buf_bytes'] > prev['buf_bytes']
    prev['frame'] = s['frame']
    prev['buf_bytes'] = s['buf_bytes']
    prev['t'] = now
    return '\n'.join(lines), s, prev


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--once', action='store_true', help='single snapshot')
    ap.add_argument('--interval', type=float, default=1.0)
    args = ap.parse_args()

    while True:
        cands = [pid for pid, name in processes() if name == TARGET]
        if not cands:
            other = [(pid, name) for pid, name in processes() if name != TARGET]
            if args.once:
                print(f'No recording server ({TARGET}) is running.')
                return 1
            os.system('cls')
            print('BC2 recorder monitor')
            print('-' * 66)
            print(f'Czekam na uruchomienie: {TARGET}')
            if other:
                print('Inne procesy Frost: ' + ', '.join(f'{n} (PID {p})' for p, n in other))
            time.sleep(args.interval)
            continue
        if len(cands) > 1:
            print(f'WARNING: {len(cands)} recording servers found: {cands}')
        pid = cands[0]
        try:
            reader = Reader(pid)
        except OSError as exc:
            print(f'Cannot open PID {pid}: {exc}')
            return 2
        try:
            prev = {}
            if args.once:
                text, _, _ = render(reader, prev)
                print(text)
                return 0
            while True:
                text, _, prev = render(reader, prev)
                os.system('cls')
                print(text)
                time.sleep(args.interval)
        finally:
            reader.close()
        if args.once:
            return 0
        time.sleep(args.interval)


if __name__ == '__main__':
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        sys.exit(130)