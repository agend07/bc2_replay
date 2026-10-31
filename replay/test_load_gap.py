"""Offline tests for replay.load_gap (skip the empty load gap after a join; r242 option C).
Run from the repo root:  python -m pytest replay/test_load_gap.py -q
"""
import io
import struct
import tempfile
import unittest
from pathlib import Path

from replay import load_gap as lg

E = lg.EMPTY_DELTA_BITS
KEYS = list(range(0, 30000, 150))


def our_demo(tail=19, gap=1988, content=50):
    """cl04_1088 shape: join-batch tail deltas 1..19, empty 20..2007, content from 2008."""
    return [4000] * tail + [E] * gap + [4500] * content


def fbgd_bytes(delta_bits):
    out = io.BytesIO()
    out.write(b'EA Digital Illusions CE AB\0ROMEPC133769\0')
    out.write(struct.pack('<BII', 1, 4, 1) + b'\0' * 4)  # key0
    for i, bits in enumerate(delta_bits, start=1):
        size = (bits + 7) // 8
        out.write(struct.pack('<BII', 2, size, bits) + b'\0' * size)
        if i % 150 == 0:
            out.write(struct.pack('<BII', 1, 4, 1) + b'\0' * 4)
    out.write(b'\0')
    return out.getvalue()


class FirstContent(unittest.TestCase):
    def test_our_demo(self):
        self.assertEqual(lg.first_content(our_demo()), 2008)  # 66.9 s, as r242 measured on 1088
        self.assertEqual(lg.first_content([E] * 2119 + [300]), 2120)  # cl04_1019: empty from delta 1

    def test_no_gap_or_short_gap(self):
        self.assertIsNone(lg.first_content([232, 344] * 2000))  # server demo: ghost 51 updates
        self.assertIsNone(lg.first_content([4000] * 19 + [E] * 200 + [4500] * 100))  # under 10 s
        self.assertIsNone(lg.first_content([4000] * 200 + [E] * 2000 + [4500]))  # starts too late
        self.assertIsNone(lg.first_content([E] * 5000))  # never ends
        self.assertIsNone(lg.first_content([]))

    def test_scan_file_and_cache(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'x.fbgd'
            path.write_bytes(fbgd_bytes(our_demo()))
            self.assertEqual(lg.scan_fbgd(path), 2008)
            self.assertEqual(lg.scan_fbgd(path), 2008)
            bad = Path(tmp) / 'bad.fbgd'
            bad.write_bytes(b'no prolog here')
            self.assertIsNone(lg.content_frame(None, bad))
            self.assertIsNone(lg.content_frame(None, Path(tmp) / 'missing.fbgd'))
            # players.json wins when it has the value
            self.assertEqual(lg.content_frame({'game_start_frame': 2006}, path), 2006)
            self.assertEqual(lg.content_frame({'game_start_frame': None}, path), 2008)


class Target(unittest.TestCase):
    def test_lead_and_key_snap(self):
        self.assertEqual(lg.seek_target(2008, KEYS), 1800)  # 2008 - 90 = 1918 -> key 1800 (1:00)
        self.assertEqual(lg.seek_target(2120, KEYS), 1950)
        self.assertEqual(lg.seek_target(2008), 1918)

    def test_too_early_or_unknown(self):
        self.assertIsNone(lg.seek_target(None, KEYS))
        self.assertIsNone(lg.seek_target(390, KEYS))  # 300 -> 10 s: not > 10 s
        self.assertIsNone(lg.seek_target(500, [0]))   # only key 0 at or before


class Clock:
    def __init__(self):
        self.t = 100.0

    def __call__(self):
        return self.t


class Decision(unittest.TestCase):
    def setUp(self):
        self.clock = Clock()
        self.g = lg.GapSeek(clock=self.clock)

    def test_retry_every_2s_until_accepted(self):
        self.assertTrue(self.g.arm(1800))
        self.assertEqual(self.g.action(0), 'seek')
        self.clock.t += 1.0
        self.assertEqual(self.g.action(30), 'wait')
        self.clock.t += 1.1
        self.assertEqual(self.g.action(60), 'seek')
        self.g.accepted()
        self.assertEqual(self.g.action(1801), 'idle')

    def test_give_up_after_60s(self):
        self.g.arm(1800)
        self.clock.t += 61
        self.assertEqual(self.g.action(0), 'give_up')
        self.assertEqual(self.g.action(0), 'idle')

    def test_user_seek_or_already_past(self):
        self.g.arm(1800)
        self.g.cancel()  # user seeked
        self.assertEqual(self.g.action(0), 'idle')
        self.g.arm(1800)
        self.assertEqual(self.g.action(1800), 'idle')  # playback already there
        self.assertEqual(self.g.action(0), 'idle')

    def test_nothing_to_arm(self):
        self.assertFalse(self.g.arm(None))
        self.assertEqual(self.g.action(0), 'idle')


if __name__ == '__main__':
    unittest.main()
