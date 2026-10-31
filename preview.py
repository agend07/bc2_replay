"""BC2 local replay technical preview. Explicit commands; no automatic elevation."""
import argparse
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
from instance_defaults import STARTUP
from preview_config import APP_ROOT, VERSION, ORIGINAL_HASH, TRACE_HASH, TIMELINE_HASH, CLIENT_HASH, server_dir, client_exe, trace_exe, timeline_exe, runtime_dir, recording, recording_argument, level, available_recordings, missing_companions
from preview_config import verify, raw_config


def check_recording():
    base=recording()
    missing=missing_companions(base)
    if missing:
        others=[item for item in available_recordings() if item!='demos/'+Path(base).stem]
        hint=' Another complete recording is available: '+others[0]+'.' if others else \
             ' No complete recording set was found in demos/.'
        raise RuntimeError('Missing recording companions: '+', '.join(missing)+'.'+hint)
    level()
    recording_argument()


def check(require_recording=True):
    # r264b: build / init-instance pass False: a fresh install has no demos yet (testers record their own).
    if os.name!='nt': raise RuntimeError('This preview requires Windows.')
    verify(server_dir()/'Frost.Game.Main_Win32_Final.exe',ORIGINAL_HASH)
    verify(client_exe(),CLIENT_HASH)
    recording_ok=True
    if require_recording: check_recording()
    else:
        try: check_recording()
        except RuntimeError: recording_ok=False
    built=trace_exe().is_file()
    if built: verify(trace_exe(),TRACE_HASH)
    timeline_built=timeline_exe().is_file()
    if timeline_built: verify(timeline_exe(),TIMELINE_HASH)
    return {'supported_original_server':True,'supported_client':True,
            'recording_companions_present':recording_ok,'playback_build_present':built,
            'timeline_build_present':timeline_built,
            'active_recording':recording_argument() if recording_ok else None,
            'available_recordings':available_recordings(),
            'instance_present':(APP_ROOT/'runtime/Instance-local-preview/ServerOptions.ini').is_file(),
            'note':'File checks only; not proof of working playback or offline independence.'}


def initialize_instance():
    # Never copy an existing server's accounts, provider IDs or admin password.
    check(require_recording=False)
    target=runtime_dir()/'Instance-local-preview'
    if target.exists(): raise RuntimeError('Preview instance already exists; not overwritten.')
    target.mkdir()
    (target/'AdminScripts').mkdir()
    lvl=level()
    from replay.timeline import write_maplist  # r153: unsuffixed MP_005 is Conquest (playlist_for_level)
    options='[Options]\nName=Local Replay\nPort=19567\nRemoteAdminPort=48888\nRemoteAdminPassword='+secrets.token_hex(8)+'\nPunkBuster=false\nRanked=true\nNumGameClientSlots=32\nRevisionLevel=8\nGameModID=BC2\n'
    (target/'ServerOptions.ini').write_text(options,encoding='utf-8')
    write_maplist(target,lvl)
    for name in ('banlist.txt','reservedslotslist.txt','textchatmoderationlist.txt'):
        (target/name).write_text('',encoding='ascii')
    (target/'AdminScripts/Startup.txt').write_text(STARTUP,encoding='ascii')
    print('Created separate Instance-local-preview with a random admin password.')
    print('Do not publish this generated instance or its runtime logs.')


def require_free_udp(port):
    with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as sock:
        if os.name=='nt': sock.setsockopt(socket.SOL_SOCKET,socket.SO_EXCLUSIVEADDRUSE,1)
        try: sock.bind(('0.0.0.0',port))
        except OSError as exc: raise RuntimeError('UDP port '+str(port)+' is occupied; stop the other server first.') from exc


