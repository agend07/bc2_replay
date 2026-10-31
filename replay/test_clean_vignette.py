"""r226 offline tests for replay.clean_vignette (fake process memory, no live client).

Run from the repo root:  python -m unittest replay.test_clean_vignette -v
"""
import struct
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from replay import clean_vignette as cv

EXE = Path('C:/Program Files/EA Games/Battlefield Bad Company 2/BFBC2Game.exe')


def entry(mode, scale=(0.0, 0.0), blur=0.0, opacity=0.0, exponent=1.0, sat=1.0, pad=b'\0'):
    e = bytearray(pad * cv.ENTRY_SIZE)
    struct.pack_into('<2f', e, 0x00, *scale)
    struct.pack_into('<3f', e, 0x10, 0.0, 0.0, 0.0)
    for o in cv._CT:
        struct.pack_into('<f', e, 0x20 + o, 0.0)
    struct.pack_into('<3f', e, 0x20, sat, sat, sat)
    struct.pack_into('<3f', e, 0xC0, blur, opacity, exponent)
    struct.pack_into('<I', e, cv.MODE_OFFSET, mode)
    return bytes(e)


OFF = entry(0, scale=(-1.8663, 1.6634), opacity=0.3, exponent=4.0)
DEATH = entry(4, scale=(2.4, 2.4), blur=1.0, opacity=0.45, exponent=1.6, sat=0.2, pad=b'\xab')
CRIT = entry(1, scale=(2.0, 2.0), opacity=0.6, sat=0.4)
MENU = entry(2, blur=1.0, opacity=0.5)
TABLE = [OFF, CRIT, MENU, DEATH]


class FakeClient:
    pid = 1234

    def __init__(self, table=TABLE, current_mode=4, count_at='vector'):
        self.mem = {}
        self.hud, self.cfg, self.data = 0x10000000, 0x20000000, 0x30000000
        self.put(cv.HUD_SINGLETON, struct.pack('<I', self.hud))
        self.put(self.hud, struct.pack('<I', cv.HUD_VTABLE))
        self.put(self.hud + cv.HUD_SELF_REF, struct.pack('<I', self.hud))
        mgr = self.hud + cv.HUD_VIGNETTE_MANAGER
        self.put(mgr, struct.pack('<I', self.hud))
        self.put(mgr + cv.MGR_MODE, struct.pack('<I', current_mode))
        self.put(self.hud + cv.HUD_CONFIG, struct.pack('<I', self.cfg))
        arr = self.cfg + cv.CONFIG_VIGNETTES
        end = self.data + len(table) * cv.ENTRY_SIZE if count_at == 'vector' else 0
        self.put(arr, struct.pack('<IIII', cv.ARRAY_VTABLE, 0x1C06CFC, self.data, end))
        self.put(self.data - 4, struct.pack('<I', len(table) if count_at == 'data-4' else 0x15491D0))
        self.put(self.data, b''.join(table))
        by_mode = {cv.entry_mode(e): e for e in table}
        if current_mode in by_mode:
            self.put(mgr + cv.MGR_TARGET, by_mode[current_mode])
        self.writes = []

    def put(self, address, data):
        for i, b in enumerate(data):
            self.mem[address + i] = b

    def read(self, address, size):
        try:
            return bytes(self.mem[address + i] for i in range(size))
        except KeyError:
            raise OSError('unmapped')

    def u32(self, address):
        return struct.unpack('<I', self.read(address, 4))[0]

    def write(self, address, data):
        self.writes.append((address, bytes(data)))
        self.put(address, data)

    def close(self):
        pass


class PureTests(unittest.TestCase):
    def test_parse_rejects_bad_tables(self):
        blob = b''.join(TABLE)
        self.assertEqual(len(cv.parse_table(blob, 4)), 4)
        for bad in (0, 65, 5):
            with self.assertRaises(ValueError):
                cv.parse_table(blob, bad)
        with self.assertRaises(ValueError):            # duplicate mode
            cv.parse_table(OFF + OFF, 2)
        with self.assertRaises(ValueError):            # no VMOff
            cv.parse_table(DEATH + CRIT, 2)
        with self.assertRaises(ValueError):            # enum out of range
            cv.parse_table(OFF + entry(17), 2)
        nan = bytearray(DEATH)
        struct.pack_into('<I', nan, 0xC0, 0x7FC00000)
        with self.assertRaises(ValueError):
            cv.parse_table(OFF + bytes(nan), 2)

    def test_plan_copies_off_fields_keeps_mode_padding_and_menus(self):
        todo = cv.plan(TABLE)
        self.assertEqual([t[1] for t in todo], [1, 4])           # menu (2) untouched
        index, mode, original, new = todo[1]
        self.assertEqual(len(new), cv.WRITE_SIZE)                # Mode never written
        self.assertEqual(cv.fields(new + DEATH[cv.WRITE_SIZE:]), cv.fields(OFF))
        self.assertEqual(new[0x08:0x10], b'\xab' * 8)            # padding preserved
        self.assertEqual(original, DEATH[:cv.WRITE_SIZE])

    def test_plan_is_idempotent(self):
        once = [e if cv.entry_mode(e) not in cv.SUPPRESS_MODES else
                cv.neutral_image(e, OFF) + e[cv.WRITE_SIZE:] for e in TABLE]
        self.assertEqual(cv.plan(once), [])

    def test_restore_action(self):
        self.assertEqual(cv.restore_action(b'w', b'o', b'w'), 'restore')
        self.assertEqual(cv.restore_action(b'o', b'o', b'w'), 'native')
        self.assertEqual(cv.restore_action(b'x', b'o', b'w'), 'changed')

    def test_copy_fields_cover_native_copy(self):
        self.assertEqual(len(cv.COPY_FIELDS), 5 + 28 + 3)
        self.assertTrue(all(o % 4 == 0 and o < cv.MODE_OFFSET for o in cv.COPY_FIELDS))


