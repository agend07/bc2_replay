"""BC2 local replay technical preview. Explicit commands; no automatic elevation."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
from instance_defaults import STARTUP
from preview_config import APP_ROOT, ORIGINAL_HASH, TRACE_HASH, TIMELINE_HASH, CLIENT_HASH, server_dir, client_exe, trace_exe, timeline_exe, runtime_dir, recording, recording_argument, level

COMPANIONS=('.fbgd','.fbgk','.fbgdd','.fbgdk','.fbidd','.fbidk','.fbcdd','.fbcdk','.txt')


def verify(path, expected):
    if not path.is_file(): raise RuntimeError('Missing file: '+str(path))
    if hashlib.sha256(path.read_bytes()).hexdigest()!=expected:
        raise RuntimeError('Unsupported or changed executable: '+str(path))


def check_recording():
    base=recording()
    missing=[str(base)+ext for ext in COMPANIONS if not Path(str(base)+ext).is_file()]
    if missing: raise RuntimeError('Missing recording companions: '+', '.join(missing))
    level()
    recording_argument()


def check():
    if os.name!='nt': raise RuntimeError('This preview requires Windows.')
    verify(server_dir()/'Frost.Game.Main_Win32_Final.exe',ORIGINAL_HASH)
    verify(client_exe(),CLIENT_HASH)
    check_recording()
    built=trace_exe().is_file()
    if built: verify(trace_exe(),TRACE_HASH)
    timeline_built=timeline_exe().is_file()
    if timeline_built: verify(timeline_exe(),TIMELINE_HASH)
    return {'supported_original_server':True,'supported_client':True,
            'recording_companions_present':True,'playback_build_present':built,
            'timeline_build_present':timeline_built,
            'instance_present':(APP_ROOT/'runtime/Instance-local-preview/ServerOptions.ini').is_file(),
            'note':'File checks only; not proof of working playback or offline independence.'}


def initialize_instance():
    # Never copy an existing server's accounts, provider IDs or admin password.
    check()
    target=runtime_dir()/'Instance-local-preview'
    if target.exists(): raise RuntimeError('Preview instance already exists; not overwritten.')
    target.mkdir()
    (target/'AdminScripts').mkdir()
    lvl=level()
    playlist='CONQUEST' if lvl.upper().endswith('CQ') else 'RUSH'
    options='[Options]\nName=Local Replay\nPort=19567\nRemoteAdminPort=48888\nRemoteAdminPassword='+secrets.token_hex(8)+'\nPunkBuster=false\nRanked=true\nNumGameClientSlots=32\nRevisionLevel=8\nGameModID=BC2\n'
    (target/'ServerOptions.ini').write_text(options,encoding='utf-8')
    (target/'maplist.txt').write_text(f'{playlist}\n{lvl.lower()} 1\n',encoding='ascii')
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
    parser.add_argument('action',nargs='?',default='controls',choices=('check','build','init-instance','backend','server','client','controls'))
    args=parser.parse_args()
    if args.action=='check': print(json.dumps(check(),indent=2))
    elif args.action=='build':
        check()
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
        verify(timeline_exe(),TIMELINE_HASH)
        exe_path=timeline_exe()
        from replay.server import processes
        if processes():raise RuntimeError('Another BC2 dedicated server is running; stop it first.')
        require_free_udp(19567)
        instance=runtime_dir()/'Instance-local-preview'
        if not (instance/'ServerOptions.ini').is_file():raise RuntimeError('Run init-instance first.')
        # Trace output prefix is fixed by the verified executable hash.
        (server_dir()/'diagnostics').mkdir(exist_ok=True)
        command=[str(exe_path),'-server','-dedicated','-serverInstancePath',instance.as_posix()+'/',
                 '-mapPack2Enabled','1','-port','19567','-timeStampLogNames',
                 '-Demo.PlaybackDemoFileName',recording_argument(),'-Server.Level',level(),'-displayAsserts','0']
        print('Starting experimental playback ('+exe_path.name+'). Ctrl+C/closing this console stops it.')
        process=subprocess.Popen(command,cwd=server_dir())
        try:process.wait()
        except KeyboardInterrupt:
            if process.poll() is None:process.terminate()
            process.wait()
        if process.returncode:raise RuntimeError('Playback server exited with code '+str(process.returncode))
    elif args.action=='client':
        verify(client_exe(),CLIENT_HASH)
        # Avoid duplicate clients without terminating any existing process.
        from replay.client import client_pids
        if client_pids():raise RuntimeError('BC2 client is already running.')
        subprocess.Popen([str(client_exe())],cwd=client_exe().parent)
    else:controls()

if __name__=='__main__':
    try:main()
    except (OSError,ValueError,RuntimeError,subprocess.CalledProcessError) as exc:
        print('Stopped: '+str(exc),file=sys.stderr)
        raise SystemExit(1)
