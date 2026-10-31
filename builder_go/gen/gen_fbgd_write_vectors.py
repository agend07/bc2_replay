"""Generate the internal/fbgd write-side test vectors with the Python builder modules (the reference semantics).
Usage: <python> gen_fbgd_write_vectors.py <builder_go>/testdata/fbgd_write [--src <dir with rows_to_fbgd.py, fbgd_write.py,
       fbgd_read.py, v3_refmap.py, v3_assemble.py>]   (default --src: env BC2_BUILDER_SRC, else ../builder_src: the frozen Python oracle, kept outside this repo since r292)
Synthetic and name-free (seeded random rows, templates and chunks). Deterministic: same python + modules -> same bytes.
Writes vectors.json.gz (gzip mtime 0):
  assemble   [{name, t0, keysnap, tpl, rows, fbgd (sha256), fbgk (hex), info} | {..., error}]   rows_to_fbgd.assemble
  delta      [{counts, d, payload (hex), nbits, counts_after}]                                fbgd_write.encode_delta
  delnull    [{rows, types, soldier, out_rows, hits, stat}]                                   rows_to_fbgd.delnull
  refmap     [{fn, chunk, positions?, map?, result | error}]                                  v3_refmap
  property   [{rec, vt, map, widen_p12, camera_p5_flag, result | error}]                      v3_assemble.property_chunks
  initial    [{rec, result}]                                                                  v3_assemble.initial_property_state
Encodings: a chunk value is its bit string ("" = 0 bits); a time is str(int) or float.hex(); a chunk key is an int or
[section, property]; a type key is an int or a str; refs are [[target, flag], ...].
"""
import argparse
import gzip
import hashlib
import json
import random
import os
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
R = random.Random(20261029)
T0 = 8_411_843_320_483


def bs(v, n): return format(v, '0%db' % n) if n else ''


def tstr(t): return float.hex(t) if isinstance(t, float) else str(t)


def chunk_out(c, with_index=True):
    o = {'bits': bs(c['value'], c['bits']), 'refs': [list(r) if isinstance(r, (tuple, list)) else [r, 0] for r in c.get('refs', [])]}
    if with_index: o['index'] = c['index']
    return o


def rnd_chunk(index=0, maxw=90, refs=True):
    n = R.choice([0, 1, 3, 5, 13, 17, 31, 32, 33, 63, 64, 65, R.randrange(maxw + 1)])
    v = R.getrandbits(n) if n else 0
    rf = [(R.randrange(1, 1 << 13), R.randrange(2)) for _ in range(R.choice([0, 0, 0, 1, 2]))] if refs else []
    return dict(index=index, bits=n, value=v, refs=rf)


def rnd_template():
    secs = []
    for si in range(R.choice([1, 1, 2, 3])):
        cnt = R.choice([1, 2, 4, 6, 9])
        secs.append(dict(header=R.getrandbits(32), count=cnt, mask=R.getrandbits(cnt),
                         defaults=[rnd_chunk(p) for p in range(cnt)]))
    return {'sections': secs}


def row_out(r):
    o = {'k': r['k'], 't': tstr(r['t']), 'id': r['id']}
    if r['k'] == 'c':
        o['type'] = r['type']; o['create'] = chunk_out(r['create'])
    if r['k'] == 'u':
        o['chunks'] = [[list(p) if isinstance(p, tuple) else p, chunk_out(c, False)] for p, c in r['chunks'].items()]
    return o


