"""r234 offline tests for replay.pov (player list, builder resolution / env) + ui import.

Run from the repo root:  python -m unittest replay.test_pov -v
"""
import json
import tempfile
import types
import unittest
from pathlib import Path

from replay import pov

PLAYERS = {
    'version': 1, 'demo': 'cl04_1097', 'game_start_frame': 2008,
    'viewer': {'demo_id': 167, 'name': 'viewer_a'},
    'players': [
        {'demo_id': 249, 'name': 'CopoLY'},
        {'demo_id': 248, 'name': 'Davorich'},
        {'demo_id': 233, 'name': 'HybrisTM'},
        {'demo_id': 241, 'name': 'BBC_750'},
    ],
}


class ListTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.srv = Path(self.tmp.name)
        (self.srv / 'demos').mkdir()
        (self.srv / 'demos' / 'cl04_1097.players.json').write_text(json.dumps(PLAYERS, ensure_ascii=False), encoding='utf-8')

    def tearDown(self):
        self.tmp.cleanup()

    def test_load_next_to_demo(self):
        d = pov.load_players(self.srv, 'demos/cl04_1097')
        self.assertEqual([p['name'] for p in d['players']], ['CopoLY', 'Davorich', 'HybrisTM', 'BBC_750'])
        self.assertIsNone(pov.load_players(self.srv, 'demos/cl04_1088'))

    def test_bad_json_is_none(self):
        (self.srv / 'demos' / 'bad.players.json').write_text('{nope', encoding='utf-8')
        self.assertIsNone(pov.load_players(self.srv, 'demos/bad'))

    def test_mmss(self):
        self.assertEqual(pov.mmss(66.87), '1:07')
        self.assertEqual(pov.mmss(None), 'end')


ENV_KEYS = ('PYTHONIOENCODING', 'BC2_CLIENT_EXE', 'BC2_DEMOS_DIR', 'BC2_DATAPACK_DIR', 'BC2_WORK_DIR', 'BC2_CACHE_DIR')


GO_VERSION = {'name': 'bc2-builder', 'engine': 'go', 'version': '0.1.0-preview.2', 'go': 'go1.27.3', 'format': 1}


def fake_version_run(data=GO_VERSION, rc=0, calls=None):
    """subprocess.run stand-in answering 'builder.exe version --json'."""
    def run(cmd, **kw):
        if calls is not None:
            calls.append((cmd, kw))
        return types.SimpleNamespace(returncode=rc, stdout=json.dumps(data), stderr='')
    return run


def fake_app_root(testcase, builder=True, datapack=True):
    """Temp app root with a fake flat builder\\ (builder.exe stub + builder.json, the release layout) and datapack\\."""
    tmp = tempfile.TemporaryDirectory()
    testcase.addCleanup(tmp.cleanup)
    root = Path(tmp.name)
    b = root / pov.BUILDER_DIR
    if builder:
        b.mkdir()
        (b / pov.BUILDER_EXE).write_bytes(b'MZ stub')
        (b / 'builder.json').write_text(json.dumps({'format': 2, 'runtime': 'go', 'go': 'go1.27.3',
                                                     'version': '0.1.0-preview.2'}), encoding='utf-8')
    if datapack:
        (root / pov.DATAPACK_DIR / pov.DATAPACK_MARKER).parent.mkdir(parents=True)
        (root / pov.DATAPACK_DIR / pov.DATAPACK_MARKER).write_text('{}', encoding='utf-8')
    return root


def other_dir(testcase):
    tmp = tempfile.TemporaryDirectory()
    testcase.addCleanup(tmp.cleanup)
    return Path(tmp.name)


