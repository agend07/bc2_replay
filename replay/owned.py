"""Processes this controller started (BC2 client, replay server) and their shutdown when it closes.

Ownership = one record per kind in runtime/private_owned_<kind>.json: pid, the image path and the
creation time read from that pid right after the controller started it (so the server is tracked
by its PID).
A stop re-reads image + creation time through the very handle it terminates, so a reused PID is
never touched. A BC2 client the user started by hand never gets a record and is left alone.
The backend is stopped through its Popen handle (launcher_panel.stop_backend), not from here.

A force-killed previous panel bypasses its close handler; the records it leaves
behind are swept by the next panel at start (sweep_stale), with the same identity check.
"""
import ctypes as c
from ctypes import wintypes as w
import json
import os

from preview_config import APP_ROOT

RUNTIME = APP_ROOT / 'runtime'
KINDS = ('client', 'server')  # close order: client, then server (backend last, by launcher_panel)
CLIENT_ENV = 'BC2_PANEL_OWNED_CLIENT'  # set by the panel's Launch Game: only then the client is recorded
STOP_TIMEOUT_S = 5.0

PROCESS_TERMINATE = 0x0001
PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
SYNCHRONIZE = 0x00100000
WAIT_OBJECT_0 = 0
WM_CLOSE = 0x0010

k = c.WinDLL('kernel32', use_last_error=True)
u = c.WinDLL('user32', use_last_error=True)
k.OpenProcess.argtypes = [w.DWORD, w.BOOL, w.DWORD]
k.OpenProcess.restype = w.HANDLE
k.CloseHandle.argtypes = [w.HANDLE]
k.QueryFullProcessImageNameW.argtypes = [w.HANDLE, w.DWORD, w.LPWSTR, c.POINTER(w.DWORD)]
k.GetProcessTimes.argtypes = [w.HANDLE] + [c.POINTER(w.FILETIME)] * 4
k.TerminateProcess.argtypes = [w.HANDLE, w.UINT]
k.WaitForSingleObject.argtypes = [w.HANDLE, w.DWORD]
k.WaitForSingleObject.restype = w.DWORD
WNDENUMPROC = c.WINFUNCTYPE(w.BOOL, w.HWND, w.LPARAM)
u.EnumWindows.argtypes = [WNDENUMPROC, w.LPARAM]
u.GetWindowThreadProcessId.argtypes = [w.HWND, c.POINTER(w.DWORD)]
u.PostMessageW.argtypes = [w.HWND, w.UINT, w.WPARAM, w.LPARAM]


# ---------------------------------------------------------------- pure helpers (tested)

def same_process(record, identity):
    """identity: (image path, creation time) read now, or None (gone). True only for the same image
    (case-insensitive) and the same creation time as when the controller started it."""
    if not record or identity is None:
        return False
    image, created = identity
    return str(image).lower() == str(record.get('image', '')).lower() and created == record.get('created')


def shutdown_plan(records):
    """{kind: record} -> records in close order (client before server); unknown kinds dropped."""
    return [records[kind] for kind in KINDS if records.get(kind)]


# ---------------------------------------------------------------- Win32 (read identity, stop)

def _identity(handle):
    size = w.DWORD(32768)
    buf = c.create_unicode_buffer(size.value)
    if not k.QueryFullProcessImageNameW(handle, 0, buf, c.byref(size)):
        return None
    times = [w.FILETIME() for _ in range(4)]
    if not k.GetProcessTimes(handle, *[c.byref(t) for t in times]):
        return None
    return buf.value, (times[0].dwHighDateTime << 32) | times[0].dwLowDateTime


def _open(pid, access):
    return k.OpenProcess(access, False, int(pid)) if pid else None


def identity(pid):
    """(image path, creation time) of a running pid, or None. Only queries; never writes."""
    handle = _open(pid, PROCESS_QUERY_LIMITED_INFORMATION | SYNCHRONIZE)
    if not handle:
        return None
    try:
        if k.WaitForSingleObject(handle, 0) == WAIT_OBJECT_0:
            return None  # exited (handle still valid while someone holds it)
        return _identity(handle)
    finally:
        k.CloseHandle(handle)


def make_record(kind, pid):
    found = identity(pid)
    if found is None:
        return None
    return {'kind': kind, 'pid': int(pid), 'image': found[0], 'created': found[1]}


def alive(record):
    return bool(record) and same_process(record, identity(record.get('pid')))


def _post_close(pid):
    def each(hwnd, _lparam):
        owner = w.DWORD(0)
        u.GetWindowThreadProcessId(hwnd, c.byref(owner))
        if owner.value == pid:
            u.PostMessageW(hwnd, WM_CLOSE, 0, 0)
        return True
    u.EnumWindows(WNDENUMPROC(each), 0)