def assemble_case(A, name, keysnap):
    tpl = {0x1431d14: rnd_template(), 0x143ac38: rnd_template(), 'WM1436cbc/2/v7': rnd_template()}
    types = list(tpl)
    DT = A.DT_TICKS
    # times: dense around the keyframe bins 151 / 302 / 453 (both sides of the bin edge), some floats
    def rnd_t():
        b = R.choice([0, 1, 5, 150, 151, 151, 152, 300, 301, 302, 302, 303, 452, 453, R.randrange(470)])
        t = T0 + int(b * DT) + R.choice([0, 1, 2, -1, R.randrange(int(DT))])
        return float(t) + R.choice([0.0, 0.25, 0.5]) if R.random() < 0.08 else t
    rows, typ = [], {}
    ids = list(range(1, 25))
    for _ in range(R.randrange(150, 400)):
        g = R.choice(ids); t = rnd_t(); k = R.random()
        if k < 0.18 or g not in typ:
            typ[g] = R.choice(types)
            cr = rnd_chunk(32, 120)
            rows.append(dict(k='c', t=t, id=g, type=typ[g], create=cr))
            if R.random() < 0.6:  # initial-state row at the create's t (r181/r182)
                ini = rnd_chunks(tpl[typ[g]], defaults_bias=0.5)
                rows.append(dict(k='u', t=t, id=g, chunks=ini))
                if R.random() < 0.3:  # same bin, back to the template defaults: the r182 tplnew_bad form
                    rs = {}
                    for p in ini:
                        si, pi = p if isinstance(p, tuple) else (0, p)
                        if si < len(tpl[typ[g]]['sections']) and pi < tpl[typ[g]]['sections'][si]['count']:
                            d = tpl[typ[g]]['sections'][si]['defaults'][pi]; rs[p] = dict(value=d['value'], bits=d['bits'])
                    rows.append(dict(k='u', t=t + 1, id=g, chunks=rs))
        elif k < 0.30:
            rows.append(dict(k='d', t=t, id=g))
        else:
            rows.append(dict(k='u', t=t, id=g, chunks=rnd_chunks(tpl[typ[g]], defaults_bias=0.4)))
    R.shuffle(rows) if R.random() < 0.3 else None
    rows_in = [row_out(r) for r in rows]
    case = dict(name=name, t0=str(T0), keysnap=keysnap,
                tpl=[[k, {'sections': [dict(header=s['header'], count=s['count'], mask=s['mask'],
                                            defaults=[chunk_out(c) for c in s['defaults']]) for s in v['sections']]}]
                     for k, v in tpl.items()], rows=rows_in)
    try:
        data, k, info = A.assemble(rows, tpl, t0=T0, keysnap=keysnap)
        case.update(fbgd=hashlib.sha256(data).hexdigest(), fbgk=k.hex(), info=dict(info, tplnew_bad=[list(x) for x in info['tplnew_bad']]))
    except Exception as e:
        case['error'] = type(e).__name__
    return case


def rnd_chunks(tp, defaults_bias=0.4):
    ch = {}
    for _ in range(R.randrange(0, 5)):
        si = R.randrange(len(tp['sections']) + (1 if R.random() < 0.05 else 0))
        cnt = tp['sections'][si]['count'] if si < len(tp['sections']) else 3
        pi = R.randrange(cnt + (1 if R.random() < 0.05 else 0))
        if si < len(tp['sections']) and pi < cnt and R.random() < defaults_bias:
            d = tp['sections'][si]['defaults'][pi]; c = dict(value=d['value'], bits=d['bits'])
        else:
            c = rnd_chunk(pi); c.pop('index')
            if not c['refs'] and R.random() < 0.5: c.pop('refs')
        key = pi if si == 0 and R.random() < 0.8 else (si, pi)
        ch[key] = c
    return ch


def delta_case(W):
    counts = {g: [R.randrange(0, 6) for _ in range(R.randrange(1, 4))] for g in range(1, 8)}
    dirty = []
    for g in R.sample(range(1, 10), R.randrange(0, 6)):
        if g > 7 or R.random() < 0.3:
            secs = [dict(header=R.getrandbits(32), count=c, mask=R.getrandbits(c), chunks=[rnd_chunk(p) for p in range(c)])
                    for c in [R.randrange(0, 5) for _ in range(R.randrange(1, 3))]]
            dirty.append((g, 'new', dict(id=g, create=rnd_chunk(32), sections=secs)))
        else:
            body = [(R.randrange(4), R.randrange(7), rnd_chunk()) for _ in range(R.randrange(0, 6))]
            dirty.append((g, 'upd', body))
    d = dict(dt=R.getrandbits(31), sim=R.getrandbits(31), deleted=R.sample(range(1, 12), R.randrange(0, 4)), dirty=dirty)
    before = {g: list(v) for g, v in counts.items()}
    case = dict(counts=[[g, v] for g, v in before.items()], d=dict(dt=d['dt'], sim=d['sim'], deleted=d['deleted'], dirty=[
        [g, kind, (dict(id=b['id'], create=chunk_out(b['create']), sections=[dict(header=s['header'], count=s['count'], mask=s['mask'],
         chunks=[chunk_out(c) for c in s['chunks']]) for s in b['sections']]) if kind == 'new'
                   else [[si, pi, chunk_out(c)] for si, pi, c in b])] for g, kind, b in dirty]))
    try:
        pl, nb = W.encode_delta(d, counts)
        case.update(payload=pl.hex(), nbits=nb, counts_after=[[g, v] for g, v in counts.items()])
    except Exception as e:
        case['error'] = type(e).__name__
    return case


