"""Load readable, sanitized protocol fixtures. No private captures or network I/O."""
import base64
import json
from pathlib import Path

def _find_protocol_fixtures():
    here = Path(__file__).with_name('protocol_fixtures.json')
    if here.is_file():
        return here
    import sys
    if getattr(sys, 'frozen', False):
        cand = Path(sys.executable).resolve().parent / 'backend' / 'protocol_fixtures.json'
        if cand.is_file():
            return cand
        meipass = getattr(sys, '_MEIPASS', None)
        if meipass:
            cand2 = Path(meipass) / 'backend' / 'protocol_fixtures.json'
            if cand2.is_file():
                return cand2
    return here

DATA = json.loads(_find_protocol_fixtures().read_text(encoding='utf-8'))
if DATA.get('schema') != 1:
    raise ValueError('Unsupported fixture schema')


def frame(magic, kind, payload):
    return magic.encode('ascii') + bytes([kind, 0, 0, 0]) + (len(payload)+12).to_bytes(4, 'big') + payload


def payload(fields):
    return ('\n'.join(k+'='+v for k,v in fields.items())+'\x00').encode('ascii')


def group(item):
    body = payload(item['fields'])
    if not item.get('fragmented'):
        return [frame(item['magic'], 0x80, body)]
    encoded = base64.b64encode(body).decode('ascii')
    # Size is the total number of base64 characters, not decoded bytes.
    return [frame(item['magic'], 0xb0, payload({'size':str(len(encoded)), 'decodedSize':str(len(body)), 'data':encoded[i:i+4096]}))
            for i in range(0,len(encoded),4096)]


def tables():
    client, server = [{txn:[group(item) for item in items] for txn,items in DATA[role].items()}
                      for role in ('client','server')]
    memcheck = frame('fsys', 0x80, payload({'TXN':'MemCheck','memcheck.[]':'0','salt':'0','type':'0'}))
    ping = frame('fsys', 0, payload({'TXN':'Ping'}))
    return client, server, memcheck, ping


def stats_reply(magic, fields):
    """Return one dummy value per requested key, never captured personal stats."""
    import re
    count = int(fields.get('keys.[]', '0'))
    if not 0 <= count <= 4096:
        raise ValueError('Invalid stats key count')
    reply = {'TXN':'GetStats', 'stats.[]':str(count)}
    for i in range(count):
        key = fields.get('keys.'+str(i))
        if key is None or not re.fullmatch(r'[A-Za-z0-9_.:%-]{1,128}', key):
            raise ValueError('Invalid stats key identifier')
        reply['stats.'+str(i)+'.key'] = key
        reply['stats.'+str(i)+'.value'] = '0'
    return group({'magic':magic, 'fields':reply, 'fragmented':len(payload(reply))>4096})


def entitlements(role):
    return {tuple(sorted(item['query'].items())):group(item['reply'])
            for item in DATA[role+'_entitlements']}


def theater():
    return [frame(item['magic'], 0, payload(item['fields'])) for item in DATA['theater']]


def http():
    # These reproduce the nonpersonal response bodies in the working prototype.
    # They are JSON HTTP metadata, despite the historical game.xml route name.
    # Do not claim these are a general replacement for the game's configuration.
    bodies = {
      b'/easo/editorial/BF/2010/BFBC2/config/PC/game.xml':
        b'{"version":"1.1","content":{"headers":[{"key":"Content-Type","value":["text/xml; charset=utf-8"]}]},"statusCode":200,"reasonPhrase":"OK","headers":[],"trailingHeaders":[],"requestMessage":null,"isSuccessStatusCode":true}',
      b'/easo/editorial/BF/2010/BFBC2/config/PC/version':
        b'{"version":"1.1","content":{"headers":[]},"statusCode":200,"reasonPhrase":"OK","headers":[],"trailingHeaders":[],"requestMessage":null,"isSuccessStatusCode":true}'
    }
    return {route:b'HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n'
            + format(len(body),'x').encode()+b'\r\n'+body+b'\r\n0\r\n\r\n' for route,body in bodies.items()}
