"""Offline tests for replay.first_run (Check setup). Temp folders and mocks only; never touches the
real game/server folder or %LOCALAPPDATA%. replay.client_install is never imported here (proxy_status
is patched).

Run from the repo root:  python -m unittest replay.test_first_run -v
"""
import hashlib
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import preview_config
from replay import first_run as fr
from replay.client_install import STATES

EXE = b'MZ fake BFBC2Game.exe bytes'
EXE_HASH = hashlib.sha256(EXE).hexdigest()


def proxy(state, text='proxy text'):
    return mock.patch.object(fr, 'proxy_status', return_value={'state': state, 'text': text})


def server_proxy(state='installed', text='Client proxy installed (sha 12345678, 9 B), Rome chain ok.'):
    return mock.patch.object(fr, 'folder_proxy_status', return_value={'state': state, 'text': text})


def builder(ok=True, text='Builder: builder\\ + datapack\\ found next to the panel.'):
    return mock.patch('replay.pov.builder_status', return_value=(ok, text))


class Base(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.game = self.root / 'game'
        self.game.mkdir()
        self.exe = self.game / 'BFBC2Game.exe'
        self.exe.write_bytes(EXE)
        self.server = self.root / 'server'
        self.server.mkdir()
        (self.server / preview_config.ORIGINAL_NAME).write_bytes(b'MZ server')
        self.lad = self.root / 'lad'
        p = mock.patch.object(preview_config, 'CLIENT_HASH', EXE_HASH)
        p.start()
        self.addCleanup(p.stop)
        self.cfg = {'client_exe': str(self.exe), 'server_dir': str(self.server)}

    def run_checks(self, cfg=None, state='installed', builder_ok=True, server_state='installed'):
        with proxy(state), server_proxy(server_state), builder(builder_ok):
            return fr.checks(self.cfg if cfg is None else cfg, self.root, local_app_data=str(self.lad))


class ChecksTests(Base):
    def test_all_ok_in_order(self):
        res = self.run_checks()
        self.assertEqual([r['name'] for r in res], list(fr.NAMES))
        self.assertTrue(all(r['ok'] is True for r in res), res)
        self.assertEqual(res[0]['text'], f'BC2 client EXE verified (BFBC2Game.exe, sha {EXE_HASH[:8]})')
        self.assertEqual(res[2]['text'], 'Server folder ok: Frost.Game.Main_Win32_Final.exe found')
        self.assertEqual(res[3]['text'], 'Server folder proxy: Proxy installed (sha 12345678, 9 B), Rome chain ok.')
        self.assertEqual(res[5]['text'],
                         '%LOCALAPPDATA%\\BC2Replay writable (captures, logs and crash dumps go there)')
        self.assertTrue((self.lad / 'BC2Replay').is_dir())
        self.assertFalse((self.lad / 'BC2Replay' / '.write_test').exists())

    def test_client_not_set(self):
        res = self.run_checks({'server_dir': str(self.server)})
        self.assertIs(res[0]['ok'], False)
        self.assertIn('not set', res[0]['text'])
        self.assertIsNone(res[1]['ok'])  # proxy not applicable without a client
        self.assertEqual(res[1]['text'], 'Set the BC2 client EXE to check the proxy.')

    def test_client_missing_file(self):
        res = self.run_checks(dict(self.cfg, client_exe=str(self.game / 'nope.exe')))
        self.assertIs(res[0]['ok'], False)
        self.assertIn('file not found', res[0]['text'])

    def test_client_wrong_hash(self):
        self.exe.write_bytes(b'MZ other build')
        other = hashlib.sha256(b'MZ other build').hexdigest()[:8]
        res = self.run_checks()
        self.assertIs(res[0]['ok'], False)
        self.assertEqual(res[0]['text'], f'BC2 client EXE: unsupported build (sha {other}); this preview supports '
                                         f'only the retail BFBC2Game.exe sha {EXE_HASH[:8]}')

    def test_proxy_states(self):
        for state, ok in (('installed', True), ('rome', False), ('upgrade', False), ('missing', False)):
            with proxy(state, 'Project Rome found; proxy not installed.') as m, server_proxy(), builder():
                res = fr.checks(self.cfg, self.root, local_app_data=str(self.lad))
            self.assertIs(res[1]['ok'], ok, state)
            self.assertEqual(res[1]['text'], 'Project Rome found; proxy not installed.')
            m.assert_called_once_with(str(self.exe), self.root)

    def test_proxy_status_raising_is_reported(self):
        with mock.patch.object(fr, 'proxy_status', side_effect=ImportError('no client_install')), server_proxy(), builder():
            res = fr.checks(self.cfg, self.root, local_app_data=str(self.lad))
        self.assertIs(res[1]['ok'], False)
        self.assertEqual(res[1]['text'], 'Proxy status unavailable: no client_install')

    def test_server_missing_original(self):
        (self.server / preview_config.ORIGINAL_NAME).unlink()
        res = self.run_checks()
        self.assertIs(res[2]['ok'], False)
        self.assertEqual(res[2]['text'], 'Server folder missing Frost.Game.Main_Win32_Final.exe '
                                         '(select the dedicated server folder, not the client)')

    def test_server_not_set_or_missing_folder(self):
        self.assertIs(self.run_checks({'client_exe': str(self.exe)})[2]['ok'], False)
        res = self.run_checks(dict(self.cfg, server_dir=str(self.root / 'nope')))
        self.assertIs(res[2]['ok'], False)
        self.assertIn('not found', res[2]['text'])

    def test_builder_passthrough(self):
        with proxy('installed'), server_proxy(), builder(False, 'Builder missing text') as m:
            res = fr.checks(self.cfg, self.root, local_app_data=str(self.lad))
        self.assertEqual(res[4], {'name': 'builder', 'ok': False, 'text': 'Builder missing text'})
        m.assert_called_once_with(self.cfg, self.root)

    def test_bc2replay_unwritable_never_raises(self):
        with proxy('installed'), server_proxy(), builder(), \
                mock.patch.object(Path, 'mkdir', side_effect=PermissionError('denied')):
            res = fr.checks(self.cfg, self.root, local_app_data=str(self.lad))
        self.assertIs(res[5]['ok'], False)
        self.assertIn('not writable', res[5]['text'])
        self.assertIn('denied', res[5]['text'])

    def test_bc2replay_probe_write_fails(self):
        with mock.patch.object(Path, 'write_bytes', side_effect=OSError('disk full')):
            item = fr.check_bc2replay(str(self.lad))
        self.assertIs(item['ok'], False)
        self.assertIn('disk full', item['text'])

    def test_bc2replay_default_env(self):
        with mock.patch.dict('os.environ', {'LOCALAPPDATA': str(self.lad)}):
            self.assertIs(fr.check_bc2replay()['ok'], True)
        with mock.patch.dict('os.environ', {}, clear=True):
            item = fr.check_bc2replay()
        self.assertIs(item['ok'], False)
        self.assertIn('LOCALAPPDATA', item['text'])

    def test_empty_or_bad_cfg(self):
        for cfg in ({}, None, []):
            with proxy('installed'), server_proxy() as sp, builder():
                res = fr.checks(cfg, self.root, local_app_data=str(self.lad))
            self.assertEqual(len(res), 6)
            self.assertIs(res[0]['ok'], False)
            self.assertIsNone(res[1]['ok'])
            self.assertIsNone(res[3]['ok'])
            sp.assert_not_called()


class ReportTests(unittest.TestCase):
    RESULTS = [{'name': 'client', 'ok': True, 'text': 'c ok'},
               {'name': 'proxy', 'ok': False, 'text': 'Project Rome found; proxy not installed.'},
               {'name': 'server', 'ok': True, 'text': 's ok'},
               {'name': 'server_proxy', 'ok': True, 'text': 'Server folder proxy: ok'},
               {'name': 'builder', 'ok': False, 'text': 'b missing'},
               {'name': 'bc2replay', 'ok': True, 'text': 'w ok'}]

    def test_summary_first_failure(self):
        self.assertEqual(fr.summary(self.RESULTS),
                         'Setup check: 4 of 6 ok - proxy: Project Rome found; proxy not installed.')

    def test_summary_clean_and_none_not_counted(self):
        clean = [dict(r, ok=True) for r in self.RESULTS]
        self.assertEqual(fr.summary(clean), 'Setup check: all 6 ok.')
        clean[1]['ok'] = None
        self.assertEqual(fr.summary(clean), 'Setup check: all 5 ok.')
        clean[4]['ok'] = False
        self.assertEqual(fr.summary(clean), 'Setup check: 4 of 5 ok - builder: b missing')

    def test_format_report(self):
        results = list(self.RESULTS)
        results[2] = dict(results[2], ok=None)
        lines = fr.format_report(results).splitlines()
        self.assertEqual(len(lines), 6)
        self.assertEqual(lines[0], 'OK    client: c ok')
        self.assertEqual(lines[1], 'FAIL  proxy: Project Rome found; proxy not installed.')
        self.assertEqual(lines[2], '--    server: s ok')


class VersionCheckTests(Base):
    TEXT = 'Client proxy installed (build X, sha 12345678, 9 B), Rome chain ok.'

    def both(self, version, state='installed'):
        st = {'state': state, 'text': self.TEXT, 'version': version}
        with mock.patch.object(fr, 'proxy_status', return_value=st), \
                mock.patch.object(fr, 'folder_proxy_status', return_value=st):
            return fr.check_proxy(self.cfg, self.root), fr.check_server_proxy(self.cfg, self.root)

    def test_mismatch_fails_both(self):
        client, server = self.both('0.0.9')
        want = 'proxy build 0.0.9, panel ' + preview_config.VERSION + ': reinstall from this zip'
        self.assertEqual((client['ok'], client['text']), (False, want))
        self.assertEqual((server['ok'], server['text']), (False, fr.SERVER_PROXY_PREFIX + want))

    def test_equal_or_missing_version_ok(self):
        for version in (preview_config.VERSION, None):
            client, server = self.both(version)
            self.assertIs(client['ok'], True, version)
            self.assertIs(server['ok'], True, version)
            self.assertEqual(client['text'], self.TEXT)
            self.assertEqual(server['text'], 'Server folder proxy: Proxy installed (build X, sha 12345678, 9 B), '
                                             'Rome chain ok.')

    def test_mismatch_ignored_when_not_installed(self):
        client, _ = self.both('0.0.9', state='upgrade')
        self.assertEqual((client['ok'], client['text']), (False, self.TEXT))

    def test_prepare_skips_installed_mismatch(self):
        st = {'state': 'installed', 'text': self.TEXT, 'version': '0.0.9'}
        plan = fr.prepare_plan({'game_dir': 'G', 'server_dir': 'S', 'client_proxy': st, 'server_proxy': st,
                                'timeline_exe': True, 'server_exe': True, 'instance': True})
        self.assertIsNone(plan['stop'])
        self.assertEqual(fr.needed_steps(plan), [])


class ServerProxyCheckTests(Base):
    def test_states(self):
        for state, ok in (('installed', True), ('rome', False), ('upgrade', False), ('missing', False),
                          ('broken', False), ('mute', False), ('unknown', False), ('no-folder', False)):
            with server_proxy(state, 'No dinput8.dll in the game folder: install Project Rome first.') as m:
                item = fr.check_server_proxy(self.cfg, self.root)
            self.assertEqual(item['name'], 'server_proxy')
            self.assertIs(item['ok'], ok, state)
            self.assertEqual(item['text'],
                             'Server folder proxy: No dinput8.dll in the server folder: install Project Rome first.')
            m.assert_called_once_with(str(self.server), self.root)

    def test_no_folder_text(self):
        with server_proxy('no-folder', 'Game folder not found: X.'):
            item = fr.check_server_proxy(self.cfg, self.root)
        self.assertEqual(item['text'], 'Server folder proxy: Server folder not found: X.')

    def test_unset_is_not_applicable(self):
        for cfg in ({}, {'server_dir': '  '}, {'server_dir': 5}):
            with server_proxy() as m:
                item = fr.check_server_proxy(cfg, self.root)
            self.assertIsNone(item['ok'])
            self.assertEqual(item['text'], 'Server folder proxy: set the Server folder to check it.')
            m.assert_not_called()

    def test_status_raising_is_reported(self):
        with mock.patch.object(fr, 'folder_proxy_status', side_effect=OSError('boom')):
            item = fr.check_server_proxy(self.cfg, self.root)
        self.assertIs(item['ok'], False)
        self.assertEqual(item['text'], 'Server folder proxy: status unavailable: boom')

    def test_in_checks_order_and_counted(self):
        res = self.run_checks(server_state='rome')
        self.assertEqual(res[3]['name'], 'server_proxy')
        self.assertIs(res[3]['ok'], False)
        self.assertTrue(fr.summary(res).startswith('Setup check: 5 of 6 ok - server_proxy: Server folder proxy: '))

    def test_proxy_status_delegates_to_folder(self):
        with mock.patch.object(fr, 'folder_proxy_status', return_value={'state': 'rome'}) as m:
            fr.proxy_status(str(self.exe), self.root)
        m.assert_called_once_with(self.exe.parent, self.root)


def facts(client='rome', server='rome', timeline=False, trace=False, instance=False, game='G', srv='S', **extra):
    f = {'game_dir': game, 'server_dir': srv,
         'client_proxy': None if client is None else {'state': client, 'text': 'client ' + str(client)},
         'server_proxy': None if server is None else {'state': server, 'text': 'server ' + str(server)},
         'timeline_exe': timeline, 'server_exe': trace, 'instance': instance}
    f.update(extra)
    return f


class PreparePlanTests(unittest.TestCase):
    def test_every_combination(self):
        for cs in STATES:
            for ss in STATES:
                for timeline in (False, True):
                    for trace in (False, True):
                        for instance in (False, True):
                            plan = fr.prepare_plan(facts(cs, ss, timeline, trace, instance))
                            key = (cs, ss, timeline, trace, instance)
                            steps = plan['steps']
                            self.assertEqual([s['step'] for s in steps], list(fr.PREPARE_STEPS), key)
                            self.assertEqual(steps[0]['needed'], cs in ('rome', 'upgrade'), key)
                            self.assertEqual(steps[1]['needed'], ss in ('rome', 'upgrade'), key)
                            self.assertEqual(steps[2]['needed'], not (timeline and trace), key)
                            self.assertEqual(steps[3]['needed'], not instance, key)
                            bad_c = cs not in ('rome', 'upgrade', 'installed')
                            bad_s = ss not in ('rome', 'upgrade', 'installed')
                            if bad_c:
                                self.assertEqual(plan['stop'], 'Client proxy: client ' + cs, key)
                            elif bad_s:
                                self.assertEqual(plan['stop'], 'Server folder proxy: server ' + ss, key)
                            else:
                                self.assertIsNone(plan['stop'], key)

    def test_reasons_and_folders(self):
        plan = fr.prepare_plan(facts('upgrade', 'installed', timeline=True))
        c, s, b, i = plan['steps']
        self.assertEqual((c['folder'], c['state'], c['reason']), ('G', 'upgrade', 'Client proxy: client upgrade'))
        self.assertEqual((s['folder'], s['needed'], s['reason']), ('S', False, 'Server folder proxy: server installed'))
        self.assertEqual(b['reason'], 'missing ' + preview_config.SERVER_NAME)
        self.assertEqual(i['reason'], 'runtime\\Instance-local-preview\\ServerOptions.ini missing')
        done = fr.prepare_plan(facts('installed', 'installed', True, True, True))
        self.assertEqual(fr.needed_steps(done), [])
        self.assertEqual(done['steps'][2]['reason'], 'playback executables present')

    def test_unset_paths_stop(self):
        plan = fr.prepare_plan(facts(None, 'rome', game=None))
        self.assertEqual(plan['stop'], 'BC2 client EXE not set (Setup: BC2 client EXE).')
        plan = fr.prepare_plan(facts('rome', None, srv=None))
        self.assertEqual(plan['stop'], 'Server folder not set (Setup: Server folder).')

    def test_missing_proxy_source_stops_only_when_installing(self):
        f = facts('rome', 'installed')
        f['client_proxy']['source_ok'] = False
        f['client_proxy']['source'] = 'proxy\\dinput8.dll'
        self.assertIn('proxy\\dinput8.dll is missing or not the shared build', fr.prepare_plan(f)['stop'])
        f = facts('installed', 'installed')
        f['client_proxy']['source_ok'] = False
        self.assertIsNone(fr.prepare_plan(f)['stop'])

    def test_format_plan(self):
        lines = fr.format_plan(fr.prepare_plan(facts('installed', 'rome', True, True, True))).splitlines()
        self.assertEqual(lines[0], 'skip  client_proxy: Client proxy: client installed')
        self.assertEqual(lines[1], 'RUN   server_proxy: Server folder proxy: server rome')

    def test_prepare_facts_probes(self):
        seen = []

        def status(folder, app_root):
            seen.append((folder, app_root))
            return {'state': 'rome', 'text': 't'}
        probed = []

        def is_file(path):
            probed.append(Path(path))
            return Path(path).name == preview_config.TIMELINE_NAME
        root = Path('APP')
        cfg = {'client_exe': str(Path('GAME') / 'BFBC2Game.exe'), 'server_dir': 'SRV'}
        f = fr.prepare_facts(cfg, root, status=status, is_file=is_file)
        self.assertEqual(seen, [(str(Path('GAME')), root), ('SRV', root)])
        self.assertEqual((f['game_dir'], f['server_dir']), (str(Path('GAME')), 'SRV'))
        self.assertEqual((f['timeline_exe'], f['server_exe'], f['instance']), (True, False, False))
        self.assertIn(root / 'runtime' / 'Instance-local-preview' / 'ServerOptions.ini', probed)
        f = fr.prepare_facts({}, root, status=status, is_file=is_file)
        self.assertEqual((f['game_dir'], f['client_proxy'], f['server_proxy'], f['timeline_exe']),
                         (None, None, None, False))

    def test_prepare_facts_status_raising(self):
        def status(folder, app_root):
            raise OSError('nope')
        f = fr.prepare_facts({'client_exe': 'G\\x.exe', 'server_dir': 'S'}, 'R', status=status, is_file=lambda p: False)
        self.assertEqual(f['client_proxy']['state'], 'unknown')
        self.assertIn('Client proxy: status unavailable: nope', fr.prepare_plan(f)['stop'])


class RunPrepareTests(unittest.TestCase):
    def setUp(self):
        self.calls = []
        self.lines = []
        self.results = [{'name': 'client', 'ok': True, 'text': 'x'}]

    def install(self, folder, source, **kw):
        self.calls.append(('install', str(folder), source, tuple(sorted(kw))))
        if kw.get('check_processes') is not None:
            self.assertIs(kw['check_processes'], fr.no_server_running)

    def run_command(self, action):
        self.calls.append(('run', action))

    def check(self, cfg, app_root):
        self.calls.append(('check',))
        return self.results

    def go(self, plan, **kw):
        return fr.run_prepare({'a': 1}, 'ROOT', kw.pop('run_command', self.run_command), kw.pop('install', self.install),
                              self.lines.append, runtime_dir='RT', plan=plan, check=self.check, source='SRC', **kw)

    def test_all_steps_in_order(self):
        res = self.go(fr.prepare_plan(facts('rome', 'upgrade')))
        self.assertIs(res, self.results)
        self.assertEqual(self.calls, [('install', 'G', 'SRC', ('runtime_dir',)),
                                      ('install', 'S', 'SRC', ('check_processes', 'runtime_dir')),
                                      ('run', 'build'), ('run', 'init-instance'), ('check',)])
        self.assertEqual(self.lines, [fr.PROGRESS[k] for k in ('client_proxy', 'server_proxy', 'build',
                                                                 'instance', 'check')])
        self.assertEqual(self.lines[0], 'Prepare: installing client proxy… (answer the Windows permission prompt)')

    def test_skip_when_not_needed(self):
        self.go(fr.prepare_plan(facts('installed', 'rome', True, True, True)))
        self.assertEqual(self.calls, [('install', 'S', 'SRC', ('check_processes', 'runtime_dir')), ('check',)])
        self.calls.clear()
        self.go(fr.prepare_plan(facts('installed', 'installed', False, True, True)))
        self.assertEqual(self.calls, [('run', 'build'), ('check',)])
        self.calls.clear()
        self.go(fr.prepare_plan(facts('installed', 'installed', True, True, True)))
        self.assertEqual(self.calls, [('check',)])
        self.assertEqual(self.lines[-1], 'Prepare: checking setup…')

    def test_stop_before_any_step(self):
        for cs, ss in (('mute', 'rome'), ('rome', 'broken'), ('installed', 'missing'), ('unknown', 'installed')):
            with self.assertRaises(fr.PrepareStopped) as ctx:
                self.go(fr.prepare_plan(facts(cs, ss)))
            self.assertTrue(str(ctx.exception).startswith('Prepare stopped: '))
            self.assertEqual(self.calls, [])
            self.assertEqual(self.lines, [])
        with self.assertRaises(fr.PrepareStopped) as ctx:
            self.go(fr.prepare_plan(facts('rome', 'broken')))
        self.assertEqual(str(ctx.exception), 'Prepare stopped: Server folder proxy: server broken')

    def test_failure_names_the_step(self):
        def bad_install(folder, source, **kw):
            if 'check_processes' in kw:
                raise RuntimeError(fr.SERVER_RUNNING)
        with self.assertRaises(fr.PrepareFailed) as ctx:
            self.go(fr.prepare_plan(facts('rome', 'rome')), install=bad_install)
        self.assertEqual(str(ctx.exception), 'Prepare failed at server folder proxy: ' + fr.SERVER_RUNNING)
        self.assertEqual(ctx.exception.step, 'server_proxy')
        self.assertNotIn(('run', 'build'), self.calls)

        def bad_run(action):
            self.calls.append(('run', action))
            if action == 'init-instance':
                raise RuntimeError('Preview instance already exists; not overwritten.')
        self.calls.clear()
        with self.assertRaises(fr.PrepareFailed) as ctx:
            self.go(fr.prepare_plan(facts('installed', 'installed')), run_command=bad_run)
        self.assertEqual(str(ctx.exception), 'Prepare failed at instance: Preview instance already exists; not overwritten.')
        self.assertEqual(self.calls, [('run', 'build'), ('run', 'init-instance')])

    def test_check_failure(self):
        def bad_check(cfg, app_root):
            raise ValueError('x')
        with self.assertRaises(fr.PrepareFailed) as ctx:
            fr.run_prepare({}, 'R', self.run_command, self.install, plan=fr.prepare_plan(facts('installed', 'installed', True, True, True)),
                           check=bad_check)
        self.assertEqual(str(ctx.exception), 'Prepare failed at check: x')

    def test_plan_computed_when_not_given(self):
        with mock.patch.object(fr, 'prepare_facts', return_value=facts('installed', 'installed', True, True, True)) as m:
            fr.run_prepare({'k': 1}, 'R', self.run_command, self.install, check=self.check)
        m.assert_called_once_with({'k': 1}, 'R')
        self.assertEqual(self.calls, [('check',)])

    def test_no_server_running(self):
        with mock.patch('replay.server.processes', return_value=[]):
            fr.no_server_running()
        with mock.patch('replay.server.processes', return_value=[(1, 'frost.game.main_win32_final.exe')]):
            with self.assertRaises(RuntimeError) as ctx:
                fr.no_server_running()
        self.assertEqual(str(ctx.exception), 'Stop every BC2 dedicated server first (it has dinput8.dll loaded).')


class CleanupTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.server = self.root / 'server'
        self.server.mkdir()
        self.instance = self.root / 'runtime' / 'Instance-local-preview'

    def statuses(self, client='installed', server='installed'):
        return {'client_proxy': client and {'state': client, 'text': 'c ' + client},
                'server_proxy': server and {'state': server, 'text': 's ' + server}}

    def test_plan_order_and_states(self):
        for name in fr.built_names():
            (self.server / name).write_bytes(b'MZ')
        (self.instance / 'AdminScripts').mkdir(parents=True)
        actions = fr.cleanup_plan(self.statuses(), str(self.server), self.root, game_dir='G')
        self.assertEqual([(a['action'], a['step']) for a in actions],
                         [('uninstall', 'client_proxy'), ('uninstall', 'server_proxy'), ('delete_file', 'build'),
                          ('delete_file', 'build'), ('delete_tree', 'instance')])
        self.assertEqual(actions[2]['path'], str(self.server / preview_config.TIMELINE_NAME))
        self.assertEqual(actions[3]['path'], str(self.server / preview_config.SERVER_NAME))
        self.assertEqual(actions[4]['text'], 'runtime\\Instance-local-preview deleted')
        for state in ('rome', 'missing', 'no-folder'):
            self.assertEqual(fr.cleanup_plan(self.statuses(state, state), str(self.root / 'empty'),
                                             self.root / 'empty', game_dir='G'), [])
        actions = fr.cleanup_plan(self.statuses('upgrade', 'broken'), None, self.root, game_dir='G')
        self.assertEqual([(a['action'], a['step']) for a in actions][:1], [('uninstall', 'client_proxy')])
        self.assertNotIn('server_proxy', [a['step'] for a in actions])  # server folder unset: not touched
        actions = fr.cleanup_plan(self.statuses('mute', 'broken'), 'S', self.root, game_dir='G',
                                  exists=lambda p: False)
        self.assertEqual([a['action'] for a in actions], ['skip', 'skip'])
        self.assertEqual(actions[1]['text'], 'Server folder proxy: s broken (not touched)')
        self.assertEqual(fr.cleanup_plan({}, None, self.root, exists=lambda p: False), [])

    def uninstall(self, folder, **kw):
        self.calls.append(('uninstall', str(folder), tuple(sorted(kw))))
        return {'restored': True}

    def test_run_order_and_result(self):
        self.calls = []
        lines = []
        actions = fr.cleanup_plan(self.statuses(), 'S', self.root, game_dir='G', exists=lambda p: True)
        line = fr.run_cleanup(actions, self.uninstall, running=lambda: False,
                              remove_file=lambda p: self.calls.append(('file', Path(p).name)),
                              remove_tree=lambda p: self.calls.append(('tree', Path(p).name)),
                              runtime_dir='RT', progress=lines.append)
        self.assertEqual(self.calls, [('uninstall', 'G', ('runtime_dir',)),
                                      ('uninstall', 'S', ('check_processes', 'runtime_dir')),
                                      ('file', preview_config.TIMELINE_NAME), ('file', preview_config.SERVER_NAME),
                                      ('tree', 'Instance-local-preview')])
        self.assertEqual(line, 'Clean up finished: client proxy removed, server folder proxy removed, '
                               f'{preview_config.TIMELINE_NAME} deleted, {preview_config.SERVER_NAME} deleted, '
                               'runtime\\Instance-local-preview deleted.')
        self.assertEqual(lines[0], 'Clean up: removing client proxy… (answer the Windows permission prompt)')
        self.assertEqual(lines[1], 'Clean up: removing server folder proxy…')
        self.assertEqual(lines[2], 'Clean up: deleting ' + preview_config.TIMELINE_NAME + '…')
        self.assertEqual(lines[4], 'Clean up: deleting runtime\\Instance-local-preview…')

    def test_refused_while_running(self):
        self.calls = []
        actions = fr.cleanup_plan(self.statuses(), 'S', self.root, game_dir='G', exists=lambda p: True)
        with self.assertRaises(RuntimeError) as ctx:
            fr.run_cleanup(actions, self.uninstall, running=lambda: True, remove_file=self.fail, remove_tree=self.fail)
        self.assertEqual(str(ctx.exception), fr.CLEANUP_REFUSED)
        self.assertEqual(self.calls, [])

    def test_nothing_and_skips(self):
        self.assertEqual(fr.run_cleanup([], self.fail, running=lambda: False), 'Clean up finished: nothing to remove.')
        actions = fr.cleanup_plan(self.statuses('mute', None), None, self.root, game_dir='G', exists=lambda p: False)
        self.assertEqual(fr.run_cleanup(actions, self.fail, running=lambda: False),
                         'Clean up finished: nothing to remove. Client proxy: c mute (not touched).')

    def test_failure_reports_step_and_progress(self):
        self.calls = []
        actions = fr.cleanup_plan(self.statuses(), 'S', self.root, game_dir='G', exists=lambda p: False)

        def uninstall(folder, **kw):
            if 'check_processes' in kw:
                raise RuntimeError('Elevation was cancelled')
            return {'restored': True}
        with self.assertRaises(RuntimeError) as ctx:
            fr.run_cleanup(actions, uninstall, running=lambda: False)
        self.assertEqual(str(ctx.exception), 'Clean up failed at server folder proxy: Elevation was cancelled '
                                             '(already removed: client proxy removed)')

    def test_real_deletes_only_planned_paths(self):
        for name in fr.built_names():
            (self.server / name).write_bytes(b'MZ')
        keep = self.server / preview_config.ORIGINAL_NAME
        keep.write_bytes(b'MZ keep')
        (self.instance / 'AdminScripts').mkdir(parents=True)
        (self.instance / 'ServerOptions.ini').write_text('x')
        other = self.root / 'runtime' / 'keep.log'
        other.write_text('x')
        actions = fr.cleanup_plan(self.statuses('rome', 'rome'), str(self.server), self.root, game_dir='G')
        line = fr.run_cleanup(actions, self.fail, running=lambda: False)
        self.assertTrue(line.startswith('Clean up finished: '))
        self.assertTrue(keep.is_file())
        self.assertTrue(other.is_file())
        self.assertFalse(self.instance.exists())
        self.assertEqual(sorted(p.name for p in self.server.iterdir()), [preview_config.ORIGINAL_NAME])

    def test_remove_tree_guard(self):
        with self.assertRaises(RuntimeError):
            fr._remove_tree(self.server)
        self.assertTrue(self.server.is_dir())

    def test_statuses_and_format(self):
        st = fr.cleanup_statuses('G', None, 'R', status=lambda f, r: {'state': 'rome', 'folder': f})
        self.assertEqual(st, {'client_proxy': {'state': 'rome', 'folder': 'G'}, 'server_proxy': None})
        st = fr.cleanup_statuses('G', 'S', 'R', status=mock.Mock(side_effect=OSError('x')))
        self.assertEqual(st['server_proxy']['state'], 'unknown')
        self.assertEqual(fr.format_cleanup([]), 'Nothing to clean up.')
        line = fr.format_cleanup([{'action': 'delete_file', 'text': 'a deleted', 'path': 'P'},
                                  {'action': 'skip', 'text': 'b', 'path': 'Q'}])
        self.assertEqual(line.splitlines(), ['DEL   a deleted: P', 'skip  b: Q'])


if __name__ == '__main__':
    unittest.main()
