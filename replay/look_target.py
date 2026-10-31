"""Free camera: name of the player closest to the screen centre, for the VCR label (read-only).

Camera: FreeCamera.transform (+0x10, 64 bytes, reverser 'confirmed'): rows right, up, back,
position (position at +0x40, the same value camera_native.status reports). The view direction is
MINUS the third row: native look-at 0x7EE930 starts with eye - target (bytes 0x7EE93C..0x7EE973)
and camera.focus_matrix writes 'back' there, which Focus/Follow use live.
Players: replay.players roster + validated reciprocal soldier links + soldier transform position.
Only ReadProcessMemory; never writes. Pick: smallest angle between the view direction and
(player - camera) inside a cone (MAX_ANGLE_DEG) and range (panel 'max m', DEFAULT_DISTANCE_M), else nothing.
"""
import math
import struct

FREE_CAMERA_VTABLE = 0x142C6E8
TRANSFORM = 0x10
GLOBAL_ACTIVE_CAMERA = 0x1570D80 + 0xAC  # CameraStackManager.globalActiveCamera (as camera.status)
MAX_ANGLE_DEG = 10.0
DEFAULT_DISTANCE_M = 50.0  # panel Spinbox default; the user picks DISTANCE_RANGE_M
DISTANCE_RANGE_M = (10, 300)
MIN_DISTANCE_M = 1.0   # skip a soldier the camera sits inside
AIM_HEIGHT_M = 1.0     # soldier transform is at the feet; aim at the torso


# ---------------------------------------------------------------- pure helpers (tested)

def camera_pose(matrix):
    """64-byte row-major camera transform -> (position, unit view direction), or None if invalid."""
    if len(matrix) != 64:
        return None
    rows = struct.unpack('<16f', matrix)
    if not all(math.isfinite(x) for x in rows):
        return None
    back = rows[8:11]
    length = math.sqrt(sum(x * x for x in back))
    if not 0.9 < length < 1.1:
        return None
    return list(rows[12:15]), [-x / length for x in back]


def pick_looked_at(cam_pos, forward, players, max_angle_deg=MAX_ANGLE_DEG,
                   max_distance=DEFAULT_DISTANCE_M, min_distance=MIN_DISTANCE_M, aim_height=AIM_HEIGHT_M):
    """players: iterable of (name, (x, y-up, z)). Returns the name with the smallest angle to the
    view direction inside the cone and distance range, or None."""
    cos_limit = math.cos(math.radians(max_angle_deg))
    best = None
    for name, pos in players:
        d = [pos[0] - cam_pos[0], pos[1] + aim_height - cam_pos[1], pos[2] - cam_pos[2]]
        dist = math.sqrt(sum(x * x for x in d))
        if not math.isfinite(dist) or not min_distance <= dist <= max_distance:
            continue
        cos_a = sum(a * b for a, b in zip(d, forward)) / dist
        if cos_a >= cos_limit and (best is None or cos_a > best[0]):
            best = (cos_a, name)
    return best[1] if best else None


def clamp_distance(value, default=DEFAULT_DISTANCE_M):
    """Panel Spinbox text -> metres within DISTANCE_RANGE_M (default if not a number)."""
    try:
        metres = float(value)
    except (TypeError, ValueError):
        return default
    if not math.isfinite(metres):
        return default
    return max(DISTANCE_RANGE_M[0], min(DISTANCE_RANGE_M[1], metres))


# ---------------------------------------------------------------- client memory (read-only)

class LookTarget:
    """Keeps one read-only client handle (Client() hashes the client exe; the player manager is
    cached on it). Call read() from a worker thread only, one call at a time."""

    def __init__(self, client_factory=None):
        self._factory = client_factory
        self._client = None

    def close(self):
        if self._client is not None:
            try:
                self._client.close()
            except Exception:
                pass
            self._client = None

    def _get_client(self, pid):
        if self._client is not None and self._client.pid != pid:
            self.close()
        if self._client is None:
            if self._factory is None:
                from replay.client import Client
                self._factory = Client
            self._client = self._factory()
            if self._client.pid != pid:
                self.close()
                raise RuntimeError('Camera session belongs to another client')
        return self._client

    def alive_players(self, pid):
        """Roster names (client order) and the set of them with a validated soldier link, for the
        in-game '<' / '>' player cycling. Reuses the cached handle (and its player manager)."""
        from replay.players import roster
        try:
            players = roster(self._get_client(pid))['players']
        except OSError:
            self.close()
            raise
        from replay.players import live_link
        return ([p['name'] for p in players], {p['name'] for p in players if live_link(p)})  # soldier or vehicle (r286)

    def read(self, session, max_distance=DEFAULT_DISTANCE_M):
        """session: the camera session dict (runtime/private_focus_session.json). Returns a name or None."""
        from replay.players import roster, live_link, link_position
        try:
            client = self._get_client(session['pid'])
            free = session['free_camera']
            if client.u32(free) != FREE_CAMERA_VTABLE or client.u32(GLOBAL_ACTIVE_CAMERA) != free:
                return None  # free camera not the active view (killcam / deploy transition)
            pose = camera_pose(client.read(free + TRANSFORM, 64))
            if pose is None:
                return None
            found = []
            for p in roster(client)['players']:
                link = live_link(p)  # r286: a player in a vehicle is named by the vehicle
                if not link:
                    continue
                try:
                    found.append((p['name'], link_position(client, link)))
                except (OSError, RuntimeError):
                    continue
            return pick_looked_at(pose[0], pose[1], found, max_distance=max_distance)
        except OSError:
            self.close()  # process gone or handle invalid: reopen next time
            raise
