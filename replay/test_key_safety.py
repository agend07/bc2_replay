"""Offline tests for replay.key_safety (r283: no seek onto a key whose viewer view chain names a missing ghost).
Run from the repo root:  python -m pytest replay/test_key_safety.py -q
"""
import io
import struct
import tempfile
import unittest
from pathlib import Path

from replay import key_safety as ks

VIEWER = 163


class BitWriter:
    def __init__(self):
        self.bits = []

    def put(self, value, width):
        self.bits += [(value >> (width - 1 - i)) & 1 for i in range(width)]

    def payload(self):
        n = len(self.bits)
        padded = self.bits + [0] * (-n % 8)
        data = bytes(int(''.join(map(str, padded[i:i + 8])), 2) for i in range(0, len(padded), 8))
        return data, n


def chunk(w, index, bits, value, refs=()):
    w.put(index, 6)
    w.put(bits, 16)
    w.put(value, bits)
    w.put(len(refs), 6)
    for r in refs:
        w.put(r, 16)
        w.put(0, 1)


def comp_create(player):
    return int(ks.COMP_CLASS, 2) << 13 | player


def key_payload(ghosts):
    """ghosts: [(id, (create bits, value, refs), {chunk index: [refs]})], one section per ghost."""
    w = BitWriter()
    w.put(1, 1)
    w.put(len(ghosts) - 1, 16)
    for gid, (cbits, cval, crefs), chunks in ghosts:
        w.put(gid, 16)
        w.put(1, 16)
        chunk(w, 32, cbits, cval, crefs)
        idx = sorted(set(range(13)) | set(chunks))
        w.put(len(idx) << 16, 32)
        w.put((1 << len(idx)) - 1, len(idx))
        for i in idx:
            refs = chunks.get(i, [])
            chunk(w, i, 13 if refs else 1, refs[0] if refs else 0, refs)
    return w.payload()


def scene(comp_p12=159, with_159=True, other_dangling=False, player=VIEWER):
    ghosts = [
        (player, (8, 4, []), {}),
        (134, (20, comp_create(player), [player]), {0: [185], 12: [comp_p12]}),
        (185, (36, 0x6C5480000, []), {3: [186], 5: [170]}),
        (186, (12, 0x848, []), {}),
        (170, (19, 1, []), {}),
    ]
    if with_159:
        ghosts.append((159, (12, 0x0E0, []), {}))
    if other_dangling:
        ghosts.append((189, (8, 0x12, []), {5: [999]}))  # death object p5 -> deleted soldier (native form)
    return ghosts


def fbgd_bytes(keys_by_frame, last_frame):
    out = io.BytesIO()
    out.write(b'EA Digital Illusions CE AB\0ROMEPC133769\0')
    for f in range(last_frame + 1):
        if f in keys_by_frame:
            data, n = key_payload(keys_by_frame[f])
            out.write(struct.pack('<BII', 1, len(data), n) + data)
        if f < last_frame:
            out.write(struct.pack('<BII', 2, 8, 64) + b'\0' * 8)
    out.write(b'\0')
    return out.getvalue()


KEYS = [0, 150, 300, 450, 600]


class ParseTest(unittest.TestCase):
    def test_parse_key_roundtrip(self):
        data, n = key_payload(scene())
        g = ks.parse_key(data, n)
        self.assertEqual(sorted(g), [134, 159, 163, 170, 185, 186])
        self.assertEqual(g[134][1][12], [159])
        self.assertTrue(ks.is_component_of(g[134][0], {VIEWER}))
        self.assertFalse(ks.is_component_of(g[134][0], {7}))

    def test_key_frames_count_deltas(self):
        data = fbgd_bytes({0: scene(), 150: scene(), 300: scene()}, 320)
        self.assertEqual([f for f, _p, _n in ks.iter_keys(data)], [0, 150, 300])


