"""Experimental localhost BC2 replay backend. Uses sanitized fixtures only.
Accepts dummy credentials; never enter a real account password.
No upstream connection or raw traffic recording. Fresh-machine validation pending.
"""
import argparse
import base64
import secrets
import os
import re
import socket
import socketserver
import sys
import threading
import time
HEADER = 12
GID_DEFAULT = 158078
LID = 257
PID_CLIENT = 1000001
UID_HOST = 1000002
CLIENT_NAME = 'LocalViewer'
SERVER_NAME = 'local.replay.server'
LOBBY_NAME = 'bfbc2PC01'
GAME_PORT = 19567
GAME_ADDR = '127.0.0.1'
ROOT = os.path.dirname(os.path.abspath(__file__))
try:
    from backend import clean_fixtures as _cf
    FIXTURES = vars(_cf)
except ImportError:
    import runpy
    FIXTURES = runpy.run_path(os.path.join(ROOT, 'clean_fixtures.py'))
log_lock = threading.Lock()
LOG_DIR = None
QUIET = False

def log(msg):
    stamp = time.strftime('%H:%M:%S')
    with log_lock:
        line = f'[{stamp}] {msg}'
        if not QUIET:
            print(line, flush=True)
        if LOG_DIR:
            try:
                with open(os.path.join(LOG_DIR, 'rome_local.log'), 'a', encoding='utf-8') as fh:
                    fh.write(line + '\n')
            except OSError:
                pass

def frame_of(magic: bytes, ptype: int, frame_id: int, payload: bytes) -> bytes:
    return magic + bytes([ptype]) + frame_id.to_bytes(3, 'big') + (len(payload) + HEADER).to_bytes(4, 'big') + payload

def txn_of(frame: bytes) -> str:
    payload = frame[12:].replace(b'\x00', b'').decode('latin1', 'replace')
    for line in payload.splitlines():
        if line.startswith('TXN='):
            return line[4:].strip()
    return '?'
THEATER_IP_RE = re.compile(b'(theaterIp\\s*=\\s*)[\\d.]+')

def rewrite_theater_ip(frame: bytes) -> bytes:
    """Keep fixture theater addresses local and re-encode the frame length."""
    payload = frame[12:]
    new = THEATER_IP_RE.sub(b'\\g<1>' + GAME_ADDR.encode(), payload)
    if new == payload:
        return frame
    return frame_of(frame[:4], frame[4], int.from_bytes(frame[5:8], 'big'), new)

def kv(payload: bytes) -> dict:
    text = payload.replace(b'\x00', b'').decode('latin1', 'replace')
    out = {}
    for line in text.splitlines():
        if '=' in line:
            k, _, v = line.partition('=')
            out[k.strip()] = v.strip()
    return out

def build_tables():
    return FIXTURES['tables']()
TABLES_CLIENT, TABLES_SERVER, MEMCHECK_PUSH, PING_PUSH = build_tables()

def request_signature(frame):
    fields = kv(frame[12:])
    fields.pop('masterUserId', None)
    return tuple(sorted(fields.items()))

def entitlement_replies(role='client'):
    return FIXTURES['entitlements'](role)
ENTITLEMENT_REPLIES = entitlement_replies()
SERVER_ENTITLEMENT_REPLIES = entitlement_replies('server')

