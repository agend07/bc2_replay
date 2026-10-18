"""Timeline camera commands with exact-build guards and saved-view recovery.
attach installs an idle update hook without changing camera mode.
free/focus change the camera ONLY when explicitly invoked. restore/player/detach
are recovery actions, available without a running playback server.
"""
import ctypes as c
import datetime
import json
import math
import struct
import time
from ctypes import wintypes as w
from replay.client import k
from preview_config import APP_ROOT as ROOT
from replay.camera_native import VTABLE, UPDATE, status as native_status
from replay.camera_adapter import (build, REQUEST, ACK, COMMAND, RESULT, SAVED,
                                               TARGET, ORIGINAL, HOLD_FREE, MODE_RESETS,
                                               OBSERVED_MODE, MODE_AFTER_OVERRIDE, VERSION, CONTEXT_CALLBACK)
from replay.players import roster, PLAYER_TABLE, soldier_link
from replay.players import transform

STATE=ROOT/'runtime'/'private_focus_session.json'
MAGIC=b'BC2FCS1\0'


def guard(replay_kind='timeline'):
    if replay_kind != 'timeline':
        raise ValueError('Only Timeline playback is supported')
    from replay.timeline_guard import check
    return check()


def session(client):
    data=json.loads(STATE.read_text(encoding='utf-8'))
    if data['pid']!=client.pid or client.read(data['mailbox'],8)!=MAGIC:
        raise RuntimeError('Focus session differs from this client')
    if data.get('adapter_version') != VERSION:
        raise RuntimeError('Unsupported camera session version; restart the client')
    if client.u32(data['owner']) not in (data['clone_vtable'],VTABLE):
        raise RuntimeError('Camera owner changed; restart client for a full reset')
    if (client.u32(data['owner']+0xa4)!=data['free_camera'] or
            client.u32(data['free_camera'])!=0x142c6e8):
        raise RuntimeError('Free camera changed; no writes made')
    return data


def status(client,data):
    result=native_status(client,data)
    result['focus_request']=client.u32(data['mailbox']+REQUEST)
    result['focus_ack']=client.u32(data['mailbox']+ACK)
    result['focus_result']=client.u32(data['mailbox']+RESULT)
    result['saved_transform']=bool(client.u32(data['mailbox']+SAVED))
    result['actual_mode']=client.u32(data['owner']+0x98)
    result['adapter_version']=data.get('adapter_version',1)
    result['hold_free']=bool(client.u32(data['mailbox']+HOLD_FREE))
    result['mode_reset_count']=client.u32(data['mailbox']+MODE_RESETS)
    result['observed_mode_before_override']=client.u32(data['mailbox']+OBSERVED_MODE)
    result['mode_after_override']=client.u32(data['mailbox']+MODE_AFTER_OVERRIDE)
    active=client.u32(0x1570d80+0xac)
    result['global_active_camera']=hex(active) if active else None
    result['global_active_table']=hex(client.u32(active)) if active else None
    result['free_camera_is_global_active']=active==data['free_camera']
    return result


def log(action,data,result):
    with (ROOT/'runtime'/'private_focus_commands.jsonl').open('a',encoding='utf-8') as f:
        f.write(json.dumps({'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),
                            'action':action,'pid':data['pid'],'status':result})+'\n')


