"""Offline tests for replay.ui_logic (Time entry, auto-launch after Replay, player cycling).
Run from the repo root:  python -m pytest replay/test_ui_logic.py -q
"""
import unittest
from pathlib import Path
from unittest import mock

from replay.ui_logic import FPS, parse_time_entry, should_auto_launch, cycle_player, follow_button_text

LIMIT = 1815 * FPS  # 30:15


class TimeEntryTests(unittest.TestCase):
    def test_accepts_seconds_mmss_hhmmss(self):
        self.assertEqual(parse_time_entry('90', LIMIT), (90 * FPS, None))
        self.assertEqual(parse_time_entry(' 1:30 ', LIMIT), (90 * FPS, None))
        self.assertEqual(parse_time_entry('0:01:30', LIMIT), (90 * FPS, None))
        self.assertEqual(parse_time_entry('12.5', LIMIT), (round(12.5 * FPS), None))
        self.assertEqual(parse_time_entry('0', LIMIT), (0, None))
        self.assertEqual(parse_time_entry('30:15', LIMIT), (LIMIT, None))

    def test_rejects_garbage(self):
        for text in ('', '  ', 'abc', '1:2:3:4', '1:60', '1.5:10', '-5', 'nan', 'inf'):
            frame, err = parse_time_entry(text, LIMIT)
            self.assertIsNone(frame, text)
            self.assertTrue(err, text)

    def test_rejects_out_of_range(self):
        frame, err = parse_time_entry('30:16', LIMIT)
        self.assertIsNone(frame)
        self.assertEqual(err, 'Out of range: 0 to 30:15')
        self.assertIsNone(parse_time_entry('1:00:00', LIMIT)[0])
        self.assertIsNone(parse_time_entry('1', 0)[0])  # no demo running: only 0 fits


class AutoLaunchTests(unittest.TestCase):
    def test_waits_until_switched_server_is_ready(self):
        self.assertEqual(should_auto_launch(None, 'demos/a', True, set()), 'wait')
        self.assertEqual(should_auto_launch('demos/a', 'demos/a', False, set()), 'wait')
        self.assertEqual(should_auto_launch('demos/a', 'demos/b', True, set()), 'wait')
        self.assertEqual(should_auto_launch('demos/a', None, True, set()), 'wait')

    def test_launches_only_without_running_client(self):
        self.assertEqual(should_auto_launch('demos/a', 'demos/a', True, set()), 'launch')
        self.assertEqual(should_auto_launch('demos/a', 'demos/a', True, {1234}), 'skip')


class CyclePlayerTests(unittest.TestCase):
    NAMES = ['a', 'b', 'c', 'd']

    def test_next_and_prev_wrap(self):
        valid = set(self.NAMES)
        self.assertEqual(cycle_player(self.NAMES, 'b', valid, 1), 'c')
        self.assertEqual(cycle_player(self.NAMES, 'd', valid, 1), 'a')
        self.assertEqual(cycle_player(self.NAMES, 'b', valid, -1), 'a')
        self.assertEqual(cycle_player(self.NAMES, 'a', valid, -1), 'd')

    def test_skips_players_without_soldier(self):
        self.assertEqual(cycle_player(self.NAMES, 'a', {'a', 'd'}, 1), 'd')
        self.assertEqual(cycle_player(self.NAMES, 'd', {'b', 'd'}, 1), 'b')  # wraps past a
        self.assertEqual(cycle_player(self.NAMES, 'c', {'a', 'd'}, -1), 'a')  # skips b
        self.assertEqual(cycle_player(self.NAMES, 'b', {'b'}, 1), 'b')  # only one alive: stay on it

    def test_no_current_and_nobody_valid(self):
        self.assertEqual(cycle_player(self.NAMES, '', set(self.NAMES), 1), 'a')
        self.assertEqual(cycle_player(self.NAMES, 'gone', set(self.NAMES), -1), 'd')
        self.assertIsNone(cycle_player(self.NAMES, 'a', set(), 1))
        self.assertIsNone(cycle_player([], 'a', {'a'}, 1))


