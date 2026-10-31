"""Free-camera home pose: after 'Enable free camera' put the camera somewhere findable (r220).
Order: a live soldier (the local/selected player first, then any player with a validated soldier link),
else the last soldier position remembered for this level, else a per-map seed (a ground spawn point
decoded from our demos). World (0,0,0) is not used: Y is up and the maps sit at y ~68..135, so the
origin is underground or off-map. The pose itself is focus_matrix (3 m above, 6 m behind the point).
"""
import json
import time
import struct
from preview_config import APP_ROOT as ROOT, server_dir
from replay.players import roster, transform
from replay.camera import STATE, focus_matrix, require_idle, status, log, guard, CameraNotReady
from replay.camera_adapter import REQUEST, COMMAND, TARGET, ORIGINAL, VERSION

MEMORY = ROOT/'runtime'/'private_camera_home.json'
HOME_LIFT = 12.0  # metres added to the home point (r222c)
# Soldier spawn positions (x, y-up, z) decoded from demo soldier p2 (r214 p2pos.py, origin-zero = world).
SEEDS = {
    'Levels/MP_006CQ': (139.5, 79.0, 114.3),       # Arica Harbor, cl04_1088 viewer soldier f2870
    'Levels/MP_005': (229.1, 68.6, 464.2),         # Atacama, cl04_1081 viewer soldier f2182
    'Levels/MP_SP_005CQ': (-186.3, 135.5, -693.0),  # Heavy Metal, cl04_1084 viewer soldier f2166
}


def current_level():
    """Level of the demo the panel plays (demos/<stem>.txt 'Level: ...'), None if unknown."""
    try:
        from replay.timeline import active_demo
        for line in (server_dir()/(active_demo()+'.txt')).read_text(encoding='utf-8', errors='replace').splitlines():
            if line.startswith('Level:'):
                return line.split(':', 1)[1].strip()
    except Exception:
        pass
    return None


def _load():
    try:
        return json.loads(MEMORY.read_text(encoding='utf-8'))
    except (OSError, ValueError):
        return {}


def remember(level, position):
    if not level:
        return
    data = _load(); data[level] = [round(x, 2) for x in position]
    try:
        MEMORY.write_text(json.dumps(data, indent=2), encoding='utf-8')
    except OSError:
        pass


def live_position(client):
    """(name, position) of a live soldier: selected/local player first, then roster order; None if nobody is alive."""
    try:
        players = roster(client)['players']
    except (OSError, RuntimeError):
        return None
    for p in sorted(players, key=lambda p: not p['selected_local_object']):
        if p['soldier']['status'] != 'validated_reciprocal_reference':
            continue
        try:
            return p['name'], transform(client, int(p['soldier']['address'], 16))['candidate_position']
        except (OSError, RuntimeError):
            continue
    return None


def home_position(client, level=None):
    """(source, position) or (None, None)."""
    level = level or current_level()
    live = live_position(client)
    if live:
        remember(level, live[1])
        return 'player ' + live[0], live[1]
    saved = _load().get(level) if level else None
    if saved:
        return 'last known position', saved
    if level in SEEDS:
        return 'map spawn point', list(SEEDS[level])
    return None, None


def home_command(client, data, position, *, replay_kind='timeline'):
    """One-shot pose (like Focus on a name, but on a position). Requires free mode active."""
    if data.get('adapter_version', 1) != VERSION:
        raise RuntimeError('Unsupported camera version; restart client')
    current = require_idle(client, data)
    if not current['enabled']:
        raise RuntimeError('Enable free camera first')
    guard(replay_kind)
    if current['actual_mode'] != 0 or not current['free_camera_is_global_active']:
        raise CameraNotReady('Free camera is not the active camera; no pose applied')
    # r222c: live cl04_1096 pre-spawn home (another player's soldier) put the camera under terrain /
    # inside a building. Lift the look-at point; the eye ends ~HOME_LIFT+3 m above the point.
    position = [position[0], position[1]+HOME_LIFT, position[2]]
    client.write(data['mailbox']+TARGET, focus_matrix(position))
    command = 3 if current['saved_transform'] else 1
    sequence = (current['focus_request']+1) & 0xffffffff
    client.write(data['mailbox']+COMMAND, struct.pack('<I', command))
    client.write(data['mailbox']+REQUEST, struct.pack('<I', sequence))
    deadline = time.monotonic()+5
    while time.monotonic() < deadline:
        result = status(client, data)
        if result['focus_ack'] == sequence:
            if result['focus_result']:
                raise RuntimeError('Home pose rejected: '+str(result['focus_result']))
            if command == 1:
                data['saved_focus_transform'] = client.read(data['mailbox']+ORIGINAL, 64).hex()
                STATE.write_text(json.dumps(data, indent=2), encoding='utf-8')
            log('home', data, result)
            return result
        time.sleep(.03)
    raise RuntimeError('Home pose timed out; inspect status before recovery')