class LiveFlowTests(unittest.TestCase):
    def setUp(self):
        tmp = Path(tempfile.mkdtemp())
        self.patches = [mock.patch.object(cv, 'STATE', tmp / 'state.json'),
                        mock.patch.object(cv, '_created', lambda c: 42),
                        mock.patch.object(cv, '_writable', lambda c, a, s: True)]
        for p in self.patches:
            p.start()

    def tearDown(self):
        for p in self.patches:
            p.stop()

    def test_locate_vector_layout_and_crosscheck(self):
        self.assertEqual(cv.locate(FakeClient())['count_source'], 'vector')
        self.assertEqual(cv.locate(FakeClient())['count'], 4)
        with self.assertRaises(RuntimeError):            # only a legacy guess validates -> refuse
            cv.locate(FakeClient(count_at='data-4'))
        odd = FakeClient()                               # end not on a 0xD0 boundary
        odd.put(odd.cfg + cv.CONFIG_VIGNETTES + cv.ARRAY_END, struct.pack('<I', odd.data + 0x100))
        with self.assertRaises(RuntimeError):
            cv.locate(odd)
        amb = FakeClient()                               # a second distinct table also validates
        amb.put(amb.data - 4, struct.pack('<I', 1))
        with self.assertRaises(RuntimeError):
            cv.locate(amb)
        bad = FakeClient()
        bad.put(bad.hud + cv.HUD_VIGNETTE_MANAGER + cv.MGR_TARGET, CRIT)
        with self.assertRaises(RuntimeError):
            cv.locate(bad)
        nohud = FakeClient()
        nohud.put(nohud.hud + cv.HUD_VIGNETTE_MANAGER, b'\0\0\0\0')
        with self.assertRaises(RuntimeError):
            cv.locate(nohud)

    def test_enable_writes_after_recovery_then_disable_restores(self):
        client = FakeClient()
        before = client.read(client.data, 4 * cv.ENTRY_SIZE)
        out = cv.enable(client)
        self.assertEqual(out['neutralized'], ['CriticalHealth', 'Death'])
        self.assertTrue(cv.STATE.exists())
        self.assertEqual({a for a, _ in client.writes},
                         {client.data + cv.ENTRY_SIZE, client.data + 3 * cv.ENTRY_SIZE})
        death = client.read(client.data + 3 * cv.ENTRY_SIZE, cv.ENTRY_SIZE)
        self.assertEqual(cv.fields(death), cv.fields(OFF))
        self.assertEqual(cv.entry_mode(death), 4)
        self.assertEqual(cv.enable(client)['newly_written'], 0)       # idempotent
        self.assertEqual(cv.status(client)['neutralized'], {'CriticalHealth': True, 'Death': True})
        res = cv.disable(client)
        self.assertEqual(res, {'CriticalHealth': 'restored', 'Death': 'restored'})
        self.assertEqual(client.read(client.data, 4 * cv.ENTRY_SIZE), before)
        self.assertFalse(cv.STATE.exists())
        self.assertEqual(cv.disable(client), {'vignette': 'not installed'})

    def test_disable_leaves_bytes_changed_by_game(self):
        client = FakeClient()
        cv.enable(client)
        client.put(client.data + 3 * cv.ENTRY_SIZE, b'\x11' * 8)
        res = cv.disable(client)
        self.assertEqual(res['Death'], 'changed by game (left)')
        self.assertEqual(client.read(client.data + 3 * cv.ENTRY_SIZE, 8), b'\x11' * 8)

    def test_inspect_is_read_only_and_reports_layouts(self):
        client = FakeClient(count_at='data-4')
        out = cv.inspect(client)
        self.assertEqual(client.writes, [])
        names = {r['layout']: r['valid'] for r in out['layouts']}
        self.assertEqual(names, {'vector': False, 'data-4': True, 'array+4': False})
        self.assertTrue(out['enable_would_use'].startswith('REFUSE'))
        good = cv.inspect(FakeClient())
        self.assertEqual(good['enable_would_use'], 'vector')
        self.assertIn('raw_around_data', good['layouts'][0])

    def test_unwritable_table_writes_nothing(self):
        client = FakeClient()
        with mock.patch.object(cv, '_writable', lambda c, a, s: False):
            with self.assertRaises(RuntimeError):
                cv.enable(client)
        self.assertEqual(client.writes, [])
        self.assertFalse(cv.STATE.exists())

    def test_no_optin_gate(self):
        for name in ('OPTIN', 'opted_in', 'set_optin'):
            self.assertFalse(hasattr(cv, name), name)


