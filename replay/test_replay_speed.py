"""r233 offline tests: replay speed (mailbox+0x38) cave bytes, the raw write path (write_speed, used by Keep
destruction seeks), VCR label.

Run from the repo root:  python -m unittest replay.test_replay_speed -v
Reads the original server exe from preview.json server_dir (skips if absent); never touches a process.
"""
import struct
import unittest
from unittest import mock

import server_build as sb
from replay import server as srv
from replay import timeline as tl
from replay import vcr_overlay as v

SPEED_CODE = bytes.fromhex('8b4338 85c0 0f8e16000000 3d0000807f 0f830b000000 d9442440 d84b38 d95c2440')
MAILBOX = sb.MAILBOX


def timeline_image():
    try:
        original = (sb.server_dir() / 'Frost.Game.Main_Win32_Final.exe').read_bytes()
    except Exception as exc:  # no preview.json / server folder
        raise unittest.SkipTest('original server exe unavailable: %s' % exc)
    trace = sb.make_trace(original)
    return trace, sb.make_timeline(trace)


class CaveBytes(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.trace, (cls.image, cls.code) = timeline_image()

    def test_speed_block_after_ready_and_before_status(self):
        ready = bytes.fromhex('c7433401000000')
        at = self.code.index(ready) + len(ready)
        self.assertEqual(self.code[at:at + len(SPEED_CODE)], SPEED_CODE)
        self.assertEqual(self.code[at + len(SPEED_CODE):at + len(SPEED_CODE) + 4], bytes.fromhex('837b3000'))
        self.assertLess(sb.CODE + len(self.code), 0x165E000, 'cave beyond mapped .text page')

    def test_dt_offset_from_hook_frame(self):
        # ServerPeer::update 0x76A790: push ecx/ebx/ebp/esi/edi; mov edi,ecx; call cave.
        off = 0x76A790 - sb.BASE
        self.assertEqual(self.trace[off:off + 7], bytes.fromhex('51 53 55 56 57 8bf9'.replace(' ', '')))
        self.assertEqual(self.trace[0x76AA16 - sb.BASE:0x76AA1A - sb.BASE], bytes.fromhex('d9442418'))  # native dt read
        # entry dt [esp+4] + 5 pushes + call ret + pushfd + pushad = [esp+0x40]
        self.assertEqual(4 + 5 * 4 + 4 + 4 + 32, 0x40)
        cave = self.code[:12]
        self.assertEqual(cave[5:7], bytes.fromhex('9c60'))  # call 0x6663D0 (returns first) then pushfd/pushad

    def test_emulated_scaling(self):
        try:
            from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
            from unicorn.x86_const import UC_X86_REG_ESP, UC_X86_REG_EBX
        except ImportError:
            self.skipTest('unicorn missing')
        for raw_speed, dt, want in ((2.0, 0.033, 0.066), (0.25, 0.04, 0.01), (4.0, 0.0333, 0.1332),
                                    (0.0, 0.033, 0.033), (-2.0, 0.033, 0.033), (float('inf'), 0.033, 0.033),
                                    (float('nan'), 0.033, 0.033)):
            mu = Uc(UC_ARCH_X86, UC_MODE_32)
            mu.mem_map(0x1000000, 0x10000)
            code_va, mb, stack = 0x1000000, 0x1004000, 0x1008000
            mu.mem_write(code_va, SPEED_CODE)
            mu.mem_write(mb + 0x38, struct.pack('<f', raw_speed))
            mu.mem_write(stack + 0x40, struct.pack('<f', dt))
            mu.reg_write(UC_X86_REG_ESP, stack)
            mu.reg_write(UC_X86_REG_EBX, mb)
            mu.emu_start(code_va, code_va + len(SPEED_CODE))
            got = struct.unpack('<f', bytes(mu.mem_read(stack + 0x40, 4)))[0]
            self.assertAlmostEqual(got, want, places=5, msg=repr(raw_speed))
            self.assertEqual(bytes(mu.mem_read(mb + 0x38, 4)), struct.pack('<f', raw_speed))


class FakeKernel:
    def __init__(self, mem):
        self.mem = mem

    def WriteProcessMemory(self, handle, address, buffer, size, got):
        off = address - MAILBOX
        self.mem[off:off + size] = buffer.raw[:size]
        got._obj.value = size
        return 1

    def CloseHandle(self, handle):
        return 1


def fake_server(mem):
    s = srv.Server.__new__(srv.Server)
    s.handle, s.pid = 1, 1234
    s.read = lambda address, size: bytes(mem[address - MAILBOX:address - MAILBOX + size])
    return s


class PanelWrite(unittest.TestCase):
    def setUp(self):
        self.mem = bytearray(sb.MAGIC + bytes(56))
        patcher = mock.patch.object(srv, 'k', FakeKernel(self.mem))
        patcher.start()
        self.addCleanup(patcher.stop)
        self.server = fake_server(self.mem)

    def run_set(self, x):
        with mock.patch.object(tl, 'Server', lambda write=False: self.server):
            return tl.write_speed(x)

    def test_write_speed_writes_float(self):
        st = self.run_set(2.0)
        self.assertEqual(bytes(self.mem[0x38:0x3C]), struct.pack('<f', 2.0))
        self.assertEqual(st['speed'], 2.0)
        self.assertEqual(self.run_set(50.0)['speed'], 50.0)  # destr_seek fast play
        st = self.run_set(1.0)
        self.assertEqual(bytes(self.mem[0x38:0x3C]), bytes(4))  # 1x = native path
        self.assertEqual(st['speed'], 1.0)
        self.assertEqual(self.mem[:8], sb.MAGIC)
        self.assertEqual(bytes(self.mem[8:0x38]), bytes(0x30))  # no other field touched

    def test_write_guard_still_strict(self):
        for off, data in ((0x30, b'\0' * 4), (0x34, b'\0' * 4), (0x38, b'\0' * 8), (0x3C, b'\0' * 4), (4, b'\0' * 4)):
            with self.assertRaises(ValueError):
                self.server.write(off, data)

    def test_status_speed_decode(self):
        self.assertEqual(srv.speed_from_raw(0), 1.0)
        self.assertEqual(srv.speed_from_raw(0x80000000), 1.0)  # -0
        self.assertEqual(srv.speed_from_raw(struct.unpack('<I', struct.pack('<f', 0.5))[0]), 0.5)
        self.assertEqual(srv.speed_from_raw(0x7FC00000), 1.0)  # NaN
        self.assertEqual(srv.speed_from_raw(0xBF800000), 1.0)  # -1


class Label(unittest.TestCase):
    def test_vcr_label_speed(self):
        self.assertEqual(v.format_label('play', 247, speed=2.0), '\u25b6\u25b6 2x 04:07' + '\n' + v.PLAY_HINT)
        self.assertEqual(v.format_label('play', 247, speed=0.5), '\u25b6 0.5x 04:07' + '\n' + v.PLAY_HINT)
        self.assertEqual(v.format_label('play', 247, True, 4.0), '>> 4x 04:07' + '\n' + v.PLAY_HINT)
        self.assertEqual(v.format_label('play', 247, speed=1.0), '\u25b6 PLAY 04:07' + '\n' + v.PLAY_HINT)
        self.assertEqual(v.format_label('pause', 247, speed=2.0), '\u275a\u275a PAUSE 04:07\n' + v.PAUSE_HINT)

    def test_clock_extrapolates_at_speed(self):
        t = [100.0]
        ck = v.Clock(30, now=lambda: t[0])
        ck.update({'ready': 1, 'paused': 0, 'frame': 300, 'speed': 2.0})
        t[0] += 0.5
        self.assertAlmostEqual(ck.seconds(), 11.0)
        self.assertEqual(ck.label(True), '>> 2x 00:11' + chr(10) + v.PLAY_HINT)

    def test_ui_imports(self):
        import replay.ui as ui
        self.assertTrue(callable(ui.main))


if __name__ == '__main__':
    unittest.main(verbosity=2)