class UnsafeTest(unittest.TestCase):
    def test_safe_key(self):
        data, n = key_payload(scene())
        self.assertEqual(ks.dangling(ks.parse_key(data, n), {VIEWER}), [])

    def test_dangling_component_p12(self):
        data, n = key_payload(scene(with_159=False))  # cl04_1084 key 4500
        self.assertEqual(ks.dangling(ks.parse_key(data, n), {VIEWER}), ['comp134.p12->159'])

    def test_dangling_camera_p3(self):
        g = scene()
        g[3] = (999, (12, 0x848, []), {})  # soldier 186 gone, camera p3 still names it
        data, n = key_payload(g)
        self.assertEqual(ks.dangling(ks.parse_key(data, n), {VIEWER}), ['cam185.p3->186'])

    def test_other_dangling_stays_safe_unless_all_refs(self):
        data, n = key_payload(scene(other_dangling=True))
        g = ks.parse_key(data, n)
        self.assertEqual(ks.dangling(g, {VIEWER}), [])
        self.assertEqual(ks.dangling(g, {VIEWER}, all_refs=True), ['g189.p5->999'])

    def test_other_players_component_ignored(self):
        data, n = key_payload(scene(with_159=False, player=7))
        self.assertEqual(ks.dangling(ks.parse_key(data, n), {VIEWER}), [])

    def test_unsafe_keys_and_no_viewer(self):
        data = fbgd_bytes({0: scene(), 150: scene(), 300: scene(with_159=False), 450: scene()}, 500)
        self.assertEqual(ks.unsafe_keys(data, {VIEWER}), {300: ['comp134.p12->159']})
        self.assertEqual(ks.unsafe_keys(data, set()), {})

    def test_cached_file(self):
        data = fbgd_bytes({0: scene(), 150: scene(with_159=False)}, 200)
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'x.fbgd'
            p.write_bytes(data)
            self.assertEqual(ks.unsafe_keys_cached(p, {VIEWER}), {150: ['comp134.p12->159']})
            self.assertEqual(ks.unsafe_keys_cached(p, {VIEWER}), {150: ['comp134.p12->159']})


class ViewerIdsTest(unittest.TestCase):
    def test_viewer(self):
        self.assertEqual(ks.viewer_ids({'viewer': {'demo_id': 163}, 'povid': None}), {163})
        self.assertEqual(ks.viewer_ids({'viewer': {'demo_id': 'x'}}), set())
        self.assertEqual(ks.viewer_ids(None), set())
        self.assertEqual(ks.viewer_ids({'players': []}), set())


class RedirectTest(unittest.TestCase):
    def test_no_unsafe(self):
        self.assertEqual(ks.redirect(KEYS, {}, 310), (310, None))

    def test_safe_floor_passes(self):
        self.assertEqual(ks.redirect(KEYS, {300: ['x']}, 299), (299, None))
        self.assertEqual(ks.redirect(KEYS, {300: ['x']}, 450), (450, None))

    def test_exact_unsafe_key_moves_before(self):  # cl04_1084: 2:30 (4500) -> 2:25 (4350)
        self.assertEqual(ks.redirect(KEYS, {300: ['x']}, 300), (150, 300))
        self.assertEqual(ks.redirect(KEYS, {300: ['x']}, 449), (150, 300))

    def test_skips_unsafe_run(self):  # cl04_1088: three unsafe keys in a row
        self.assertEqual(ks.redirect(KEYS, {150: ['x'], 300: ['x']}, 310), (0, 300))

    def test_moves_after_when_nothing_before(self):
        self.assertEqual(ks.redirect(KEYS, {0: ['x']}, 10), (150, 0))

    def test_all_unsafe_unchanged(self):
        self.assertEqual(ks.redirect(KEYS, {k: ['x'] for k in KEYS}, 310), (310, None))


class TimelineSeekRedirectTest(unittest.TestCase):
    """timeline.seek_redirect: players.json viewer + .fbgd keys of the active demo."""

    def run_redirect(self, frame, players_json):
        import json
        from unittest import mock
        from replay import timeline
        data = fbgd_bytes({0: scene(), 150: scene(), 300: scene(with_159=False), 450: scene()}, 500)
        with tempfile.TemporaryDirectory() as d:
            demos = Path(d) / 'demos'
            demos.mkdir()
            (demos / 'x.fbgd').write_bytes(data)
            if players_json is not None:
                (demos / 'x.players.json').write_text(json.dumps(players_json), encoding='utf-8')
            with mock.patch.object(timeline, 'recording', return_value=('demos/x', 'Levels/MP_005', 500)),                     mock.patch.object(timeline, 'server_dir', return_value=Path(d)),                     mock.patch.object(timeline, 'key_frames', return_value=[0, 150, 300, 450]):
                return timeline.seek_redirect(frame, 'demos/x')

    def test_redirects_with_viewer(self):
        self.assertEqual(self.run_redirect(310, {'viewer': {'demo_id': VIEWER}, 'povid': None}), (150, 300))
        self.assertEqual(self.run_redirect(460, {'viewer': {'demo_id': VIEWER}, 'povid': None}), (460, None))

    def test_no_players_json_passes(self):
        self.assertEqual(self.run_redirect(310, None), (310, None))


if __name__ == '__main__':
    unittest.main()
