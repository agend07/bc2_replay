"""r286 offline tests: Focus / looked-at / '<' '>' on a player in a vehicle (ClPlayer+0xC68 = vehicle entity).

Run from the repo root:  python -m unittest replay.test_players_vehicle -v
"""
import struct
import unittest

from replay import players as P

PLAYER, VEHICLE, STORAGE = 0x1000, 0x2000, 0x3000
VEH_TABLE, THUNK = 0x1436D38, 0x53DFB0


class FakeClient:
    def __init__(self, table=VEH_TABLE, thunk_target=P.GET_TRANSFORM, entity=VEHICLE):
        self.mem = {}
        self.put32(PLAYER, P.PLAYER_TABLE)
        self.put32(PLAYER + 0xc68, entity)
        self.put32(VEHICLE, table)
        self.put32(table + 0x44, THUNK)
        rel = (thunk_target - (THUNK + 11)) & 0xffffffff
        self.put(THUNK, P.TRANSFORM_THUNK + struct.pack('<I', rel))
        self.put(VEHICLE + 0x26, b'\x00')  # flags: no index -> storage pointer is the matrix
        self.put32(VEHICLE + 0x28, STORAGE)
        self.put(STORAGE, struct.pack('<16f', 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, -187.5, 135.7, -681.2, 1))

    def put(self, a, data):
        for i, b in enumerate(data):
            self.mem[a + i] = b

    def put32(self, a, v):
        self.put(a, struct.pack('<I', v))

    def read(self, a, n):
        try:
            return bytes(self.mem[a + i] for i in range(n))
        except KeyError:
            raise OSError('unmapped') from None

    def u32(self, a):
        return struct.unpack('<I', self.read(a, 4))[0]


class VehicleLink(unittest.TestCase):
    def setUp(self):
        P._BODY_TABLES.clear()

    def test_vehicle_link_and_position(self):
        c = FakeClient()
        self.assertEqual(P.soldier_link(c, PLAYER)['status'], 'unavailable')
        link = P.vehicle_link(c, PLAYER)
        self.assertEqual(link['status'], P.VEHICLE_LINK)
        self.assertEqual(link['table'], hex(VEH_TABLE))
        pos = P.link_position(c, link)
        self.assertAlmostEqual(pos[0], -187.5, places=3)
        self.assertAlmostEqual(pos[2], -681.2, places=3)

    def test_class_without_transform_accessor_rejected(self):
        c = FakeClient(thunk_target=0x500000)
        self.assertEqual(P.vehicle_link(c, PLAYER)['status'], 'unavailable')

    def test_dead_player_no_link(self):
        c = FakeClient(entity=0)
        self.assertEqual(P.vehicle_link(c, PLAYER)['status'], 'unavailable')

    def test_soldier_class_not_a_vehicle(self):
        c = FakeClient(table=P.SOLDIER_TABLE)
        self.assertEqual(P.vehicle_link(c, PLAYER)['status'], 'unavailable')

    def test_vehicle_replaced_during_read(self):
        c = FakeClient()
        link = P.vehicle_link(c, PLAYER)
        c.put32(VEHICLE, 0x1234560)
        with self.assertRaises(RuntimeError):
            P.link_position(c, link)

    def test_live_link_prefers_soldier(self):
        soldier = {'status': 'validated_reciprocal_reference', 'address': '0x10'}
        vehicle = {'status': P.VEHICLE_LINK, 'address': '0x20', 'table': '0x30'}
        self.assertIs(P.live_link({'soldier': soldier, 'vehicle': vehicle}), soldier)
        self.assertIs(P.live_link({'soldier': {'status': 'unavailable'}, 'vehicle': vehicle}), vehicle)
        self.assertIsNone(P.live_link({'soldier': {'status': 'unavailable'}}))


if __name__ == '__main__':
    unittest.main()
