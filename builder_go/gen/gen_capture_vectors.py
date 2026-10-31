"""Generate the internal/capture test vectors with the Python builder modules (the reference semantics).
Usage: <python> gen_capture_vectors.py <builder_go>/testdata/capture [--src <dir with recorder_v3.py, live_recorder_v3.py,
       v3_types.py, bc2raw.py>]   (default --src: env BC2_BUILDER_SRC, else ../builder_src: the frozen Python oracle, kept outside this repo since r292)
Synthetic and name-free: hand-built records (seeded random payloads). Deterministic: same python + modules -> same bytes.
Writes:
  m-log.bin + {parse,decode,to_v1_rows}.jsonl.gz  golden stages (SPEC v1) of parse_log / v3_types.load / to_v1_rows
  errors.json        [{name, hex (m-log bytes), error (str of the exception)}] for parse_log + decode failures
  synth.bc2raw + extract/{m-log.bin,raw-log.bin,m-summary.json,capture.json}  bc2raw.extract output, LF line ends,
                     m-summary 'log' -> "@LOG@", capture.json 'recorded' -> "@REC@" (local time)
  bc2raw.json        {header (recorded -> "@REC@"), level_of, header_errors [{hex, error}], extract_level (no S name)}
"""
import argparse
import gzip
import importlib.util
import json
import os
import random
import struct
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
R = random.Random(20261029)


def load_ref():
    spec = importlib.util.spec_from_file_location('golden_ref', HERE / 'golden_ref.py')
    m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
    return m


def rec(typ, body):
    body += b'\0' * (-len(body) % 4)
    return struct.pack('<I', typ | (len(body) + 4) << 8) + body


def t3(t, torn=False):
    hi, lo = t >> 32, t & 0xFFFFFFFF
    return (hi, lo, hi + 1 if torn else hi)


def P(seq, t, payload, limit=None, span=None, torn=False):
    span = len(payload) if span is None else span
    return rec(1, struct.pack('<8I', *t3(t, torn), seq, 0x1000 + seq, limit or 8 * len(payload),
                              struct.unpack('<I', (payload + b'\0' * 4)[:4])[0], span) + payload)


def Q(qseq, t, payload):
    return rec(9, struct.pack('<8I', *t3(t), qseq, 0x2000, 8 * len(payload), 7, len(payload)) + payload)


def C(t0, t1):
    return rec(3, struct.pack('<2I', len(t0), len(t1)) + bytes(range(32)) + struct.pack('<%dI' % len(t0), *t0)
               + struct.pack('<%dI' % len(t1), *t1))


SOLG, SOLS = 0x14378F0, 0x14377F8