class FeslSession:
    """Replay engine for one FESL connection (role: client|server)."""

    def __init__(self, sock, addr, role):
        self.sock = sock
        self.addr = addr
        self.role = role
        src = TABLES_CLIENT if role == 'client' else TABLES_SERVER
        self.queues = {t: list(q) for t, q in src.items()}
        self.last_reply = {}
        self.fragments = {}
        self.stop = False

    def send_frame(self, frame, note=''):
        try:
            self.sock.sendall(frame)
            log(f'{self.role} fesl -> {txn_of(frame)} ({len(frame)}B) {note}')
        except OSError:
            self.stop = True

    def send_txn_reply(self, txn, request=None):
        if txn == 'GetStats' and request is not None:
            fields = kv(request[12:])
            group = FIXTURES['stats_reply'](request[:4].decode('ascii'), fields)
            log('{} stats metadata: requested_keys={} reply_frames={}'.format(self.role, fields.get('keys.[]', '0'), len(group)))
            for frame in group:
                frame = frame_of(frame[:4], frame[4], int.from_bytes(request[5:8], 'big'), frame[12:])
                self.send_frame(frame)
            return
        q = self.queues.get(txn)
        if txn == 'NuGetEntitlements' and request is not None:
            entitlement_table = ENTITLEMENT_REPLIES if self.role == 'client' else SERVER_ENTITLEMENT_REPLIES
            group = entitlement_table.get(request_signature(request))
            if group is None:
                log(f'{self.role} fesl: unmatched entitlement query (values omitted)')
                group = [frame_of(request[:4], 128, 0, b'TXN=NuGetEntitlements\nentitlements.[]=0\x00')]
        elif q:
            group = q.pop(0)
            self.last_reply[txn] = group
        else:
            group = self.last_reply.get(txn)
            if group is None:
                log(f'{self.role} fesl: no captured reply for {txn}, sending stub')
                group = [frame_of(b'fsys', 128, 0, f'TXN={txn}\nTID=0\n'.encode())]
        for frame in group:
            if txn == 'Hello':
                frame = rewrite_theater_ip(frame)
            if request is not None:
                frame = frame_of(frame[:4], frame[4], int.from_bytes(request[5:8], 'big'), frame[12:])
            self.send_frame(frame)

    def ping_loop(self):
        while not self.stop:
            time.sleep(20)
            if not self.stop:
                self.send_frame(PING_PUSH or frame_of(b'fsys', 0, 0, b'TXN=Ping\n'), '(keepalive)')

    def run(self):
        threading.Thread(target=self.ping_loop, daemon=True).start()
        buf = b''
        memcheck_sent = False
        try:
            self.sock.settimeout(600)
            while not self.stop:
                try:
                    data = self.sock.recv(65536)
                except socket.timeout:
                    continue
                except OSError:
                    break
                if not data:
                    break
                buf += data
                while True:
                    if len(buf) < HEADER:
                        break
                    magic = buf[:4]
                    if not (magic.isascii() and magic.islower()):
                        log(f'{self.role} fesl: bad magic {buf[:8].hex()}, dropping byte')
                        buf = buf[1:]
                        continue
                    total = int.from_bytes(buf[8:12], 'big')
                    if total < HEADER or len(buf) < total:
                        break
                    frame, buf = (buf[:total], buf[total:])
                    ptype, t = (frame[4], txn_of(frame))
                    if ptype == 240:
                        import base64
                        fields = kv(frame[12:])
                        key = frame[:8]
                        expected = int(fields.get('size', '0'))
                        if not 0 < expected <= 4 * 1024 * 1024:
                            raise ValueError('invalid fragmented request size')
                        previous_size, encoded = self.fragments.get(key, (expected, ''))
                        if previous_size != expected:
                            raise ValueError('fragment size changed')
                        from urllib.parse import unquote
                        chunk = fields.get('data', '')
                        escaped = chunk.count('%')
                        # Wire values percent-escape base64 padding and sometimes +/ /.
                        # size counts base64 characters AFTER percent decoding.
                        # Do not use unquote_plus: + is a legitimate base64 character.
                        chunk = unquote(chunk, encoding='ascii', errors='strict')
                        encoded += chunk
                        log('{} fragment metadata: advertised={} accumulated={} escapes={}'.format(self.role, expected, len(encoded), escaped))
                        if len(encoded) > expected:
                            raise ValueError('fragmented request exceeds size')
                        if len(encoded) < expected:
                            self.fragments[key] = (expected, encoded)
                            continue
                        self.fragments.pop(key, None)
                        frame = frame_of(magic, 192, int.from_bytes(frame[5:8], 'big'), base64.b64decode(encoded, validate=True))
                        t = txn_of(frame)
                    if ptype in (192, 240):
                        log(f'{self.role} fesl <- {t}')
                        if t == 'Hello':
                            self.send_txn_reply('Hello', frame)
                            if not memcheck_sent and MEMCHECK_PUSH is not None:
                                memcheck_sent = True
                                self.send_frame(MEMCHECK_PUSH, '(MemCheck push)')
                        elif t == 'UpdateStats':
                            self.send_frame(frame, '(echo)')
                        else:
                            self.send_txn_reply(t, frame)
                    elif ptype == 128:
                        log(f'{self.role} fesl <- ack {t}')
                    else:
                        log(f'{self.role} fesl <- type {ptype:02x} {t}')
        except Exception as exc:
            log(f'{self.role} fesl session error: {exc!r}')
        finally:
            try:
                self.sock.close()
            except OSError:
                pass
            log(f'{self.role} fesl closed {self.addr}')

