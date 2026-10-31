"""Step 0 offline tests for replay.recordings (capture list, build command, builder transcript, busy refusal) + ui import.

Run from the repo root:  python -m pytest replay/test_recordings.py -q
No real builder runs: subprocess.run / Popen are faked.
"""
import io
import json
import os
import tempfile
import types
import unittest
from pathlib import Path

from replay import recordings

CAPTURES = [
    {'dir': 'C:/x/v3-trial-09-arica-online', 'name': 'v3-trial-09-arica-online', 'level': 'Levels/MP_006CQ',
     'label': 'Arica Harbor', 'seconds': 641.5, 'packets': 19245, 'records': 30000, 'drops': 0, 'bytes': 123,
     'recorded': '2026-10-23T16:51:52',
     'buildable': True, 'reason': None, 'src': 'patched_20261023_165152_405', 'paired': False,
     'reference': 'cl04_1088', 'builds': ['cl04_c09_1', 'cl04_c09_2'], 'manifest': 'C:/x/capture.json'},
    {'dir': 'C:/x/v3-trial-02-x', 'name': 'v3-trial-02-x', 'level': None, 'label': None, 'seconds': None,
     'packets': None, 'records': 10, 'drops': 3, 'bytes': 5, 'buildable': False, 'reason': 'no capture.json',
     'src': None, 'paired': None, 'reference': None, 'builds': [], 'manifest': None},
]

TRANSCRIPT = (
    'STEP 1/4 manifest: Levels/MP_006CQ src patched_20261023_165152_405\n'
    'baseline: 9/9 files\n'
    'STEP 2/4 v3_trial build cl04_c09_3\n'
    '\n'
    'STEP 3/4 publish\n'
    'STEP 4/4 players.json\n'
    'DEMO cl04_c09_3\n'
    'RESULT: OK cl04_c09_3 published (142 s)\n'
)


def fake_builder(testcase):
    """preview.json "pov_builder": {"exe": <stub builder.exe>} -> (cfg, its folder, exe)."""
    tmp = tempfile.TemporaryDirectory()
    testcase.addCleanup(tmp.cleanup)
    repo = Path(tmp.name)
    exe = repo / 'builder.exe'
    exe.write_bytes(b'MZ stub')
    return {'pov_builder': {'exe': str(exe)}}, repo, exe


def fake_popen(text, rc=0, calls=None):
    class P:
        def __init__(s, cmd, **kw):
            s.cmd, s.kw = cmd, kw
            s.stdout = io.StringIO(text)
            if calls is not None:
                calls.append((cmd, kw))

        def wait(s):
            return rc
    return P


class ListTests(unittest.TestCase):
    def setUp(self):
        self.cfg, self.repo, self.exe = fake_builder(self)

    def test_list_command_and_parse(self):
        calls = []

        def run(cmd, **kw):
            calls.append((cmd, kw))
            return types.SimpleNamespace(returncode=0, stdout=json.dumps(CAPTURES + ['junk']), stderr='')
        cfg = dict(self.cfg, capture_roots=['D:/caps', '', 'E:/more'])
        items = recordings.list_captures(cfg, run=run)
        self.assertEqual([i['name'] for i in items], ['v3-trial-09-arica-online', 'v3-trial-02-x'])
        cmd, kw = calls[0]
        self.assertEqual(cmd[0], str(self.exe))
        self.assertEqual(cmd[1:], ['list', '--json', '--root', 'D:/caps', '--root', 'E:/more'])
        self.assertEqual(kw['cwd'], str(self.repo))

    def test_no_roots(self):
        cmd, _repo = recordings.list_command(self.cfg)
        self.assertNotIn('--root', cmd)

    def test_list_failures(self):
        bad_rc = lambda cmd, **kw: types.SimpleNamespace(returncode=2, stdout='', stderr='Traceback\nKeyError: x\n')
        with self.assertRaisesRegex(RuntimeError, 'rc=2.*KeyError'):
            recordings.list_captures(self.cfg, run=bad_rc)
        not_json = lambda cmd, **kw: types.SimpleNamespace(returncode=0, stdout='{nope', stderr='')
        with self.assertRaisesRegex(RuntimeError, 'not JSON'):
            recordings.list_captures(self.cfg, run=not_json)
        not_list = lambda cmd, **kw: types.SimpleNamespace(returncode=0, stdout='{}', stderr='')
        with self.assertRaisesRegex(RuntimeError, 'array'):
            recordings.list_captures(self.cfg, run=not_list)

    def test_missing_builder(self):
        with self.assertRaises(RuntimeError):
            recordings.list_command({'pov_builder': {'exe': r'C:\nonexistent_step0\builder.exe'}})