class CameraButtonTests(unittest.TestCase):
    def test_follow_toggle_text(self):
        self.assertEqual(follow_button_text(False), 'Follow (experimental)')
        self.assertEqual(follow_button_text(True), 'Stop follow')

    def test_camera_buttons(self):
        """Free camera on/off is F in game; Follow and Stop follow are one toggle; the switch button is ▶."""
        import pathlib
        import re
        src = (pathlib.Path(__file__).parent / 'ui.py').read_text(encoding='utf-8')
        labels = re.findall(r"ttk\.Button\([^)]*?text='([^']*)'", src)
        for gone in ('Enable free camera', 'Restore player view', 'Stop Follow', 'Switch Recording'):
            self.assertNotIn(gone, labels)
        self.assertNotIn('Replay', labels)  # the switch button is the ▶ (play) icon
        self.assertIn("ttk.Button(rec_row, text='▶', width=3, command=start_switch_recording)", src)
        self.assertIn('Focus', labels)
        self.assertIn("start_camera('restore', closing=True)", src)  # closing the panel still restores the view
        self.assertNotIn('Switch Recording', src)


class LauncherImportTests(unittest.TestCase):
    def test_launcher_api(self):
        import inspect
        import launcher_panel
        self.assertFalse(hasattr(launcher_panel, 'redirect_status'))
        self.assertEqual(list(inspect.signature(launcher_panel.add_launcher).parameters),
                         ['window', 'on_paths_changed', 'on_status'])



