"""Generate the pyc/bits test vectors with the frozen CPython (the reference semantics).
Usage: <oracle>/python.exe gen_pyc_vectors.py <builder_go>/testdata
Synthetic and name-free: seeded random values only. Floats are f64 bit patterns (16 hex digits), ints decimal strings,
so the files never depend on the float repr or int parsing they test. Deterministic: same python -> same bytes.
"""
import json
import math
import random
import struct
import sys
import unicodedata
from pathlib import Path

R = random.Random(20261029)
DT_TICKS = 10_000_000 / 30
I64 = (-(1 << 63), (1 << 63) - 1)


def fh(x): return struct.pack('>d', x).hex()
def hf(h): return struct.unpack('>d', bytes.fromhex(h))[0]
def fbits(u): return struct.unpack('>d', u.to_bytes(8, 'big'))[0]


def write(path, obj):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(obj, indent=0, ensure_ascii=True) + '\n', encoding='ascii', newline='\n')
    n = len(obj['cases']) if 'cases' in obj else sum(len(v) for v in obj.values() if isinstance(v, list))
    print(path.name, n)


def err(fn):
    try:
        return fn()
    except (ZeroDivisionError, OverflowError, ValueError, struct.error) as e:
        return type(e).__name__


# ---- special and random doubles
SPECIAL = [0.0, -0.0, math.inf, -math.inf, math.nan, 1.0, -1.0, 0.5, -0.5, 1.5, 2.5, -2.5, 3.0, -3.0, 0.1, -0.1,
           5e-324, -5e-324, 2.2250738585072014e-308, 2.225073858507201e-308, 1e-310, -1e-310, 1.7976931348623157e308,
           -1.7976931348623157e308, 1e16, 1e15, 9999999999999998.0, 1e-4, 1e-5, 0.0001, 0.00001, 1e22, 1e23, 123456789.0,
           DT_TICKS, -DT_TICKS, 2.0 ** 53, 2.0 ** 53 + 2, 2.0 ** 63, -2.0 ** 63, 2.0 ** 64, 4503599627370495.5, 0.49999999999999994,
           fbits(0x7ff0000000000001), fbits(0xfff8000000000001), fbits(0x7ff4000000000000)]


def rand_double():
    k = R.randrange(6)
    if k == 0: return fbits(R.getrandbits(64))
    if k == 1: return R.uniform(-1e6, 1e6)
    if k == 2: return R.randint(-10 ** 6, 10 ** 6) / R.choice([2, 4, 8, 10, 100, 1000, 3])
    if k == 3: return R.uniform(-1, 1) * 10.0 ** R.randint(-320, 308)
    if k == 4: return float(R.randint(-(1 << 60), 1 << 60))
    return R.choice([-1, 1]) * (R.randint(0, 1 << 20) + R.choice([0.5, 0.25, 0.05, 0.005, 0.125, 0.375]))


def doubles(n): return SPECIAL + [rand_double() for _ in range(n)]


