"""r286 offline tests: composite destruction reset (stub bytes, part selection). No process is touched.

Run from the repo root:  python -m unittest replay.test_destruction_reset -v
"""
import struct
import unittest
from unittest import mock

from replay import destruction_reset as dr


class Stub(unittest.TestCase):
    def test_tail_jumps_to_native_update_tick(self):
        code = dr.stub(0x10000000, 0x10001000)
        self.assertEqual(code[:2], b'\x9c\x60')  # pushfd; pushad
        self.assertEqual(code[-7:-5], b'\x61\x9d')  # popad; popfd
        rel = struct.unpack('<i', code[-4:])[0]
        self.assertEqual((0x10000000 + len(code) + rel) & 0xFFFFFFFF, dr.UPDATE_TICK)

    def test_calls_set_health_state_slot_and_checks_part_vtable(self):
        code = dr.stub(0x10000000, 0x10001000)
        self.assertIn(b'\xff\x90' + struct.pack('<I', dr.SET_HEALTH_STATE_SLOT), code)
        self.assertIn(b'\x81\x39' + struct.pack('<I', dr.PART_VTABLE), code)
        self.assertIn(b'\xbb' + struct.pack('<I', 0x10001000), code)


class Reset(unittest.TestCase):
    def test_only_changed_parts_go_back_to_the_snapshot(self):
        snap = dr.Snapshot(7, 0x500, {0x10: 1, 0x20: 1, 0x30: 0})
        client = mock.Mock(pid=7)
        with mock.patch.object(dr, 'part_states', return_value=(0x500, {0x10: 0, 0x20: 1, 0x30: 0})), \
                mock.patch.object(dr, 'run_batch', return_value={'calls': 1, 'skipped': 0}) as batch:
            r = dr.reset(snap, client)
        batch.assert_called_once_with(client, [(0x10, 1)], quiet=True)
        self.assertEqual(r['changed'], 1)

    def test_other_level_or_process_refused(self):
        snap = dr.Snapshot(7, 0x500, {0x10: 1})
        with mock.patch.object(dr, 'part_states', return_value=(0x600, {0x10: 0})):
            with self.assertRaises(RuntimeError):
                dr.reset(snap, mock.Mock(pid=7))
        with self.assertRaises(RuntimeError):
            dr.reset(snap, mock.Mock(pid=8))


if __name__ == '__main__':
    unittest.main()
