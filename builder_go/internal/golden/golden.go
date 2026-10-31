// Package golden writes, reads and compares stage dumps in the canonical form of SPEC.md (gzipped JSONL, one
// canonical JSON tree per line), the form the Python dumper produces from the frozen oracle.
package golden

import (
	"bufio"
	"compress/gzip"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/pyc"
)

// Pair is one item of a dict that cannot be a JSON object (non-str keys or a key starting with '$').
type Pair struct{ K, V any }

// Pairs is such a dict in insertion order; it encodes as {"$dict": [[k, v], ...]}.
type Pairs []Pair

// Set is a Python set; it encodes as {"$set": [...]} sorted by each item's canonical line.
type Set []any

var intLimit = new(big.Int).Lsh(big.NewInt(1), 53)

// bigTree is the int rule: |v| < 2**53 -> JSON number, else {"$int": "[-]hex"}.
func bigTree(z *big.Int) any {
	if z.CmpAbs(intLimit) < 0 {
		return z.Int64()
	}
	o := pyc.NewObject()
	if z.Sign() < 0 {
		o.Set("$int", "-"+new(big.Int).Neg(z).Text(16))
	} else {
		o.Set("$int", z.Text(16))
	}
	return o
}

func int64Tree(x int64) any {
	if -1<<53 < x && x < 1<<53 {
		return x
	}
	return bigTree(big.NewInt(x))
}

func tag(k, v string) *pyc.Object {
	o := pyc.NewObject()
	o.Set(k, v)
	return o
}

// Tree converts a Go value to the canonical JSON-native tree (SPEC.md). Supported: nil, bool, Go ints, *big.Int,
// bits.Bits (its value), float64, string, []byte, []any and other slices of those, *pyc.Object, Pairs, Set,
// pyc.Valuer.
func Tree(v any) (any, error) {
	switch x := v.(type) {
	case nil, bool, string:
		return x, nil
	case int:
		return int64Tree(int64(x)), nil
	case int64:
		return int64Tree(x), nil
	case int32:
		return int64(x), nil
	case uint32:
		return int64(x), nil
	case uint64:
		return bigTree(new(big.Int).SetUint64(x)), nil
	case *big.Int:
		return bigTree(x), nil
	case bits.Bits:
		return bigTree(x.Big()), nil
	case float64:
		return tag("$f64", fmt.Sprintf("%016x", math.Float64bits(x))), nil
	case []byte:
		return tag("$bytes", hex.EncodeToString(x)), nil
	case []any:
		return treeSlice(len(x), func(i int) any { return x[i] })
	case []string:
		return treeSlice(len(x), func(i int) any { return x[i] })
	case []int:
		return treeSlice(len(x), func(i int) any { return x[i] })
	case []int64:
		return treeSlice(len(x), func(i int) any { return x[i] })
	case []float64:
		return treeSlice(len(x), func(i int) any { return x[i] })
	case *pyc.Object:
		plain := true
		for _, k := range x.Keys() {
			if strings.HasPrefix(k, "$") {
				plain = false
			}
		}
		if !plain {
			ps := make(Pairs, 0, x.Len())
			for _, k := range x.Keys() {
				val, _ := x.Get(k)
				ps = append(ps, Pair{k, val})
			}
			return Tree(ps)
		}
		o := pyc.NewObject()
		for _, k := range x.Keys() {
			val, _ := x.Get(k)
			t, err := Tree(val)
			if err != nil {
				return nil, err
			}
			o.Set(k, t)
		}
		return o, nil
	case Pairs:
		items := make([]any, len(x))
		for i, p := range x {
			k, err := Tree(p.K)
			if err != nil {
				return nil, err
			}
			val, err := Tree(p.V)
			if err != nil {
				return nil, err
			}
			items[i] = []any{k, val}
		}
		o := pyc.NewObject()
		o.Set("$dict", items)
		return o, nil
	case Set:
		type kv struct {
			line string
			t    any
		}
		items := make([]kv, len(x))
		for i, it := range x {
			t, err := Tree(it)
			if err != nil {
				return nil, err
			}
			l, err := dump(t)
			if err != nil {
				return nil, err
			}
			items[i] = kv{l, t}
		}
		slices.SortStableFunc(items, func(a, b kv) int { return strings.Compare(a.line, b.line) })
		out := make([]any, len(items))
		for i, it := range items {
			out[i] = it.t
		}
		o := pyc.NewObject()
		o.Set("$set", out)
		return o, nil
	case pyc.Valuer:
		return Tree(x.PyValue())
	}
	return nil, fmt.Errorf("golden: convert %T explicitly", v)
}

