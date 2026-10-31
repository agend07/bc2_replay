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
from replay.players import roster, PLAYER_TABLE, soldier_link, vehicle_link, link_position, VEHICLE_LINK

STATE=ROOT/'runtime'/'private_focus_session.json'
# r222d: deploy-screen mouse-look fallback, look-axis units per cursor pixel (live device quantum
# 0.0082). X -> mapper +0x24, Y -> +0x20. Negate to invert; 0 for X disables. Applies on next attach.
LOOK_SCALE_X=0.0082
LOOK_SCALE_Y=-0.0082  # live 2026-10-26: up/down was inverted vs normal look
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
    # r218 diagnostics: [0x1570E98] = the one input mapper currently fed by input.
    current_mapper=client.u32(0x1570e98)
    result['current_input_mapper']=hex(current_mapper) if current_mapper else None
    result['free_mapper_is_current']=current_mapper==client.u32(data['free_camera']+0x80)
    return result


def probe(client,data):
    """r222 read-only diagnostics for 'free camera stops moving after death'.
    FreeCamera update 0x7AC1E0 (vslot +0x28) reads its mapper's axes +0x10/+0x14/+0x18 (move)
    and +0x20/+0x24 (look) and accumulates look into FreeCamera +0x98. Axes 0 while WASD held =
    mapper not fed; axes set but position/yaw static = FreeCamera update not running;
    position moving on its own = someone else writes the transform."""
    owner,free=data['owner'],data['free_camera']
    mapper=client.u32(free+0x80)
    f=lambda a:round(struct.unpack('<f',client.read(a,4))[0],4)
    stack_begin,stack_end=client.u32(0x1570ed0),client.u32(0x1570ed4)
    span=stack_end-stack_begin
    stack=[]
    if stack_begin and 0<=span<=0x80 and span%8==0:
        raw=client.read(stack_begin,span)
        stack=['%#x:%d'%(m,p) for m,p in struct.iter_unpack('<Ii',raw)]
    elif span:stack=['unread span %#x'%span]
    blocked=(client.u32(0x1570ee4)-client.u32(0x1570ee0))//4
    return {
        'owner_vtable_is_clone':client.u32(owner)==data['clone_vtable'],
        'owner_target_c4':hex(client.u32(owner+0xc4)),
        'owner_view_ac':hex(client.u32(owner+0xac)),
        'owner_entry_b8':hex(client.u32(owner+0xb8)),
        'move_axes':[f(mapper+o) for o in (0x10,0x14,0x18)],
        'look_axes':[f(mapper+o) for o in (0x20,0x24)],
        'free_look_accum_98':f(free+0x98),
        'pos':[round(x,2) for x in struct.unpack('<3f',client.read(free+0x40,12))],
        'mapper_stack':stack,
        'ui_blocked_actions':blocked if 0<=blocked<=256 else None,
        'ui_block_bypass':client.read(0x1570e9c,1)[0],
        # r222b: free mapper vtable 0x142BDD0 update 0x78DC60 reads axes via device [mapper+4]
        # (attached by push 0x7E1610 from [0x1570E90]); +0x30 = ticks since FreeCamera consumed.
        'mapper_device':hex(client.u32(mapper+4)),
        'input_device':hex(client.u32(0x1570e90)),
        'mapper_ticks_30':client.u32(mapper+0x30),
        'owner_mode3_obj_d0':hex(client.u32(owner+0xd0)),
        # r222c: input device (vtable 0x14196F8) action read 0x580550 -> binding map [dev+0x14]
        # (0x7C32E0) -> per-binding read 0x596D20; log the map and the device flag bytes.
        'device_binding_map_14':hex(client.u32(client.u32(0x1570e90)+0x14)),
        'device_flags_10_1c':client.read(client.u32(0x1570e90)+0x10,0x10).hex(),
    }


def log(action,data,result,client=None):
    if client is not None:  # r222: snapshot owner/target/input state with each command
        try:result=dict(result,probe=probe(client,data))
        except Exception as exc:result=dict(result,probe=str(exc))
    with (ROOT/'runtime'/'private_focus_commands.jsonl').open('a',encoding='utf-8') as f:
        f.write(json.dumps({'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),
                            'action':action,'pid':data['pid'],'status':result})+'\n')