class FlatBuilderTests(unittest.TestCase):
    """r262: builder\\ + datapack\\ next to the panel exe; child env on every launch."""

    def setUp(self):
        from replay.test_pov import fake_app_root
        self.root = fake_app_root(self)
        self.b = self.root / 'builder'
        self.cfg = {'server_dir': r'C:\t\srv', 'client_exe': r'C:\t\BFBC2Game.exe'}

    def test_tool_auto_detect(self):
        """Release layout: [builder\\builder.exe] + args."""
        exe = str(self.b / 'builder.exe')
        self.assertEqual(recordings.tool({}, app_root=self.root), ([exe], self.b))
        cmd, repo = recordings.build_command(self.cfg, r'C:\caps\x', app_root=self.root)
        self.assertEqual(cmd, [exe, 'build', r'C:\caps\x', '--publish'])
        self.assertEqual(repo, self.b)
        self.assertEqual(recordings.list_command(dict(self.cfg, capture_roots=['D:/c']), app_root=self.root)[0],
                         [exe, 'list', '--json', '--root', 'D:/c'])
        self.assertEqual(recordings.extract_command(self.cfg, r'C:\r.bc2raw', app_root=self.root)[0],
                         [exe, 'extract', r'C:\r.bc2raw'])
        self.assertEqual(recordings.prune_command(self.cfg, 2, True, True, app_root=self.root)[0],
                         [exe, 'prune', '--keep', '2', '--extracted', '--dry-run'])

    def test_tool_configured_exe_missing(self):
        with self.assertRaisesRegex(RuntimeError, 'Builder not found'):
            recordings.tool({'pov_builder': {'exe': str(self.root / 'nope' / 'builder.exe')}}, app_root=self.root)

    def test_tool_missing_uses_status_text(self):
        from replay import pov
        from replay.test_pov import fake_app_root
        with self.assertRaises(RuntimeError) as ctx:
            recordings.tool({}, app_root=fake_app_root(self, builder=False))
        self.assertEqual(str(ctx.exception), pov.MISSING_BUILDER)

    def test_list_env(self):
        calls = []

        def run(cmd, **kw):
            calls.append((cmd, kw))
            return types.SimpleNamespace(returncode=0, stdout='[]', stderr='')
        self.assertEqual(recordings.list_captures(self.cfg, run=run, app_root=self.root), [])
        env = calls[0][1]['env']
        self.assertEqual((env['PYTHONIOENCODING'], env['BC2_CLIENT_EXE'], env['BC2_DEMOS_DIR'], env['BC2_DATAPACK_DIR'],
                          env['BC2_WORK_DIR'], env['BC2_CACHE_DIR']),
                         ('utf-8', r'C:\t\BFBC2Game.exe', r'C:\t\srv\demos', str(self.root / 'datapack'),
                          str(self.root / 'runtime' / 'builder'), str(self.root / 'runtime' / 'builder' / 'cache')))

    def test_popen_env_build_extract_prune(self):
        calls = []
        recordings.run_build(self.cfg, r'C:\caps\x', lambda p: None, app_root=self.root,
                             popen=fake_popen('DEMO s\nRESULT: OK\n', calls=calls))
        recordings.run_extract(self.cfg, r'C:\r.bc2raw', lambda p: None, app_root=self.root,
                               popen=fake_popen('RESULT: OK C:\\caps\\y\n', calls=calls))
        recordings.run_prune(self.cfg, 3, False, True, lambda p: None, app_root=self.root,
                             popen=fake_popen('RESULT: OK\n', calls=calls))
        self.assertEqual(len(calls), 3)
        for _cmd, kw in calls:
            self.assertEqual(kw['env']['BC2_WORK_DIR'], str(self.root / 'runtime' / 'builder'))
            self.assertEqual(kw['env']['BC2_DEMOS_DIR'], r'C:\t\srv\demos')

    def test_dev_repo_env_has_no_work_dir(self):
        cfg, repo, _exe = fake_builder(self)
        calls = []
        recordings.run_build(dict(cfg, **self.cfg), r'C:\caps\x', lambda p: None, app_root=self.root,
                             popen=fake_popen('DEMO s\nRESULT: OK\n', calls=calls))
        env = calls[0][1]['env']
        self.assertEqual(calls[0][1]['cwd'], str(repo))
        for key in ('BC2_WORK_DIR', 'BC2_CACHE_DIR'):  # dev repo: its default work/private stays (inherited only)
            self.assertEqual(env.get(key), os.environ.get(key))
        self.assertEqual(env['BC2_DATAPACK_DIR'], str(self.root / 'datapack'))
        self.assertFalse((self.root / 'runtime').exists())


class BuildTests(unittest.TestCase):
    def setUp(self):
        self.cfg, self.repo, self.exe = fake_builder(self)

    def test_build_command(self):
        cmd, repo = recordings.build_command(self.cfg, 'C:/x/cap')
        self.assertEqual(cmd, [str(self.exe), 'build', 'C:/x/cap', '--publish'])
        self.assertEqual(repo, self.repo)

    def test_parse_line(self):
        self.assertEqual(recordings.parse_line('DEMO cl04_c09_1\n'), ('demo', 'cl04_c09_1'))
        self.assertEqual(recordings.parse_line('RESULT: OK 9/9'), ('result', True, 'OK 9/9'))
        self.assertEqual(recordings.parse_line('RESULT: FAIL another build is running (pid 7)'),
                         ('result', False, 'FAIL another build is running (pid 7)'))
        self.assertEqual(recordings.parse_line('STEP 2/4 building'), ('step', '2/4 building'))
        self.assertEqual(recordings.parse_line('  baseline ok \r\n'), ('info', 'baseline ok'))
        self.assertEqual(recordings.parse_line('POV_DEMO x'), ('info', 'POV_DEMO x'))  # not this builder's tag

    def test_full_transcript(self):
        seen, calls = [], []
        ok, stem, text = recordings.run_build(self.cfg, 'C:/x/cap', seen.append, popen=fake_popen(TRANSCRIPT, calls=calls))
        self.assertEqual((ok, stem, text), (True, 'cl04_c09_3', 'OK cl04_c09_3 published (142 s)'))
        self.assertEqual([p[0] for p in seen], ['step', 'info', 'step', 'info', 'step', 'step', 'demo', 'result'])
        self.assertEqual(seen[0], ('step', '1/4 manifest: Levels/MP_006CQ src patched_20261023_165152_405'))
        cmd, kw = calls[0]
        self.assertIn('--publish', cmd)
        self.assertEqual(kw['cwd'], str(self.repo))

    def test_fail_and_missing_result(self):
        ok, stem, text = recordings.run_build(self.cfg, 'c', lambda p: None,
                                              popen=fake_popen('STEP 1/4 x\nRESULT: FAIL no data pack for Levels/MP_001\n', rc=1))
        self.assertEqual((ok, stem, text), (False, None, 'FAIL no data pack for Levels/MP_001'))
        ok, stem, text = recordings.run_build(self.cfg, 'c', lambda p: None, popen=fake_popen('Traceback\nboom\n', rc=1))
        self.assertFalse(ok)
        self.assertIn('without RESULT (boom)', text)

    def test_ok_needs_rc0_and_demo(self):
        ok, _stem, text = recordings.run_build(self.cfg, 'c', lambda p: None, popen=fake_popen('RESULT: OK\n'))
        self.assertFalse(ok)
        self.assertIn('no DEMO line', text)
        ok, stem, _text = recordings.run_build(self.cfg, 'c', lambda p: None, popen=fake_popen(TRANSCRIPT, rc=3))
        self.assertFalse(ok)
        self.assertEqual(stem, 'cl04_c09_3')


