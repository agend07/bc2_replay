"""Exact timeline-only camera authorization. Read-only; never issues commands."""
import struct
from replay.server import Server, processes, EXE_NAME


def validate_state(state):
    if (not state['ready'] or not state['active'] or not state['initialized']
            or state['connections'] != 1 or state['sequence'] != state['ack']):
        raise RuntimeError('Timeline camera requires ready active playback, one viewer, and no pending command')


def read_timeline_ready(server):
    state = server.status()
    validate_state(state)
    def u32(a):return struct.unpack('<I',server.read(a,4))[0]
    demo, peer = u32(0x19c4cc0+0x20), u32(0x19c4cc0+0x38)
    if not demo or not peer:raise RuntimeError('Timeline playback objects missing')
    pb = u32(peer+0x2d4)
    if not pb:raise RuntimeError('Timeline playback missing')
    begin,end,capacity = (u32(peer+o) for o in (0x208,0x20c,0x210))
    if not begin <= end <= capacity or end-begin != 4 or capacity-begin > 4096:
        raise RuntimeError('Expected exactly one native viewer')
    if (u32(demo+0x1c) != 1 or u32(demo+0x24) != 0 or
            u32(demo+0x18)&0x220 or not u32(pb+4) or not u32(pb+0xd8)
            or not server.read(peer+0x305,1)[0]):
        raise RuntimeError('Expected initialized playback-only input with no pending seek')
    if (demo,peer,pb) != (u32(0x19c4cc0+0x20),u32(0x19c4cc0+0x38),u32(peer+0x2d4)):
        raise RuntimeError('Timeline objects changed during guard')
    if (begin,end,capacity) != tuple(u32(peer+o) for o in (0x208,0x20c,0x210)):
        raise RuntimeError('Viewer collection changed during guard')
    final=server.status();validate_state(final)
    if final['sequence'] != state['sequence']:raise RuntimeError('Timeline command changed during guard')
    return final


def check():
    # Do not authorize a camera against a timeline while another dedicated
    # server is also present. Server verifies exact workspace path/build/magic.
    entries = processes()
    if len(entries) != 1 or entries[0][1] != EXE_NAME:
        raise RuntimeError('Expected only the exact timeline server, no competing dedicated server')
    server = Server(write=False)
    try:
        final = read_timeline_ready(server)
        return {'replay_kind':'timeline','pid':server.pid,'state':final,
                'exact_path_build_mailbox_verified':True}
    finally:server.close()


class TimelineFollowGuard:
    """Persistent READ-ONLY exact-build timeline guard for fast camera-follow checks."""
    def __init__(self):
        self.server = None
        entries = processes()
        if len(entries) != 1 or entries[0][1] != EXE_NAME:
            raise RuntimeError('Expected only the exact timeline server, no competing dedicated server')
        self.server = Server(write=False)
        try:
            self.pid = self.server.pid
            self.ready()
        except Exception:
            self.close()
            raise

    def ready(self):
        state = read_timeline_ready(self.server)
        return {'paused': bool(state['paused']), 'frame': state['frame']}

    def close(self):
        if self.server:
            self.server.close()
            self.server = None
