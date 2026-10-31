"""Conservative cleanup of orphan backends from the exact same installation."""
import base64
import subprocess
import time


def cleanup_orphan_backends(cmd):
    """Only backend-mode processes with our exact path and no living parent.

    Uses a process handle opened before the final identity checks so PID reuse
    cannot redirect termination to a replacement process. Unrelated installs,
    active controllers and non-backend processes are never terminated.
    """
    def quote(value):
        return "'" + str(value).replace("'", "''") + "'"

    executable = quote(cmd[0])
    # Exact generated command line also distinguishes Python source scripts.
    expected_command = quote(subprocess.list2cmdline(cmd))
    ps = r"""
$ErrorActionPreference = 'Stop'
$exe = EXE_VALUE
$expectedCommand = COMMAND_VALUE
$items = @(Get-CimInstance Win32_Process | Where-Object {
    $_.ExecutablePath -eq $exe -and $_.CommandLine -ceq $expectedCommand
})
foreach ($item in $items) {
    if (Get-Process -Id $item.ParentProcessId -ErrorAction SilentlyContinue) { continue }
    try {
        $p = Get-Process -Id $item.ProcessId -ErrorAction Stop
        $handle = $p.Handle
        $again = Get-CimInstance Win32_Process -Filter ('ProcessId=' + $item.ProcessId)
        if (-not $again -or $again.CreationDate -ne $item.CreationDate -or
            $again.ExecutablePath -ne $exe -or $again.CommandLine -ne $item.CommandLine -or
            $again.ParentProcessId -ne $item.ParentProcessId) { continue }
        if (Get-Process -Id $again.ParentProcessId -ErrorAction SilentlyContinue) { continue }
        $p.Kill()
        if (-not $p.WaitForExit(5000)) { throw 'Orphan backend did not exit' }
    } finally {
        if ($p) { $p.Dispose(); $p = $null }
    }
}
""".replace('EXE_VALUE', executable).replace('COMMAND_VALUE', expected_command)
    encoded = base64.b64encode(ps.encode('utf-16-le')).decode('ascii')
    result = subprocess.run(
        ['powershell.exe', '-NoProfile', '-NonInteractive', '-EncodedCommand', encoded],
        capture_output=True, text=True, timeout=30,
        creationflags=subprocess.CREATE_NO_WINDOW)
    if result.returncode:
        raise RuntimeError('Cannot safely clean up the previous backend. Close it manually and retry.')


def stop_owned_backend(process):
    """Popen retains the original Windows handle, avoiding PID-only termination."""
    if process is not None and process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


def stop_preview_server(timeout=10.0, expected_pid=None):
    """Stop only this installation's playback server, re-validated first.

    Identity comes from the existing `Server(stop=True)` guard: exact image
    path, builder hash and the playback mailbox magic. Anything else (an
    unrelated BC2 server, a foreign build, a missing mailbox) raises and is
    left running. expected_pid (controller close): stop only that process, the one the
    controller started. Returns a small status dict; never raises for "not running".
    """
    from replay.server import NotRunning, Server, processes, replay_candidates
    from replay import server as server_module

    # Server() keeps only the candidate with our path, hash and mailbox MAGIC, so a normal
    # Vietnam server is never terminated.
    if not replay_candidates():
        return {'stopped': False, 'reason': 'no playback server was running'}
    try:
        server = Server(stop=True)
    except NotRunning as exc:
        return {'stopped': False, 'reason': str(exc)}
    pid = server.pid
    if expected_pid is not None and pid != expected_pid:
        server.close()
        return {'stopped': False, 'reason': f'running server {pid} is not the one this controller started'}
    try:
        if not server_module.k.TerminateProcess(server.handle, 0):
            raise RuntimeError('Could not stop the preview server')
    finally:
        server.close()
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if pid not in [p for p, _name in processes()]:
            return {'stopped': True, 'pid': pid}
        time.sleep(0.1)
    raise RuntimeError('The playback server did not exit in time')
