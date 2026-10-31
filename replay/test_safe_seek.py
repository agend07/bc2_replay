"""r224 offline tests for replay.safe_seek (fake key table, fake clock).

Run from the repo root:  python -m unittest replay.test_safe_seek -v
"""
import struct
import unittest

from replay import safe_seek as ss

KEYS = list(range(0, 18750 + 1, 150))   # cl04_1094 shape: 126 keys, last 18750
LIMIT = 18750 - 150                      # last_frame - END_MARGIN_FRAMES
USABLE = ss.usable_keys(KEYS, LIMIT)


def fbgk_bytes(frames, interval=150):
    out = struct.pack('<I', interval)
    for i, f in enumerate(frames):
        out += struct.pack('<IfI', f, 0.0, 40 + i * 1000)
    return out


class FakeClock:
    def __init__(self):
        self.t = 100.0

    def __call__(self):
        return self.t


class ParseTests(unittest.TestCase):
    def test_parse(self):
        self.assertEqual(ss.parse_fbgk(fbgk_bytes([0, 150, 300])), [0, 150, 300])

    def test_bad_layout_and_order(self):
        with self.assertRaises(ValueError):
            ss.parse_fbgk(fbgk_bytes([0, 150])[:-1])
        with self.assertRaises(ValueError):
            ss.parse_fbgk(fbgk_bytes([0, 300, 150]))

    def test_usable_excludes_last_key(self):
        self.assertEqual(USABLE[-1], 18600)
        self.assertNotIn(18750, USABLE)


class SnapTests(unittest.TestCase):
    def test_typed_snaps_to_key_at_or_before(self):
        self.assertEqual(ss.snap_floor(USABLE, 7416), 7350)   # 04:07.2 -> 04:05
        self.assertEqual(ss.snap_floor(USABLE, 7500), 7500)   # exact key stays
        self.assertEqual(ss.snap_floor(USABLE, 149), 0)
        self.assertEqual(ss.snap_floor(USABLE, 18700), 18600)

    def test_at_key_tolerance(self):
        self.assertEqual(ss.at_key(USABLE, 7501), 7500)  # server lands at target+1
        self.assertEqual(ss.at_key(USABLE, 7503), 7500)
        self.assertEqual(ss.at_key(USABLE, 7504), 7504)


class StepTests(unittest.TestCase):
    def test_from_key_one_key_each_way(self):
        for cur in (7500, 7501, 7503):
            for d in (30, 150):
                self.assertEqual(ss.step_key(USABLE, cur, d), 7650, (cur, d))
                self.assertEqual(ss.step_key(USABLE, cur, -d), 7350, (cur, d))

    def test_mid_interval(self):
        self.assertEqual(ss.step_key(USABLE, 7416, 30), 7500)
        self.assertEqual(ss.step_key(USABLE, 7416, 150), 7650)
        self.assertEqual(ss.step_key(USABLE, 7416, -30), 7350)
        self.assertEqual(ss.step_key(USABLE, 7416, -150), 7200)

    def test_never_zero_movement(self):
        for cur in range(0, LIMIT + 1, 7):
            for d in (-150, -30, 30, 150):
                t = ss.step_key(USABLE, cur, d)
                if t is None:
                    continue
                self.assertIn(t, USABLE)
                ref = ss.at_key(USABLE, cur)
                self.assertTrue(t > ref if d > 0 else t < ref, (cur, d, t))
                self.assertGreaterEqual(abs(t - ref), min(abs(d), 1), (cur, d, t))

    def test_edges_refuse(self):
        self.assertIsNone(ss.step_key(USABLE, 0, -30))
        self.assertIsNone(ss.step_key(USABLE, 1, -150))
        self.assertIsNone(ss.step_key(USABLE, 18601, 30))
        self.assertEqual(ss.step_key(USABLE, 18590, 30), 18600)

    def test_irregular_keys(self):
        keys = [0, 151, 301, 452]
        self.assertEqual(ss.step_key(keys, 152, 30), 301)
        self.assertEqual(ss.step_key(keys, 152, -30), 0)
        self.assertEqual(ss.step_key(keys, 0, 1000), 452)


class ResolveTests(unittest.TestCase):
    def test_coalesced_steps(self):
        self.assertEqual(ss.resolve(USABLE, 7501, {'steps': [30, 30, 30]}), 7950)
        self.assertEqual(ss.resolve(USABLE, 7501, {'steps': [150, -30]}), 7500)

    def test_base_then_steps(self):
        self.assertEqual(ss.resolve(USABLE, 100, {'base': 3600, 'steps': [-150]}), 3450)

    def test_nothing_to_do(self):
        self.assertIsNone(ss.resolve(USABLE, 0, {'steps': [-30]}))
        self.assertEqual(ss.resolve(USABLE, 0, {'base': 0}), 0)


class GateTests(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock()
        self.gate = ss.SeekGate(clock=self.clock)

    def test_first_request_goes_immediately(self):
        self.gate.queue({'steps': [30]})
        self.assertEqual(self.gate.take(), {'steps': [30]})
        self.assertTrue(self.gate.in_flight)

    def test_busy_while_in_flight_and_cooldown(self):
        self.gate.queue({'steps': [30]})
        self.gate.take()
        self.gate.queue({'steps': [30]})
        self.assertIsNone(self.gate.take())          # seek still running
        self.clock.t += 5.0
        self.assertIsNone(self.gate.take())          # still running, time alone is not enough
        self.gate.done()
        self.clock.t += 1.9
        self.assertFalse(self.gate.ready())
        self.assertIsNone(self.gate.take())
        self.clock.t += 0.1
        self.assertTrue(self.gate.ready())
        self.assertEqual(self.gate.take(), {'steps': [30]})

    def test_rapid_presses_coalesce_into_one_seek(self):
        self.gate.start()
        for _ in range(3):
            self.gate.queue({'steps': [30]})
        self.gate.done()
        self.clock.t += 2.0
        req = self.gate.take()
        self.assertEqual(req['steps'], [30, 30, 30])
        self.assertIsNone(self.gate.take())
        self.assertEqual(ss.resolve(USABLE, 7501, req), 7950)

    def test_typed_seek_replaces_pending_steps(self):
        self.gate.start()
        self.gate.queue({'steps': [150, 150]})
        self.gate.queue({'base': 3600, 'asked': 3700})
        self.gate.queue({'steps': [-30]})
        self.gate.done()
        self.clock.t += 2.0
        req = self.gate.take()
        self.assertEqual(req, {'base': 3600, 'asked': 3700, 'steps': [-30]})
        self.assertEqual(ss.resolve(USABLE, 9000, req), 3450)


if __name__ == '__main__':
    unittest.main()