def controls():
    if os.name!='nt': raise RuntimeError('Windows is required.')
    # Setup UI must also open on a fresh installation without configured paths/builds.
    import msvcrt
    lock_path=runtime_dir()/'controller.lock'
    with lock_path.open('a+b') as lock:
        if lock.tell()==0:lock.write(b'0');lock.flush()
        lock.seek(0)
        try:msvcrt.locking(lock.fileno(),msvcrt.LK_NBLCK,1)
        except OSError as exc:raise RuntimeError('A preview controller is already open.') from exc
        try:
            print('Opening controls does not change the server or camera. Do not open another controller.')
            from replay.ui import main as ui_main
            ui_main()
        finally:
            lock.seek(0);msvcrt.locking(lock.fileno(),msvcrt.LK_UNLCK,1)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action',nargs='?',default='controls',choices=('check','build','init-instance','backend','server','client','stop-server','controls',
                                                           'install-client','uninstall-client','client-status','check-setup','prepare','cleanup'))
    parser.add_argument('--version',action='version',version=VERSION)  # r266: release_pack.py provenance check
    args=parser.parse_args()
    if args.action=='check': print(json.dumps(check(),indent=2))
    elif args.action=='build':
        check(require_recording=False)
        from server_build import build
        build()
    elif args.action=='init-instance':initialize_instance()
    elif args.action=='backend':
        runtime_dir()
        print('Local backend: dummy credentials only. No raw login recording.')
        from backend import rome_local
        old_argv=sys.argv[:]
        try:
            sys.argv=['rome_local.py','--out',str(runtime_dir()/'backend')]
            rome_local.main()
        finally:
            sys.argv=old_argv
    elif args.action=='server':
        check()
        exe_path=timeline_exe()
        verify(exe_path,TIMELINE_HASH)
        from replay.server import processes
        if processes():raise RuntimeError('Another BC2 dedicated server is running; stop it first.')
        require_free_udp(19567)
        instance=runtime_dir()/'Instance-local-preview'
        if not (instance/'ServerOptions.ini').is_file():raise RuntimeError('Run init-instance first.')
        # Trace output prefix is fixed by the verified executable hash.
        (server_dir()/'diagnostics').mkdir(exist_ok=True)
        from replay.timeline import server_command
        command=server_command(exe_path,instance,recording_argument(),level())
        print('Starting experimental playback ('+exe_path.name+'). Ctrl+C/closing this console stops it.')
        from replay.web_launch import local_env
        process=subprocess.Popen(command,cwd=server_dir(),env=local_env())
        try:process.wait()
        except KeyboardInterrupt:
            if process.poll() is None:process.terminate()
            process.wait()
        if process.returncode:raise RuntimeError('Playback server exited with code '+str(process.returncode))
    elif args.action=='stop-server':
        # Stops only this installation's playback server, re-validated first.
        # Deliberately does not call check(): a stale recording must not block a stop.
        from backend_lifecycle import stop_preview_server
        print(json.dumps(stop_preview_server()))
    elif args.action in ('install-client','uninstall-client','client-status'):
        # r264: client game folder only; dinput8.dll (Rome kept as dinput8_rome.dll). May show a UAC prompt.
        from replay import client_install as ci
        game=ci.game_dir_of(client_exe())
        if args.action=='install-client':result=ci.install(game,ci.proxy_source(APP_ROOT),runtime_dir=runtime_dir())
        elif args.action=='uninstall-client':result=ci.uninstall(game,runtime_dir=runtime_dir())
        else:result=ci.status(game,ci.proxy_source(APP_ROOT))
        print(json.dumps(result,indent=2,default=str))
    elif args.action=='check-setup':
        # Raw preview.json (not settings()): a half-configured install still gets a partial report.
        from replay import first_run
        results=first_run.checks(raw_config(),APP_ROOT)
        print('Panel version '+VERSION)
        print(first_run.format_report(results))
        print(first_run.summary(results))
        if any(r['ok'] is False for r in results):raise SystemExit(1)
    elif args.action=='prepare':
        # Same plan + runner as the panel's Prepare button (replay.first_run). May show a UAC prompt.
        from replay import first_run
        from replay import client_install as ci
        cfg=raw_config()
        plan=first_run.prepare_plan(first_run.prepare_facts(cfg,APP_ROOT))
        print(first_run.format_plan(plan),flush=True)
        def run_command(action):
            # build / init-instance in-process: the same code paths as `preview.py build|init-instance`.
            if action=='build':
                check(require_recording=False)
                from server_build import build
                build()
            elif action=='init-instance':initialize_instance()
            else:raise RuntimeError('unknown action '+action)
        try:results=first_run.run_prepare(cfg,APP_ROOT,run_command,ci.install,
                                         lambda line:print(line,flush=True),runtime_dir=runtime_dir(),plan=plan)
        except (first_run.PrepareStopped,first_run.PrepareFailed) as exc:
            print(str(exc),file=sys.stderr)
            raise SystemExit(1)
        print(first_run.format_report(results))
        print(first_run.summary(results))
        if any(r['ok'] is False for r in results):raise SystemExit(1)
    elif args.action=='cleanup':
        # Removes only what Prepare created. Prints the plan; acts only with BC2_CLEANUP=1 in the environment.
        from replay import first_run
        from replay import client_install as ci
        cfg=raw_config()
        client=cfg.get('client_exe') if isinstance(cfg.get('client_exe'),str) and cfg.get('client_exe').strip() else None
        server=cfg.get('server_dir').strip() if isinstance(cfg.get('server_dir'),str) and cfg.get('server_dir').strip() else None
        game=str(Path(client.strip()).parent) if client else None
        actions=first_run.cleanup_plan(first_run.cleanup_statuses(game,server,APP_ROOT),server,APP_ROOT,game_dir=game)
        print(first_run.format_cleanup(actions))
        if os.environ.get('BC2_CLEANUP')!='1':
            print('Plan only: nothing changed. Set BC2_CLEANUP=1 and run cleanup again to remove these.')
            return
        try:print(first_run.run_cleanup(actions,ci.uninstall,runtime_dir=runtime_dir(),
                                        progress=lambda line:print(line,flush=True)))
        except RuntimeError as exc:
            print(str(exc),file=sys.stderr)
            raise SystemExit(1)
    elif args.action=='client':
        verify(client_exe(),CLIENT_HASH)
        # Avoid duplicate clients without terminating any existing process.
        from replay.client import client_pids
        if client_pids():raise RuntimeError('BC2 client is already running.')
        from replay.web_launch import client_args, client_env, serve_pipe
        # client_env: BC2_REPLAY=1 -> the client recorder in the shared proxy stays off for replay sessions.
        process=subprocess.Popen([str(client_exe())]+client_args(),cwd=client_exe().parent,env=client_env())
        try:
            from replay.owned import record_launched_client
            record_launched_client(process.pid)  # the panel stops it when it closes (Launch Game only)
        except Exception as exc:print('Could not record the launched client:',exc,flush=True)
        serve_pipe(process)
    else:controls()

def log_without_console():
    # pythonw.exe has no console: sys.stdout/stderr are None, so prints
    # vanish and errors are lost. Send both to runtime/panel_<stamp>.private.log instead.
    if sys.stdout is not None and sys.stderr is not None:return
    try:
        import datetime
        stamp=datetime.datetime.now().strftime('%Y%m%d_%H%M%S_%f')
        log=open(runtime_dir()/('panel_'+stamp+'.private.log'),'a',encoding='utf-8',buffering=1)
        if sys.stdout is None:sys.stdout=log
        if sys.stderr is None:sys.stderr=log
    except Exception:pass

if __name__=='__main__':
    log_without_console()
    try:main()
    except (OSError,ValueError,RuntimeError,subprocess.CalledProcessError) as exc:
        print('Stopped: '+str(exc),file=sys.stderr)
        raise SystemExit(1)