class FlatBuilderTests(unittest.TestCase):
    """r262: the release zip's builder\\ + datapack\\ next to the panel exe."""

    def test_auto_detect(self):
        root = fake_app_root(self)
        self.assertEqual(pov.resolve_builder({}, app_root=root),
                         {'repo': root / 'builder', 'exe': root / 'builder' / 'builder.exe',
                          'flat': True, 'configured': False})

    def test_folder_without_exe_is_missing(self):
        root = fake_app_root(self, builder=False)
        (root / 'builder').mkdir()
        (root / 'builder' / 'v3_captures.py').write_text('', encoding='utf-8')  # an old Python builder folder
        self.assertIsNone(pov.resolve_builder({}, app_root=root)['repo'])

    def test_explicit_exe_wins(self):
        root = fake_app_root(self)
        exe = other_dir(self) / 'builder.exe'
        cfg = {'pov_builder': {'exe': str(exe), 'repo': 'ignored'}}
        self.assertEqual(pov.resolve_builder(cfg, app_root=root),
                         {'repo': exe.parent, 'exe': exe, 'flat': False, 'configured': True})

    def test_missing_builder_message(self):
        root = fake_app_root(self, builder=False)
        self.assertIsNone(pov.resolve_builder({}, app_root=root)['repo'])
        self.assertRegex(pov.builder_status({}, app_root=root)[1],
                         r'builder\\ with builder.exe next to BC2LocalReplay')

    def test_env_flat(self):
        root = fake_app_root(self)
        cfg = {'server_dir': r'C:\t\srv', 'client_exe': r'C:\t\BFBC2Game.exe'}
        base = {'PATH': 'x', 'BC2_WORK_DIR': 'stale', 'PYTHONIOENCODING': 'cp1252'}
        env = pov.builder_env(cfg, app_root=root, environ=base)
        self.assertEqual({k: env[k] for k in ENV_KEYS}, {
            'PYTHONIOENCODING': 'utf-8', 'BC2_CLIENT_EXE': r'C:\t\BFBC2Game.exe', 'BC2_DEMOS_DIR': r'C:\t\srv\demos',
            'BC2_DATAPACK_DIR': str(root / 'datapack'), 'BC2_WORK_DIR': str(root / 'runtime' / 'builder'),
            'BC2_CACHE_DIR': str(root / 'runtime' / 'builder' / 'cache')})
        self.assertEqual(set(env), set(ENV_KEYS) | {'PATH'})
        self.assertTrue((root / 'runtime' / 'builder' / 'cache').is_dir())
        self.assertEqual(base['BC2_WORK_DIR'], 'stale')  # the given environ is not mutated

    def test_env_configured_exe_keeps_its_work_dir(self):
        root = fake_app_root(self, datapack=False)
        exe = other_dir(self) / 'builder.exe'
        cfg = {'pov_builder': {'exe': str(exe)}, 'server_dir': r'C:\t\srv'}
        env = pov.builder_env(cfg, app_root=root, environ={})
        self.assertEqual(env, {'PYTHONIOENCODING': 'utf-8', 'BC2_DEMOS_DIR': r'C:\t\srv\demos'})
        self.assertFalse((root / 'runtime').exists())
        cfg['pov_builder'].update(work_dir=str(root / 'w'), cache_dir=str(root / 'c'), datapack=str(root / 'dp'))
        env = pov.builder_env(cfg, app_root=root, environ={})
        self.assertEqual((env['BC2_WORK_DIR'], env['BC2_CACHE_DIR'], env['BC2_DATAPACK_DIR']),
                         (str(root / 'w'), str(root / 'c'), str(root / 'dp')))

    def test_env_no_builder(self):
        root = fake_app_root(self, builder=False, datapack=False)
        self.assertEqual(pov.builder_env({}, app_root=root, environ={}), {'PYTHONIOENCODING': 'utf-8'})

    def test_status(self):
        root = fake_app_root(self)
        calls = []
        self.assertEqual(pov.builder_status({}, app_root=root, run=fake_version_run(calls=calls)),
                         (True, 'Builder: builder\\ (Go builder 0.1.0-preview.2, go1.27.3) + datapack\\ found next to '
                                'the panel.'))
        self.assertEqual(calls[0][0], [str(root / 'builder' / 'builder.exe'), 'version', '--json'])
        self.assertEqual(calls[0][1]['timeout'], pov.VERSION_TIMEOUT_S)
        pov.builder_status({}, app_root=root, run=fake_version_run(calls=calls))
        self.assertEqual(len(calls), 1)  # cached per exe (path, size, mtime)
        self.assertEqual(pov.builder_status({}, app_root=fake_app_root(self), run=fake_version_run(rc=1)),
                         (True, 'Builder: builder\\ + datapack\\ found next to the panel.'))
        self.assertEqual(pov.builder_status({}, app_root=fake_app_root(self, datapack=False)),
                         (False, pov.MISSING_DATAPACK))
        self.assertEqual(pov.builder_status({}, app_root=fake_app_root(self, builder=False)),
                         (False, pov.MISSING_BUILDER))
        self.assertEqual(pov.MISSING_DATAPACK,
                         'Data pack not found: expected datapack\\baselines\\baselines.json next to the panel.')
        exe = root / 'builder' / 'my.exe'
        self.assertFalse(pov.builder_status({'pov_builder': {'exe': str(exe)}}, app_root=root)[0])
        exe.write_bytes(b'MZ')
        self.assertEqual(pov.builder_status({'pov_builder': {'exe': str(exe)}}, app_root=root, run=fake_version_run()),
                         (True, f'Builder: {exe} (Go builder 0.1.0-preview.2, go1.27.3) (configured in preview.json)'))


class UiImportTests(unittest.TestCase):
    def test_ui_imports(self):
        import replay.ui as ui
        self.assertTrue(callable(ui.main))


if __name__ == '__main__':
    unittest.main()
