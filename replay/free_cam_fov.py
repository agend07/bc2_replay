"""r227: fixed field of view for the free camera (data only). Always on: clean
screen applies it on every enable (replay/clean_screen.apply_extras); no opt-in.
The value is the soldier FOV at enable time if it looks unzoomed, else DEFAULT_FOV.

Static evidence (exact build 3911fcc8, offline disassembly, not yet tested live):
* Camera base ctor 0x78CCE0 (used by FreeCamera ctor 0x7AC080, vtable 0x142C6E8)
  sets float [cam+0x74] = 0.0 -- a per-camera FOV override in degrees.
* View setup 0x7A5D90 (camera manager, esi = [0x1570D80]):
    fov = ([mgr+0xAC] && [[mgr+0xAC]+0x74] > 0) ? [[mgr+0xAC]+0x74] : [[mgr+0xB0]+0x30]
  then fov += delta, * pi / 180, -> 0x965970 (render view). The same selection
  is in 0x78D520 / 0x78D550 / 0x7B192C / 0x7B3C82 (getters / zoom ratio).
* With the free camera active, [mgr+0xAC] is the FreeCamera, its +0x74 is 0.0,
  so the render FOV falls back to [[mgr+0xB0]+0x30]: the viewer soldier's
  camera FOV, which changes with ADS/zoom/sprint -> the random FOV in free cam.

Mechanism: write a positive float to FreeCamera+0x74 (the FreeCamera from the
camera session, CameraOwner+0xA4, vtable 0x142C6E8). Recovery JSON first;
restore writes the original value only while our value is still there.

r281 (live, cl04_1084 2:13/2:18): the FOV still jumped when the followed player
entered or left a vehicle, with the camera, its +0x74 and the view FOV unchanged.
The renderer ([0x156D4F8]) keeps a FOV scale at +0x117C (1.0 in a vehicle, 1.4182
on foot here); its setView 0x5D7000 applies it every frame (0x965930: view+0xC,
view+0x14 = fov * scale * aspect). The game sets it only through renderer vtable
slot 0x24 = 0x5BE970 (movss [ecx+0x117C]; ret 4; the only other writer is the
ctor 0x5DD274). The vtable is a writable heap copy, so while the free camera is on
that slot points at the setter's own `ret 4` (0x5BE97E) and the scale is pinned to
1.0 (the locked FOV is then the drawn FOV); restore puts both back.
"""
import json
import struct

from preview_config import APP_ROOT as ROOT

STATE = ROOT / 'runtime' / 'private_free_cam_fov_session.json'
CAMERA_SESSION = ROOT / 'runtime' / 'private_focus_session.json'

FREE_CAMERA_VTABLE = 0x142C6E8
OWNER_FREE_CAMERA = 0xA4
CAM_FOV_OVERRIDE = 0x74
CAMERA_MANAGER = 0x1570D80
MGR_ACTIVE = 0xAC
MGR_FALLBACK = 0xB0
FALLBACK_FOV = 0x30
DEFAULT_FOV = 55.0
FOV_RANGE = (20.0, 120.0)
RENDERER = 0x156D4F8           # r281: renderer object pointer
RENDER_FOV_SCALE = 0x117C      # renderer FOV scale, applied by setView 0x5D7000
VT_SET_FOV_SCALE = 0x24        # renderer vtable slot of the only setter
SET_FOV_SCALE = 0x5BE970       # movss [ecx+0x117C], [esp+4]; ret 4
SET_FOV_SCALE_RET = 0x5BE97E   # its `ret 4`: the slot target while pinned
PINNED_SCALE = 1.0


def f32(value):
    return struct.pack('<f', value)


def choose_fov(requested, fallback):
    """Explicit value wins; else the current soldier FOV if it looks unzoomed; else DEFAULT_FOV."""
    if requested is not None:
        if not FOV_RANGE[0] <= requested <= FOV_RANGE[1]:
            raise ValueError(f'FOV {requested} outside {FOV_RANGE}')
        return float(requested)
    if fallback is not None and 50.0 <= fallback <= 95.0:
        return float(fallback)
    return DEFAULT_FOV


def _read_f32(client, address):
    return struct.unpack('<f', client.read(address, 4))[0]


def fallback_fov(client):
    try:
        holder = client.u32(CAMERA_MANAGER + MGR_FALLBACK)
        return _read_f32(client, holder + FALLBACK_FOV) if holder else None
    except OSError:
        return None


def locate(client):
    """FreeCamera from the live camera session; raise unless it validates."""
    if not CAMERA_SESSION.exists():
        raise RuntimeError('No camera session; start Free Camera first')
    data = json.loads(CAMERA_SESSION.read_text(encoding='utf-8'))
    if data.get('pid') != client.pid:
        raise RuntimeError('Camera session belongs to another client process')
    owner, free = data.get('owner'), data.get('free_camera')
    if not owner or not free or client.u32(owner + OWNER_FREE_CAMERA) != free \
            or client.u32(free) != FREE_CAMERA_VTABLE:
        raise RuntimeError('FreeCamera failed validation; nothing written')
    return free