class TheaterState:

    def __init__(self):
        self.lock = threading.Lock()
        self.server_sock = None
        self.game = {}
        self.ekey = 'AAAAAAAAAAAAAAAAAAAAAA%3d'
        self.secret = base64.b64encode(secrets.token_bytes(64)).decode('ascii')
        self.ugid = '00000000-0000-4000-8000-000000000001'
        self.gid = GID_DEFAULT
        self.next_ticket = 8143829
        self.max_players = '32'
STATE = TheaterState()

def payload_kv(tid, pairs):
    out = [f'TID={tid}']
    out.extend((f'{k}={v}' for k, v in pairs))
    return ('\n'.join(out) + '\x00').encode()

def load_theater_client_replies():
    return FIXTURES['theater']()
CLIENT_THEATER_REPLIES = load_theater_client_replies()

def tid_swap(payload: bytes, tid) -> bytes:
    return re.sub(b'TID=\\d+', b'TID=' + str(tid).encode(), payload, count=1)

def kv_patch(payload: bytes, key: str, value) -> bytes:
    return re.sub(('(?m)^' + re.escape(key) + '=[^\\n\\x00]*').encode(), (key + '=' + str(value)).encode(), payload, count=1)

def captured_reply(magic: bytes, nth=0):
    frames = [f for f in CLIENT_THEATER_REPLIES if f[:4] == magic]
    if not frames:
        return None
    return frames[min(nth, len(frames) - 1)]

def send_captured(sock, magic: bytes, tid, nth=0, patches=None):
    """Replay one of Rome's theater replies, TID-adjusted, optionally patched."""
    frame = captured_reply(magic, nth)
    if frame is None:
        out = frame_of(magic, 0, 0, payload_kv(tid, []))
    else:
        payload = frame[12:]
        payload = tid_swap(payload, tid)
        for k, v in patches or []:
            payload = kv_patch(payload, k, v)
        out = frame_of(frame[:4], frame[4], int.from_bytes(frame[5:8], 'big'), payload)
    return out

def our_game_gdat(tid, player_count=None):
    """The captured GDAT entry of OUR server (GID 158078), IP rewritten."""
    for f in CLIENT_THEATER_REPLIES:
        if f[:4] == b'GDAT' and b'GID=158078' in f[12:]:
            payload = f[12:]
            payload = re.sub(b'(?m)^I=[\\d.]+$', b'I=' + GAME_ADDR.encode(), payload)
            payload = tid_swap(payload, tid)
            g = STATE.game
            if g.get('B-U-level'):
                payload = kv_patch(payload, 'B-U-level', g['B-U-level'])
            if g.get('B-U-gamemode'):
                payload = kv_patch(payload, 'B-U-gamemode', g['B-U-gamemode'])
            if g.get('B-U-Time'):
                payload = kv_patch(payload, 'B-U-Time', g['B-U-Time'].replace(' ', '%20'))
            for key in ('AP', 'B-U-elo'):
                if key in g:
                    payload = kv_patch(payload, key, g[key])
            if player_count is not None:
                payload = kv_patch(payload, 'AP', player_count)
            return frame_of(b'GDAT', 0, 0, payload)
    return frame_of(b'GDAT', 0, 0, payload_kv(tid, [('LID', LID), ('GID', STATE.gid), ('TYPE', 'G'), ('HN', SERVER_NAME), ('HU', UID_HOST), ('N', '"Local%20Replay"'), ('I', GAME_ADDR), ('P', GAME_PORT), ('J', 'O'), ('MP', STATE.max_players), ('AP', player_count if player_count is not None else 0), ('PL', 'PC'), ('PW', ''), ('V', '2.0'), ('B-U-level', STATE.game.get('B-U-level', 'Levels/MP_006')), ('B-U-gamemode', STATE.game.get('B-U-gamemode', 'CONQUEST')), ('B-U-gameMod', 'BC2'), ('B-U-public', '1')]))