COMP, CAM, SOLDIER = 0x1431df8, 0x1431c0c, 0x14377f8


def delnull_case(A):
    types = {g: R.choice([COMP, COMP, CAM, SOLDIER, 0x143ac38, 'SRC5']) for g in range(1, 12)}
    rows = []
    for _ in range(R.randrange(10, 60)):
        g = R.randrange(1, 12); t = T0 + R.randrange(0, 4_000_000); k = R.random()
        if k < 0.15: rows.append(dict(k='c', t=t, id=g, type=types[g], create=dict(index=32, bits=4, value=1, refs=[])))
        elif k < 0.35: rows.append(dict(k='d', t=t, id=g))
        else:
            ch = {}
            for p in R.sample([3, 5, 12, 7], R.randrange(1, 3)):
                refs = [(R.randrange(0, 12), R.randrange(2)) for _ in range(R.randrange(0, 3))]
                c = dict(value=R.getrandbits(13), bits=13, refs=refs)
                if R.random() < 0.2: c.pop('refs')
                ch[p if R.random() < 0.8 else (0, p)] = c
            rows.append(dict(k='u', t=t, id=g, chunks=ch))
    case = dict(rows=[row_out(r) for r in rows], types=[[g, v] for g, v in types.items()], soldier=SOLDIER)
    stat = {}
    n0 = len(rows)
    hits = A.delnull(rows, types, SOLDIER, stat)
    case.update(out_rows=[row_out(r) for r in rows[n0:]], hits=[[tstr(t), h, p, x] for t, h, p, x in hits], stat=list(stat.items()))
    return case


def refmap_cases(RM):
    out = []
    for _ in range(120):
        n = R.choice([13, 20, 26, 40, 64, 70, 184, R.randrange(0, 90)])
        v = R.getrandbits(n) if n else 0
        b = bs(v, n)
        refs = []
        for _ in range(R.choice([0, 1, 2, 3])):  # ids found in the body (or not)
            if n >= 13 and R.random() < 0.8:
                i = R.randrange(n - 12); refs.append((int(b[i:i + 13], 2), R.randrange(2)))
            else:
                refs.append((R.randrange(1, 1 << 13), R.randrange(2)))
        c = dict(index=R.randrange(64), bits=n, value=v, refs=refs)
        mp = {r[0]: R.choice([0, R.randrange(1, 1 << 13)]) for r in refs}
        fn = R.choice(['positions_from_table', 'remap_server_chunk', 'remap'])
        case = dict(fn=fn, chunk=chunk_out(c), map=[[k, x] for k, x in mp.items()])
        try:
            if fn == 'positions_from_table': res = RM.positions_from_table(c)
            elif fn == 'remap_server_chunk': res = chunk_out(RM.remap_server_chunk(c, lambda o: mp.get(o, 0)))
            else:
                pos = [R.randrange(max(1, n - 5)) for _ in range(R.randrange(0, 3))]
                case['positions'] = pos; res = chunk_out(RM.remap(c, pos, lambda o: mp.get(o, 0) % (1 << 13)))
            case['result'] = res
        except ValueError as e:
            case['error'] = str(e)
        out.append(case)
    for pre, n in [('00011100', 20), ('10101000', 20), ('00100010', 184), ('00100110', 184), ('00100110', 183), ('0010', 4), ('', 0)]:
        v = int(pre + bs(R.getrandbits(n - len(pre)), n - len(pre)), 2) if n else 0
        c = dict(index=32, bits=n, value=v, refs=[])
        out.append(dict(fn='create_positions', chunk=chunk_out(c), result=RM.create_positions(c)))
    return out


