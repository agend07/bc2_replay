"""Host-side 20 Hz Follow using fresh validated named soldier references.
Only acknowledged camera-mailbox commands are sent. Death/respawn unavailability
causes NO pose writes, and no actor address is retained between ticks.
"""
import datetime
import json
import time
from replay.camera_native import ControlClient
from replay.camera import ROOT, attach, session, native_command, focus_command, target_position, TargetUnavailable, CameraNotReady
from replay.timeline_guard import TimelineFollowGuard


def follow_tick(client,data,name,replay_guard):
    try:
        result=focus_command(client,data,'move',name,follow_guard=replay_guard,
                             replay_kind=data.get('replay_kind','timeline'))
        return {'state':'following','position':result['position']}
    except TargetUnavailable as exc:
        return {'state':'waiting_for_target','reason':str(exc)}
    except CameraNotReady as exc:
        return {'state':'waiting_for_camera','reason':str(exc)}


def follow_log(event):
    with (ROOT/'runtime'/'private_follow_events.jsonl').open('a',encoding='utf-8') as f:
        f.write(json.dumps({'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),**event})+'\n')


def make_follow_guard(replay_kind='timeline'):
    if replay_kind != 'timeline':
        raise ValueError('Only Timeline playback is supported')
    return TimelineFollowGuard()


def run_follow(name,stop_event,on_event,interval=.05,max_seconds=None, *, replay_kind='timeline'):
    if not .03<=interval<=1:raise ValueError('Follow interval out of bounds')
    client=None;replay_guard=None;updates=0;started=time.monotonic();last_state=None;last_report=0
    try:
        replay_guard=make_follow_guard(replay_kind)
        client=ControlClient()
        # Require a validated live target before changing camera mode at startup.
        target_position(client,name)
        attach(client,replay_kind=replay_kind);data=session(client)
        if stop_event.is_set():return
        native_command(client,data,'free',replay_kind=replay_kind)
        follow_log({'event':'start','player':name,'pid':client.pid,'replay_kind':replay_kind})
        started=time.monotonic()
        camera_wait_since=None
        while not stop_event.is_set():
            tick_start=time.monotonic()
            event=follow_tick(client,data,name,replay_guard)
            if event['state']=='following':updates+=1;camera_wait_since=None
            elif event['state']=='waiting_for_camera':
                if camera_wait_since is None:camera_wait_since=tick_start
                if tick_start-camera_wait_since>2:raise RuntimeError('Follow stopped: active camera was not restored within two seconds')
            if event['state']!=last_state:
                follow_log({'event':event['state'],'player':name,'updates':updates,'reason':event.get('reason')})
                last_state=event['state']
            if tick_start-last_report>=.5:
                on_event(dict(event,player=name,updates=updates));last_report=tick_start
            if max_seconds is not None and time.monotonic()-started>=max_seconds:break
            stop_event.wait(max(0,interval-(time.monotonic()-tick_start)))
        follow_log({'event':'stop','player':name,'updates':updates})
        on_event({'state':'stopped','player':name,'updates':updates})
    finally:
        # Stop leaves free camera at its last position. UI restore/close separately
        # restores original transform/mode after this worker has fully stopped.
        if client:client.close()
        if replay_guard:replay_guard.close()
