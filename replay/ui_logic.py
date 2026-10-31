"""Pure helpers of the controller window (no tkinter widgets) so they can be tested offline."""
import time

from replay.timeline import FPS, seek_frame


def format_mmss(seconds):
    total = max(0, int(round(seconds)))
    m, s = divmod(total, 60)
    h, m = divmod(m, 60)
    return f'{h}:{m:02d}:{s:02d}' if h else f'{m:02d}:{s:02d}'


def parse_time_entry(text, limit):
    """Time entry -> (frame, None) or (None, error). Accepts seconds, mm:ss or hh:mm:ss within 0..limit frames."""
    if not str(text).strip():
        return None, 'Type a time: seconds, mm:ss or hh:mm:ss'
    try:
        return seek_frame(text, limit), None
    except ValueError as exc:
        if str(exc).startswith('Choose a time'):
            return None, f'Out of range: 0 to {format_mmss(limit / FPS)}'
        return None, str(exc)


def should_auto_launch(armed_base, active_base, server_ready, client_pids):
    """After Replay (button): launch BC2 once the switched-to server is ready, unless a client already runs.

    Returns 'wait' (keep armed), 'launch' or 'skip' (disarm: a client is running)."""
    if not armed_base or not server_ready or active_base != armed_base:
        return 'wait'
    return 'skip' if client_pids else 'launch'


def cycle_player(names, current, valid, step):
    """In-game '<' / '>': the previous (step -1) or next (+1) player after current in names (the Player
    list order) that is in valid (players with a live soldier), wrapping around. None if nobody is valid."""
    if not names:
        return None
    n = len(names)
    start = names.index(current) if current in names else (-1 if step > 0 else 0)
    for k in range(1, n + 1):
        name = names[(start + step * k) % n]
        if name in valid:
            return name
    return None


FOLLOW_TEXT = 'Follow (experimental)'
STOP_FOLLOW_TEXT = 'Stop follow'


def follow_button_text(following):
    """The single Follow toggle: its label follows the follow thread (reverts when Follow ends by itself)."""
    return STOP_FOLLOW_TEXT if following else FOLLOW_TEXT


class AutoPlay:
    """r286: after a join at the start of a demo, start playback by itself once the client is in the map.

    'In the map' = the level-start destruction snapshot was taken (the client's level is bound). The HUD and
    the load-gap seek come only once playback runs (live cl04_1084), so they cannot be waited for. Plays
    SETTLE_S after that; dropped when playback already runs (SPACE) or after GIVE_UP_S."""
    SETTLE_S = 3.0  # level bound -> map on screen (live: 10 s after the join was always fine)
    GIVE_UP_S = 180.0

    def __init__(self, clock=time.monotonic):
        self.clock = clock
        self.armed_at = None
        self.level_at = None

    def arm(self):
        self.armed_at, self.level_at = self.clock(), None

    def cancel(self):
        self.armed_at = self.level_at = None

    @property
    def armed(self):
        return self.armed_at is not None

    def action(self, paused, in_level):
        """'idle' (not armed), 'wait', 'play' (caller plays now and cancels) or 'give_up'."""
        if self.armed_at is None:
            return 'idle'
        now = self.clock()
        if not paused:
            self.cancel()  # already playing
            return 'idle'
        if now - self.armed_at > self.GIVE_UP_S:
            self.cancel()
            return 'give_up'
        if not in_level:
            return 'wait'
        if self.level_at is None:
            self.level_at = now
        return 'play' if now - self.level_at >= self.SETTLE_S else 'wait'


PLAY_REJECTED_TEXT = ('BC2 left the replay right after Play - the demo has data the client rejects; '
                      'please report the demo name: {name}')


class PlayWatch:
    """Playability oracle (2026-10-30, 10 server logs): a client that survives playback logs
    obtainVoiceReference ~1.9 s after Play; one that rejects the demo data leaves by itself ~0.2 s after Play.
    Armed when a plain Play lands with a viewer connected; the viewer count dropping to 0 within WINDOW_S =
    'rejected'. The caller cancels it on seeks (a seek can drop the client for other reasons), recording
    switches and panel stops (those kill the client on purpose)."""
    WINDOW_S = 10.0

    def __init__(self, clock=time.monotonic):
        self.clock = clock
        self.started_at = None

    def arm(self):
        self.started_at = self.clock()

    def cancel(self):
        self.started_at = None

    @property
    def armed(self):
        return self.started_at is not None

    def check(self, connections):
        """'idle' (not armed), 'wait', 'ok' (survived the window; disarmed) or 'rejected' (disarmed)."""
        if self.started_at is None:
            return 'idle'
        elapsed = self.clock() - self.started_at
        if connections >= 1:
            if elapsed > self.WINDOW_S:
                self.cancel()
                return 'ok'
            return 'wait'
        self.cancel()
        return 'rejected' if elapsed <= self.WINDOW_S else 'idle'
