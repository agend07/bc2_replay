"""Offline tests for replay.vcr_overlay pure helpers (no window, no server)."""
import unittest
from replay import vcr_overlay as v
H = chr(10) + v.PAUSE_HINT  # r286: second line while paused
P = chr(10) + v.PLAY_HINT  # r286: second line while playing


class Fake:
    def __init__(self): self.t = 100.0
    def __call__(self): return self.t


class T(unittest.TestCase):
    def test_clock(self):
        self.assertEqual(v.format_clock(0), '00:00')
        self.assertEqual(v.format_clock(247.9), '04:07')
        self.assertEqual(v.format_clock(3725), '1:02:05')
        self.assertEqual(v.format_clock(-3), '00:00')
        self.assertEqual(v.format_clock(None), '--:--')
        self.assertEqual(v.format_clock(float('nan')), '--:--')

    def test_label(self):
        self.assertEqual(v.format_label('play', 247), '\u25b6 PLAY 04:07' + P)
        self.assertEqual(v.format_label('pause', 247), '\u275a\u275a PAUSE 04:07' + H)
        self.assertEqual(v.format_label('play', 247, ascii_only=True), '> PLAY 04:07' + P)
        self.assertEqual(v.format_label('pause', 247, ascii_only=True), '|| PAUSE 04:07' + H)
        self.assertEqual(v.format_label('init', 0), 'INIT 00:00')
        self.assertEqual(v.format_label(None, 5), '')

    def test_label_view(self):
        self.assertEqual(v.format_label('play', 225, view='fpv'), '▶ PLAY 03:45 FPV' + P)
        self.assertEqual(v.format_label('pause', 225, view='free'), '❚❚ PAUSE 03:45 FREE' + H)
        self.assertEqual(v.format_label('pause', 225, ascii_only=True, view='fpv'), '|| PAUSE 03:45 FPV' + H)
        self.assertEqual(v.format_label('play', 225, speed=2.0, view='free'), '▶▶ 2x 03:45 FREE' + P)
        self.assertEqual(v.format_label('init', 0, view='free'), 'INIT 00:00')
        self.assertEqual(v.format_label(None, 5, view='free'), '')
        self.assertEqual(v.label_color(v.format_label('pause', 5, view='free')), v.RED)
        ck = v.Clock(30, now=Fake())
        ck.update({'ready': 1, 'paused': 1, 'frame': 30 * 225})
        ck.view = 'free'
        self.assertEqual(ck.label(True), '|| PAUSE 03:45 FREE' + H)

    def test_label_target(self):
        self.assertEqual(v.format_label('pause', 225, view='free', target='Davorich'), '❚❚ PAUSE 03:45 FREE · Davorich' + H)
        self.assertEqual(v.format_label('play', 225, ascii_only=True, view='free', target='Davorich'), '> PLAY 03:45 FREE - Davorich' + P)
        self.assertEqual(v.format_label('play', 225, view='fpv', target='Davorich'), '▶ PLAY 03:45 FPV' + P)  # FREE only
        self.assertEqual(v.format_label('play', 225, view='free', target=None), '▶ PLAY 03:45 FREE' + P)

    def test_pause_bars(self):
        for h in (14, 20, 27, 40):
            (a, b), width = v.pause_bars(h, 5, 7)
            bar, gap = a[2] - a[0], b[0] - a[2]
            self.assertEqual(bar, b[2] - b[0])
            self.assertEqual(gap, bar)  # gap equals bar width (like U+23F8), not a wide font gap
            self.assertEqual(width, b[2] - 5)
            self.assertEqual(a[0], 5)
            self.assertTrue(7 < a[1] < a[3] < 7 + h)
            self.assertGreater(a[3] - a[1], 2 * bar)  # taller than wide

    def test_label_color(self):  # r240
        self.assertEqual(v.label_color(v.format_label('pause', 5)), v.RED)
        self.assertEqual(v.label_color(v.format_label('play', 5)), v.GREEN)

    def test_borderless_style(self):  # r291
        framed = 0x14CF0000  # WS_OVERLAPPEDWINDOW | WS_VISIBLE | WS_CLIPSIBLINGS
        self.assertEqual(v.borderless_style(framed), 0x14000000)
        self.assertEqual(v.borderless_style(0x14000000), 0x14000000)  # already borderless: unchanged
        self.assertEqual(v.borderless_style(0x94000000 - 2**32), 0x94000000 - 2**32)  # WS_POPUP, signed LONG

    def test_mode_from_state(self):
        self.assertIsNone(v.mode_from_state(None))
        self.assertEqual(v.mode_from_state({'ready': 0, 'paused': 1, 'frame': 0}), 'init')
        self.assertEqual(v.mode_from_state({'ready': 1, 'paused': 1, 'frame': 0}), 'pause')
        self.assertEqual(v.mode_from_state({'ready': 1, 'paused': 0, 'frame': 0}), 'play')

    def test_interpolation(self):
        f = Fake(); ck = v.Clock(30, now=f)
        ck.update({'ready': 1, 'paused': 0, 'frame': 7350})
        f.t += 0.6
        self.assertAlmostEqual(ck.seconds(), 245.6)
        f.t += 10  # stale poll: capped
        self.assertAlmostEqual(ck.seconds(), 245 + v.MAX_EXTRAPOLATE_S)
        ck.update({'ready': 1, 'paused': 1, 'frame': 7380})
        f.t += 5
        self.assertEqual(ck.seconds(), 246.0)
        self.assertEqual(ck.label(True), '|| PAUSE 04:06' + H)
        ck.clear()
        self.assertEqual(ck.label(), '')

    def test_corner(self):
        r = (100, 50, 1380, 770)
        self.assertEqual(v.corner_position(r, (200, 40), 'tl', 12), (112, 62))
        self.assertEqual(v.corner_position(r, (200, 40), 'tr', 12), (1168, 62))
        self.assertEqual(v.corner_position(r, (200, 40), 'bl', 12), (112, 718))
        self.assertEqual(v.corner_position(r, (200, 40), 'br', 12), (1168, 718))

    def test_recording_name(self):
        self.assertEqual(v.recording_name('demos/cl04_1088'), 'cl04_1088')
        self.assertEqual(v.recording_name('demos\\pov_trial09_11c_Davorich.fbgd'), 'pov_trial09_11c_Davorich')
        self.assertEqual(v.recording_name('cl04_1088'), 'cl04_1088')
        self.assertEqual(v.recording_name(None), '')
        self.assertEqual(v.recording_name(''), '')

    def test_name_label(self):
        self.assertEqual(v.name_label(v.format_label('play', 5), 'demos/cl04_1088'), ('cl04_1088', v.GREEN))
        self.assertEqual(v.name_label(v.format_label('pause', 5, view='free', target='X'), 'demos/cl04_1088'),
                         ('cl04_1088', v.RED))
        self.assertEqual(v.name_label('', 'demos/cl04_1088'), ('', None))  # no replay running
        self.assertEqual(v.name_label(v.format_label(None, 5), 'demos/cl04_1088'), ('', None))
        self.assertEqual(v.name_label(v.format_label('play', 5), None), ('', None))

    def test_line_offsets(self):
        self.assertEqual(v.line_offsets([120, 80], 'right'), [0, 40])  # right edges aligned
        self.assertEqual(v.line_offsets([80, 120], 'right'), [40, 0])
        self.assertEqual(v.line_offsets([120, 80], 'left'), [0, 0])
        self.assertEqual(v.line_offsets([50]), [0])
        self.assertEqual(v.line_offsets([120, 80], 'center'), [0, 20])

    def test_banner(self):  # r286: Keep destruction seek in progress
        self.assertEqual(v.banner_text(True), 'REWINDING…' + chr(10) + 'please wait')
        self.assertEqual(v.banner_text(False, ascii_only=True), 'FAST FORWARD...' + chr(10) + 'please wait')
        self.assertEqual(v.center_position((100, 50, 900, 650), (200, 100)), (400, 300))

    def test_name_position(self):
        self.assertEqual(v.name_corner('tl'), 'tr')
        self.assertEqual(v.name_corner('tr'), 'tl')
        self.assertEqual(v.name_corner('bl'), 'br')
        r = (100, 50, 1380, 770)
        # same offset from the top edge as the VCR label (tl at y=62), anchored right
        self.assertEqual(v.corner_position(r, (150, 40), v.name_corner('tl'), 12), (1218, 62))


if __name__ == '__main__':
    unittest.main()