class TheaterClientSession:
    """Byte-exact replay of Rome's theater replies to the client.

    Only TIDs, the game-server IP, the join ticket/EKEY and the lobby/game
    counts (65 -> 1) are adjusted; everything else is Rome's own bytes.
    """

    def __init__(self, sock, addr):
        self.sock = sock
        self.addr = addr
        self.stop = False

    def send(self, frame):
        try:
            self.sock.sendall(frame)
        except OSError:
            self.stop = True

    def run(self):
        buf = b''
        try:
            self.sock.settimeout(600)
            while not self.stop:
                try:
                    data = self.sock.recv(65536)
                except socket.timeout:
                    continue
                except OSError:
                    break
                if not data:
                    break
                buf += data
                while len(buf) >= HEADER:
                    magic = buf[:4]
                    if not (magic.isascii() and magic.isupper()):
                        buf = buf[1:]
                        continue
                    total = int.from_bytes(buf[8:12], 'big')
                    if total < HEADER or len(buf) < total:
                        break
                    frame, buf = (buf[:total], buf[total:])
                    if magic == b'PING':
                        pf = captured_reply(b'PING')
                        self.send(pf or frame_of(b'PING', 0, 0, b'\x00'))
                        continue
                    kvs = kv(frame[12:])
                    tid = kvs.get('TID', '0')
                    log(f'theater-client <- {magic.decode()} TID={tid}')
                    if magic == b'CONN':
                        self.send(send_captured(self.sock, b'CONN', tid))
                    elif magic == b'USER':
                        self.send(send_captured(self.sock, b'USER', tid))
                    elif magic == b'LLST':
                        self.send(send_captured(self.sock, b'LLST', tid))
                        n = 1 if STATE.server_sock else 0
                        self.send(send_captured(self.sock, b'LDAT', tid, patches=[('PASSING', n), ('NUM-GAMES', n)]))
                    elif magic == b'GLST':
                        cursor = int(kvs.get('GID', '0'))
                        n = 1 if STATE.server_sock and cursor <= STATE.gid else 0
                        self.send(send_captured(self.sock, b'GLST', tid, patches=[('LOBBY-NUM-GAMES', 1 if STATE.server_sock else 0), ('NUM-GAMES', n)]))
                        if n:
                            self.send(our_game_gdat(tid))
                    elif magic == b'EGAM':
                        self.handle_join(kvs, tid)
                    elif magic == b'GDAT':
                        self.send(our_game_gdat(tid, player_count=1))
                        self.send(send_captured(self.sock, b'GDET', tid))
                        self.send(send_captured(self.sock, b'PDAT', tid))
                    else:
                        log(f'theater-client: unhandled {magic!r}')
        except Exception as exc:
            log(f'theater-client error: {exc!r}')
        finally:
            try:
                self.sock.close()
            except OSError:
                pass
            log(f'theater-client closed {self.addr}')

    def handle_join(self, kvs, tid):
        gid = kvs.get('GID', str(STATE.gid))
        with STATE.lock:
            ticket = STATE.next_ticket
            STATE.next_ticket += 7
        srv = STATE.server_sock
        if srv is not None:
            egrq = payload_kv(tid, [('R-INT-PORT', kvs.get('R-INT-PORT', '10000')), ('R-INT-IP', kvs.get('R-INT-IP', '127.0.0.1')), ('PORT', kvs.get('PORT', '10000')), ('NAME', f'"{CLIENT_NAME}"'), ('PTYPE', 'P'), ('TICKET', ticket), ('PID', PID_CLIENT), ('UID', PID_CLIENT), ('IP', GAME_ADDR), ('LID', LID), ('GID', gid)])
            try:
                srv.sendall(frame_of(b'EGRQ', 0, 0, egrq))
                log(f'join: EGRQ pushed to server (ticket={ticket})')
            except OSError:
                log('join: server session dead')
        else:
            log('join: NO registered server session!')
        self.send(send_captured(self.sock, b'EGAM', tid))
        egeg = captured_reply(b'EGEG')
        payload = egeg[12:]
        payload = tid_swap(payload, tid)
        for k, v in (('TICKET', ticket), ('EKEY', STATE.ekey), ('I', GAME_ADDR), ('INT-IP', GAME_ADDR), ('INT-PORT', GAME_PORT), ('UGID', STATE.ugid)):
            payload = kv_patch(payload, k, v)
        self.send(frame_of(b'EGEG', 0, 0, payload))
        log(f'join: EGEG sent (ticket={ticket}, {GAME_ADDR}:{GAME_PORT})')