def stop(record, timeout=STOP_TIMEOUT_S, graceful=False):
    """Stop the recorded process if it is still the same one. graceful: WM_CLOSE first, force after
    timeout. Returns 'gone', 'not_owned' (pid reused: left alone), 'closed' or 'killed'."""
    handle = _open(record.get('pid'), PROCESS_TERMINATE | PROCESS_QUERY_LIMITED_INFORMATION | SYNCHRONIZE)
    if not handle:
        return 'gone'
    try:
        if k.WaitForSingleObject(handle, 0) == WAIT_OBJECT_0:
            return 'gone'
        if not same_process(record, _identity(handle)):
            return 'not_owned'
        if graceful:
            _post_close(record['pid'])
            if k.WaitForSingleObject(handle, int(timeout * 1000)) == WAIT_OBJECT_0:
                return 'closed'
        k.TerminateProcess(handle, 1)
        k.WaitForSingleObject(handle, int(timeout * 1000))
        return 'killed'
    finally:
        k.CloseHandle(handle)


# ---------------------------------------------------------------- records on disk

def path(kind, runtime=None):
    return (runtime or RUNTIME) / f'private_owned_{kind}.json'


def save(record, runtime=None):
    target = path(record['kind'], runtime)
    target.parent.mkdir(parents=True, exist_ok=True)
    temp = target.with_suffix('.tmp')
    temp.write_text(json.dumps(record) + '\n', encoding='utf-8')
    os.replace(temp, target)


def record(kind, pid, runtime=None):
    """Remember a process the controller just started. Returns the record (None if already gone)."""
    rec = make_record(kind, pid)
    if rec:
        save(rec, runtime)
    return rec


def load(kind, runtime=None):
    try:
        data = json.loads(path(kind, runtime).read_text(encoding='utf-8'))
    except (OSError, ValueError):
        return None
    return data if isinstance(data, dict) and data.get('kind') == kind else None


def forget(kind, runtime=None):
    try:
        path(kind, runtime).unlink()
    except OSError:
        pass


def load_all(runtime=None):
    return {kind: load(kind, runtime) for kind in KINDS}


def record_launched_client(pid):
    """Called by 'preview.py client' after it started BFBC2Game.exe; only when the panel asked."""
    if os.environ.get(CLIENT_ENV) == '1':
        record('client', pid)


def stop_clients(pids, runtime=None, stopper=None):
    """Recording switch: a client that stays running gets confused by the new server (user, r285), so stop
    every BC2 client by PID (identity-checked, forced) and forget the client record. Returns {pid: result}."""
    stopper = stopper or (lambda rec: stop(rec, graceful=False))
    done = {}
    for pid in pids:
        rec = make_record('client', pid)
        done[pid] = stopper(rec) if rec else 'gone'
    forget('client', runtime)
    return done


def sweep_stale(runtime=None, stopper=None):
    """Panel start: stop what a force-killed previous panel left. The
    controller lock guarantees no other panel is open, so every verified record is stale."""
    stopper = stopper or (lambda rec: stop(rec, graceful=False))
    done = {}
    for rec in shutdown_plan(load_all(runtime)):
        try:
            done[rec['kind']] = stopper(rec)
        except Exception as exc:
            done[rec['kind']] = f'error: {exc}'
        forget(rec['kind'], runtime)
    return done


def close_owned(runtime=None, stop_server=None, stopper=None, is_alive=None):
    """Controller close: the BC2 client it launched (WM_CLOSE, forced after the timeout), then the
    replay server it started (the stop-server path, limited to that PID; forced by handle if that
    refuses). Each step is bounded by STOP_TIMEOUT_S. Returns {kind: result}."""
    stopper = stopper or stop
    is_alive = is_alive or alive
    if stop_server is None:
        from backend_lifecycle import stop_preview_server

        def stop_server(pid):
            return stop_preview_server(timeout=STOP_TIMEOUT_S, expected_pid=pid)
    done = {}
    for rec in shutdown_plan(load_all(runtime)):
        kind = rec['kind']
        try:
            if kind == 'client':
                done[kind] = stopper(rec, graceful=True)
            elif not is_alive(rec):
                done[kind] = 'gone'
            else:
                try:
                    done[kind] = stop_server(rec['pid'])
                except Exception as exc:
                    done[kind] = f'stop-server refused: {exc}'
                if is_alive(rec):
                    done[kind] = stopper(rec, graceful=False)
        except Exception as exc:
            done[kind] = f'error: {exc}'
        forget(kind, runtime)
    return done
