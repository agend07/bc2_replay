"""r227 offline tests for replay.free_cam_fov and clean_vignette.watch helpers.

Run from the repo root:  python -m unittest replay.test_free_cam_fov -v
"""
import json
import struct
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from replay import free_cam_fov as fc
from replay import clean_vignette as cv
from replay.test_clean_vignette import EXE, ExeEvidenceTests


class FakeClient:
    pid = 77

    def __init__(self, fov74=0.0, soldier=70.0):
        self.mem = {}
        self.owner, self.free, self.holder = 0x11000000, 0x12000000, 0x13000000
        self.put(self.owner + fc.OWNER_FREE_CAMERA, struct.pack('<I', self.free))
        self.put(self.free, struct.pack('<I', fc.FREE_CAMERA_VTABLE))
        self.put(self.free + fc.CAM_FOV_OVERRIDE, struct.pack('<f', fov74))
        self.put(fc.CAMERA_MANAGER + fc.MGR_ACTIVE, struct.pack('<I', self.free))
        self.put(fc.CAMERA_MANAGER + fc.MGR_FALLBACK, struct.pack('<I', self.holder))
        self.put(self.holder + fc.FALLBACK_FOV, struct.pack('<f', soldier))
        self.renderer, self.vt = 0x14000000, 0x15000000                  # r281 renderer FOV scale
        self.put(fc.RENDERER, struct.pack('<I', self.renderer))
        self.put(self.renderer, struct.pack('<I', self.vt))
        self.put(self.vt + fc.VT_SET_FOV_SCALE, struct.pack('<I', fc.SET_FOV_SCALE))
        self.put(self.renderer + fc.RENDER_FOV_SCALE, struct.pack('<f', 1.5))
        self.writes = []

    def put(self, a, d):
        for i, b in enumerate(d):
            self.mem[a + i] = b

    def read(self, a, n):
        try:
            return bytes(self.mem[a + i] for i in range(n))
        except KeyError:
            raise OSError('unmapped')

    def u32(self, a):
        return struct.unpack('<I', self.read(a, 4))[0]

    def write(self, a, d):
        self.writes.append((a, bytes(d)))
        self.put(a, d)

    def close(self):
        pass


class FovTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.cam = self.tmp / 'cam.json'
        self.patches = [mock.patch.object(fc, 'STATE', self.tmp / 'state.json'),
                        mock.patch.object(fc, 'CAMERA_SESSION', self.cam),
                        mock.patch.object(fc, '_created', lambda c: 5)]
        for p in self.patches:
            p.start()

    def tearDown(self):
        for p in self.patches:
            p.stop()

    def session(self, client, **extra):
        data = dict(pid=client.pid, owner=client.owner, free_camera=client.free)
        data.update(extra)
        self.cam.write_text(json.dumps(data), encoding='utf-8')

    def test_choose_fov(self):
        self.assertEqual(fc.choose_fov(None, 70.0), 70.0)
        self.assertEqual(fc.choose_fov(None, 20.0), fc.DEFAULT_FOV)     # zoomed soldier -> default
        self.assertEqual(fc.choose_fov(None, None), fc.DEFAULT_FOV)
        self.assertEqual(fc.choose_fov(65, 20.0), 65.0)
        with self.assertRaises(ValueError):
            fc.choose_fov(200, 70.0)

    def test_enable_disable_roundtrip(self):
        c = FakeClient()
        self.session(c)
        out = fc.enable(c)
        self.assertEqual(out['fov_locked'], 70.0)
        self.assertTrue(fc.STATE.exists())
        self.assertEqual(c.writes, [(c.free + 0x74, struct.pack('<f', 70.0)),
                                    (c.vt + 0x24, struct.pack('<I', fc.SET_FOV_SCALE_RET)),
                                    (c.renderer + 0x117C, struct.pack('<f', 1.0))])
        c.put(c.holder + 0x30, struct.pack('<f', 25.0))                  # soldier zooms
        self.assertEqual(fc.enable(c)['fov_locked'], fc.DEFAULT_FOV)   # re-enable keeps original
        self.assertTrue(fc.status(c)['locked'])
        self.assertEqual(fc.disable(c), {'free_cam_fov': 'restored'})
        self.assertEqual(c.read(c.free + 0x74, 4), struct.pack('<f', 0.0))
        self.assertEqual(c.u32(c.vt + 0x24), fc.SET_FOV_SCALE)              # setter back
        self.assertEqual(c.read(c.renderer + 0x117C, 4), struct.pack('<f', 1.5))
        self.assertEqual(fc.disable(c), {'free_cam_fov': 'not installed'})

    def test_scale_slot_refused_when_unknown(self):
        c = FakeClient()
        self.session(c)
        c.put(c.vt + 0x24, struct.pack('<I', 0x401000))
        with self.assertRaises(RuntimeError):
            fc.enable(c)
        self.assertEqual(c.writes, [])

    def test_refuses_without_valid_camera(self):
        c = FakeClient()
        with self.assertRaises(RuntimeError):
            fc.enable(c)                                   # no camera session
        self.session(c, free_camera=c.free + 4)
        with self.assertRaises(RuntimeError):
            fc.enable(c)
        weird = FakeClient(fov74=1e9)
        self.session(weird)
        with self.assertRaises(RuntimeError):
            fc.enable(weird)
        self.assertEqual(c.writes + weird.writes, [])

    def test_disable_leaves_foreign_value(self):
        c = FakeClient()
        self.session(c)
        fc.enable(c, fov=60)
        c.put(c.free + 0x74, struct.pack('<f', 33.0))
        self.assertEqual(fc.disable(c), {'free_cam_fov': 'changed by game (left)'})

    def test_always_on_no_optin(self):
        for name in ('OPTIN', 'opted_in', 'set_optin', 'requested_fov'):
            self.assertFalse(hasattr(fc, name), name)
        c = FakeClient(soldier=20.0)                       # zoomed soldier, no file anywhere
        self.session(c)
        self.assertEqual(fc.enable(c)['fov_locked'], fc.DEFAULT_FOV)
        self.assertNotIn('optin', fc.status(c))