class PrepareButtonTests(unittest.TestCase):
    """Setup section: one Prepare button + a confirmed Clean up… button (pure helpers in replay.first_run)."""

    def plan(self, client='rome', server='rome', timeline=False, trace=False, instance=False):
        from replay import first_run as fr
        return fr.prepare_plan({'game_dir': 'GAMEDIR', 'server_dir': 'SRVDIR',
                                'client_proxy': {'state': client, 'text': 'c'},
                                'server_proxy': {'state': server, 'text': 's'},
                                'timeline_exe': timeline, 'server_exe': trace, 'instance': instance})

    def test_button_labels(self):
        from replay.first_run import prepare_button_text
        ok = {'name': 'a', 'ok': True, 'text': ''}
        self.assertEqual(prepare_button_text(None), 'Prepare')
        self.assertEqual(prepare_button_text([]), 'Prepare')
        self.assertEqual(prepare_button_text([ok, dict(ok, ok=False)]), 'Prepare')
        self.assertEqual(prepare_button_text([ok, dict(ok, ok=None)]), 'Check')
        self.assertEqual(prepare_button_text([ok]), 'Check')

    def test_confirm_all_steps(self):
        from replay.first_run import prepare_confirm_text
        text = prepare_confirm_text(self.plan(), 'GAMEDIR', 'SRVDIR')
        self.assertEqual(text, 'Prepare will:\n'
                               '- Install the dinput8.dll proxy into GAMEDIR (Windows will ask for administrator '
                               'permission)\n'
                               '- Install the dinput8.dll proxy into SRVDIR\n'
                               '- Build the playback server executables in SRVDIR\n'
                               '- Create the local preview instance under runtime\\\n'
                               'Nothing else is changed. Continue?')
        self.assertEqual(text.count('administrator'), 1)

    def test_confirm_only_needed_steps(self):
        from replay.first_run import prepare_confirm_text
        text = prepare_confirm_text(self.plan('installed', 'rome', True, True, True), 'GAMEDIR', 'SRVDIR')
        self.assertEqual(text, 'Prepare will:\n- Install the dinput8.dll proxy into SRVDIR\n'
                               'Nothing else is changed. Continue?')
        self.assertNotIn('administrator', text)
        self.assertNotIn('GAMEDIR', text)
        text = prepare_confirm_text(self.plan('upgrade', 'upgrade', True, True, True), 'GAMEDIR', 'SRVDIR')
        self.assertIn('- Upgrade the dinput8.dll proxy in GAMEDIR (Windows will ask for administrator permission)\n',
                      text)
        self.assertIn('- Upgrade the dinput8.dll proxy in SRVDIR\n', text)
        self.assertIsNone(prepare_confirm_text(self.plan('installed', 'installed', True, True, True), 'G', 'S'))

    def test_cleanup_confirm(self):
        import preview_config
        from replay.first_run import cleanup_confirm_text
        text = cleanup_confirm_text('GAMEDIR', 'SRVDIR')
        self.assertEqual(text, "Clean up removes what Prepare created: the proxy in GAMEDIR (Project Rome's dinput8.dll "
                               'restored; administrator prompt), the proxy in SRVDIR, the built playback executables '
                               f'{preview_config.TIMELINE_NAME} and {preview_config.SERVER_NAME} in SRVDIR, and '
                               'runtime\\Instance-local-preview. Your game, server files and recordings stay. '
                               'Close the game and the replay server first. Continue?')

    def test_launcher_wiring(self):
        import inspect
        import launcher_panel
        src = inspect.getsource(launcher_panel.add_launcher)
        self.assertIn("ttk.Button(row, text='Prepare', command=prepare)", src)
        self.assertIn("ttk.Button(row, text='Clean up…', command=cleanup)", src)
        self.assertIn("b.pack(side='right'", src)
        for gone in ("'Install client proxy'", "'Uninstall proxy'", "'Check setup'", "text='Setup'",
                     "start('setup')", "action == 'setup'"):
            self.assertNotIn(gone, src)
        self.assertIn("return lambda: start('client')", inspect.getsource(launcher_panel))
        self.assertIn('textvariable=proxy_text', src)  # own StringVar: autosave owns `status`
        self.assertIn("first_run.run_prepare(", src)
        self.assertIn("first_run.run_cleanup(", src)
        self.assertIn("first_run.prepare_confirm_text(", src)
        self.assertIn("first_run.cleanup_confirm_text(", src)
        self.assertIn("events.put(('progress', ", src)
        self.assertIn("item[0] == 'progress'", src)
        self.assertIn("if any(r.get('ok') is False for r in item[1]):", src)  # report box only on a failure
        self.assertIn('threading.Thread(target=worker', src)
        self.assertNotIn("command('install-client')", src)  # in-process worker, not a subprocess
        self.assertIn("'Setup needed: set both paths, then click Prepare.'", src)

    def test_progress_before_done_keeps_busy(self):
        """handle(): 'progress' returns before busy is cleared (structural check, no Tk)."""
        import inspect
        import launcher_panel
        src = inspect.getsource(launcher_panel.add_launcher)
        handle = src[src.index('    def handle(item):'):src.index('    def poll():')]
        self.assertLess(handle.index("item[0] == 'progress'"), handle.index('busy = False'))
        self.assertIn('return False', handle[:handle.index('busy = False')])


class PreviewPrepareCliTests(unittest.TestCase):
    def test_actions_registered(self):
        src = (Path(__file__).resolve().parent.parent / 'preview.py').read_text(encoding='utf-8')
        for action in ("'prepare'", "'cleanup'", "'install-client'", "'uninstall-client'", "'client-status'",
                       "'check-setup'"):
            self.assertIn(action, src)
        self.assertIn("os.environ.get('BC2_CLEANUP')!='1'", src)
        self.assertIn('first_run.run_prepare(', src)
        self.assertIn('first_run.run_cleanup(', src)


if __name__ == '__main__':
    unittest.main()


