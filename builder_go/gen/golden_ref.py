"""Reference encoder for the golden stage-dump form (internal/golden/SPEC.md); the Python dumper must produce the same
lines. Usage: <oracle>/python.exe golden_ref.py <builder_go>/testdata/golden/sample.jsonl.gz  (synthetic sample)
"""
import gzip
import json
import math
import random
import struct
import sys

INT_LIMIT = 1 << 53


def enc(x):
    """Python value -> JSON-native canonical tree (tags: $int, $f64, $bytes, $dict, $set)."""
    if x is None or isinstance(x, (bool, str)):
        return x
    if isinstance(x, int):
        return int(x) if -INT_LIMIT < x < INT_LIMIT else {'$int': ('-' if x < 0 else '') + format(abs(x), 'x')}
    if isinstance(x, float):
        return {'$f64': struct.pack('>d', x).hex()}
    if isinstance(x, (bytes, bytearray, memoryview)):
        return {'$bytes': bytes(x).hex()}
    if isinstance(x, (list, tuple)):
        return [enc(i) for i in x]
    if isinstance(x, dict):
        if all(isinstance(k, str) and not k.startswith('$') for k in x):
            return {k: enc(v) for k, v in x.items()}
        return {'$dict': [[enc(k), enc(v)] for k, v in x.items()]}
    if isinstance(x, (set, frozenset)):
        return {'$set': sorted((enc(i) for i in x), key=line)}
    raise TypeError('golden: convert %s explicitly' % type(x).__name__)


def line(tree):
    return json.dumps(tree, ensure_ascii=True, separators=(',', ':'), allow_nan=False)


class Writer:
    """with Writer(path, stage, meta) as w: w.write(record)  -- gzip mtime 0, no file name."""
    def __init__(self, path, stage, meta=None):
        self.raw = open(path, 'wb')
        self.gz = gzip.GzipFile(filename='', mode='wb', fileobj=self.raw, mtime=0)
        self.write({'golden': 1, 'stage': stage, 'meta': meta or {}})

    def write(self, rec):
        self.gz.write((line(enc(rec)) + '\n').encode('ascii'))

    def __enter__(self): return self

    def __exit__(self, *a):
        self.gz.close(); self.raw.close()


def sample(path):
    R = random.Random(53)
    with Writer(path, 'sample', {'seed': 53}) as w:
        w.write({'none': None, 't': True, 'f': False, 'i': 0, 'neg': -5, 'edge': [(1 << 53) - 1, 1 << 53, -(1 << 53) + 1, -(1 << 53)]})
        w.write({'floats': [0.0, -0.0, 1.0, 0.1, math.inf, -math.inf, math.nan, 5e-324, 1e300, 1 / 3]})
        w.write({'bytes': b'', 'ba': bytearray(b'\x00\xff\x10'), 'str': 'a"\\\n\x01\x7f\xe9™\U0001F600'})
        w.write({'chunk': dict(index=7, bits=200, value=R.getrandbits(200), refs=[(3, 1)]), 'small': dict(bits=13, value=4097)})
        w.write({(1, 'el'): 5, 'k': 1})          # non-str key -> $dict
        w.write({'$ref': 1, 'b': 2})             # '$' key -> $dict
        w.write({'s': {3, 1, 2, 'x', (1, 2)}, 'fs': frozenset({b'\x01', 1.5}), 'e': set()})
        w.write([[], {}, (), [[]], {'a': {}}])
        for i in range(200):
            w.write({'id': i, 'v': R.getrandbits(R.randint(0, 300)) * R.choice([1, -1]), 'f': R.uniform(-1e9, 1e9),
                     'b': R.getrandbits(8 * 6).to_bytes(6, 'big'), 'l': [R.random() for _ in range(R.randint(0, 3))]})


if __name__ == '__main__':
    sample(sys.argv[1])
