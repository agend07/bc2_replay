# Golden stage-dump form (v1)

Shared by the Python dumper (P0a, run against the frozen oracle) and the Go builder. Reference encoder:
`gen/golden_ref.py` (`enc`, `line`, `Writer`); Go: `internal/golden` (`Tree`, `Line`, `Writer`, `Reader`, `Untag`, `Diff`).
`testdata/golden/sample.jsonl.gz` is written by the reference and must re-encode byte-identically in Go.

## File
- gzip, `mtime=0`, no file name (`gzip.GzipFile(filename='', mode='wb', fileobj=f, mtime=0)`). Compression level is free:
  **dumps are compared on their decompressed bytes**, never on the gzip bytes (Python zlib and Go flate differ).
- UTF-8 (in practice ASCII) JSONL, one record per line, each line ends with `\n`.
- Line 1 is the header `enc({"golden": 1, "stage": <name>, "meta": {...}})`; the rest are records in production order.

## Line serialization
`json.dumps(enc(record), ensure_ascii=True, separators=(',', ':'), allow_nan=False)`; dict order is insertion order
(never `sort_keys`).

## enc(x)
| Python value | canonical tree |
|---|---|
| `None`, `True`, `False`, `str` | itself (bool is checked before int) |
| `int` with `-2**53 < x < 2**53` | JSON number |
| any other `int` | `{"$int": "[-]<lowercase hex, no 0x>"}` |
| `float` | `{"$f64": "<16 hex digits of the IEEE-754 bits, big-endian>"}` (`struct.pack('>d', x).hex()`); -0.0, inf and NaN payloads are kept |
| `bytes`, `bytearray`, `memoryview` | `{"$bytes": "<lowercase hex>"}` |
| `list`, `tuple` | JSON array of `enc(item)` (tuples are not distinguished) |
| `dict` with only `str` keys, none starting with `$` | JSON object, same key order, values `enc`'d |
| any other `dict` (non-str key, or a key starting with `$`) | `{"$dict": [[enc(k), enc(v)], ...]}` in insertion order |
| `set`, `frozenset` | `{"$set": [...]}`: `enc(item)` sorted by its line serialization (Python str order) |
| anything else | error: the dumper converts it explicitly (dataclass -> dict, Path -> str, ...) |

Chunk values (`dict(bits=n, value=v, ...)`) need no special case: `value` follows the int rule above and `bits`
sits next to it. (This replaces the plan's `{bits, hex}` wording: one uniform int rule, no shape detection.) A
canonical tree only contains objects that are plain dicts or one of the five `$` tags; a plain dict can never have a
key starting with `$`, so the tags are unambiguous.

## Comparing
`golden.Diff` skips byte-equal lines, otherwise decodes both lines and reports the first differing JSON path
(`line 12 $.rows[3].value: A=... B=...`), key-order differences (`keys a,b` vs `keys b,a`), length differences and a
dump ending early. Two NaN `$f64` with different payloads count as equal unless `Options.StrictNaN`.