func treeSlice(n int, at func(int) any) (any, error) {
	out := make([]any, n)
	for i := range out {
		t, err := Tree(at(i))
		if err != nil {
			return nil, err
		}
		out[i] = t
	}
	return out, nil
}

// dump is the line serialization: json.dumps(tree, ensure_ascii=True, separators=(',', ':'), allow_nan=False).
func dump(tree any) (string, error) {
	return pyc.Dumps(tree, pyc.DumpOptions{EnsureASCII: true, ItemSep: ",", KeySep: ":", DisallowNaN: true})
}

// Line is the canonical line of a Go value, without the trailing newline.
func Line(v any) (string, error) {
	t, err := Tree(v)
	if err != nil {
		return "", err
	}
	return dump(t)
}

// Writer writes a dump: gzip (mtime 0, no name), a header line, then one record per line.
type Writer struct {
	gz *gzip.Writer
}

// NewWriter starts a dump of stage with its header {"golden": 1, "stage": ..., "meta": meta}; meta may be nil.
func NewWriter(w io.Writer, stage string, meta *pyc.Object) (*Writer, error) {
	gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	gw := &Writer{gz: gz}
	if meta == nil {
		meta = pyc.NewObject()
	}
	h := pyc.NewObject()
	h.Set("golden", 1)
	h.Set("stage", stage)
	h.Set("meta", meta)
	return gw, gw.Write(h)
}

// Write appends one record.
func (w *Writer) Write(rec any) error {
	l, err := Line(rec)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w.gz, l+"\n")
	return err
}

// Close flushes the gzip stream (the underlying writer stays open).
func (w *Writer) Close() error { return w.gz.Close() }

// Reader iterates the lines of a dump.
type Reader struct {
	gz   *gzip.Reader
	sc   *bufio.Scanner
	line int
}

// NewReader opens a gzipped dump.
func NewReader(r io.Reader) (*Reader, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 1<<20), 1<<30)
	return &Reader{gz: gz, sc: sc}, nil
}

// Next returns the next raw line (without newline); io.EOF at the end.
func (r *Reader) Next() ([]byte, error) {
	if !r.sc.Scan() {
		if err := r.sc.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	r.line++
	return r.sc.Bytes(), nil
}

// LineNo is the 1-based number of the line Next returned last.
func (r *Reader) LineNo() int { return r.line }

// Untag turns a decoded canonical tree back into Go values: $int -> *big.Int, $f64 -> float64, $bytes -> []byte,
// $dict -> Pairs, $set -> Set, plain ints -> int64, objects -> *pyc.Object. Tree(Untag(t)) reproduces t.
func Untag(t any) (any, error) {
	switch x := t.(type) {
	case []any:
		out := make([]any, len(x))
		for i, it := range x {
			u, err := Untag(it)
			if err != nil {
				return nil, err
			}
			out[i] = u
		}
		return out, nil
	case *pyc.Object:
		if x.Len() == 1 {
			k := x.Keys()[0]
			val, _ := x.Get(k)
			s, isStr := val.(string)
			switch {
			case k == "$int" && isStr:
				z, ok := new(big.Int).SetString(s, 16) // accepts a leading '-'
				if !ok {
					return nil, fmt.Errorf("golden: bad $int %q", s)
				}
				return z, nil
			case k == "$f64" && isStr:
				u, err := strconv.ParseUint(s, 16, 64)
				return math.Float64frombits(u), err
			case k == "$bytes" && isStr:
				return hex.DecodeString(s)
			case k == "$dict":
				items, _ := val.([]any)
				ps := make(Pairs, len(items))
				for i, it := range items {
					kv, ok := it.([]any)
					if !ok || len(kv) != 2 {
						return nil, fmt.Errorf("golden: bad $dict item")
					}
					kk, err := Untag(kv[0])
					if err != nil {
						return nil, err
					}
					vv, err := Untag(kv[1])
					if err != nil {
						return nil, err
					}
					ps[i] = Pair{kk, vv}
				}
				return ps, nil
			case k == "$set":
				items, _ := val.([]any)
				u, err := Untag(items)
				if err != nil {
					return nil, err
				}
				return Set(u.([]any)), nil
			}
		}
		o := pyc.NewObject()
		for _, k := range x.Keys() {
			val, _ := x.Get(k)
			u, err := Untag(val)
			if err != nil {
				return nil, err
			}
			o.Set(k, u)
		}
		return o, nil
	}
	return t, nil
}