# ---- int floor div / mod (int64; MinInt64 // -1 overflows int64 and is excluded)
def gen_intdiv():
    edge = [0, 1, -1, 2, -2, 3, -3, 7, -7, 10, -10, 1 << 31, -(1 << 31), 1 << 62, -(1 << 62), I64[0], I64[1], I64[0] + 1]
    pairs = [(a, b) for a in edge for b in edge]
    for _ in range(4000):
        a = R.choice([R.randint(-1000, 1000), R.randint(*I64), R.randint(-(1 << 31), 1 << 31)])
        b = R.choice([R.randint(-20, 20), R.randint(*I64), R.randint(-(1 << 16), 1 << 16)])
        pairs.append((a, b))
    cases = []
    for a, b in pairs:
        if a == I64[0] and b == -1: continue
        if b == 0: cases.append([str(a), str(b), 'ZeroDivisionError', 'ZeroDivisionError']); continue
        cases.append([str(a), str(b), str(a // b), str(a % b)])
    return dict(doc='[a, b, a // b, a % b] int64 (Python ints; MinInt64 // -1 excluded)', cases=cases)


# ---- float floordiv / mod (float_divmod)
def gen_floatdiv():
    xs, cases = doubles(1500), []
    ys = SPECIAL + [rand_double() for _ in range(60)]
    for _ in range(6000):
        x, y = R.choice(xs), R.choice(ys)
        cases.append([fh(x), fh(y), err(lambda: fh(x // y)), err(lambda: fh(x % y))])
    for x in SPECIAL:
        for y in SPECIAL:
            cases.append([fh(x), fh(y), err(lambda: fh(x // y)), err(lambda: fh(x % y))])
    return dict(doc='[x, y, x // y, x % y] f64 bits or the exception name', cases=cases)


def gen_dtbin():
    """rows_to_fbgd: b = int((t - t0) // DT_TICKS); nt = t0 + b * DT_TICKS - 1 (t, t0 filetime-scale ints)."""
    cases = []
    for _ in range(5000):
        t0 = R.randint(13 * 10 ** 16, 14 * 10 ** 16)
        k = R.randrange(4)
        if k == 0: d = R.randint(0, 36 * 10 ** 9)
        elif k == 1: d = round(R.randint(0, 100000) * DT_TICKS) + R.randint(-2, 2)
        elif k == 2: d = R.randint(-10 ** 8, 10 ** 8)
        else: d = int(R.randint(0, 3000) * 10_000_000 / 30)
        t = t0 + d
        b = int((t - t0) // DT_TICKS)
        nt = t0 + b * DT_TICKS - 1
        cases.append([str(t), str(t0), str(b), fh(nt)])
    return dict(doc='[t, t0, int((t - t0) // DT_TICKS), t0 + b * DT_TICKS - 1] DT_TICKS = 10_000_000 / 30', cases=cases)


# ---- round
def gen_round():
    xs = doubles(3000) + [k + 0.5 for k in range(-20, 20)] + [k * 0.05 for k in range(-200, 200)] + \
        [k * 0.005 for k in range(-200, 200)] + [2.675, 1.005, 0.125, 0.375, 1e300 * 1.5, 0.285, 5e-5, 0.15, 0.25, 0.35]
    r0 = [[fh(x), err(lambda: str(round(x)))] for x in xs]
    nd_all = [-400, -309, -308, -307, -20, -5, -3, -2, -1, 0, 1, 2, 3, 5, 10, 15, 16, 17, 20, 300, 322, 323, 324, 400]
    rn = []
    for x in SPECIAL + xs[:300]:
        for n in nd_all:
            rn.append([fh(x), n, err(lambda: fh(round(x, n)))])
    for x in xs:
        for _ in range(2):
            n = R.randint(-3, 3)
            rn.append([fh(x), n, err(lambda: fh(round(x, n)))])
    ri = []
    for _ in range(2000):
        v = R.choice([R.randint(-10 ** 6, 10 ** 6), R.randint(*I64), R.randint(-99, 99) * 10 + 5, R.randint(-999, 999) * 100 + 50])
        n = R.choice([0, 1, 5, -1, -2, -3, -R.randint(1, 25)])
        ri.append([str(v), n, str(round(v, n))])
    return dict(doc='round0: [x, str(round(x)) | exc]; ndigits: [x, n, round(x, n) bits | exc]; int: [v, n, round(v, n)]',
                round0=r0, ndigits=rn, int=ri)


# ---- struct '<f'
def gen_f32():
    fmax = struct.unpack('<f', b'\xff\xff\x7f\x7f')[0]
    near = [fmax, math.nextafter(fmax, math.inf), 3.4028235677973366e38, math.nextafter(3.4028235677973366e38, 0),
            math.nextafter(3.4028235677973366e38, math.inf), 1e39, -1e39, 1.4e-45, 7e-46, 7.006492321624085e-46,
            7.006492321624087e-46, 1e-46, 1.1754943508222875e-38, 1.1754942106924411e-38]
    xs = doubles(3000) + near + [-v for v in near] + [R.uniform(-1, 1) * 10.0 ** R.randint(-46, 39) for _ in range(3000)]
    xs += [fbits(0x7ff0000000000000 | R.getrandbits(52) | 1) for _ in range(100)]
    xs += [fbits(0xfff0000000000000 | R.getrandbits(52) | 1) for _ in range(100)]
    pack = [[fh(x), err(lambda: struct.pack('>f', x).hex())] for x in xs]
    us = [R.getrandbits(32) for _ in range(4000)] + [0, 0x80000000, 0x7f800000, 0xff800000, 0x7fc00000, 0x7f800001,
                                                    0x7fbfffff, 0xffc00001, 0x00000001, 0x007fffff, 0x7f7fffff]
    us += [(R.getrandbits(1) << 31) | 0x7f800000 | (R.getrandbits(23) | 1) for _ in range(200)]
    unpack = [['%08x' % u, fh(struct.unpack('>f', u.to_bytes(4, 'big'))[0])] for u in us]
    return dict(doc="pack: [x, struct.pack('>f', x) hex | exc]; unpack: [u32, f64 bits]", pack=pack, unpack=unpack)


# ---- repr(float)
def gen_repr():
    xs = doubles(8000)
    for e in range(-330, 310):
        for m in (1.0, 1.5, 9.999999999999999, 1.2345678901234567):
            try: xs.append(float('%re%d' % (m, e)))
            except OverflowError: pass
    return dict(doc='[x bits, repr(x)]', cases=[[fh(x), repr(x)] for x in xs])


# ---- json
ASCII_POOL = [chr(c) for c in range(0x20, 0x7f)]
CTRL = [chr(c) for c in range(0, 0x20)] + ['\x7f', '"', '\\', '/']
UNI = ['é', 'ß', 'Ж', '中', '™', ' ', ' ', '﻿', '￿', '\U0001F600', '\U00010000', '\U0010FFFF', '\x80', '\xa0']


def rstr(n=None):
    n = R.randint(0, 12) if n is None else n
    return ''.join(R.choice(R.choice([ASCII_POOL, ASCII_POOL, CTRL, UNI])) for _ in range(n))


def rnum():
    k = R.randrange(5)
    if k == 0: return R.randint(-1000, 1000)
    if k == 1: return R.randint(-(1 << 200), 1 << 200)
    if k == 2: return R.choice(SPECIAL)
    if k == 3: return rand_double()
    return R.choice([True, False, None])


def robj(d=0):
    k = R.randrange(4 if d < 4 else 1)
    if k == 0: return R.choice([rnum, rstr])()
    if k == 1: return [robj(d + 1) for _ in range(R.randint(0, 4))]
    if k == 2: return {rstr(R.randint(0, 6)): robj(d + 1) for _ in range(R.randint(0, 4))}
    return rnum()


CONFIGS = [(True, None, False, None), (False, None, False, None), (True, 1, False, None), (False, 1, False, None),
           (True, 2, False, None), (False, None, True, (',', ':')), (True, 1, True, None), (True, None, False, (',', ':'))]


def gen_json_dumps():
    objs = [robj() for _ in range(3000)] + [[], {}, [[]], {'a': {}}, '', 0, -0.0, 1e16, 10 ** 30, -(10 ** 30), math.nan,
                                           [math.inf, -math.inf], {'k': [1, 2.0, 'x']}, 2 ** 53, 2 ** 64, 1.0, 1e-7]
    cases = []
    for o in objs:
        outs = [json.dumps(o, ensure_ascii=ea, indent=ind, sort_keys=sk, separators=sep) for ea, ind, sk, sep in CONFIGS]
        cases.append([json.dumps(o), outs])
    return dict(doc='[json.dumps(obj), [json.dumps(obj, **cfg) for cfg in configs]]',
                configs=[dict(ensure_ascii=a, indent=b, sort_keys=c, separators=d) for a, b, c, d in CONFIGS], cases=cases)


def gen_json_loads():
    texts = ['0', '-0', '-0.0', '1E400', '-1e400', '1e-400', '123456789012345678901234567890', '1.5e+3', '1.5E-3', '1e5',
             'NaN', 'Infinity', '-Infinity', ' [1 , 2 ]\n', '{"a":1,"b":2,"a":3}', '{"b":1,"a":2,"b":{"c":1}}', '"\\u00e9\\ud83d\\ude00"',
             '"\\/\\b\\f\\n\\r\\t"', '[]', '{}', '  {  }  ', 'true', 'false', 'null', '"x"', '01', '1.', '.5', '+1', '[1,]',
             '{"a" 1}', '"\x01"', 'nan', 'infinity', '', '[', '{"a":1}x', '1 2', '"\\x"', '-', '2.', '1e', '1e+', '0x1',
             '"a\tb"', '9007199254740993', '-9223372036854775809', '18446744073709551616', '0.1e1', '1.0', '[-0]',
             '{"":0}', '"\\uD834\\uDD1E"', '"\\u0041"', ' \t\r\n1 \t\r\n']
    for _ in range(2500):
        o = robj()
        s = json.dumps(o, ensure_ascii=R.random() < 0.5, indent=R.choice([None, 0, 1, 3]))
        if R.random() < 0.3: s = ' \n' + s.replace(':', ' : ') + '\t '
        texts.append(s)
    cases = []
    for t in texts:
        try: cases.append([t, json.dumps(json.loads(t))])
        except ValueError: cases.append([t, None])
    return dict(doc='[text, json.dumps(json.loads(text)) | null when loads raises]', cases=cases)


# ---- utf-8 decode 'replace'
VALID = ['a', 'é', 'ß', '中', '€', '\U0001F600', '\U00010000', '\U0010FFFF', '￿', 'ࠀ', '\x80', '߿', '™']


def gen_utf8():
    bs = []
    for ch in VALID:  # every prefix of every valid sequence, alone and followed by ASCII / another sequence
        e = ch.encode()
        for i in range(1, len(e) + 1):
            bs += [e[:i], e[:i] + b'x', e[:i] + 'é'.encode(), b'x' + e[:i]]
    for lead in range(0x80, 0x100):
        for nxt in (0x00, 0x41, 0x7f, 0x80, 0x8f, 0x90, 0x9f, 0xa0, 0xbf, 0xc0, 0xff):
            bs += [bytes([lead, nxt]), bytes([lead, nxt, 0x80]), bytes([lead, nxt, 0x80, 0x80]), bytes([lead, 0x80, nxt])]
    for _ in range(3000):  # synthetic names with multibyte chars truncated at 17..24 bytes
        s = ''.join(R.choice(VALID + ASCII_POOL) for _ in range(R.randint(8, 24))).encode()
        bs.append(s[:R.randint(17, 24)])
    for _ in range(2000):
        bs.append(bytes(R.getrandbits(8) for _ in range(R.randint(0, 24))))
    return dict(doc="[bytes hex, b.decode('utf-8', 'replace').encode() hex]",
                cases=[[b.hex(), b.decode('utf-8', 'replace').encode('utf-8').hex()] for b in bs])


# ---- NFKD -> ascii ignore
NFKD_POOL = [(0x20, 0x7e), (0xa0, 0x24f), (0x300, 0x36f), (0x370, 0x3ff), (0x400, 0x4ff), (0x1e00, 0x1eff), (0x2000, 0x2bff),
             (0x3000, 0x33ff), (0xac00, 0xac40), (0xfb00, 0xfb4f), (0xfe10, 0xfe6f), (0xff00, 0xffef), (0x1d400, 0x1d7ff),
             (0x1f100, 0x1f1ff), (0x1f600, 0x1f64f), (0x4e00, 0x4e40)]


def gen_nfkd():
    cases = []
    for _ in range(5000):
        s = ''.join(chr(R.randint(*R.choice(NFKD_POOL))) for _ in range(R.randint(0, 20)))
        cases.append([s, unicodedata.normalize('NFKD', s).encode('ascii', 'ignore').decode('ascii')])
    return dict(doc="[s, unicodedata.normalize('NFKD', s).encode('ascii', 'ignore').decode('ascii')]",
                unidata_version=unicodedata.unidata_version, cases=cases)


# ---- bits idioms (rows_to_fbgd / fbgd_write / v3_assemble / wire_py_core / v3_wire_companions)
def tobytes(bits): return int(bits + '0' * (-len(bits) % 8) or '0', 2).to_bytes((len(bits) + 7) // 8, 'big') if bits else b''


def gen_bits():
    widths = [0, 1, 2, 7, 8, 9, 13, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128, 129, 191, 192, 255, 256, 300, 600]
    cases = []
    for _ in range(4000):
        n = R.choice(widths + [R.randint(0, 700)])
        v = R.choice([R.getrandbits(n) if n else 0, (1 << n) - 1, 0, 1 if n else 0])
        body = format(v, '0%db' % n) if n else ''
        assert bin(v | 1 << n)[3:] == body and (int(body, 2) if body else 0) == v
        w = R.randint(0, n + 5)
        k = R.randint(0, n + 3)
        m = R.randint(0, n + 3)
        a, b = sorted(R.randint(-n - 2, n + 2) for _ in range(2))
        n2 = R.choice(widths)
        v2 = R.getrandbits(n2) if n2 else 0
        body2 = format(v2, '0%db' % n2) if n2 else ''
        cases.append(dict(
            n=n, v=str(v), body=body, bit_length=v.bit_length(), fmt_w=w, fmt=format(v, '0%db' % w),
            shr_k=k, shr=str(v >> k), shl=str(v << k), mask_m=m, mask=str(v & ((1 << m) - 1)),
            sl=[a, b], sl_body=body[a:b], cat_n=n2, cat_v=str(v2), cat=str(int(body + body2 or '0', 2)),
            tobytes=tobytes(body).hex(), hex_trunc=''.join(f'{x:08b}' for x in tobytes(body))[:n] == body))
    for _ in range(1000):
        p = bytes(R.getrandbits(8) for _ in range(R.randint(0, 40)))
        cases.append(dict(payload=p.hex(), bits_of=bin(int.from_bytes(b'\x01' + p, 'big'))[3:],
                          join08=''.join(f'{x:08b}' for x in p)))
    return dict(doc='synthetic Python bit-string idioms on (bits, value) chunks', cases=cases)


def main():
    out = Path(sys.argv[1])
    write(out / 'pyc' / 'intdiv.json', gen_intdiv())
    write(out / 'pyc' / 'floatdiv.json', gen_floatdiv())
    write(out / 'pyc' / 'dtbin.json', gen_dtbin())
    write(out / 'pyc' / 'round.json', gen_round())
    write(out / 'pyc' / 'f32.json', gen_f32())
    write(out / 'pyc' / 'repr.json', gen_repr())
    write(out / 'pyc' / 'json_dumps.json', gen_json_dumps())
    write(out / 'pyc' / 'json_loads.json', gen_json_loads())
    write(out / 'pyc' / 'utf8.json', gen_utf8())
    write(out / 'pyc' / 'nfkd.json', gen_nfkd())
    write(out / 'bits' / 'bits.json', gen_bits())


if __name__ == '__main__':
    main()