def pick_owner(rows):
    """r222: choose the local CameraOwner. Before the viewer's soldier exists (joining, deploy
    screen, demos without a viewer soldier) activeTarget +0xC4 is 0, so it is no longer required.
    r222b: before spawn the single owner sits in mode 3 (target 0, view +0xAC set); adapter v6 enables
    from 3 or 4. Entering free mode pushes the +0xAC camera, so view must be non-zero.
    Preference: exactly one with a target, else exactly one at all. Error text lists every owner."""
    usable=[r for r in rows if r['free_ok'] and r['mode'] in (3,4) and r['view']]
    targeted=[r for r in usable if r['target']]
    pick=targeted if len(targeted)==1 else usable
    if len(pick)==1:return pick[0]['owner'],pick[0]['free']
    detail=', '.join('%#x mode=%d target=%#x view=%#x free_ok=%d'%(r['owner'],r['mode'],r['target'],r['view'],r['free_ok'])
                     for r in rows[:6]) or 'none'
    raise RuntimeError('Expected one player camera in mode 3/4; found %d owner(s), %d usable: %s'%(len(rows),len(usable),detail))


_OWNERS={}  # pid -> camera owner found by the last attach


def owner_rows(client,owners):
    rows=[]
    for owner in owners:
        try:
            free=client.u32(owner+0xa4)
            rows.append({'owner':owner,'free':free,'free_ok':client.u32(free)==0x142c6e8,
                         'mode':client.u32(owner+0x98),'target':client.u32(owner+0xc4),
                         'view':client.u32(owner+0xac)})
        except OSError:pass
    return rows


def warm_owner(client):
    """Read-only (r285): find this client's camera owner in the background so the first attach skips the scan."""
    if client.pid in _OWNERS:return
    hits,_=client.scan({'owner':VTABLE})
    try:_OWNERS[client.pid]=pick_owner(owner_rows(client,hits['owner']))[0]
    except RuntimeError:pass


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
    picked=None
    cached=_OWNERS.get(client.pid)  # r285: skip the ~2.4 s scan when this client's owner still validates
    if cached is not None:
        try:
            if client.u32(cached)==VTABLE:picked=pick_owner(owner_rows(client,[cached]))
        except (OSError,RuntimeError):picked=None
    if picked is None:
        hits,_=client.scan({'owner':VTABLE})
        picked=pick_owner(owner_rows(client,hits['owner']))
    owner,free=picked
    _OWNERS[client.pid]=owner
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
    for offset,text in ((0x80,b'USER32.dll\0'),(0x90,b'GetAsyncKeyState\0'),  # r222d: cursor look
                        (0xa4,b'GetCursorInfo\0'),(0xb4,b'GetCursorPos\0'),(0xc4,b'SetCursorPos\0')):
        initial[offset:offset+len(text)]=text
    struct.pack_into('<ff',initial,0x60,LOOK_SCALE_X,LOOK_SCALE_Y)
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
            log('attach',data,current,client);return current
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
            log(action,data,result,client);return result
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


VEHICLE_FOCUS_DISTANCE = 12.0  # r286: 6 m behind a vehicle origin is inside a tank / under a rotor


def target_position(client,name):
    return target_body(client,name)[0]


def target_body(client,name):
    """(position, 'soldier' | 'vehicle') of the named player's soldier, or of the vehicle it is in (r286)."""
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
    if link['status']!='validated_reciprocal_reference':
        link=vehicle_link(client,player)  # r286: in a vehicle +0xC68 is the vehicle
        if link['status']!=VEHICLE_LINK:raise RuntimeError('No validated soldier or vehicle link')
    body=int(link['address'],16);position=link_position(client,link)
    vehicle=link['status']==VEHICLE_LINK
    if (client.u32(player+0xc68)!=body or (not vehicle and client.u32(body+0x220)!=player) or
            (begin,end,capacity)!=tuple(client.u32(manager+o) for o in (0x94,0x98,0x9c))):
        raise RuntimeError('Target changed during read')
    return position,('vehicle' if vehicle else 'soldier')


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
        position,kind=target_body(client,name)
        desired=focus_matrix(position,VEHICLE_FOCUS_DISTANCE if kind=='vehicle' else 6.0)
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
            if action!='move':log(action,data,result,client)
            return result
        time.sleep(.005 if action=='move' else .03)
    raise RuntimeError('Focus command timed out; inspect status before recovery')