class CleanScreenAlwaysOnTests(unittest.TestCase):
    """clean_screen applies no-vignette and fixed FOV on every enable, no opt-in file."""

    def test_extras_always_applied(self):
        from replay import clean_screen as cs
        from replay import free_cam_fov as fc
        client = object()
        with mock.patch.object(cv, 'enable', return_value={'neutralized': ['Death']}) as ven, \
                mock.patch.object(fc, 'enable', return_value={'fov_locked': 70.0}) as fen:
            result = cs.apply_extras(client, {'ui': 'gated'})
        ven.assert_called_once_with(client)
        fen.assert_called_once_with(client)
        self.assertEqual(result, {'ui': 'gated', 'vignette': {'neutralized': ['Death']},
                                  'fov': {'fov_locked': 70.0}})

    def test_extras_failure_is_reported_not_raised(self):
        from replay import clean_screen as cs
        from replay import free_cam_fov as fc
        with mock.patch.object(cv, 'enable', side_effect=RuntimeError('no HUD')), \
                mock.patch.object(fc, 'enable', side_effect=RuntimeError('no camera')):
            result = cs.apply_extras(object(), {'ui': 'gated'})
        self.assertEqual(result, {'ui': 'gated', 'vignette': 'not applied: no HUD',
                                  'fov': 'not applied: no camera'})
        self.assertFalse(hasattr(cs.clean_vignette, 'opted_in'))
        self.assertFalse(hasattr(cs.free_cam_fov, 'opted_in'))


@unittest.skipUnless(EXE.exists(), 'installed client not present')
class ExeEvidenceTests(unittest.TestCase):
    """Pin the static facts the module relies on to the exact installed build."""

    @classmethod
    def setUpClass(cls):
        import hashlib
        cls.data = EXE.read_bytes()
        if hashlib.sha256(cls.data).hexdigest() != '3911fcc8914b0158c434cee36f19295316e715d4c9a0e70c857e8c39e3d54258':
            raise unittest.SkipTest('different client build')
        pe = struct.unpack_from('<I', cls.data, 0x3C)[0]
        count = struct.unpack_from('<H', cls.data, pe + 6)[0]
        opt = struct.unpack_from('<H', cls.data, pe + 20)[0]
        cls.secs = [struct.unpack_from('<IIII', cls.data, pe + 24 + opt + 40 * i + 8) for i in range(count)]

    def at(self, va, n):
        for _vsize, rva, size, raw in self.secs:
            off = va - 0x400000 - rva
            if 0 <= off < size:
                return self.data[raw + off:raw + off + n]
        raise AssertionError(hex(va))

    def test_find_vignette_walks_mode_at_cc_stride_d0(self):
        self.assertEqual(self.at(0x92F983, 6), bytes.fromhex('8b899c000000'))     # [[mgr]+0x9C]
        self.assertEqual(self.at(0x92F9B7, 7), bytes.fromhex('399c3acc000000'))   # cmp [entry+0xCC], mode
        self.assertEqual(self.at(0x92F9C7, 6), bytes.fromhex('81c7d0000000'))     # stride 0xD0
        self.assertEqual(self.at(0x13D3344, 6), bytes.fromhex('81c194000000'))    # config+0x94
        self.assertEqual(self.at(0x92F9B4, 3), bytes.fromhex('8b5108'))           # begin = [arr+8]

    def test_array_count_is_end_minus_begin_div_d0(self):
        self.assertEqual(self.at(0x1473CAC, 4), struct.pack('<I', 0xB6F410))     # vtable slot 0
        # mov edx,[ecx+0xC]; sub edx,[ecx+8]; mov eax,0x4EC4EC4F; imul edx; sar edx,6  (/0xD0)
        self.assertEqual(self.at(0xB6F410, 16), bytes.fromhex('8b510c2b5108b84fecc44ef7eac1fa06'))

    def test_hud_owns_manager_and_feeds_post_process(self):
        self.assertEqual(self.at(0x7762FA, 6), bytes.fromhex('8d8e10780200'))     # init hud+0x27810
        self.assertEqual(self.at(0x786B71, 6), bytes.fromhex('8d8e10780200'))     # tick update
        self.assertEqual(self.at(0x90C7F1, 6), bytes.fromhex('8b0df8d45601'))     # [0x156D4F8]
        self.assertEqual(self.at(0x90C7FF, 3), bytes.fromhex('8b4048'))           # vtable +0x48
        self.assertEqual(self.at(0x5C9680, 6), bytes.fromhex('81c1d0040000'))     # -> this+0x4D0

    def test_vignette_mode_enum_names(self):
        for va, name in ((0x1473744, b'VMDeath\0'), (0x1473768, b'VMCriticalHealth\0'),
                         (0x147377C, b'VMOff\0')):
            self.assertEqual(self.at(va, len(name)), name)


if __name__ == '__main__':
    unittest.main()
