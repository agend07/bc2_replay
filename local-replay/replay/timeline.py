"""Guarded timeline playback, recording selection and server switching."""
import math
from replay.server import Server, recording, FPS, END_MARGIN_FRAMES, EXE_NAME, processes, k
from replay import seek_guard as seek_view_guard

from preview_config import CONFIG, APP_ROOT, TIMELINE_HASH, server_dir, recording_argument, timeline_exe


def list_recordings():
    """Return available validated recordings in server_dir/demos sorted by name."""
    srv = server_dir()
    demos_dir = srv / 'demos'
    items = []
    if not demos_dir.is_dir():
        return items
    for txt_path in sorted(demos_dir.glob('*.txt')):
        base = 'demos/' + txt_path.stem
        try:
            rel, lvl, last_frame = recording(base)
            limit = max(0, last_frame - END_MARGIN_FRAMES)
            items.append({
                'base': rel,
                'name': txt_path.stem,
                'level': lvl,
                'last_frame': last_frame,
                'limit_frame': limit,
                'seconds': round(last_frame / FPS, 1),
            })
        except Exception:
            continue
    return items


def active_demo():
    return recording_argument()


def playlist_for_level(lvl):
    return 'CONQUEST' if lvl.upper().endswith('CQ') else 'RUSH'


def switch_recording(base):
    """Validate base, update config/instance if present, and restart the Timeline server."""
    import json
    import subprocess
    import time
    rel, lvl, last_frame = recording(base)
    srv = server_dir()
    exe = timeline_exe()
    from preview import verify
    verify(exe, TIMELINE_HASH)

    inst = APP_ROOT / 'runtime' / 'Instance-local-preview'
    if not (inst / 'ServerOptions.ini').is_file():
        raise RuntimeError('Run Setup / Build first')
    entries = processes()
    if entries and (len(entries) != 1 or entries[0][1] != EXE_NAME):
        raise RuntimeError('Another dedicated server is running; stop it first')
    # Reuse exact path/hash/mailbox validation before stopping our own server.
    server = Server(stop=True) if entries else None
    try:
        cfg = json.loads(CONFIG.read_text(encoding='utf-8'))
        cfg.update(recording_base=rel, level=lvl)
        CONFIG.write_text(json.dumps(cfg, indent=2) + '\n', encoding='utf-8')
        (inst / 'maplist.txt').write_text(
            f'{playlist_for_level(lvl)}\n{lvl.lower()} 1\n', encoding='ascii'
        )
        if server and not k.TerminateProcess(server.handle, 0):
            raise RuntimeError('Could not stop preview server')
    finally:
        if server:
            server.close()
    instance_arg = inst.as_posix() + '/'

    deadline = time.monotonic() + 5.0
    while time.monotonic() < deadline and processes():
        time.sleep(0.1)
    if processes():
        raise RuntimeError('Previous dedicated server did not exit in time')

    (srv / 'diagnostics').mkdir(exist_ok=True)
    args = [
        str(exe), '-server', '-dedicated', '-serverInstancePath', instance_arg,
        '-mapPack2Enabled', '1', '-port', '19567', '-timeStampLogNames',
        '-Demo.PlaybackDemoFileName', rel, '-Server.Level', lvl, '-displayAsserts', '0',
    ]
    child = subprocess.Popen(
        args, cwd=srv, creationflags=subprocess.CREATE_NO_WINDOW,
        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    return {
        'pid': child.pid,
        'recording_base': rel,
        'level': lvl,
        'last_frame': last_frame,
        'limit_frame': max(0, last_frame - END_MARGIN_FRAMES),
    }


def seek_frame(text,limit):
    parts=text.strip().split(':')
    if not 1<=len(parts)<=3:raise ValueError('Use seconds, mm:ss or hh:mm:ss')
    try:values=[float(part) for part in parts]
    except ValueError:raise ValueError('Use seconds, mm:ss or hh:mm:ss') from None
    if not all(math.isfinite(value) and value>=0 for value in values):
        raise ValueError('Time must be finite and nonnegative')
    if len(values)>1 and (any(value>=60 for value in values[1:]) or any(value!=int(value) for value in values[:-1])):
        raise ValueError('Use whole hours/minutes and seconds below 60')
    seconds=0.0
    for value in values:seconds=seconds*60+value
    if not math.isfinite(seconds):raise ValueError('Time is too large')
    frame=round(seconds*FPS)
    if not 0<=frame<=limit:raise ValueError('Choose a time from 0 to '+str(limit/FPS)+' seconds')
    return frame


def limit_frame():return max(0,recording(active_demo())[2]-END_MARGIN_FRAMES)


def ensure_seek_guard():
    return seek_view_guard.install()


def operate(action,frame=0):
    if action not in ('status','pause','play','seek'):raise ValueError('Unknown timeline action')
    if action=='seek' and (not isinstance(frame,int) or not 0<=frame<=limit_frame()):
        raise ValueError('Seek outside safe indexed range')
    server=Server(write=action!='status')
    try:
        state=server.status()
        if action=='status':return state
        if action=='seek':
            if state['connections']!=1:
                raise RuntimeError('Seek trial requires exactly one connected viewer')
            guard_status=ensure_seek_guard()
        if action=='play':
            if state['frame']>=limit_frame():raise RuntimeError('End guard: seek earlier before Play')
            return server.resume()
        result=server.command(1 if action=='pause' else 3,frame)
        if action=='seek':
            result['requested_frame']=frame
            result['target_reached']=abs(result['frame']-frame)<=1
            result['seek_guard']=guard_status
        return result
    finally:server.close()
