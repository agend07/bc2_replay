"""Offline tests for replay.look_target (synthetic camera/players; no client)."""
import math
import struct
import unittest
from replay import look_target as lt


def matrix(eye, forward):
    """Camera transform like camera.focus_matrix: rows right, up, back (= -forward), eye."""
    n = math.sqrt(sum(x * x for x in forward))
    back = [-x / n for x in forward]
    up0 = [0.0, 1.0, 0.0]
    right = [up0[1] * back[2] - up0[2] * back[1], up0[2] * back[0] - up0[0] * back[2], up0[0] * back[1] - up0[1] * back[0]]
    rn = math.sqrt(sum(x * x for x in right))
    right = [x / rn for x in right]
    up = [back[1] * right[2] - back[2] * right[1], back[2] * right[0] - back[0] * right[2], back[0] * right[1] - back[1] * right[0]]
    return struct.pack('<16f', *(right + [0] + up + [0] + back + [0] + list(eye) + [1]))


class Pick(unittest.TestCase):
    cam = [0.0, 1.0, 0.0]   # eye at torso height of a soldier standing at y = 0
    fwd = [0.0, 0.0, 1.0]

    def pick(self, players, **kw):
        return lt.pick_looked_at(self.cam, self.fwd, players, **kw)

    def test_straight_ahead(self):
        self.assertEqual(self.pick([('Davorich', (0, 0, 50))]), 'Davorich')

    def test_smallest_angle_wins_not_nearest(self):
        players = [('near_off', (3, 0, 30)), ('far_center', (0.5, 0, 200))]  # ~5.7 deg vs ~0.14 deg
        self.assertEqual(self.pick(players, max_distance=300), 'far_center')

    def test_cone(self):
        off = math.tan(math.radians(12)) * 100
        self.assertIsNone(self.pick([('x', (off, 0, 100))], max_distance=300))
        off = math.tan(math.radians(8)) * 100
        self.assertEqual(self.pick([('x', (off, 0, 100))], max_distance=300), 'x')

    def test_distance_limits(self):
        self.assertIsNone(self.pick([('far', (0, 0, 301))], max_distance=300))
        self.assertEqual(self.pick([('ok', (0, 0, 299))], max_distance=300), 'ok')
        self.assertIsNone(self.pick([('inside', (0, 0, 0.5))]))

    def test_default_and_panel_distance(self):
        self.assertEqual(lt.DEFAULT_DISTANCE_M, 50)
        self.assertEqual(self.pick([('ok', (0, 0, 49))]), 'ok')
        self.assertIsNone(self.pick([('far', (0, 0, 51))]))
        self.assertIsNone(self.pick([('x', (0, 0, 25))], max_distance=20))
        self.assertEqual(self.pick([('x', (0, 0, 25))], max_distance=30), 'x')

    def test_clamp_distance(self):
        self.assertEqual(lt.clamp_distance('120'), 120)
        self.assertEqual(lt.clamp_distance('5'), 10)
        self.assertEqual(lt.clamp_distance(1000), 300)
        for bad in ('', 'abc', None, 'nan', 'inf'):
            self.assertEqual(lt.clamp_distance(bad), lt.DEFAULT_DISTANCE_M, bad)

    def test_behind_and_empty(self):
        self.assertIsNone(self.pick([('behind', (0, 0, -50))]))
        self.assertIsNone(self.pick([]))

    def test_aim_height(self):
        # camera 1 m above the feet, looking level: target aim point (feet + 1 m) is dead ahead
        self.assertEqual(self.pick([('a', (0, 0, 5))], max_angle_deg=1), 'a')
        self.assertIsNone(self.pick([('a', (0, 0, 5))], max_angle_deg=1, aim_height=0))