class TheaterServerSession:

    def __init__(self, sock, addr):
        self.sock = sock
        self.addr = addr
        self.stop = False

    def send(self, magic, payload, note=''):
        try:
            self.sock.sendall(frame_of(magic, 0, 0, payload))
            log(f'theater-server -> {magic.decode()} {note}')
        except OSError:
            self.stop = True

    def run(self):
        STATE.server_sock = self.sock
        buf = b''
        try:
            self.sock.settimeout(600)
            while not self.stop:
                try:
                    data = self.sock.recv(65536)
                except socket.timeout:
                    continue
                except OSError:
                    break
                if not data:
                    break
                buf += data
                while len(buf) >= HEADER:
                    magic = buf[:4]
                    if not (magic.isascii() and magic.isupper()):
                        buf = buf[1:]
                        continue
                    total = int.from_bytes(buf[8:12], 'big')
                    if total < HEADER or len(buf) < total:
                        break
                    frame, buf = (buf[:total], buf[total:])
                    kvs = kv(frame[12:])
                    tid = kvs.get('TID', '0')
                    if magic == b'PING':
                        self.send(b'PING', payload_kv('0', []))
                        continue
                    log(f'theater-server <- {magic.decode()} TID={tid}')
                    if magic == b'CONN':
                        self.send(b'CONN', payload_kv(tid, [('TIME', '0'), ('activityTimeoutSecs', '240'), ('PROT', '2')]))
                    elif magic == b'USER':
                        self.send(b'USER', payload_kv(tid, [('NAME', SERVER_NAME)]))
                    elif magic == b'CGAM':
                        with STATE.lock:
                            STATE.ugid = kvs.get('UGID') or STATE.ugid
                            STATE.gid = GID_DEFAULT
                            STATE.secret = kvs.get('SECRET') or STATE.secret
                            STATE.max_players = kvs.get('MAX-PLAYERS', '32')
                        self.send(b'CGAM', payload_kv(tid, [('MAX-PLAYERS', STATE.max_players), ('EKEY', STATE.ekey), ('UGID', STATE.ugid), ('JOIN', '0'), ('LID', LID), ('SECRET', STATE.secret), ('J', 'O'), ('GID', STATE.gid)]), f'(registered GID={STATE.gid})')
                        log(f"theater-server: game '{kvs.get('NAME', '?')}' registered, level={kvs.get('B-U-level', '?')}")
                    elif magic == b'UBRA':
                        self.send(b'UBRA', payload_kv(tid, []))
                    elif magic == b'UGAM':
                        STATE.game.update({k: v for k, v in kvs.items() if k.startswith('B-U-') or k == 'NAME'})
                        log(f"theater-server: UGAM {kvs.get('B-U-Time', '')}")
                    elif magic == b'UGDE':
                        STATE.game.update({k: v for k, v in kvs.items() if k.startswith('D-')})
                    elif magic == b'EGRS':
                        self.send(b'EGRS', payload_kv(tid, []), '(join allowed)')
                    elif magic == b'PENT':
                        self.send(b'PENT', payload_kv(tid, [('PID', kvs.get('PID', PID_CLIENT))]))
                    elif magic == b'PLVT':
                        self.send(b'PLVT', payload_kv(tid, [('PID', kvs.get('PID', PID_CLIENT)), ('LID', LID), ('GID', STATE.gid)]))
                    else:
                        log(f'theater-server: unhandled {magic!r}')
        except Exception as exc:
            log(f'theater-server error: {exc!r}')
        finally:
            try:
                self.sock.close()
            except OSError:
                pass
            if STATE.server_sock is self.sock:
                STATE.server_sock = None
                STATE.game = {}
            log(f'theater-server closed {self.addr}')

