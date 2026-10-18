"""Generate an experimental update-thread Focus wrapper; does not install/run it.
Uses a separate mailbox extension. Existing camera adapter handles native modes.
All addresses are exact-build-specific. No remote functions are invoked here.
"""
import struct
from replay.camera_native import adapter

# Extension relative to existing mailbox. Publish request sequence LAST.
REQUEST=0x200
ACK=0x204
COMMAND=0x208  # 1 = save-and-focus, 2 = restore, 3 = move preserving original
RESULT=0x20c   # 0 success, 1 invalid state, 2 no saved transform, 3 already saved
SAVED=0x210
TARGET=0x220   # desired 64-byte camera transform
ORIGINAL=0x260 # saved 64-byte camera transform
WRAPPER_OFFSET=0x400
MODE_RESETS=0x2a0
OBSERVED_MODE=0x2a4
MODE_AFTER_OVERRIDE=0x2a8
HOLD_FREE=0x2ac  # explicit policy, disabled until controller requests free mode
VERSION=4
CONTEXT_CALLBACK=0x800ac0
CONTEXT_BRIDGE_OFFSET=0x800


def make_wrapper(address, original_adapter, mailbox):
    code=bytearray();labels={};fixups=[]
    def emit(h):code.extend(bytes.fromhex(h))
    def word(v):code.extend(struct.pack('<I',v))
    def label(name):labels[name]=len(code)
    def jump(op,name):
        emit(op);fixups.append((len(code),name));word(0)
    # Wrapper runs before existing adapter, which preserves native update semantics.
    emit('9c 60 bb');word(mailbox)
    # Record native mode even when no Focus command is pending.
    emit('3b 4b 24');jump('0f85','return')
    emit('8b 81 98 00 00 00 89 83');word(OBSERVED_MODE)
    emit('83 bb');word(HOLD_FREE);emit('01');jump('0f85','requests')
    emit('83 7b 2c 01');jump('0f85','requests')
    emit('83 7b 30 00');jump('0f85','requests')
    emit('8b 43 08 3b 43 10');jump('0f85','requests')
    # Let native player/detach commands run without reasserting free mode.
    emit('8b 91 a4 00 00 00 3b 53 28');jump('0f85','requests')
    emit('85 d2');jump('0f84','requests')
    emit('81 3a');word(0x142c6e8);jump('0f85','requests')
    emit('83 b9 98 00 00 00 00');jump('0f84','requests')
    emit('ff 83');word(MODE_RESETS)
    emit('6a 00 6a 00 e8')
    word((0x6cbe40-address-len(code)-4)&0xffffffff)
    emit('8b 4b 24 8b 81 98 00 00 00 89 83');word(MODE_AFTER_OVERRIDE)
    label('requests')
    emit('8b 83');word(REQUEST)
    emit('3b 83');word(ACK)
    jump('0f84','return')
    emit('89 c5')  # EBP = request sequence
    emit('c7 83');word(RESULT);word(1)
    # ECX is view owner supplied by game's update dispatch; only our object.
    emit('3b 4b 24');jump('0f85','ack')
    # Native free mode enabled and not detached; no pending native mode command.
    emit('83 7b 2c 01');jump('0f85','ack')
    emit('83 7b 30 00');jump('0f85','ack')
    emit('8b 43 08 3b 43 10');jump('0f85','ack')
    # Restoration of our own saved free-camera transform remains valid even
    # if native replay logic returned the active view to mode 4.
    emit('83 bb');word(COMMAND);emit('02');jump('0f84','check_camera')
    emit('83 b9 98 00 00 00 00');jump('0f85','ack')
    label('check_camera')
    emit('8b 91 a4 00 00 00 3b 53 28');jump('0f85','ack')
    emit('85 d2');jump('0f84','ack')
    emit('81 3a');word(0x142c6e8);jump('0f85','ack')
    emit('8b 83');word(COMMAND)
    emit('83 f8 01');jump('0f84','focus')
    emit('83 f8 02');jump('0f84','restore')
    emit('83 f8 03');jump('0f84','move')
    jump('e9','ack')
    label('focus')
    emit('83 bb');word(SAVED);emit('00');jump('0f85','already_saved')
    # Save transform only on game thread immediately before applying target.
    emit('8d 72 10 8d bb');word(ORIGINAL)
    emit('b9 10 00 00 00 fc f3 a5')
    emit('c7 83');word(SAVED);word(1)
    emit('8d b3');word(TARGET)
    jump('e9','copy')
    label('move')
    emit('83 bb');word(SAVED);emit('01');jump('0f85','focus')
    emit('8d b3');word(TARGET)
    jump('e9','copy')
    label('restore')
    emit('83 bb');word(SAVED);emit('01');jump('0f85','no_saved')
    emit('8d b3');word(ORIGINAL)
    label('copy')
    emit('8d 7a 10 b9 10 00 00 00 fc f3 a5')
    emit('c7 83');word(RESULT);word(0)
    # Keep saved transform after restore for recovery; reject repeated Focus.
    jump('e9','ack')
    label('no_saved')
    emit('c7 83');word(RESULT);word(2)
    jump('e9','ack')
    label('already_saved')
    emit('c7 83');word(RESULT);word(3)
    label('ack')
    emit('89 ab');word(ACK)
    label('return')
    emit('61 9d e9');word((original_adapter-address-len(code)-4)&0xffffffff)
    for off,name in fixups:struct.pack_into('<i',code,off,labels[name]-off-4)
    return bytes(code)


def build(code_address,mailbox,clone_vtable):
    native=adapter(code_address,mailbox,clone_vtable)
    if native[-7:-4]!=b'\x61\x9d\xe9':raise RuntimeError('Native adapter return shape changed')
    # Existing command adapter now runs inside owner virtual +0x7C AFTER its
    # original context callback. Return to UPDATE rather than jumping to UPDATE
    # again. Same-length tail replacement preserves all generated branch offsets.
    native=native[:-5]+b'\xc2\x04\x00\xcc\xcc'
    if len(native)>WRAPPER_OFFSET:raise RuntimeError('Native adapter overlaps wrapper')
    wrapper=make_wrapper(code_address+WRAPPER_OFFSET,code_address,mailbox)
    result=native+b'\xcc'*(WRAPPER_OFFSET-len(native))+wrapper
    if len(result)>CONTEXT_BRIDGE_OFFSET:raise RuntimeError('Focus wrapper overlaps context bridge')
    bridge_address=code_address+CONTEXT_BRIDGE_OFFSET
    # thiscall(float dt): retain original ECX, duplicate dt for native callee,
    # invoke original callback, restore owner ECX, then execute command wrapper.
    bridge=bytearray.fromhex('51 ff 74 24 08 e8')
    bridge.extend(struct.pack('<i',CONTEXT_CALLBACK-(bridge_address+len(bridge)+4)))
    bridge.extend(bytes.fromhex('59 e9'))
    bridge.extend(struct.pack('<i',code_address+WRAPPER_OFFSET-(bridge_address+len(bridge)+4)))
    result+=b'\xcc'*(CONTEXT_BRIDGE_OFFSET-len(result))+bridge
    if len(result)>4096:raise RuntimeError('Adapter exceeds RX page')
    if ORIGINAL+64>0x1000:raise RuntimeError('Mailbox extension exceeds RW page')
    return result,bridge_address