def G(pseq, gid, phase=0, gvt=0x1111, svt=0x2222, ec=100, cur=120, t=0x0000_0001_0000_0010, torn=False, ccur=0):
    return rec(2, struct.pack('<IIIIHHIIIIII3II', *t3(t, torn), pseq, gid, phase, gvt, svt, ec, cur, 0x80000000 | cur,
                              (cur // 32) * 4 + 4, 0x3F800000, 0, 0xBF800000, ccur))


def M(*secs): return rec(4, struct.pack('<%dI' % (len(secs) + 1), len(secs), *secs))


def B(pre, gvt, secs, props, post=999):
    flat = [x for e in secs + props for x in e]
    return rec(5, struct.pack('<8I', pre, post, gvt, 1, len(secs) + 3, len(props) + 5, len(secs), len(props))
               + struct.pack('<%dI' % len(flat), *flat))


def S(name, flags=1, n=None):
    nb = name if isinstance(name, bytes) else name.encode('ascii')
    return rec(6, struct.pack('<9IQQ', 1, flags, 0xC0, 0xD0, 13, 4, 5, 0x7000, len(nb) if n is None else n,
                              0x01DC_0000_0000_0000, 123456789) + nb)


def T(qseq, pseq, drops=0, cont=0, skip=0):
    return rec(7, struct.pack('<QQ6I', 1000 + qseq, 0x01DC_0000_0000_0001, qseq, pseq, 4096, drops, cont, skip))


def E(reason): return rec(8, struct.pack('<IQII', reason, 5555, 9, 10))


def pay(n): return bytes(R.getrandbits(8) for _ in range(n))


def mlog_stream():
    """every decode branch: orphans, torn time, seq overwrite, transparent S/T/E/Q/O between G and M/B, bookmarks on
    single and multi ghosts, M with n=0, unknown type and PAD records, short P payload."""
    out = [G(1, 9),                                    # G before any P/C: orphan
           P(1, 0x10, pay(40)), G(1, 8),               # G before any C: orphan
           C([1, 3, 5, 7], [2, 4]),
           G(1, 5, phase=1, ec=10, cur=50, ccur=20),   # create
           G(1, 6, ec=64, cur=80),                     # same packet -> end_hint of the previous
           B(80, 0x1111, [(0x10, 0x20), (0x30, 0x40)], [(1, 2)]),     # attaches to id 6
           B(80, 0x1111, [], []),                      # last reset by the B before: borphan
           P(2, 0x0000_0002_0000_0001, pay(24), torn=True),
           G(2, SOLG & 0xFFFF, gvt=SOLG, svt=SOLS, cur=33, t=0x0000_0002_0000_0002, torn=True),  # soldier update, torn t
           G(2, 7, gvt=SOLG, svt=SOLS, phase=1, cur=40),                # soldier create: not a v1 row
           G(2, 3, svt=0, ec=90, cur=91),              # multi
           S('Levels/MP_TEST'), T(1, 2), Q(1, 0x30, pay(8)), rec(10, b'\x01\x02\x03\x04'),     # transparent
           M(0x100, 0x200, 0x300),
           B(91, 0x1111, [(5, 6)], [(7, 8), (9, 10)]),  # bookmarks on the multi, after its M
           G(2, 4, svt=0, cur=95), M(),                # multi with n = 0 (kept, sections [])
           G(2, 2, svt=0, cur=99), P(3, 0x40, pay(8)), M(1),    # M not right after its G: orphan, G dropped from multi
           M(5, 6),                                    # M without a multi G: orphan
           G(3, 11, gvt=SOLG, svt=SOLS, cur=10), G(3, 12, gvt=SOLG, svt=SOLS, cur=20, ec=30),
           P(2, 0x50, pay(12)),                         # seq 2 again: value replaced, key keeps its position
           G(2, 13, gvt=SOLG, svt=SOLS, cur=12),
           G(9, 14),                                    # missing packet: orphan
           C([9], []), G(3, 15, cur=7),                 # config 1
           P(4, 0x60, pay(16), span=40),               # span past the body: payload clamps
           G(4, 16, gvt=SOLG, svt=SOLS, cur=17, ec=5),
           rec(0x42, b'????'), rec(0xFF, b''), Q(1, 0x70, pay(4)), Q(2, 0x80, b''), E(1),
           S(b'Lev\xe9ls', flags=3), S('frag', flags=2), T(3, 4, drops=1, cont=2, skip=3)]
    return b''.join(out)


ERRORS = [  # (name, m-log bytes)
    ('corrupt_head', struct.pack('<I', 1 | 3 << 8) + b'\0' * 8),
    ('corrupt_past_end', rec(1, b'\0' * 32)[:-4]),
    ('short_p', rec(1, b'\0' * 28)), ('short_g', rec(2, b'\0' * 56)), ('short_c', rec(3, b'\0' * 4)),
    ('short_c_tables', rec(3, struct.pack('<2I', 3, 0) + b'\0' * 40)),
    ('short_c_t1', rec(3, struct.pack('<2I', 0, 2) + b'\0' * 32 + b'\0' * 4)),
    ('short_c_child', rec(3, struct.pack('<2I', 0, 0) + b'\0' * 28)),
    ('short_m', P(1, 1, b'') + C([], []) + G(1, 1, svt=0) + rec(4, struct.pack('<I', 3) + b'\0' * 8)),
    ('short_b', rec(5, b'\0' * 28)), ('short_b_list', rec(5, struct.pack('<8I', 0, 0, 0, 0, 0, 0, 2, 1) + b'\0' * 16)),
    ('short_s', rec(6, b'\0' * 48)), ('short_t', rec(7, b'\0' * 36)), ('short_e', rec(8, b'\0' * 16)),
    ('short_q', rec(9, b'\0' * 28)),
    ('trailing_bytes_ok', P(1, 1, b'ab') + b'\x01\x02'),
]


def header(ft=0x01DD_4A5B_1234_5670, fmt=1, magic=b'BC2RAW\0\0'):
    h = struct.pack('<8sHH32sI16sQQI', magic, fmt, 0x0003, bytes(range(32)), 2,
                    bytes(range(0xA0, 0xB0)), ft, 0x0000_0012_3456_7890, 1 << 22)
    return h + b'\0' * (128 - len(h))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('out', type=Path)
    ap.add_argument('--src', type=Path, default=Path(os.environ.get('BC2_BUILDER_SRC') or HERE.parents[1] / 'builder_src'))
    a = ap.parse_args()
    out = a.out; out.mkdir(parents=True, exist_ok=True)
    sys.dont_write_bytecode = True
    sys.path.insert(0, str(a.src))
    gref = load_ref()
    raw = mlog_stream()
    (out / 'm-log.bin').write_bytes(raw)
    os.environ['V3_LOG'] = str(out / 'm-log.bin')
    import live_recorder_v3 as L, recorder_v3 as RV, v3_types as V, bc2raw as BR

    def stage(name, recs):
        with gref.Writer(str(out / f'{name}.jsonl.gz'), name, {'synthetic': 1}) as w:
            for r in recs: w.write(r)
        print(name, len(recs))
    recs = L.parse_log(raw)
    stage('parse', [{'call': 0, 'i': i, 'typ': t, 'body': b} for i, (t, b) in enumerate(recs)])
    d = V.load()
    lines = []
    for part, v in d.items():
        if isinstance(v, dict): lines += [{'call': 0, 'part': part, 'key': k, 'v': x} for k, x in v.items()]
        elif isinstance(v, list): lines += [{'call': 0, 'part': part, 'i': i, 'v': x} for i, x in enumerate(v)]
        else: lines.append({'call': 0, 'part': part, 'v': v})
    stage('decode', lines)
    rows, gs = RV.to_v1_rows(d), [g for g in d['ghosts'] if (g['ghost_vt'], g['section_vt'], g['phase']) == (SOLG, SOLS, 0)]
    stage('to_v1_rows', [dict({k: v for k, v in r.items() if k not in ('payload', 'child_raw', 'tables')},
                              pseq=g['pseq'], config=g['config']) for g, r in zip(gs, rows)])
    errs = []
    for name, b in ERRORS:
        try: RV.decode(L.parse_log(b)); e = None
        except (ValueError, struct.error) as x: e = str(x)
        errs.append(dict(name=name, hex=b.hex(), error=e))
    (out / 'errors.json').write_text(json.dumps(errs, indent=1) + '\n', encoding='ascii', newline='\n')
    print('errors', len(errs))

    # .bc2raw: legacy + new records, a PAD, then a truncated tail (2 bytes)
    frames = mlog_stream() + rec(0xFF, b'\0' * 12) + S('Levels/MP_SECOND') + b'\x07\x00'
    rawf = out / 'synth.bc2raw'
    rawf.write_bytes(header() + frames)
    with tempfile.TemporaryDirectory() as td:
        x = Path(td) / 'x'
        BR.extract(rawf, x, label='labél')
        ex = out / 'extract'; ex.mkdir(exist_ok=True)
        for f in ('m-log.bin', 'raw-log.bin'): (ex / f).write_bytes((x / f).read_bytes())
        s = json.loads((x / 'm-summary.json').read_text(encoding='utf-8'))
        assert s['log'] == str(x / 'm-log.bin'); s['log'] = '@LOG@'
        (ex / 'm-summary.json').write_text(json.dumps(s, indent=2), encoding='utf-8', newline='\n')
        m = json.loads((x / 'capture.json').read_text(encoding='utf-8')); m['recorded'] = '@REC@'
        (ex / 'capture.json').write_text(json.dumps(m, ensure_ascii=False, indent=1), encoding='utf-8', newline='\n')
        y = Path(td) / 'y'  # no S with a valid name -> --level
        noname = out / 'noname.bc2raw'
        noname.write_bytes(header() + P(1, 1, b'abcd') + S('x', flags=0))
        lvl = BR.extract(noname, y, level='Levels/MP_GIVEN')['level']
    h = BR.parse_header(header()); h['recorded'] = '@REC@'
    herrs = []
    for b in (header()[:100], header(magic=b"BC2'RAW\x00"), header(magic=b'BC2RAW\x00\x01'), header(fmt=2)):
        try: BR.parse_header(b); e = None
        except ValueError as x: e = str(x)
        herrs.append(dict(hex=b.hex(), error=e))
    v = dict(header=h, level_of=BR.level_of(rawf), level_of_noname=BR.level_of(noname), header_errors=herrs, extract_level=lvl)
    (out / 'bc2raw.json').write_text(json.dumps(v, indent=1) + '\n', encoding='ascii', newline='\n')
    print('bc2raw ok')


if __name__ == '__main__':
    main()
