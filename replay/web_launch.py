"""Auto-login and auto-join for Launch Game via the client's web-launch path.

The client accepts -loginToken / -requestState / -requestStateParams, but only
acts on them once something connects to its pipe \\\\.\\pipe\\rome_snowroller
(EA's browser plugin did that). serve_pipe() plays that plugin: it connects,
then drains the client's status messages until the client exits.
Pipe frame (BFBC2Game.exe 0x5a95f0): 'K' 'E' total_len name_len name data_len data.
"""
import ctypes
from ctypes import wintypes as w
import os
import time
from backend.rome_local import CLIENT_NAME, GID_DEFAULT, LID

PIPE = r'\\.\pipe\rome_snowroller'
# gameid is 64-bit; both halves must be nonzero (0x5b3fa3).
GAME_ID = (LID << 32) | GID_DEFAULT


def local_env():
    """Environment for client and server: the rome_proxy dinput8.dll then points
    Rome at the local backend without a hosts entry (no effect without the proxy)."""
    return dict(os.environ, BC2_ROME_HOST='127.0.0.1')


def client_env():
    """local_env() for the panel's REPLAY client only: BC2_REPLAY=1 tells the shared dinput8 proxy's client
    recorder (rome_proxy README section 4) to record nothing (recorder_<pid>.log: 'off (BC2_REPLAY=1 ...)').
    A client started any other way records its online session by default."""
    return dict(local_env(), BC2_REPLAY='1')


def client_args():
    # requestStateParams are read as attributes of <data> (0x5a97a0).
    params = '<data personaref="%s" clantag="" gameid="%d"/>' % (CLIENT_NAME, GAME_ID)
    return ['-loginToken', 'local', '-sessionId', 'local',
            '-requestState', 'State_ConnectToGameId', '-requestStateParams', params]


def parse(buf):
    msgs = []
    while len(buf) >= 5 and buf[:2] == b'KE':
        n = buf[3]
        msgs.append((buf[4:4 + n].decode(errors='replace'),
                     buf[5 + n:5 + n + buf[4 + n]].decode(errors='replace')))
        buf = buf[5 + n + buf[4 + n]:]
    return msgs


def serve_pipe(process, timeout=120):
    """Connect to the client's pipe and print its messages until it closes."""
    deadline = time.monotonic() + timeout
    while True:
        if process.poll() is not None:
            raise RuntimeError('BC2 client exited before its launch pipe opened.')
        try:
            pipe = open(PIPE, 'r+b', buffering=0)
            break
        except OSError:
            if time.monotonic() > deadline:
                raise RuntimeError('BC2 client launch pipe did not open; log in manually.')
            time.sleep(0.5)
    print('Launch pipe connected; auto-login and join requested.', flush=True)
    raised = bring_to_front(process.pid)
    with pipe:
        while True:
            try:
                chunk = pipe.read(4096)
            except OSError:
                break
            if not chunk:
                break
            for name, data in parse(chunk):
                print('client:', name, data, flush=True)
            if not raised:
                raised = bring_to_front(process.pid)


def bring_to_front(pid):
    """Raise the client's visible top-level window; False if it has none yet."""
    user32 = ctypes.windll.user32
    found = []

    @ctypes.WINFUNCTYPE(w.BOOL, w.HWND, w.LPARAM)
    def collect(hwnd, _):
        owner = w.DWORD()
        user32.GetWindowThreadProcessId(hwnd, ctypes.byref(owner))
        if owner.value == pid and user32.IsWindowVisible(hwnd):
            found.append(hwnd)
        return True

    user32.EnumWindows(collect, 0)
    for hwnd in found:
        # A synthetic ALT tap lifts Windows' foreground lock for SetForegroundWindow.
        user32.keybd_event(0x12, 0, 0, 0)
        user32.keybd_event(0x12, 0, 2, 0)
        user32.ShowWindow(hwnd, 9)  # SW_RESTORE
        user32.SetForegroundWindow(hwnd)
    return bool(found)