class SetupWithoutRecordingTests(unittest.TestCase):
    """r264b: Setup (build / init-instance) must not require a recording in demos/; `server` still does."""

    def test_check_tolerates_missing_recording_when_not_required(self):
        import preview
        with mock.patch.object(preview, 'verify'), \
             mock.patch.object(preview, 'check_recording', side_effect=RuntimeError('no demos')), \
             mock.patch.object(preview, 'server_dir', return_value=Path('C:/srv')), \
             mock.patch.object(preview, 'client_exe', return_value=Path('C:/c.exe')), \
             mock.patch.object(preview, 'trace_exe', return_value=Path('C:/srv/none.exe')), \
             mock.patch.object(preview, 'timeline_exe', return_value=Path('C:/srv/none2.exe')), \
             mock.patch.object(preview, 'available_recordings', return_value=[]):
            result = preview.check(require_recording=False)
            self.assertFalse(result['recording_companions_present'])
            self.assertIsNone(result['active_recording'])
            with self.assertRaises(RuntimeError):
                preview.check()

    def test_build_and_init_instance_do_not_require_a_recording(self):
        src = (Path(__file__).resolve().parent.parent / 'preview.py').read_text(encoding='utf-8')
        self.assertIn("args.action=='build':\n        check(require_recording=False)", src)
        self.assertIn("admin password.\n    check(require_recording=False)", src)
        self.assertIn("args.action=='server':\n        check()", src)


class AutoPlayTests(unittest.TestCase):  # r286
    def setUp(self):
        from replay.ui_logic import AutoPlay
        self.t = [100.0]
        self.a = AutoPlay(clock=lambda: self.t[0])

    def test_idle_until_armed(self):
        self.assertEqual(self.a.action(True, True), 'idle')

    def test_waits_for_the_level_then_settles(self):
        self.a.arm()
        self.assertEqual(self.a.action(True, False), 'wait')
        self.t[0] += 20
        self.assertEqual(self.a.action(True, True), 'wait')  # settle starts
        self.t[0] += 2
        self.assertEqual(self.a.action(True, True), 'wait')
        self.t[0] += 1.5
        self.assertEqual(self.a.action(True, True), 'play')

    def test_dropped_when_already_playing_or_too_late(self):
        self.a.arm()
        self.assertEqual(self.a.action(False, True), 'idle')
        self.assertFalse(self.a.armed)
        self.a.arm()
        self.t[0] += 200
        self.assertEqual(self.a.action(True, False), 'give_up')


class PlayWatchTests(unittest.TestCase):  # playability oracle
    def setUp(self):
        from replay.ui_logic import PlayWatch
        self.t = [100.0]
        self.w = PlayWatch(clock=lambda: self.t[0])

    def test_left_right_after_play_is_rejected(self):
        self.assertEqual(self.w.check(0), 'idle')
        self.w.arm()
        self.t[0] += 0.8
        self.assertEqual(self.w.check(1), 'wait')
        self.t[0] += 0.8
        self.assertEqual(self.w.check(0), 'rejected')
        self.assertFalse(self.w.armed)
        self.assertEqual(self.w.check(0), 'idle')  # reported once

    def test_survived_window_is_ok(self):
        self.w.arm()
        self.t[0] += 10.5
        self.assertEqual(self.w.check(1), 'ok')
        self.t[0] += 1
        self.assertEqual(self.w.check(0), 'idle')  # a later leave is not a rejection

    def test_late_first_poll_without_viewer_is_not_rejected(self):
        self.w.arm()
        self.t[0] += 11
        self.assertEqual(self.w.check(0), 'idle')

    def test_cancel(self):
        self.w.arm()
        self.w.cancel()
        self.assertEqual(self.w.check(0), 'idle')

    def test_message_names_the_demo(self):
        from replay.ui_logic import PLAY_REJECTED_TEXT
        self.assertIn('cl04_x', PLAY_REJECTED_TEXT.format(name='cl04_x'))
        self.assertIn('please report the demo name', PLAY_REJECTED_TEXT)