def rnd_rec(vt):
    chunks, pos = [], 100
    for prop in R.sample(range(16), R.randrange(0, 6)) + ([12] if vt == COMP and R.random() < 0.5 else []):
        n = R.choice([1, 13, 17, 20, 40]) if R.random() < 0.7 else R.randrange(0, 30)
        v = R.getrandbits(n) if n else 0
        refs = [dict(start=pos + 1 + R.choice([0, 0, 4]) , end=0, target=0) for _ in range(R.choice([0, 1, 1, 2]))]
        if prop == 12 and vt == COMP and R.random() < 0.8: refs = [dict(start=pos + 1, end=0, target=0)]
        chunks.append(dict(property=prop, bits=n, value=v, body_start=pos + 1, references=refs))
        pos += 1 + n + R.randrange(3)
    ovs = [dict(start=R.randrange(90, pos + 10), end=0, xyz=[0.0, 0.0, 0.0]) for _ in range(R.choice([0, 0, 1, 2]))]
    for o in ovs: o['end'] = o['start'] + R.randrange(1, 60)
    return dict(error=R.choice([None] * 8 + ['x']), flags=R.choice([0] * 8 + [4]), end=pos, chunks=chunks, origin_vectors=ovs)


def rec_out(rec):
    return dict(error=rec['error'], flags=rec['flags'], end=rec['end'], origin_vectors=[[o['start'], o['end']] for o in rec['origin_vectors']],
                chunks=[dict(property=c['property'], bits=bs(c['value'], c['bits']), body_start=c['body_start'],
                             references=[r['start'] for r in c['references']]) for c in rec['chunks']])


def property_cases(S):
    out = []
    for _ in range(150):
        vt = R.choice([COMP, COMP, CAM, 0x1431d14])
        rec = rnd_rec(vt)
        seed = R.randrange(1 << 30)
        fmap = lambda o, s=seed: 0 if (o ^ s) % 3 == 0 else ((o * 7 + s) % 8191) + 1
        widen, flag = R.random() < 0.3, R.random() < 0.7
        case = dict(rec=rec_out(rec), vt=vt, seed=seed, widen_p12=widen, camera_p5_flag=flag)
        try:
            res = S.property_chunks(rec, vt, fmap, widen_p12=widen, camera_p5_flag=flag)
            case['result'] = [[p, chunk_out(c, False)] for p, c in res.items()]
        except ValueError as e:
            case['error'] = str(e)
        out.append(case)
    return out


def initial_cases(S):
    out = []
    for _ in range(80):
        rec = rnd_rec(R.choice([COMP, CAM]))
        res = S.initial_property_state(rec)
        out.append(dict(rec=rec_out(rec), result=None if res is None else [c['property'] for c in res['chunks']]))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('out', type=Path)
    ap.add_argument('--src', type=Path, default=Path(os.environ.get('BC2_BUILDER_SRC') or HERE.parents[1] / 'builder_src'))
    a = ap.parse_args()
    sys.path.insert(0, str(a.src))
    import rows_to_fbgd as A, fbgd_write as W, v3_refmap as RM
    # v3_assemble imports the decode/wire stack at module level; property_chunks / initial_property_state need only
    # v3_refmap, so they are loaded from the module source alone.
    src = (a.src / 'v3_assemble.py').read_text(encoding='utf-8')
    ns = {}
    for name in ('initial_property_state', 'property_chunks'):
        i = src.index('def ' + name); j = src.index('\ndef ', i + 1)
        exec(compile(src[i:j], str(a.src / 'v3_assemble.py'), 'exec'), ns)
    S = type('S', (), {'initial_property_state': staticmethod(ns['initial_property_state']),
                       'property_chunks': staticmethod(ns['property_chunks'])})
    vec = dict(assemble=[assemble_case(A, 'a%02d' % i, i % 3 == 2) for i in range(36)],
               delta=[delta_case(W) for _ in range(60)],
               delnull=[delnull_case(A) for _ in range(40)],
               refmap=refmap_cases(RM), property=property_cases(S), initial=initial_cases(S))
    a.out.mkdir(parents=True, exist_ok=True)
    with open(a.out / 'vectors.json.gz', 'wb') as f, gzip.GzipFile(filename='', mode='wb', fileobj=f, mtime=0) as gz:
        gz.write((json.dumps(vec, separators=(',', ':')) + '\n').encode('ascii'))
    print({k: len(v) for k, v in vec.items()}, 'assemble errors', sum('error' in c for c in vec['assemble']),
          'tplnew_bad', sum(len(c.get('info', {}).get('tplnew_bad', [])) for c in vec['assemble']),
          'delta errors', sum('error' in c for c in vec['delta']), 'delnull hits', sum(len(c['hits']) for c in vec['delnull']),
          'refmap errors', sum('error' in c for c in vec['refmap']), 'property errors', sum('error' in c for c in vec['property']))


if __name__ == '__main__':
    main()
