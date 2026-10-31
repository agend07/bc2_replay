"""r286: seek that keeps destruction.

A timeline seek never restores destruction that happened before its target, in server demos as in built ones;
forward play applies it at any tested speed up to 50x and the client stays connected (r285 §6/§8/§9, live
cl04_1084: 50x reaches 6:31 in 8.2 s). So: play forward fast from the current frame when the client's destruction
is still consistent and the target is ahead, else seek to 0 first and play from there. Speed steps down near the
target because the pause lands late by ~0.15 s x speed (50x overshot 222 frames), and a corrective seek would drop
the destruction again.
"""
import time

FAST_STAGES = ((50.0, 400), (16.0, 120), (4.0, 20))  # (speed, leave the stage this many frames before the target)
POLL_S = 0.03
STALL_S = 5.0  # no frame progress this long while playing -> give up


def plan(target, current, consistent):
    """Start frame of the fast play: current when consistent and target ahead, else 0 (after a seek there)."""
    return current if consistent and target >= current else 0


def seek_keeping_destruction(target, current, consistent, operate, write_speed, sleep=time.sleep,
                             clock=time.monotonic, after_seek_zero=None):
    """Reach target by forward play. operate(action, frame=0) is timeline.operate; write_speed(x) writes the
    raw replay speed (any positive float). Leaves playback paused at the old speed. Returns the final status
    dict plus 'replayed_from', 'elapsed_s'. Raises when the viewer disconnects or playback stalls.
    after_seek_zero(): called after the seek to 0 (r286: put the client's destruction back to level start)."""
    t0 = clock()
    start = plan(target, current, consistent)
    state = operate('status')
    old_speed = state.get('speed', 1.0)
    reset_note = None
    if start != current:
        state = operate('seek', 0)
        if after_seek_zero is not None:
            reset_note = after_seek_zero()
    try:
        frame = state['frame']
        for speed, margin in FAST_STAGES:
            stop = target - margin
            if frame >= stop:
                continue
            write_speed(speed)
            state = operate('play')
            last_frame, last_move = frame, clock()
            while True:
                state = operate('status')
                if state.get('connections') != 1:
                    raise RuntimeError('the viewer disconnected during the fast replay')
                frame = state['frame']
                if frame >= stop:
                    break
                if state.get('paused'):
                    raise RuntimeError(f'playback stopped at frame {frame} (end of the demo?)')
                if frame != last_frame:
                    last_frame, last_move = frame, clock()
                elif clock() - last_move > STALL_S:
                    raise RuntimeError(f'playback stalled at frame {frame}')
                sleep(POLL_S)
        state = operate('pause')
    finally:
        try:
            write_speed(old_speed)
        finally:
            if not state.get('paused', True):
                operate('pause')
    result = dict(operate('status'))  # speed restored, paused
    result['replayed_from'] = start
    result['reset'] = reset_note
    result['elapsed_s'] = clock() - t0
    return result