class FormatTests(unittest.TestCase):
    def test_rows(self):
        self.assertEqual(recordings.format_capture(CAPTURES[0]),
                         ('v3-trial-09-arica-online', '2026-10-23 16:51', 'MP_006CQ', '10:42', '19245', '0',
                          'ready, built: cl04_c09_1, cl04_c09_2'))
        self.assertEqual(recordings.format_capture(CAPTURES[1]),
                         ('v3-trial-02-x', '-', '-', '-', '-', '3', 'no capture.json'))
        self.assertEqual(recordings.format_capture(dict(CAPTURES[0], builds=[]))[-1], 'ready')
        self.assertEqual(recordings.format_capture({'dir': 'C:/a/v3-trial-01', 'buildable': False})[0], 'v3-trial-01')
        self.assertEqual(recordings.format_capture({'buildable': False})[-1], 'not buildable')


RAW = {'kind': 'raw', 'name': '20261027_120000_4242.bc2raw', 'path': 'C:/la/BC2Replay/captures/20261027_120000_4242.bc2raw',
       'bytes': 12 * 1024 * 1024 + 300 * 1024, 'recorded': '2026-10-27T13:00:00', 'level': 'Levels/MP_006CQ',
       'buildable': False, 'reason': 'raw capture: extract first', 'builds': []}


