"""r286 offline tests: seek that keeps destruction (fast forward play, from 0 when needed).

Run from the repo root:  python -m unittest replay.test_destr_seek -v
"""
import unittest

from replay import destr_seek as ds


class FakeServer:
    """Timeline that advances speed * frames_per_poll frames per status call while playing."""

    def __init__(self, frame, per_poll=3, speed=1.0, drop_at=None, stall_at=None):
        self.frame, self.per_poll, self.speed = frame, per_poll, speed
        self.paused, self.calls, self.speeds = True, [], []
        self.drop_at, self.stall_at = drop_at, stall_at
        self.now = 0.0

    def state(self):
        conns = 0 if self.drop_at is not None and self.frame >= self.drop_at else 1
        return {'frame': self.frame, 'paused': self.paused, 'speed': self.speed, 'connections': conns}

    def operate(self, action, frame=0):
        self.calls.append((action, frame))
        if action == 'seek':
            self.frame, self.paused = frame, True
        elif action == 'play':
            self.paused = False
        elif action == 'pause':
            self.paused = True
        elif action == 'status' and not self.paused:
            if self.stall_at is None or self.frame < self.stall_at:
                self.frame += int(self.speed * self.per_poll)
        return self.state()

    def write_speed(self, x):
        self.speed = x
        self.speeds.append(x)
        return self.state()

    def sleep(self, s):
        self.now += s

    def clock(self):
        return self.now


def run(fake, target, current, consistent):
    return ds.seek_keeping_destruction(target, current, consistent, fake.operate, fake.write_speed,
                                       sleep=fake.sleep, clock=fake.clock)


class Plan(unittest.TestCase):
    def test_forward_from_current_when_consistent(self):
        self.assertEqual(ds.plan(9000, 3000, True), 3000)

    def test_back_or_inconsistent_replays_from_zero(self):
        self.assertEqual(ds.plan(1000, 3000, True), 0)
        self.assertEqual(ds.plan(9000, 3000, False), 0)


class Seek(unittest.TestCase):
    def test_back_seeks_zero_then_plays_and_lands_near_target(self):
        f = FakeServer(frame=12000)
        r = run(f, 11730, 12000, True)
        self.assertEqual(f.calls[1], ('seek', 0))
        self.assertEqual([c for c in f.calls if c[0] == 'seek'], [('seek', 0)])  # never a corrective seek
        self.assertTrue(r['paused'])
        self.assertEqual(r['replayed_from'], 0)
        self.assertLessEqual(abs(r['frame'] - 11730), 30)
        self.assertEqual(f.speeds, [50.0, 16.0, 4.0, 1.0])  # stages, then the old speed back

    def test_forward_consistent_does_not_seek(self):
        f = FakeServer(frame=3000)
        r = run(f, 6000, 3000, True)
        self.assertNotIn('seek', [c[0] for c in f.calls])
        self.assertEqual(r['replayed_from'], 3000)
        self.assertLessEqual(abs(r['frame'] - 6000), 30)

    def test_short_forward_step_uses_only_slow_stage(self):
        f = FakeServer(frame=3000)
        run(f, 3090, 3000, True)  # +3 s arrow
        self.assertEqual(f.speeds, [4.0, 1.0])

    def test_old_speed_restored(self):
        f = FakeServer(frame=0, speed=2.0)
        run(f, 3000, 0, True)
        self.assertEqual(f.speeds[-1], 2.0)

    def test_disconnect_raises_and_restores(self):
        f = FakeServer(frame=0, drop_at=1000)
        with self.assertRaises(RuntimeError):
            run(f, 9000, 0, True)
        self.assertEqual(f.speeds[-1], 1.0)
        self.assertTrue(f.paused)

    def test_stall_raises(self):
        f = FakeServer(frame=0, stall_at=500)
        with self.assertRaises(RuntimeError):
            run(f, 9000, 0, True)
        self.assertTrue(f.paused)


if __name__ == '__main__':
    unittest.main()
