"""Generate the internal/sections codec test vectors with the Python builder modules (the reference semantics).
Usage: <python> gen_sections_vectors.py <builder_go>/testdata/sections/vectors.jsonl.gz [--src <dir with v3_vehsec.py,
       v3_phys_p2.py, v3_create_rebase.py, v3_audit888.py deps>]   (default --src: env BC2_BUILDER_SRC, else ../builder_src: the frozen Python oracle, kept outside this repo since r292)
Synthetic and name-free: seeded random bit strings, origins and arguments (many end in an exception on purpose, the
dumps hold none). Deterministic: same python + modules -> same bytes. Golden form (internal/golden/SPEC.md), one record
per call: {fn, args, result} or {fn, args, exc: 'Type: message'}; fn in rebase_create, to_demo_form, parse, section,
audit_rebase, chunks_of.
"""
import argparse
import math
import random
import struct
import os
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import golden_ref as G  # noqa: E402

R = random.Random(20261029)
TABLE = [1, 3, 5, 7, 9, 11, 13]


def rbits(n, p=0.5):
    return ''.join('1' if R.random() < p else '0' for _ in range(n))


def f32(x):
    return struct.unpack('<f', struct.pack('<f', x))[0]


def origin():
    k = R.random()
    if k < 0.15: return (0.0, 0.0, 0.0)
    if k < 0.55: return tuple(float(R.randint(-600, 600)) for _ in range(3))
    if k < 0.75: return tuple(R.randint(-20000, 20000) / 32 for _ in range(3))
    if k < 0.85: return tuple(f32(R.uniform(-900, 900)) for _ in range(3))
    if k < 0.93: return (1e30, -5.0, 3.0)
    return R.choice([(math.inf, 0.0, 0.0), (0.0, math.nan, 0.0), (-math.inf, 1.0, 2.0)])


def call(w, fn, args, f):
    try:
        res = f()
    except Exception as e:  # noqa: BLE001 - every exception is part of the contract
        w.write({'fn': fn, 'args': args, 'exc': '%s: %s' % (type(e).__name__, e)})
        return
    w.write({'fn': fn, 'args': args, 'result': res})