class RawTests(unittest.TestCase):
    def setUp(self):
        self.cfg, self.repo, self.exe = fake_builder(self)

    def test_raw_row(self):
        self.assertTrue(recordings.is_raw(RAW))
        self.assertFalse(recordings.is_raw(CAPTURES[0]) or recordings.is_raw(None) or recordings.is_raw({}))
        self.assertEqual(recordings.format_capture(RAW),
                         ('20261027_120000_4242.bc2raw', '2026-10-27 13:00', 'MP_006CQ', '-', '-', '-',
                          'raw, not extracted, 12.3 MB'))
        self.assertEqual(recordings.status_text(dict(RAW, reason='bad .bc2raw: bad magic', bytes=4)),
                         'raw, not extracted, 1 KB, bad .bc2raw: bad magic')
        self.assertIsNone(recordings.play_stem(RAW))
        self.assertEqual(recordings.item_key(RAW), RAW['path'])
        self.assertEqual(recordings.item_key(CAPTURES[0]), CAPTURES[0]['dir'])
        self.assertEqual(recordings.item_key({'name': 'n'}), 'n')

    def test_format_size(self):
        self.assertEqual([recordings.format_size(n) for n in (None, -1, 0, 100, 2048, 1 << 20, 5 * (1 << 20) // 2)],
                         ['-', '-', '0 KB', '1 KB', '2 KB', '1.0 MB', '2.5 MB'])

    def test_extract_command_and_run(self):
        cmd, repo = recordings.extract_command(self.cfg, RAW['path'])
        self.assertEqual(cmd[1:], ['extract', RAW['path']])
        self.assertEqual(repo, self.repo)
        seen, calls = [], []
        out = ('STEP 1/1 extract 20261027_120000_4242.bc2raw -> C:/r/work/private/cap_20261027_120000_4242\n'
               '  level Levels/MP_006CQ, 17018 packets\n'
               'RESULT: OK C:/r/work/private/cap_20261027_120000_4242\n')
        res = recordings.run_extract(self.cfg, RAW['path'], seen.append, popen=fake_popen(out, calls=calls))
        self.assertEqual(res, (True, 'C:/r/work/private/cap_20261027_120000_4242',
                               'OK C:/r/work/private/cap_20261027_120000_4242'))
        self.assertEqual([p[0] for p in seen], ['step', 'info', 'result'])
        self.assertEqual(calls[0][1]['cwd'], str(self.repo))

    def test_extract_failures(self):
        ok, out, text = recordings.run_extract(self.cfg, 'x', lambda p: None,
                                               popen=fake_popen('RESULT: FAIL C:/r/cap exists; refusing\n', rc=1))
        self.assertEqual((ok, out, text), (False, None, 'FAIL C:/r/cap exists; refusing'))
        ok, out, text = recordings.run_extract(self.cfg, 'x', lambda p: None, popen=fake_popen('RESULT: OK d\n', rc=2))
        self.assertEqual((ok, out, text), (False, None, 'OK d (rc=2)'))
        ok, out, text = recordings.run_extract(self.cfg, 'x', lambda p: None, popen=fake_popen('Traceback\nboom\n', rc=1))
        self.assertFalse(ok)
        self.assertIn('without RESULT (boom)', text)


PROGRESS_TRANSCRIPT = (
    'STEP 1/2 resolve v3-trial-05-local\n'
    'ESTIMATE 23\n'
    'STEP 2/2 build cl04_c05_1 (build, gate, publish; ~0.5-3 min)\n'
    'PROGRESS build 1/3\n'
    'PROGRESS gate 2/3\n'
    'PROGRESS publish 3/3\n'
    'DEMO cl04_c05_1\n'
    'RESULT: OK cl04_c05_1 published (23 s)\n'
)


class ProgressTests(unittest.TestCase):
    def test_parse_progress_estimate(self):
        self.assertEqual(recordings.parse_line('PROGRESS build 2/4\n'), ('progress', 'build', 2, 4))
        self.assertEqual(recordings.parse_line('ESTIMATE 117'), ('estimate', 117.0, {}))
        self.assertEqual(recordings.parse_line('ESTIMATE 12.5\r\n'), ('estimate', 12.5, {}))
        self.assertEqual(recordings.parse_line('ESTIMATE 26 build=5.9 gate=20.3 compare=0.0'),
                         ('estimate', 26.0, {'build': 5.9, 'gate': 20.3, 'compare': 0.0}))
        for bad in ('PROGRESS build', 'PROGRESS build x/4', 'PROGRESS build 5/4', 'PROGRESS build 0/4',
                    'PROGRESS a b 1/2', 'ESTIMATE soon', 'ESTIMATE -3', 'ESTIMATE 5 build',
                    'ESTIMATE 5 build=x', 'ESTIMATE 5 gate=-1'):
            self.assertEqual(recordings.parse_line(bad)[0], 'info', bad)

    def test_run_build_forwards_progress(self):
        seen = []
        cfg, _repo, _exe = fake_builder(self)
        ok, stem, text = recordings.run_build(cfg, 'c', seen.append, popen=fake_popen(PROGRESS_TRANSCRIPT))
        self.assertEqual((ok, stem), (True, 'cl04_c05_1'))
        self.assertTrue(text.startswith('OK cl04_c05_1 published'))
        self.assertIn(('estimate', 23.0, {}), seen)
        self.assertEqual([p for p in seen if p[0] == 'progress'],
                         [('progress', 'build', 1, 3), ('progress', 'gate', 2, 3), ('progress', 'publish', 3, 3)])

    def test_phase_plan_and_weights(self):
        self.assertEqual(recordings.phase_plan(3), ['build', 'gate', 'publish'])
        self.assertEqual(recordings.phase_plan(4), ['soldiers', 'build', 'gate', 'publish'])
        w = recordings.phase_weights(['build', 'gate', 'publish'])
        self.assertEqual([round(x, 4) for x in w], [0.75, 0.22, 0.03])
        w = recordings.phase_weights(['soldiers', 'build', 'gate', 'publish'])
        self.assertEqual([round(x, 4) for x in w], [0.35, 0.4875, 0.143, 0.0195])
        self.assertAlmostEqual(sum(recordings.phase_weights(['build', 'gate'])), 1.0)

    def test_progress_fraction(self):
        f = recordings.progress_fraction
        w = recordings.phase_weights(['build', 'gate', 'publish'])
        self.assertEqual(f(0, 0, 5, 100, w), 0.0)  # nothing announced yet
        self.assertEqual(f(1, 3, 0, 100, w), 0.0)
        self.assertAlmostEqual(f(1, 3, 37.5, 100, w), 0.375)  # half of build's 75 s share
        self.assertAlmostEqual(f(1, 3, 1000, 100, w), 0.75 * 0.97)  # capped inside the phase
        self.assertAlmostEqual(f(2, 3, 0, 100, w), 0.75)  # phase start = earlier phases complete
        self.assertAlmostEqual(f(2, 3, 5, None, w), 0.75)  # no estimate: stays at the phase start
        self.assertLess(f(3, 3, 10 ** 6, 100, w), 1.0)
        self.assertEqual(f(1, 3, 1, 100, w, floor=0.5), 0.5)  # never goes backwards
        self.assertAlmostEqual(f(2, 4, 0, 100, w), 0.25)  # weights do not match k -> equal shares
        prev, values = 0.0, []
        for i, el in ((1, 0), (1, 30), (1, 200), (2, 0), (2, 10), (2, 99), (3, 0), (3, 50)):
            prev = f(i, 3, el, 100, w, floor=prev)
            values.append(prev)
        self.assertEqual(values, sorted(values))

    def test_run_weights_from_recorded_phases(self):
        names = ['build', 'gate', 'publish']
        weights, total = recordings.run_weights(names, {'build': 5.9, 'gate': 20.3, 'publish': 0.0})
        self.assertAlmostEqual(total, 26.7)  # publish counts as MIN_PHASE_S
        self.assertAlmostEqual(weights[1], 20.3 / 26.7)
        f = recordings.progress_fraction
        self.assertAlmostEqual(f(1, 3, 2.95, total, weights), 2.95 / 26.7)  # build advances by its own 5.9 s
        self.assertAlmostEqual(f(2, 3, 0, total, weights), 5.9 / 26.7)  # gate starts where build's share ends
        self.assertLess(f(3, 3, 100, total, weights), 1.0)
        fixed = recordings.phase_weights(names)
        self.assertEqual(recordings.run_weights(names, {}), (fixed, None))
        self.assertEqual(recordings.run_weights(names, {'build': 5.0, 'gate': 2.0}), (fixed, None))  # incomplete

    def test_progress_label(self):
        self.assertEqual(recordings.progress_label('build', 45.7, 120), 'build 45 s / ~120 s')
        self.assertEqual(recordings.progress_label(None, 3, None), 'starting 3 s')
        self.assertEqual(recordings.progress_label('gate', 50, 120, failed=True), 'failed in gate after 50 s')


class RecordedAndPlayTests(unittest.TestCase):
    def test_format_recorded(self):
        self.assertEqual(recordings.format_recorded('2026-10-27T21:05:33'), '2026-10-27 21:05')
        self.assertEqual(recordings.format_recorded(None), '-')
        self.assertEqual(recordings.format_recorded('2026-10-27'), '2026-10-27')

    def test_sort_newest(self):
        items = [{'name': 'a', 'recorded': '2026-10-20T10:00:00'}, {'name': 'b', 'recorded': None},
                 {'name': 'c', 'recorded': '2026-10-27T09:00:00'}, {'name': 'd', 'recorded': '2026-10-21T00:00:00'}]
        self.assertEqual([i['name'] for i in recordings.sort_newest(items)], ['c', 'd', 'a', 'b'])

    def test_play_stem(self):
        item = CAPTURES[0]
        self.assertEqual(recordings.play_stem(item), 'cl04_c09_2')  # newest built demo
        self.assertEqual(recordings.play_stem(item, {item['dir']: 'cl04_c09_3'}), 'cl04_c09_3')  # just built
        self.assertEqual(recordings.play_stem(item, {'other': 'x'}), 'cl04_c09_2')
        self.assertIsNone(recordings.play_stem(CAPTURES[1]))
        self.assertEqual(recordings.play_stem(CAPTURES[1], {CAPTURES[1]['dir']: 'cl04_c02_1'}), 'cl04_c02_1')
        self.assertIsNone(recordings.play_stem(None))
        self.assertIsNone(recordings.play_stem(dict(item, builds=['', None])))

    def test_play_event(self):
        self.assertEqual(recordings.play_event('cl04_c09_2'), ('recordings_play', 'cl04_c09_2'))


class BusyTests(unittest.TestCase):
    def test_busy_reason(self):
        self.assertIsNone(recordings.busy_reason({'server': None, 'client': None}))
        self.assertIsNone(recordings.busy_reason({}))
        self.assertIsNone(recordings.busy_reason(None))
        self.assertIn('replay server', recordings.busy_reason({'server': True, 'client': False}))
        both = recordings.busy_reason({'server': {'pid': 1}, 'client': {'pid': 2}})
        self.assertIn('replay server and a BC2 client', both)
        self.assertIn('live test', both)
        self.assertIn('already running', recordings.busy_reason({}, building=True))

    def test_privacy_note(self):
        self.assertIn('chat', recordings.PRIVACY_NOTE)


PRUNE_DRY = (
    'STEP 1/1 prune C:/la/BC2Replay/captures (5 raw files, 40000000 bytes)\n'
    'KEEP 20261027_120000_1.bc2raw 10000000 newest 2\n'
    'KEEP 20261026_120000_2.bc2raw 9000000 newest 2\n'
    'WOULD-DELETE 20261025_120000_3.bc2raw 8000000 older than the newest 2\n'
    'WOULD-DELETE 20261024_120000_4.bc2raw 7000000 already extracted to cap_4\n'
    'SKIP 20261028_120000_5.bc2raw 6000000 file in use\n'
    'RESULT: OK would delete 2 of 5 raw files, 15000000 bytes, kept 2, skipped 1\n'
)
PRUNE_REAL = PRUNE_DRY.replace('WOULD-DELETE', 'DELETE').replace(
    'RESULT: OK would delete 2 of 5 raw files, 15000000 bytes, kept 2, skipped 1',
    'RESULT: OK deleted 2 of 5 raw files, 15000000 bytes freed, kept 2, skipped 1')


class RetentionTests(unittest.TestCase):
    def setUp(self):
        self.cfg, self.repo, self.exe = fake_builder(self)

    def test_retention_defaults_and_junk(self):
        self.assertEqual(recordings.retention(None), {'keep_raws': 0, 'delete_extracted': False})
        self.assertEqual(recordings.retention({}), {'keep_raws': 0, 'delete_extracted': False})
        self.assertEqual(recordings.retention({'captures': 'junk'}), {'keep_raws': 0, 'delete_extracted': False})
        self.assertEqual(recordings.retention({'captures': {'keep_raws': 5, 'delete_extracted': True}}),
                         {'keep_raws': 5, 'delete_extracted': True})
        for junk in (None, 'x', [], {}, True, -4, 1.5, '3.2'):
            self.assertEqual(recordings.retention({'captures': {'keep_raws': junk}})['keep_raws'], 0, junk)
        self.assertEqual(recordings.retention({'captures': {'keep_raws': '7'}})['keep_raws'], 7)
        self.assertEqual(recordings.retention({'captures': {'keep_raws': 5000}})['keep_raws'], 999)
        self.assertFalse(recordings.retention({'captures': {'delete_extracted': 'yes'}})['delete_extracted'])

    def test_retention_update(self):
        cfg = {'captures': {'keep_raws': 3, 'other': 1}, 'server_dir': 'x'}
        self.assertEqual(recordings.retention_update(cfg, 10, True),
                         {'captures': {'keep_raws': 10, 'delete_extracted': True, 'other': 1}})
        self.assertEqual(recordings.retention_update({}, -2, 0), {'captures': {'keep_raws': 0, 'delete_extracted': False}})
        self.assertEqual(recordings.retention_update(None, 1000, False)['captures']['keep_raws'], 999)
        self.assertEqual(recordings.retention_update({}, 'abc', 1)['captures'], {'keep_raws': 0, 'delete_extracted': True})
        self.assertEqual(cfg['captures'], {'keep_raws': 3, 'other': 1})  # input not mutated

    def test_prune_enabled(self):
        self.assertFalse(recordings.prune_enabled({'keep_raws': 0, 'delete_extracted': False}))
        self.assertFalse(recordings.prune_enabled(None))
        self.assertTrue(recordings.prune_enabled({'keep_raws': 1, 'delete_extracted': False}))
        self.assertTrue(recordings.prune_enabled({'keep_raws': 0, 'delete_extracted': True}))

    def test_prune_command(self):
        cmd, repo = recordings.prune_command(self.cfg, 0, False, False)
        self.assertEqual(repo, self.repo)
        self.assertEqual(cmd, [str(self.exe), 'prune'])
        self.assertEqual(recordings.prune_command(self.cfg, 5, False, False)[0][1:], ['prune', '--keep', '5'])
        self.assertEqual(recordings.prune_command(self.cfg, 0, True, True)[0][1:], ['prune', '--extracted', '--dry-run'])
        cfg = dict(self.cfg, capture_roots=['D:/caps'])
        self.assertEqual(recordings.prune_command(cfg, 2, True, True)[0][1:],
                         ['prune', '--root', 'D:/caps', '--keep', '2', '--extracted', '--dry-run'])

    def test_parse_prune_lines(self):
        p = recordings.parse_line
        self.assertEqual(p('DELETE a.bc2raw 123 older than the newest 3\r\n'),
                         ('prune', 'DELETE', 'a.bc2raw', 123, 'older than the newest 3'))
        self.assertEqual(p('WOULD-DELETE a.bc2raw 5 extracted'), ('prune', 'WOULD-DELETE', 'a.bc2raw', 5, 'extracted'))
        self.assertEqual(p('SKIP b.bc2raw 7 file in use'), ('prune', 'SKIP', 'b.bc2raw', 7, 'file in use'))
        self.assertEqual(p('KEEP c.bc2raw 9'), ('prune', 'KEEP', 'c.bc2raw', 9, ''))
        for bad in ('DELETE', 'DELETE a.bc2raw', 'DELETE a.bc2raw x reason', 'SKIP a -1 r', 'DELETED a 1 r', 'keep a 1 r'):
            self.assertEqual(p(bad)[0], 'info', bad)
        self.assertEqual(p('STEP 1/1 prune C:/x (3 raw files, 9 bytes)'), ('step', '1/1 prune C:/x (3 raw files, 9 bytes)'))

    def test_run_prune_dry_and_real(self):
        seen, calls = [], []
        ok, entries, text = recordings.run_prune(self.cfg, 2, True, True, seen.append,
                                                 popen=fake_popen(PRUNE_DRY, calls=calls))
        self.assertTrue(ok)
        self.assertEqual(text, 'OK would delete 2 of 5 raw files, 15000000 bytes, kept 2, skipped 1')
        self.assertEqual([e[1] for e in entries], ['KEEP', 'KEEP', 'WOULD-DELETE', 'WOULD-DELETE', 'SKIP'])
        self.assertEqual([p[0] for p in seen], ['step'] + ['prune'] * 5 + ['result'])
        self.assertEqual(calls[0][0][1:], ['prune', '--keep', '2', '--extracted', '--dry-run'])
        self.assertEqual(calls[0][1]['cwd'], str(self.repo))
        calls = []
        ok, entries, text = recordings.run_prune(self.cfg, 2, True, False, lambda p: None,
                                                 popen=fake_popen(PRUNE_REAL, calls=calls))
        self.assertTrue(ok)
        self.assertTrue(text.startswith('OK deleted 2 of 5'))
        self.assertNotIn('--dry-run', calls[0][0])
        self.assertEqual(recordings.prune_summary(entries)['delete'], 2)

    def test_run_prune_failures(self):
        ok, entries, text = recordings.run_prune(self.cfg, 1, False, True, lambda p: None,
                                                 popen=fake_popen('RESULT: FAIL capture folder missing\n', rc=1))
        self.assertEqual((ok, entries, text), (False, [], 'FAIL capture folder missing'))
        ok, _e, text = recordings.run_prune(self.cfg, 1, False, True, lambda p: None,
                                            popen=fake_popen('RESULT: OK nothing to delete (0 raw files)\n', rc=2))
        self.assertEqual((ok, text), (False, 'OK nothing to delete (0 raw files) (rc=2)'))
        ok, _e, text = recordings.run_prune(self.cfg, 1, False, True, lambda p: None,
                                            popen=fake_popen('Traceback\nboom\n', rc=1))
        self.assertFalse(ok)
        self.assertIn('without RESULT (boom)', text)

    def test_prune_summary(self):
        _ok, entries, _t = recordings.run_prune(self.cfg, 2, True, True, lambda p: None, popen=fake_popen(PRUNE_DRY))
        self.assertEqual(recordings.prune_summary(entries),
                         {'delete': 2, 'delete_bytes': 15000000, 'skip': 1, 'skip_bytes': 6000000,
                          'keep': 2, 'keep_bytes': 19000000})
        self.assertEqual(recordings.prune_summary([])['delete'], 0)

    def test_prune_confirm_text(self):
        _ok, entries, _t = recordings.run_prune(self.cfg, 2, True, True, lambda p: None, popen=fake_popen(PRUNE_DRY))
        text = recordings.prune_confirm_text(entries, 2, True)
        lines = text.splitlines()
        self.assertIn('extracted', lines[0])
        self.assertIn('newest 2', lines[0])
        self.assertIn('20261025_120000_3.bc2raw  (7.6 MB)  older than the newest 2', lines)
        self.assertIn('1 file(s) skipped: file in use', lines)
        self.assertEqual(lines[-1], 'Delete 2 file(s), 14.3 MB?')
        self.assertNotIn('KEEP', text)
        self.assertNotIn('20261027_120000_1.bc2raw', text)  # kept files are not listed

    def test_prune_confirm_truncates_and_none(self):
        many = [('prune', 'WOULD-DELETE', f'f{i:02d}.bc2raw', 1 << 20, 'old') for i in range(15)]
        lines = recordings.prune_confirm_text(many, 3, False).splitlines()
        self.assertEqual(lines[0], 'Policy: keep the newest 3 raw file(s), delete older ones.')
        self.assertEqual(sum(1 for line in lines if line.startswith('f')), 12)
        self.assertIn('f11.bc2raw  (1.0 MB)  old', lines)
        self.assertNotIn('f12.bc2raw  (1.0 MB)  old', lines)
        self.assertIn('… and 3 more', lines)
        self.assertEqual(lines[-1], 'Delete 15 file(s), 15.0 MB?')
        self.assertIsNone(recordings.prune_confirm_text([], 3, False))
        self.assertIsNone(recordings.prune_confirm_text(
            [('prune', 'KEEP', 'a', 1, 'newest'), ('prune', 'SKIP', 'b', 2, 'in use')], 1, True))
        self.assertEqual(recordings.policy_text(0, True), 'Policy: delete raw files already extracted.')


class SaveSettingsTests(unittest.TestCase):
    def test_save_settings_merges(self):
        import preview_config
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        path = Path(tmp.name) / 'preview.json'
        orig = preview_config.CONFIG
        preview_config.CONFIG = path
        self.addCleanup(setattr, preview_config, 'CONFIG', orig)
        preview_config.save_settings({'captures': {'keep_raws': 1, 'delete_extracted': False}})  # missing file
        self.assertEqual(json.loads(path.read_text(encoding='utf-8')),
                         {'captures': {'keep_raws': 1, 'delete_extracted': False}})
        path.write_text(json.dumps({'server_dir': 'S', 'client_exe': 'C', 'captures': {'keep_raws': 1}}), encoding='utf-8')
        preview_config.save_settings(recordings.retention_update({'captures': {'keep_raws': 1}}, 4, True))
        self.assertEqual(json.loads(path.read_text(encoding='utf-8')),
                         {'server_dir': 'S', 'client_exe': 'C', 'captures': {'keep_raws': 4, 'delete_extracted': True}})
        self.assertFalse(path.with_suffix('.json.tmp').exists())


ARICA = {'kind': 'capture', 'dir': 'C:/w/cap_15884', 'name': 'cap_15884', 'level': 'Levels/MP_006CQ',
         'seconds': 347.4, 'packets': 10423, 'drops': 0, 'recorded': '2026-10-30T10:35:57', 'buildable': True,
         'reason': None, 'reference': None, 'default_round': 1,
         'rounds': [{'round': 1, 'lo': 0, 'hi': 10416, 'packets': 10416, 'seconds': 347.2, 'level': 'Levels/MP_006CQ'},
                    {'round': 2, 'lo': 10416, 'hi': 10416, 'packets': 0, 'seconds': 0.0, 'level': 'Levels/MP_006CQ'},
                    {'round': 3, 'lo': 10416, 'hi': None, 'packets': 7, 'seconds': 0.2, 'level': 'Levels/MP_005'}],
         'builds': ['cl04_c01_1', {'stem': 'cl04_c01r3_1', 'round': 3}, {'stem': 'cl04_c01r1_2', 'round': 1}]}


class RoundRowTests(unittest.TestCase):
    """r287: one row per non-empty round, play stem per round, auto-extract choice."""

    def test_round_rows(self):
        rows = recordings.round_rows(ARICA)
        self.assertEqual([r['round'] for r in rows], [1, 3])  # empty round 2 hidden
        self.assertEqual([r['name'] for r in rows], ['cap_15884 · round 1', 'cap_15884 · round 3'])
        self.assertEqual([r['key'] for r in rows], ['C:/w/cap_15884#r1', 'C:/w/cap_15884#r3'])
        self.assertEqual([r['short'] for r in rows], [False, True])
        self.assertEqual(recordings.format_row(rows[0]),
                         ('cap_15884 · round 1', '2026-10-30 10:35', 'MP_006CQ', '5:47', '10416', '0',
                          'ready, built: cl04_c01_1, cl04_c01r1_2'))
        self.assertEqual(recordings.format_row(rows[1])[2:], ('MP_005', '0:00', '7', '0', 'too short'))

    def test_single_round_and_old_items(self):
        one = dict(ARICA, rounds=[dict(ARICA['rounds'][0]), dict(ARICA['rounds'][1])], builds=[])
        rows = recordings.round_rows(one)
        self.assertEqual([(r['name'], r['round']) for r in rows], [('cap_15884', 1)])  # no suffix: one round left
        self.assertEqual(recordings.format_row(rows[0])[-1], 'ready, not built yet')
        old = recordings.round_rows(CAPTURES[0])  # pre-r287 builder: no "rounds"
        self.assertEqual([(r['key'], r['round'], r['short']) for r in old], [(CAPTURES[0]['dir'], None, False)])
        self.assertEqual(recordings.row_play_stem(old[0]), 'cl04_c09_2')
        raw = recordings.round_rows(RAW)
        self.assertEqual((raw[0]['round'], raw[0]['short'], raw[0]['key']), (None, False, RAW['path']))
        self.assertIsNone(recordings.row_play_stem(raw[0]))
        self.assertFalse(recordings.can_build(raw[0]))
        self.assertEqual(recordings.round_rows(dict(ARICA, rounds=[ARICA['rounds'][1]])), [])  # all empty
        self.assertTrue(recordings.round_rows(dict(CAPTURES[0], seconds=12.0))[0]['short'])

    def test_rows_of_order(self):
        items = [CAPTURES[0], ARICA, RAW]
        self.assertEqual([r['key'] for r in recordings.rows_of(items)],
                         ['C:/w/cap_15884#r1', 'C:/w/cap_15884#r3', RAW['path'], CAPTURES[0]['dir']])

    def test_play_stem_per_round(self):
        r1, r3 = recordings.round_rows(ARICA)
        self.assertEqual(recordings.round_stems(ARICA, 1), ['cl04_c01_1', 'cl04_c01r1_2'])  # null round = default 1
        self.assertEqual(recordings.round_stems(ARICA, 3), ['cl04_c01r3_1'])
        self.assertEqual(recordings.round_stems(ARICA, 2), [])
        self.assertEqual(recordings.row_play_stem(r1), 'cl04_c01r1_2')
        self.assertEqual(recordings.row_play_stem(r3), 'cl04_c01r3_1')
        self.assertEqual(recordings.row_play_stem(r1, {r1['key']: 'cl04_c01r1_3'}), 'cl04_c01r1_3')
        self.assertEqual(recordings.row_play_stem(r1, {r3['key']: 'x'}), 'cl04_c01r1_2')
        none = recordings.round_rows(dict(ARICA, builds=[]))[0]
        self.assertIsNone(recordings.row_play_stem(none))
        self.assertTrue(recordings.can_build(none))
        self.assertFalse(recordings.can_build(r3))  # too short
        self.assertFalse(recordings.can_build(recordings.round_rows(dict(ARICA, buildable=False))[0]))
        self.assertIsNone(recordings.row_play_stem(None))
        self.assertEqual(recordings.build_entries({'builds': ['', None, {'stem': 'a', 'round': True}, 7]}),
                         [('a', None)])
        self.assertEqual(recordings.play_stem(ARICA), 'cl04_c01r1_2')  # pre-r287 helper copes with dict builds

    def test_hide_patterns(self):
        self.assertEqual(recordings.hide_patterns({}), ['v3-trial-*'])
        self.assertEqual(recordings.hide_patterns(None), ['v3-trial-*'])
        self.assertEqual(recordings.hide_patterns({'recordings_hide': 'x'}), ['v3-trial-*'])  # not a list
        self.assertEqual(recordings.hide_patterns({'recordings_hide': []}), [])
        self.assertEqual(recordings.hide_patterns({'recordings_hide': ['cap_r248_*', 7, ' ']}), ['cap_r248_*'])
        items = [dict(ARICA), dict(ARICA, name='v3-trial-05-local'), dict(ARICA, name='cap_r248_t09'), dict(RAW), {}]
        self.assertEqual([i.get('name') for i in recordings.visible_captures(items, {})],
                         ['cap_15884', 'cap_r248_t09', RAW['name'], None])
        self.assertEqual(len(recordings.visible_captures(items, {'recordings_hide': []})), 5)
        self.assertEqual([i.get('name') for i in recordings.visible_captures(items, {'recordings_hide': ['CAP_*', '*.bc2raw']})],
                         ['v3-trial-05-local', None])  # case-insensitive, raws too

    def test_build_command_round(self):
        cfg, _repo, _exe = fake_builder(self)
        self.assertEqual(recordings.build_command(cfg, 'C:/x/cap', round_k=2)[0][1:],
                         ['build', 'C:/x/cap', '--publish', '--round', '2'])
        self.assertEqual(recordings.build_command(cfg, 'C:/x/cap')[0][1:], ['build', 'C:/x/cap', '--publish'])
        calls = []
        recordings.run_build(cfg, 'c', lambda p: None, popen=fake_popen(TRANSCRIPT, calls=calls), round_k=3)
        self.assertEqual(calls[0][0][-2:], ['--round', '3'])

    def test_raws_to_extract(self):
        now = 10_000.0
        mt = {'a': now - 600, 'b': now - 30, 'c': now - 900, 'd': None, 'e': now - 61}
        raws = [dict(RAW, path=p, name=p) for p in 'abcde']
        bad = dict(RAW, path='f', reason='bad .bc2raw: bad magic')
        unknown = lambda p: None  # writer state unknown -> the RAW_SETTLE_S mtime rule
        pick = recordings.raws_to_extract(raws + [bad, CAPTURES[0]], now, mtime=mt.get, closed=unknown)
        self.assertEqual([i['path'] for i in pick], ['c', 'a', 'e'])  # oldest first; b recent, d unknown, f bad
        self.assertEqual([i['path'] for i in recordings.raws_to_extract(raws, now, tried={'c'}, mtime=mt.get,
                                                                         closed=unknown)], ['a', 'e'])
        self.assertEqual(recordings.raws_to_extract([], now), [])
        self.assertEqual(recordings.raws_to_extract([dict(RAW, path='Z:/no/such.bc2raw')], now), [])  # no mtime
        # writer state known: closed -> at once (b, 30 s old), open -> never (a, 600 s old)
        state = {'a': False, 'b': True, 'c': None, 'e': True}
        pick = recordings.raws_to_extract(raws, now, mtime=mt.get, closed=state.get)
        self.assertEqual([i['path'] for i in pick], ['c', 'e', 'b'])

    @unittest.skipUnless(os.name == 'nt', 'Windows share modes')
    def test_writer_closed(self):
        import ctypes
        from ctypes import wintypes
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, 'x.bc2raw')
            self.assertIsNone(recordings.writer_closed(p))  # missing file
            k32 = ctypes.WinDLL('kernel32', use_last_error=True)
            k32.CreateFileW.restype = wintypes.HANDLE
            k32.CreateFileW.argtypes = (wintypes.LPCWSTR, wintypes.DWORD, wintypes.DWORD, wintypes.LPVOID,
                                        wintypes.DWORD, wintypes.DWORD, wintypes.HANDLE)
            h = k32.CreateFileW(p, 0x40000000, 0x1, None, 1, 0x80, None)  # like bc2_capture_open: WRITE, share READ
            self.assertFalse(recordings.writer_closed(p))
            k32.CloseHandle(wintypes.HANDLE(h))
            self.assertTrue(recordings.writer_closed(p))


class UiImportTests(unittest.TestCase):
    def test_ui_imports(self):
        import replay.ui as ui
        self.assertTrue(callable(ui.main))
        self.assertTrue(callable(ui.show_recordings))


if __name__ == '__main__':
    unittest.main()
