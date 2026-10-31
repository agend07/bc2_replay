"""Offline tests for replay.owned (processes the panel started; stopped when it closes).
Only processes these tests start themselves are ever terminated.
Run from the repo root:  python -m pytest replay/test_owned.py -q
"""
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from replay import owned


class Pure(unittest.TestCase):
    REC = {'kind': 'server', 'pid': 10, 'image': r'C:\BC2\ReplayTimeline.exe', 'created': 123}

    def test_same_process(self):
        self.assertTrue(owned.same_process(self.REC, (r'c:\bc2\replaytimeline.exe', 123)))
        self.assertFalse(owned.same_process(self.REC, (r'C:\BC2\other.exe', 123)))   # pid reused by another image
        self.assertFalse(owned.same_process(self.REC, (r'C:\BC2\ReplayTimeline.exe', 124)))  # same image, restarted
        self.assertFalse(owned.same_process(self.REC, None))  # gone
        self.assertFalse(owned.same_process(None, (r'C:\BC2\ReplayTimeline.exe', 123)))

    def test_shutdown_order(self):
        c, s = {'kind': 'client'}, {'kind': 'server'}
        self.assertEqual(owned.shutdown_plan({'server': s, 'client': c}), [c, s])
        self.assertEqual(owned.shutdown_plan({'server': s, 'client': None}), [s])
        self.assertEqual(owned.shutdown_plan({'backend': {'kind': 'backend'}}), [])