def attach(client, *, replay_kind='timeline'):
    guard(replay_kind)
    if STATE.exists():
        try:
            old=session(client)
            current=status(client,old)
            if current['hook_installed']:
                if old.get('replay_kind','timeline')!=replay_kind:
                    raise RuntimeError('Existing hook belongs to another replay kind; restore/restart first')
                return current
        except (OSError,ValueError,KeyError,RuntimeError):pass
    hits,_=client.scan({'owner':VTABLE})
    candidates=[]
    for owner in hits['owner']:
        try:
            free=client.u32(owner+0xa4)
            if client.u32(free)==0x142c6e8 and client.u32(owner+0x98)==4 and client.u32(owner+0xc4):
                candidates.append((owner,free))
        except OSError:pass
    if len(candidates)!=1:raise RuntimeError('Expected one unhooked initialized player camera; detach other controllers first')
    owner,free=candidates[0]
    if client.u32(VTABLE+0x44)!=UPDATE or client.u32(VTABLE+0x7c)!=CONTEXT_CALLBACK:
        raise RuntimeError('Unexpected native camera update/context callback')
    allocation=k.VirtualAllocEx(client.handle,None,8192,0x3000,4)
    if not allocation or allocation>0xffffffff-8192:raise RuntimeError('Cannot allocate x86 adapter')
    mailbox=allocation+4096;clone=mailbox+0x104
    code,entry=build(allocation,mailbox,clone)
    table=bytearray(client.read(VTABLE-4,0x84))
    struct.pack_into('<I',table,4+0x7c,entry)
    original=client.read(free+0x10,64)
    initial=bytearray(4096);initial[:8]=MAGIC
    struct.pack_into('<I',initial,0x24,owner)
    struct.pack_into('<I',initial,0x28,free)
    struct.pack_into('<I',initial,0x38,1)
    struct.pack_into('<I',initial,0x50,client.pid)
    for offset,text in ((0x80,b'USER32.dll\0'),(0x90,b'GetAsyncKeyState\0')):
        initial[offset:offset+len(text)]=text
    if len(initial)!=4096:raise RuntimeError('Mailbox initialization size differs')
    client.write(allocation,code);client.write(mailbox,initial);client.write(clone-4,table)
    protection=w.DWORD()
    if not k.VirtualProtectEx(client.handle,allocation,4096,0x20,c.byref(protection)):
        raise c.WinError(c.get_last_error())
    if not k.FlushInstructionCache(client.handle,allocation,len(code)):
        raise c.WinError(c.get_last_error())
    data={'pid':client.pid,'owner':owner,'free_camera':free,'allocation':allocation,
          'mailbox':mailbox,'clone_vtable':clone,'entry':entry,'original_vtable':VTABLE,
          'pre_attach_transform':original.hex(),'adapter_bytes':len(code),'adapter_version':VERSION,
          'replay_kind':replay_kind}
    STATE.write_text(json.dumps(data,indent=2),encoding='utf-8')
    # Save recovery addresses BEFORE publishing hook. Never free live adapter pages.
    guard(replay_kind)
    if client.u32(owner)!=VTABLE or client.u32(owner+0xa4)!=free or client.u32(free)!=0x142c6e8:
        raise RuntimeError('Camera changed before publication; no hook installed')
    client.write(owner,struct.pack('<I',clone))
    deadline=time.monotonic()+5
    while time.monotonic()<deadline:
        current=status(client,data)
        if current['heartbeat']>=2:
            log('attach',data,current);return current
        time.sleep(.05)
    if client.u32(owner)==clone:client.write(owner,struct.pack('<I',VTABLE))
    raise RuntimeError('Idle hook failed to tick; original vtable restored')


def require_idle(client,data):
    current=status(client,data)
    if not current['hook_installed'] or current['detached']:raise RuntimeError('Attach first')
    if current['sequence']!=current['ack'] or current['focus_request']!=current['focus_ack']:
        raise RuntimeError('Command pending; do not retry')
    return current


def wait_active_free(client,data):
    deadline=time.monotonic()+2;stable=0
    while time.monotonic()<deadline:
        result=status(client,data)
        if result['actual_mode']==0 and result['free_camera_is_global_active']:
            stable+=1
            if stable>=3:return result
        else:stable=0
        time.sleep(.05)
    raise RuntimeError('Free mode did not become the active camera; inspect state before Focus')


def native_command(client,data,action, *, replay_kind='timeline'):
    if data.get('replay_kind','timeline')!=replay_kind:
        raise RuntimeError('Camera session replay kind differs; no writes made')
    current=require_idle(client,data)
    if action=='free':
        guard(replay_kind)
        if data.get('adapter_version',1)!=VERSION:raise RuntimeError('Unsupported camera version; restart client')
        client.write(data['mailbox']+HOLD_FREE,struct.pack('<I',1))
    if action in ('player','detach'):
        if data.get('adapter_version',1)!=VERSION and current['saved_transform']:
            raise RuntimeError('Unsupported camera version; restart client')
        client.write(data['mailbox']+HOLD_FREE,struct.pack('<I',0))
    if action in ('player','detach') and current['saved_transform'] and current['enabled']:
        focus_command(client,data,'restore')
        current=require_idle(client,data)
    sequence=(current['sequence']+1)&0xffffffff
    client.write(data['mailbox']+12,struct.pack('<I',{'free':1,'player':2,'detach':3}[action]))
    client.write(data['mailbox']+8,struct.pack('<I',sequence))
    deadline=time.monotonic()+5
    while time.monotonic()<deadline:
        result=status(client,data)
        if result['ack']==sequence:
            if result['result']:raise RuntimeError('Native camera command rejected: '+str(result['result']))
            if action=='free':result=wait_active_free(client,data)
            log(action,data,result);return result
        time.sleep(.03)
    raise RuntimeError('Native camera command timed out; inspect status, do not retry')


