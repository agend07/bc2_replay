"""Guarded timeline playback, recording selection and server switching."""
import math
from replay.server import Server, recording, FPS, END_MARGIN_FRAMES, REPLAY_NAMES, processes, k, SPEED_OFFSET
import struct
from replay import seek_guard as seek_view_guard
from replay import safe_seek  # r224
from replay import key_safety  # r283

from preview_config import APP_ROOT, server_dir, recording_argument, timeline_exe, TIMELINE_HASH, \
    save_settings, verify


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
    # BC2 level naming: CQ/GR suffix names the mode; unsuffixed MP_001/003/005/007 are Conquest,
    # unsuffixed MP_002/004/006/008 are Rush (r153: 'Levels/MP_005' was mapped to RUSH -> server fell back to MP_009GR).
    name = lvl.upper().rsplit('/', 1)[-1]
    if name.endswith('CQ'): return 'CONQUEST'
    if name.endswith('GR'): return 'RUSH'
    if name.endswith('SDM'): return 'SQDM'
    if name.endswith('SR'): return 'SQRUSH'
    return 'CONQUEST' if name in ('MP_001', 'MP_003', 'MP_005', 'MP_007') else 'RUSH'


def write_maplist(inst, lvl):
    """Write the instance's maplist.txt for one level (playlist from playlist_for_level)."""
    (inst / 'maplist.txt').write_text(f'{playlist_for_level(lvl)}\n{lvl.lower()} 1\n', encoding='ascii')


def server_command(exe, instance, rel, lvl):
    """Replay server command line (switch_recording and `preview.py server`). Argument order is live-proven."""
    return [
        str(exe), '-server', '-dedicated', '-serverInstancePath', instance.as_posix() + '/',
        '-mapPack2Enabled', '1', '-port', '19567', '-timeStampLogNames',
        '-Demo.PlaybackDemoFileName', rel, '-Server.Level', lvl, '-displayAsserts', '0',
    ]


def switch_recording(base):
    """Validate base, update config/instance if present, and restart the Timeline server."""
    import subprocess
    import time
    rel, lvl, last_frame = recording(base)
    srv = server_dir()
    exe = timeline_exe()
    verify(exe, TIMELINE_HASH)

    inst = APP_ROOT / 'runtime' / 'Instance-local-preview'
    if not (inst / 'ServerOptions.ini').is_file():
        raise RuntimeError('Run Setup first')
    entries = processes()
    if entries and (len(entries) != 1 or entries[0][1] not in REPLAY_NAMES):
        raise RuntimeError('Another dedicated server is running; stop it first')
    # Reuse exact path/hash/mailbox validation before stopping our own server.
    server = Server(stop=True) if entries else None
    try:
        save_settings({'recording_base': rel, 'level': lvl})
        write_maplist(inst, lvl)
        if server and not k.TerminateProcess(server.handle, 0):
            raise RuntimeError('Could not stop preview server')
    finally:
        if server:
            server.close()

    deadline = time.monotonic() + 5.0
    while time.monotonic() < deadline and processes():
        time.sleep(0.1)
    if processes():
        raise RuntimeError('Previous dedicated server did not exit in time')

    (srv / 'diagnostics').mkdir(exist_ok=True)
    args = server_command(exe, inst, rel, lvl)
    from replay.web_launch import local_env
    child = subprocess.Popen(
        args, cwd=srv, env=local_env(), creationflags=subprocess.CREATE_NO_WINDOW,
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


_KEY_CACHE = {}


def key_frames(base=None):
    """r224: usable .fbgk key frames (panel/server seek frames) of the active demo, <= limit."""
    rel, _lvl, last_frame = recording(base or active_demo())
    path = server_dir().resolve() / (rel + '.fbgk')
    st = path.stat()
    sig = (str(path), st.st_mtime_ns, st.st_size)
    keys = _KEY_CACHE.get(sig)
    if keys is None:
        keys = safe_seek.parse_fbgk(path.read_bytes())
        _KEY_CACHE.clear()
        _KEY_CACHE[sig] = keys
    return safe_seek.usable_keys(keys, max(0, last_frame - END_MARGIN_FRAMES))


def seek_redirect(frame, base=None):
    """r283: (frame, None) or (safe key, avoided key) when frame's key names a ghost missing from that key in the
    viewer's view chain (cl04_1084 2:30: the client is removed on such a seek). Needs demos/<stem>.players.json viewer."""
    import json
    rel = recording(base or active_demo())[0]
    root = server_dir().resolve()
    try:
        data = json.loads((root / (rel + '.players.json')).read_text(encoding='utf-8'))
    except (OSError, ValueError):
        return frame, None
    ids = key_safety.viewer_ids(data)
    if not ids:
        return frame, None
    try:
        unsafe = key_safety.unsafe_keys_cached(root / (rel + '.fbgd'), ids)
    except (OSError, ValueError):
        return frame, None
    return key_safety.redirect(key_frames(base), unsafe, frame)


def ensure_seek_guard():
    return seek_view_guard.install()


def write_speed(x):
    """Raw replay speed write (any positive float; destr_seek plays up to 50x). Returns status()."""
    server = Server(write=True)
    try:
        server.write(SPEED_OFFSET, struct.pack('<f', 0.0 if x == 1.0 else float(x)))
        return server.status()
    finally:
        server.close()


def seek_keeping_destruction(target, current, consistent, after_seek_zero=None):
    """r286: reach target by fast forward play (from 0 when needed) so earlier destruction is shown."""
    from replay import destr_seek
    if not 0 <= target <= limit_frame():
        raise ValueError('Seek outside safe indexed range')
    return destr_seek.seek_keeping_destruction(target, current, consistent, operate, write_speed,
                                               after_seek_zero=after_seek_zero)


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
        asked,avoided,check_error=frame,None,None
        if action=='seek':
            try:frame,avoided=seek_redirect(frame)  # r283: never restore a key with a dangling viewer ref
            except Exception as exc:frame,avoided,check_error=asked,None,f'{type(exc).__name__}: {exc}'  # never block a seek
        result=server.command(1 if action=='pause' else 3,frame)
        if action=='seek':
            result['requested_frame']=asked
            if avoided is not None:
                result['avoided_key']=avoided
                result['redirected_to']=frame
            if check_error:result['key_safety_error']=check_error
            result['target_reached']=abs(result['frame']-frame)<=1
            result['seek_guard']=guard_status
        return result
    finally:server.close()