class Pose(unittest.TestCase):
    def test_forward_is_minus_back_row(self):
        pos, fwd = lt.camera_pose(matrix((10, 70, -5), (1, 0, 0)))
        self.assertEqual([round(x, 4) for x in pos], [10, 70, -5])
        self.assertEqual([round(x, 4) for x in fwd], [1, 0, 0])

    def test_focus_matrix_looks_at_its_target(self):
        from replay.camera import focus_matrix
        target = [100.0, 70.0, 200.0]
        pos, fwd = lt.camera_pose(focus_matrix(target))
        self.assertEqual(lt.pick_looked_at(pos, fwd, [('t', (target[0], target[1] + 1.5 - lt.AIM_HEIGHT_M, target[2]))],
                                           max_angle_deg=0.5), 't')

    def test_invalid(self):
        self.assertIsNone(lt.camera_pose(b'\0' * 64))
        self.assertIsNone(lt.camera_pose(struct.pack('<16f', *([float('nan')] * 16))))
        self.assertIsNone(lt.camera_pose(b'\0' * 10))


class FakeClient:
    """Free camera at FREE, one roster player 'Davorich' whose soldier stands 40 m ahead."""
    FREE = 0x1000

    def __init__(self, pid=7):
        self.pid = pid
        self.closed = False
        self.mem = {self.FREE: lt.FREE_CAMERA_VTABLE, lt.GLOBAL_ACTIVE_CAMERA: self.FREE}

    def u32(self, a):
        return self.mem.get(a, 0)

    def read(self, a, n):
        if a == self.FREE + lt.TRANSFORM and n == 64:
            return matrix((0, 1, 0), (0, 0, 1))
        raise OSError('unmapped')

    def close(self):
        self.closed = True


class Reader(unittest.TestCase):
    def setUp(self):
        import replay.players as players
        self.players = players
        self.saved = players.roster, players.transform
        players.roster = lambda c: {'players': [
            {'name': 'Davorich', 'soldier': {'status': 'validated_reciprocal_reference', 'address': '0x2000'}},
            {'name': 'dead', 'soldier': {'status': 'unavailable'}}]}
        players.transform = lambda c, a: {'candidate_position': [0.0, 0.0, 40.0]}

    def tearDown(self):
        self.players.roster, self.players.transform = self.saved

    def test_read(self):
        made = []
        r = lt.LookTarget(client_factory=lambda: made.append(FakeClient()) or made[-1])
        session = {'pid': 7, 'free_camera': FakeClient.FREE}
        self.assertEqual(r.read(session), 'Davorich')
        self.assertEqual(r.read(session), 'Davorich')
        self.assertEqual(len(made), 1)  # one client handle reused across reads
        self.assertIsNone(r.read(session, max_distance=30))  # Davorich is 40 m ahead
        self.assertEqual(r.read(session, max_distance=40), 'Davorich')

    def test_alive_players(self):
        made = []
        r = lt.LookTarget(client_factory=lambda: made.append(FakeClient()) or made[-1])
        self.assertEqual(r.alive_players(7), (['Davorich', 'dead'], {'Davorich'}))
        r.alive_players(7)
        self.assertEqual(len(made), 1)
        with self.assertRaises(RuntimeError):
            lt.LookTarget(client_factory=lambda: FakeClient(pid=8)).alive_players(7)

    def test_wrong_vtable_and_pid(self):
        r = lt.LookTarget(client_factory=lambda: FakeClient())
        self.assertIsNone(r.read({'pid': 7, 'free_camera': 0x3000}))
        fc = FakeClient(); fc.mem[lt.GLOBAL_ACTIVE_CAMERA] = 0x4000  # another camera is active
        self.assertIsNone(lt.LookTarget(client_factory=lambda: fc).read({'pid': 7, 'free_camera': FakeClient.FREE}))
        with self.assertRaises(RuntimeError):
            lt.LookTarget(client_factory=lambda: FakeClient(pid=8)).read({'pid': 7, 'free_camera': FakeClient.FREE})


if __name__ == '__main__':
    unittest.main()