class WatchHelperTests(unittest.TestCase):
    def test_diff_offsets(self):
        a = bytes(64)
        b = bytearray(a)
        b[8] = 1
        b[0x20] = 2
        self.assertEqual(cv.diff_offsets(a, bytes(b)), [8, 0x20])
        self.assertEqual(len(cv.diff_offsets(a, b'\x01' * 64, limit=3)), 3)

    def test_new_suppress_modes(self):
        self.assertTrue({7, 8, 14} <= set(cv.SUPPRESS_MODES))
        self.assertFalse({0, 2, 3, 15} & set(cv.SUPPRESS_MODES))


@unittest.skipUnless(EXE.exists(), 'installed client not present')
class FovExeEvidence(ExeEvidenceTests):
    def test_base_camera_ctor_zeroes_fov_override(self):
        self.assertEqual(self.at(0x78CD6A, 5), bytes.fromhex('f30f114674'))      # [esi+0x74] = 0
        self.assertEqual(self.at(0x7AC09E, 6), bytes.fromhex('c706e8c64201'))    # FreeCamera vtable

    def test_view_fov_selection(self):
        self.assertEqual(self.at(0x7A5F59, 6), bytes.fromhex('8b86ac000000'))    # active camera
        self.assertEqual(self.at(0x7A5F63, 5), bytes.fromhex('f30f104074'))      # its +0x74
        self.assertEqual(self.at(0x7A5F71, 6), bytes.fromhex('8b86b0000000'))    # fallback holder
        self.assertEqual(self.at(0x7A5F77, 5), bytes.fromhex('f30f104030'))      # its +0x30
        self.assertEqual(self.at(0x7A5F9A, 6), bytes.fromhex('dc0d40964d01'))    # * pi (then /180)

    def test_render_fov_scale_setter(self):
        self.assertEqual(self.at(0x5BE970, 17), bytes.fromhex('f30f10442404f30f11817c110000c20400'))
        self.assertEqual(self.at(0x5D7073, 6), bytes.fromhex('d9867c110000'))    # setView applies it
        self.assertEqual(self.at(0x141B484, 4), bytes.fromhex('70e95b00'))        # vtable slot 0x24


if __name__ == '__main__':
    unittest.main()