class Records(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_identity_of_self(self):
        image, created = owned.identity(os.getpid())
        self.assertEqual(Path(image).resolve(), Path(sys.executable).resolve())
        self.assertGreater(created, 0)
        rec = owned.make_record('client', os.getpid())
        self.assertTrue(owned.alive(rec))
        self.assertFalse(owned.alive(dict(rec, created=rec['created'] + 1)))
        self.assertIsNone(owned.identity(0))

    def test_never_stops_a_reused_pid(self):
        rec = owned.make_record('server', os.getpid())
        self.assertEqual(owned.stop(dict(rec, created=rec['created'] - 1)), 'not_owned')
        self.assertEqual(owned.stop(dict(rec, image=r'C:\nowhere\x.exe')), 'not_owned')

    def test_stop_own_child(self):
        child = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(60)'],
                                 creationflags=subprocess.CREATE_NO_WINDOW)
        try:
            rec = owned.record('client', child.pid, runtime=self.dir)
            self.assertEqual(owned.load('client', runtime=self.dir), rec)
            # no window: WM_CLOSE does nothing, forced after the timeout
            self.assertEqual(owned.stop(rec, timeout=0.3, graceful=True), 'killed')
            self.assertIsNotNone(child.wait(timeout=5))
            self.assertEqual(owned.stop(rec), 'gone')
        finally:
            if child.poll() is None:
                child.kill()

    def test_save_load_forget(self):
        rec = {'kind': 'server', 'pid': 5, 'image': 'x.exe', 'created': 1}
        owned.save(rec, runtime=self.dir)
        self.assertEqual(owned.load_all(runtime=self.dir), {'client': None, 'server': rec})
        owned.forget('server', runtime=self.dir)
        self.assertIsNone(owned.load('server', runtime=self.dir))
        owned.path('client', self.dir).write_text('not json', encoding='utf-8')
        self.assertIsNone(owned.load('client', runtime=self.dir))

    def test_client_recorded_only_when_the_panel_launched_it(self):
        saved = owned.RUNTIME
        owned.RUNTIME = self.dir
        try:
            os.environ.pop(owned.CLIENT_ENV, None)
            owned.record_launched_client(os.getpid())
            self.assertIsNone(owned.load('client'))
            os.environ[owned.CLIENT_ENV] = '1'
            owned.record_launched_client(os.getpid())
            self.assertEqual(owned.load('client')['pid'], os.getpid())
        finally:
            os.environ.pop(owned.CLIENT_ENV, None)
            owned.RUNTIME = saved

    def _two(self):
        owned.save({'kind': 'client', 'pid': 1, 'image': 'BFBC2Game.exe', 'created': 1}, runtime=self.dir)
        owned.save({'kind': 'server', 'pid': 2, 'image': 'ReplayTimeline.exe', 'created': 2}, runtime=self.dir)

    def test_close_order_and_server_fallback(self):
        self._two()
        calls = []
        alive = {'server': [True, True]}  # still alive after stop-server refused -> forced by handle

        def stopper(rec, graceful=False):
            calls.append((rec['kind'], 'stop', graceful))
            return 'killed'

        def stop_server(pid):
            calls.append(('server', 'stop-server', pid))
            raise RuntimeError('mailbox not found')

        done = owned.close_owned(runtime=self.dir, stop_server=stop_server, stopper=stopper,
                                 is_alive=lambda rec: alive[rec['kind']].pop(0))
        self.assertEqual(calls, [('client', 'stop', True), ('server', 'stop-server', 2), ('server', 'stop', False)])
        self.assertEqual(done, {'client': 'killed', 'server': 'killed'})
        self.assertEqual(owned.load_all(runtime=self.dir), {'client': None, 'server': None})

    def test_close_server_gone_or_stopped(self):
        self._two()
        calls = []
        done = owned.close_owned(runtime=self.dir, stop_server=lambda pid: calls.append(pid),
                                 stopper=lambda rec, graceful=False: 'gone', is_alive=lambda rec: False)
        self.assertEqual(calls, [])  # not ours any more: stop-server never called
        self.assertEqual(done, {'client': 'gone', 'server': 'gone'})
        owned.save({'kind': 'server', 'pid': 2, 'image': 'x', 'created': 2}, runtime=self.dir)
        state = iter([True, False])
        done = owned.close_owned(runtime=self.dir, stop_server=lambda pid: {'stopped': True, 'pid': pid},
                                 stopper=lambda rec, graceful=False: self.fail('no force needed'),
                                 is_alive=lambda rec: next(state))
        self.assertEqual(done, {'server': {'stopped': True, 'pid': 2}})

    def test_stop_clients(self):
        self._two()
        seen = []
        done = owned.stop_clients([os.getpid()], runtime=self.dir, stopper=lambda rec: seen.append(rec) or 'killed')
        self.assertEqual(done, {os.getpid(): 'killed'})
        self.assertEqual([(r['kind'], r['pid']) for r in seen], [('client', os.getpid())])
        self.assertIsNone(owned.load('client', runtime=self.dir))
        self.assertIsNotNone(owned.load('server', runtime=self.dir))

    def test_sweep_stale(self):
        self._two()
        seen = []
        done = owned.sweep_stale(runtime=self.dir, stopper=lambda rec: seen.append(rec['kind']) or 'not_owned')
        self.assertEqual(seen, ['client', 'server'])
        self.assertEqual(done, {'client': 'not_owned', 'server': 'not_owned'})
        self.assertEqual(owned.sweep_stale(runtime=self.dir), {})


class StopServerExpectedPid(unittest.TestCase):
    def setUp(self):
        import replay.server as server
        self.server = server
        self.saved = server.Server, server.replay_candidates

        class FakeServer:
            closed = []

            def __init__(self, stop=False):
                self.pid, self.handle = 77, None

            def close(self):
                FakeServer.closed.append(self.pid)

        self.Fake = FakeServer
        server.Server, server.replay_candidates = FakeServer, lambda: [(77, 'x.exe')]

    def tearDown(self):
        self.server.Server, self.server.replay_candidates = self.saved

    def test_other_pid_is_left_running(self):
        from backend_lifecycle import stop_preview_server
        res = stop_preview_server(expected_pid=78)
        self.assertFalse(res['stopped'])
        self.assertEqual(self.Fake.closed, [77])  # handle closed, nothing terminated


if __name__ == '__main__':
    unittest.main()
