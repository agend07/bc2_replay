"""Generate the internal/soldier test vectors with the Python builder modules (the reference semantics).
Usage: <python> gen_soldier_vectors.py <builder_go>/testdata/soldier [--src <dir with v3_convert_soldiers.py,
       soldier_py.py, p2_rebase.py and their imports>]   (default --src: env BC2_BUILDER_SRC, else ../builder_src: the frozen Python oracle, kept outside this repo since r292)
Synthetic and name-free: seeded random soldier update rows (payload bit densities, tables, origins incl. NaN / inf /
huge, limits past the payload and the 512-bit pad). Deterministic: same python + modules -> same bytes.
Writes vectors.json.gz (gzip mtime 0): [{payload (hex), cursor, limit, child (hex), table, origin_bits, ghost_bits, t,
id, end_hint (null = None), stats (convert() Counter keys in order), row (golden line of the converted row, null when
dropped), error (exception class name when convert() raised, else null)}]
"""
import argparse
import collections
import gzip
import importlib.util
import json
import os
import random
import struct
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
R = random.Random(20261029)


def load_ref():
    spec = importlib.util.spec_from_file_location('golden_ref', HERE / 'golden_ref.py')
    m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
    return m


def payload(n, density):
    return bytes(sum((R.random() < density) << k for k in range(8)) for _ in range(n))


def origin_word():
    r = R.random()
    if r < 0.03: return R.choice([0x7fc00000, 0xffc00001, 0x7f800000, 0xff800000, 0x7f7fffff, 0xff7fffff, 0x00000001])
    if r < 0.06: return struct.unpack('<I', struct.pack('<f', R.uniform(-3e38, 3e38)))[0]
    return struct.unpack('<I', struct.pack('<f', R.uniform(-6000, 6000)))[0]


def table():
    r = R.random()
    if r < 0.05: return []
    if r < 0.10: return [R.randrange(0, 15) for _ in range(R.randrange(64, 80))]   # past the 64-entry pad
    if r < 0.18: return [R.randrange(40, 80) for _ in range(R.randrange(1, 8))]    # > 63-bit fields
    return [R.randrange(0, 15) for _ in range(R.randrange(1, 12))]


def case():
    n = R.randrange(1, 64)
    p = payload(n, R.choice([0.03, 0.1, 0.25, 0.5, 0.75]))
    cursor = R.randrange(0, 8 * n)
    lim = R.choice([8 * n, R.randrange(cursor, 8 * n + 1), 8 * n + R.randrange(0, 600), 8 * n + 512 + R.randrange(0, 99)])
    child = struct.pack('<4I', 0, R.choice([0, 1, 2, 5, 9, 33]), 0, R.choice([0, 3, 6, 9]))
    if R.random() < 0.01: child = child[:R.randrange(0, 16)]
    eh = None if R.random() < 0.3 else R.randrange(cursor, 8 * n + 40) - 14
    return dict(payload=p.hex(), cursor=cursor, limit=lim, child=child.hex(), table=table(),
                origin_bits=[origin_word() for _ in range(3)], ghost_bits=R.choice([13, 13, 1, 6, 16]),
                t=R.randrange(1 << 40, 1 << 44), id=R.randrange(0, 1 << 13), end_hint=eh)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('out', type=Path)
    ap.add_argument('--src', type=Path, default=Path(os.environ.get('BC2_BUILDER_SRC') or HERE.parents[1] / 'builder_src'))
    ap.add_argument('-n', type=int, default=12000)
    a = ap.parse_args()
    sys.path.insert(0, str(a.src))
    os.environ.setdefault('V3_LOG', str(a.out / 'unused-m-log.bin'))  # v3_types reads it at import; never opened here
    os.environ.pop('V3_SOLDIER_ENGINE', None)
    import v3_convert_soldiers as C
    G = load_ref()
    out = []
    for _ in range(a.n):
        c = case()
        row = dict(marker=1, t=c['t'], id=c['id'], phase=0, cursor=c['cursor'], cache=0, dword_offset=0, flags=0,
                   limit=c['limit'], payload=bytes.fromhex(c['payload']), child_raw=bytes.fromhex(c['child']),
                   count0=len(c['table']), tables=[c['table'], []], origin_bits=c['origin_bits'],
                   ghost_bits=c['ghost_bits'])
        g = dict(t=c['t'], id=c['id'], end_hint=c['end_hint'])
        try:
            rows, st, _ = C.convert([(g, row)], None, None)
            c.update(stats=list(st), row=G.line(G.enc(rows[0])) if rows else None, error=None)
        except Exception as e:
            c.update(stats=[], row=None, error=type(e).__name__)
        out.append(c)
    a.out.mkdir(parents=True, exist_ok=True)
    with open(a.out / 'vectors.json.gz', 'wb') as f, gzip.GzipFile(filename='', mode='wb', fileobj=f, mtime=0) as gz:
        gz.write((json.dumps(out, separators=(',', ':')) + '\n').encode('ascii'))
    k = collections.Counter()
    for c in out:
        k.update(x[:20] if x.startswith('fail:framing') else x for x in c['stats']); k['error:' + str(c['error'])] += c['error'] is not None
        k['refs'] += bool(c['row'] and '"refs":{}' not in c['row'])
    print(len(out), dict(sorted(k.items())))


if __name__ == '__main__':
    main()