def scale_targets(client):
    """(vtable slot address, scale address) of the live renderer; raise unless the slot holds a known setter."""
    renderer = client.u32(RENDERER)
    if not renderer:
        raise RuntimeError('No renderer; nothing written')
    slot = client.u32(renderer) + VT_SET_FOV_SCALE
    if client.u32(slot) not in (SET_FOV_SCALE, SET_FOV_SCALE_RET):
        raise RuntimeError('Renderer FOV-scale slot is unexpected; nothing written')
    return slot, renderer + RENDER_FOV_SCALE


def _created(client):
    from replay.clean_screen import created
    return created(client)


def _with_client(client, write, fn):
    own = client is None
    if own:
        if write:
            from replay.clean_screen import WriteClient
            client = WriteClient()
        else:
            from replay.client import Client
            client = Client()
    try:
        return fn(client)
    finally:
        if own:
            client.close()


def _load(client):
    if not STATE.exists():
        return None
    data = json.loads(STATE.read_text(encoding='utf-8'))
    if data.get('pid') != client.pid or data.get('created') != _created(client):
        STATE.unlink()
        return None
    return data


def enable(client=None, fov=None):
    def run(client):
        free = locate(client)
        address = free + CAM_FOV_OVERRIDE
        current = client.read(address, 4)
        session = _load(client)
        if session is not None and session['address'] != address:
            raise RuntimeError('Previous FOV session targets another camera; disable first')
        value = choose_fov(fov, fallback_fov(client))
        written = f32(value)
        original = bytes.fromhex(session['original']) if session else current
        if not session:
            cur = struct.unpack('<f', current)[0]
            if not (cur == 0.0 or FOV_RANGE[0] <= cur <= FOV_RANGE[1]):
                raise RuntimeError(f'Unexpected FreeCamera+0x74 value {cur}; nothing written')
        slot, scale_at = scale_targets(client)
        old = (session or {}).get('scale')
        scale = old if old and old['slot'] == slot else dict(
            slot=slot, address=scale_at, original=client.read(scale_at, 4).hex())
        STATE.parent.mkdir(parents=True, exist_ok=True)
        STATE.write_text(json.dumps(dict(pid=client.pid, created=_created(client), address=address,
                                         original=original.hex(), written=written.hex(), scale=scale),
                                    indent=2), encoding='utf-8')  # recovery first
        client.write(address, written)
        client.write(slot, struct.pack('<I', SET_FOV_SCALE_RET))  # game can no longer change the scale
        client.write(scale_at, f32(PINNED_SCALE))
        return {'fov_locked': value, 'original': struct.unpack('<f', original)[0],
                'soldier_fov_now': fallback_fov(client), 'scale_pinned': PINNED_SCALE}
    return _with_client(client, True, run)


def disable(client=None):
    def run(client):
        session = _load(client)
        if session is None:
            return {'free_cam_fov': 'not installed'}
        address = session['address']
        try:
            current = client.read(address, 4)
        except OSError:
            STATE.unlink()
            return {'free_cam_fov': 'gone'}
        if current.hex() == session['written']:
            client.write(address, bytes.fromhex(session['original']))
            out = 'restored'
        else:
            out = 'changed by game (left)'
        scale = session.get('scale')
        if scale:
            try:
                if client.u32(scale['slot']) == SET_FOV_SCALE_RET:
                    client.write(scale['slot'], struct.pack('<I', SET_FOV_SCALE))
                    client.write(scale['address'], bytes.fromhex(scale['original']))
            except OSError:
                pass                                              # renderer gone with the game
        STATE.unlink()
        return {'free_cam_fov': out}
    return _with_client(client, True, run)


def status(client=None):
    def run(client):
        out = {'soldier_fov_now': fallback_fov(client)}
        try:
            active = client.u32(CAMERA_MANAGER + MGR_ACTIVE)
            out['active_is_free_camera'] = bool(active) and client.u32(active) == FREE_CAMERA_VTABLE
            if active:
                out['active_fov_override'] = _read_f32(client, active + CAM_FOV_OVERRIDE)
        except OSError:
            pass
        if STATE.exists():
            data = json.loads(STATE.read_text(encoding='utf-8'))
            if data.get('pid') == client.pid:
                out['locked'] = client.read(data['address'], 4).hex() == data['written']
                out['locked_fov'] = struct.unpack('<f', bytes.fromhex(data['written']))[0]
                scale = data.get('scale')
                if scale:
                    out['scale_pinned'] = client.u32(scale['slot']) == SET_FOV_SCALE_RET
                    out['render_fov_scale'] = _read_f32(client, scale['address'])
        return out
    return _with_client(client, False, run)


if __name__ == '__main__':
    import sys
    # Dev CLI, from the repo root: python -m replay.free_cam_fov status|enable [deg]|disable
    action = sys.argv[1] if len(sys.argv) > 1 else 'status'
    arg = float(sys.argv[2]) if len(sys.argv) > 2 else None
    table = {'status': status, 'disable': disable, 'enable': lambda: enable(fov=arg)}
    print(json.dumps(table[action](), indent=2))