class ThreadedTCPServer(socketserver.ThreadingMixIn, socketserver.TCPServer):
    allow_reuse_address = os.name != 'nt'
    daemon_threads = True

    def server_bind(self):
        if os.name == 'nt':
            self.socket.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
        super().server_bind()

    def get_request(self):
        sock, addr = super().get_request()
        return (sock, addr)

class FeslClientHandler(socketserver.BaseRequestHandler):

    def handle(self):
        FeslSession(self.request, self.client_address, 'client').run()

class FeslServerHandler(socketserver.BaseRequestHandler):

    def handle(self):
        FeslSession(self.request, self.client_address, 'server').run()

class WrappedHandler(socketserver.BaseRequestHandler):
    session_cls = None

    def handle(self):
        self.session_cls(self.request, self.client_address).run()

def make_handler(cls):
    return type('H', (WrappedHandler,), {'session_cls': cls})

def load_http_replies():
    return FIXTURES['http']()
HTTP_REPLIES = load_http_replies()

def http_reply(path):
    return HTTP_REPLIES.get(path, b'HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n')

class HttpHandler(socketserver.BaseRequestHandler):

    def handle(self):
        try:
            self.request.settimeout(300)
            buf = b''
            while True:
                data = self.request.recv(65536)
                if not data:
                    return
                buf += data
                if len(buf) > 65536:
                    return
                while b'\r\n\r\n' in buf:
                    header, buf = buf.split(b'\r\n\r\n', 1)
                    line = header.split(b'\r\n', 1)[0].split()
                    if len(line) != 3 or line[0] != b'GET':
                        return
                    reply = http_reply(line[1])
                    log(f"http GET {line[1].split(b'?', 1)[0].decode('ascii', 'replace')} -> {reply.splitlines()[0].decode('ascii', 'replace')}")
                    self.request.sendall(reply)
                    if b'connection: close' in reply.split(b'\r\n\r\n', 1)[0].lower():
                        return
        except OSError:
            pass

class UdpListener(threading.Thread):

    def __init__(self, port):
        super().__init__(daemon=True)
        self.port = port

    def run(self):
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        try:
            sock.bind(('127.0.0.1', self.port))
        except OSError as exc:
            log(f'udp {self.port}: bind failed: {exc}')
            return
        log(f'udp:{self.port} listening (ECHO probes ignored)')
        while True:
            try:
                data, addr = sock.recvfrom(65536)
            except OSError:
                return
            if data[:4] != b'ECHO':
                log(f'udp:{self.port} <- {len(data)}B from {addr}: {data[:40].hex()}')

def main():
    global LOG_DIR, QUIET
    ap = argparse.ArgumentParser()
    ap.add_argument('--out', default='logs')
    ap.add_argument('--quiet', action='store_true')
    args = ap.parse_args()
    QUIET = args.quiet
    LOG_DIR = args.out if os.path.isabs(args.out) else os.path.join(ROOT, args.out)
    os.makedirs(LOG_DIR, exist_ok=True)
    empty_client = [t for t, q in TABLES_CLIENT.items() if not q]
    log(f"FESL tables: client={len(TABLES_CLIENT)} txns, server={len(TABLES_SERVER)} txns, memcheck={('yes' if MEMCHECK_PUSH else 'NO')}")
    if empty_client:
        log(f'warn: client txns with empty reply queues: {empty_client}')
    servers = []
    handlers = [(80, 'http', HttpHandler), (18390, 'fesl-client', FeslClientHandler), (19021, 'fesl-server', FeslServerHandler), (18395, 'theater-client', make_handler(TheaterClientSession)), (19026, 'theater-server', make_handler(TheaterServerSession))]
    for port, hname, hcls in handlers:
        try:
            srv = ThreadedTCPServer(('127.0.0.1', port), hcls)
        except OSError as exc:
            log(f'FATAL: cannot bind 127.0.0.1:{port} - {exc}')
            log('Is another local backend already running? Close it and retry.')
            sys.exit(1)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        servers.append(srv)
        log(f'tcp:127.0.0.1:{port} listening ({hname})')
    for port in (18100, 18395, 19026):
        UdpListener(port).start()
    log('LOCAL ROME BACKEND READY - use dummy credentials to join Local Replay.')
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        log('shutting down')
if __name__ == '__main__':
    main()