def focus_matrix(position,distance=6.0):
    if len(position)!=3 or not all(math.isfinite(x) and abs(x)<1e6 for x in position):
        raise ValueError('Invalid target coordinates')
    # Experimental Y-up look-at: subject at torso height, camera above/behind.
    eye=[position[0],position[1]+3,position[2]-distance]
    if not math.isfinite(distance) or not 1<=distance<=50:
        raise ValueError('Focus distance must be between 1 and 50')
    # Native look-at 0x7EE930 takes (output, eye, target, up) and starts
    # with eye - target: row 3 is camera BACK, not viewing direction.
    # Target is soldier position + (0, 1.5, 0); forward is negative row 3.
    back=[0,1.5,-distance]
    length=math.sqrt(sum(x*x for x in back));back=[x/length for x in back]
    right=[-1,0,0]  # normalized cross(world_up, back)
    up=[0,-back[2],back[1]]  # cross(back, right)
    return struct.pack('<16f',*(right+[0]+up+[0]+back+[0]+eye+[0]))


class TargetUnavailable(RuntimeError):
    pass


class CameraNotReady(RuntimeError):
    pass


def target_position(client,name):
    try:
        return _target_position(client,name)
    except (OSError,RuntimeError) as exc:
        raise TargetUnavailable(str(exc)) from exc


def _target_position(client,name):
    # Establish manager/vector first, then find the exact current named object.
    names=[p['name'] for p in roster(client)['players']]
    if names.count(name)!=1:raise RuntimeError('Target absent or ambiguous')
    manager=client._roster_manager
    begin,end,capacity=(client.u32(manager+o) for o in (0x94,0x98,0x9c))
    if not (begin<=end<=capacity and (end-begin)%4==0 and capacity-begin<=1024):raise RuntimeError('Invalid player vector')
    matches=[]
    for slot in range(begin,end,4):
        player=client.u32(slot)
        if client.u32(player)!=PLAYER_TABLE:raise RuntimeError('Player changed')
        nb,ne=client.u32(player+8),client.u32(player+12)
        if not nb or not 0<=ne-nb<=128:raise RuntimeError('Invalid player name')
        if client.read(nb,ne-nb).decode('utf-8','replace')==name:matches.append(player)
    if len(matches)!=1:raise RuntimeError('Target changed')
    player=matches[0];link=soldier_link(client,player)
    if link['status']!='validated_reciprocal_reference':raise RuntimeError('No validated soldier link')
    soldier=int(link['address'],16);position=transform(client,soldier)['candidate_position']
    if (client.u32(player+0xc68)!=soldier or client.u32(soldier+0x220)!=player or
            (begin,end,capacity)!=tuple(client.u32(manager+o) for o in (0x94,0x98,0x9c))):
        raise RuntimeError('Target changed during read')
    return position


def focus_command(client,data,action,name=None,follow_guard=None, *, replay_kind=None):
    if replay_kind is None:replay_kind=data.get('replay_kind','timeline')
    if data.get('replay_kind','timeline')!=replay_kind:
        raise RuntimeError('Camera session replay kind differs; no writes made')
    if action not in ('focus','move','restore'):raise ValueError('Unknown pose command')
    current=require_idle(client,data)
    if not current['enabled']:raise RuntimeError('Enable free camera first')
    command=2
    if action in ('focus','move'):
        if data.get('adapter_version',1)!=VERSION:raise RuntimeError('Unsupported camera version; restart client')
        if action=='move':
            if follow_guard is None:raise RuntimeError('Follow requires persistent replay guard')
            follow_guard.ready()
        else:guard(replay_kind)
        if current['actual_mode']!=0 or not current['free_camera_is_global_active']:
            raise CameraNotReady('Free camera is not the active camera; no pose applied')
        desired=focus_matrix(target_position(client,name))
        client.write(data['mailbox']+TARGET,desired)
        command=3 if action=='move' or current['saved_transform'] else 1
    elif not current['saved_transform']:raise RuntimeError('No saved transform to restore')
    sequence=(current['focus_request']+1)&0xffffffff
    client.write(data['mailbox']+COMMAND,struct.pack('<I',command))
    client.write(data['mailbox']+REQUEST,struct.pack('<I',sequence))
    deadline=time.monotonic()+5
    while time.monotonic()<deadline:
        result=status(client,data)
        if result['focus_ack']==sequence:
            if result['focus_result']:
                if action=='move' and result['focus_result']==1:
                    raise CameraNotReady('Pose rejected during native camera transition')
                raise RuntimeError('Focus command rejected: '+str(result['focus_result']))
            if action=='focus' or (action=='move' and not current['saved_transform']):
                data['saved_focus_transform']=client.read(data['mailbox']+ORIGINAL,64).hex()
                data['focused_player']=name
                STATE.write_text(json.dumps(data,indent=2),encoding='utf-8')
            if action!='move':log(action,data,result)
            return result
        time.sleep(.005 if action=='move' else .03)
    raise RuntimeError('Focus command timed out; inspect status before recovery')