def create_bits():
    t = R.choice([3, 6, 31, 31, 6, 3, 12])
    head = format(t, '06b') + ('1' if R.random() < 0.9 else '0') + rbits(12)
    if t == 3:
        return head + rbits(R.randint(0, 200))
    if t == 31:
        q = '1'
        for _ in range(3):
            q += '1' + rbits(8) if R.random() < 0.5 else '0'
        body = rbits(1) + q[1:] + format(R.randint(1, 7), '03b') + rbits(R.choice([3 * 11, 3 * 13, 3 * 15, 3 * 17, 3 * 23]))
        return head + (body if R.random() < 0.8 else body[:R.randint(0, len(body))] + rbits(R.randint(0, 4)))
    if t == 6:
        o = ''
        p1 = R.random() < 0.6
        o += '1' if p1 else '0'
        if p1: o += format(R.randint(1, 7), '03b') + rbits(3 * R.choice([6, 8, 10, 12, 14])) + rbits(4)
        o += rbits(48)
        p2 = R.random() < 0.5
        o += '1' if p2 else '0'
        if p2: o += format(R.randint(0, 7), '03b') + rbits(3 * R.choice([6, 8, 10, 12, 14]))
        o += rbits(1)
        return head + (o if R.random() < 0.8 else o[:R.randint(0, len(o))])
    return head + rbits(R.randint(0, 60))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('out', type=Path)
    ap.add_argument('--src', type=Path, default=Path(os.environ.get('BC2_BUILDER_SRC') or HERE.parents[1] / 'builder_src'))
    a = ap.parse_args()
    sys.path.insert(0, str(a.src))
    import v3_create_rebase as CR
    import v3_phys_p2 as X
    import v3_vehsec as VS
    from v3_audit888 import chunks_of, rebase
    a.out.parent.mkdir(parents=True, exist_ok=True)
    with G.Writer(str(a.out), 'sections_vectors', {'seed': 20261029}) as w:
        for _ in range(600):
            b, o, tr = create_bits(), origin(), R.random() < 0.85
            call(w, 'rebase_create', {'bits': b, 'origin': list(o), 'trunc': tr}, lambda: list(CR.rebase_create(b, o, tr)))
        for _ in range(500):
            n = R.choice([0, 5, 40, 120, 260, 400])
            b, pos = rbits(n, R.choice([0.3, 0.5, 0.7])), R.randint(0, 12)
            o = origin()
            sh = R.choice([{}, {'sparse': False, 'inner': 0}, {'inner': 4}])
            if R.random() < 0.5 and n > pos:  # bias toward the wire flag-1 form
                b = b[:pos] + '1' + b[pos + 1:]
            fn = R.choice(['to_demo_form', 'parse'])
            args = {'bits': b, 'pos': pos, 'origin': list(o), 'sparse': sh.get('sparse', True), 'inner': sh.get('inner', 3)}
            if fn == 'parse':
                call(w, fn, args, lambda: X.parse(b, pos, o, TABLE, **sh))
            else:
                call(w, fn, args, lambda: list(X.to_demo_form(b, pos, o, TABLE, **sh)))
        vts = sorted(VS.PY) + [VS.GHOST, 0x1234]
        for _ in range(1500):
            n = R.choice([10, 60, 200, 400])
            b, cur = rbits(n, R.choice([0.3, 0.5, 0.7])), R.randint(0, 8)
            vt = R.choice(vts)
            count = R.randint(0, 5)
            o, cfg, rb = origin(), R.randint(0, 1), R.random() < 0.8
            tb = R.choice([None, [], [10], [3, 10], [2, 4]])
            tpl = None if tb is None else {'defaults': [{'bits': x} for x in tb]}
            lay = R.choice([None, 14, 29, 68, 76, 7, 37])
            args = {'bits': b, 'cur': cur, 'vt': vt, 'count': count, 'origin': list(o), 'cfg': cfg, 'rebase': rb, 'tpl': tb,
                    'layout': lay}
            call(w, 'section', args, lambda: list(VS.section(b, cur, vt, count, o, TABLE, cfg, rebase=rb, tpl=tpl, layout=lay)))
        for _ in range(500):
            body = rbits(R.randint(0, 120))
            ovs = []
            a0 = 0
            for _ in range(R.choice([0, 1, 1, 2])):
                a0 += R.randint(0, 6)
                C = R.choice([3, 5, 7, 10])
                idx = R.randint(0, 7)
                wd = (TABLE[idx - 1] + C) if idx else 32
                b0 = a0 + 3 + 3 * wd
                xyz = R.choice([tuple(R.randint(-3000, 3000) / (1 << C) for _ in range(3)), tuple(f32(R.uniform(-50, 50)) for _ in range(3)),
                                (1e20, 0.0, 0.0), (math.nan, 0.0, 0.0)])
                ovs.append((a0, b0, xyz))
                a0 = b0
            call(w, 'audit_rebase', {'body': body, 'ovs': ovs, 'table': TABLE}, lambda: rebase(body, ovs, TABLE))
        for _ in range(300):
            bits = rbits(R.randint(0, 300))
            k = R.randint(0, 6)
            pos = sorted(R.randint(0, len(bits) + 3) for _ in range(k))
            end = (pos[-1] if pos else 0) + R.randint(0, 40)
            refs = [{'start': s, 'end': s + 13, 'target': R.randint(0, 9)} for s in sorted(R.randint(0, end) for _ in range(R.randint(0, 3)))]
            ovs = [{'start': s, 'end': s + R.randint(10, 60), 'xyz': [1.5, -2.0, 3.25]} for s in sorted(R.randint(0, end) for _ in range(R.randint(0, 3)))]
            call(w, 'chunks_of', {'bits': bits, 'pos': pos, 'end': end, 'refs': refs, 'ovs': ovs},
                 lambda: chunks_of(bits, pos, end, refs, ovs))


if __name__ == '__main__':
    main()
