package pipeline

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// parseHex is int(x, 16) (optional 0x prefix, surrounding whitespace, '_' separators).
func parseHex(x string) (uint32, error) {
	s := strings.TrimSpace(x)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	v, err := strconv.ParseUint(strings.ReplaceAll(s, "_", ""), 16, 32)
	if err != nil || s == "" {
		return 0, fmt.Errorf("ValueError: invalid literal for int() with base 16: %s", strRepr(x))
	}
	return uint32(v), nil
}

// parseInt is int(x) of a decimal recipe value.
func parseInt(k, x string) (int, error) {
	v, err := strconv.Atoi(strings.TrimSpace(strings.ReplaceAll(x, "_", "")))
	if err != nil {
		return 0, fmt.Errorf("ValueError: invalid literal for int() with base 10: %s (%s)", strRepr(x), k)
	}
	return v, nil
}

// pyIdx is a Python slice bound for a sequence of length n (negative = from the end, clamped).
func pyIdx(i, n int) int {
	if i < 0 {
		i += n
		if i < 0 {
			i = 0
		}
	}
	return min(i, n)
}

// pySlice is s[a:b] with Python semantics.
func pySlice(s string, a, b int) string {
	a, b = pyIdx(a, len(s)), pyIdx(b, len(s))
	if b <= a {
		return ""
	}
	return s[a:b]
}

func int2(s string) (uint64, error) {
	if s == "" {
		return 0, errors.New("ValueError: invalid literal for int() with base 2: ''")
	}
	if len(s) > 64 {
		return 0, fmt.Errorf("pipeline: %d-bit index field", len(s))
	}
	return strconv.ParseUint(s, 2, 64)
}

func bitLen(n int) int {
	k := 0
	for ; n > 0; n >>= 1 {
		k++
	}
	return k
}

// encpos is cl04c _encpos(xyz, table, C): the origin-zero vec3 of the camera codec (index + 3 two's complement ints
// of width q + C, the first table width that fits); ValueError when inexact or out of range.
func encpos(xyz [3]float64, table []int64, C int64) (string, error) {
	if C < 0 {
		return "", errors.New("ValueError: negative shift count")
	}
	if C > 62 {
		return "", fmt.Errorf("pipeline: rebase precision C=%d", C)
	}
	scale := float64(int64(1) << C)
	var ks [3]int64
	big := false // |k| >= 2**63: exact in the float test, never fits a width
	for i, x := range xyz {
		v := x * scale
		if math.IsInf(v, 0) {
			return "", errors.New("OverflowError: cannot convert float infinity to integer")
		}
		if math.IsNaN(v) {
			return "", errors.New("ValueError: cannot convert float NaN to integer")
		}
		k, err := pyc.RoundInt64(v)
		if err != nil {
			big = true
			continue
		}
		if float64(k)/scale != x {
			return "", errors.New("ValueError: inexact rebase")
		}
		ks[i] = k
	}
	ib := bitLen(len(table))
	if !big {
		for i, q := range table {
			wd := q + C
			if wd < 1 || wd > 63 {
				continue
			}
			lim := int64(1) << (wd - 1)
			if ks[0] >= -lim && ks[0] < lim && ks[1] >= -lim && ks[1] < lim && ks[2] >= -lim && ks[2] < lim {
				s := bits.FormatBin(uint64(i+1), ib)
				for _, k := range ks {
					s += bits.FormatBin(uint64(k)&(uint64(1)<<wd-1), int(wd))
				}
				return s, nil
			}
		}
	}
	return "", errors.New("ValueError: rebase out of range")
}

// rebaseChunks is cl04c _rebase_chunks(rec, g, stat): rec's chunks with each one-vec3 chunk re-encoded origin-zero
// (wire C kept); a chunk overlapping several vectors is dropped ('multi'), raw-float vectors kept ('raw').
func (b *build) rebaseChunks(rec *wire.SliceResult, g *wire.Ghost, stat *fbgd.Counter) ([]wire.Chunk, error) {
	out := []wire.Chunk{}
	for _, c := range rec.Chunks {
		var vs []wire.Ovec
		for _, v := range rec.OriginVectors {
			if int64(v.Start) < c.BodyStart+int64(c.Value.Len()) && int64(v.End) > c.BodyStart {
				vs = append(vs, v)
			}
		}
		if len(vs) == 0 {
			out = append(out, c)
			continue
		}
		if len(vs) != 1 {
			stat.Add("multi", 1)
			continue
		}
		v := vs[0]
		cb := c.Value.String()
		a, e := int(int64(v.Start)-c.BodyStart), int(int64(v.End)-c.BodyStart)
		if g.Config < 0 || g.Config >= len(b.d.Configs) {
			return nil, errors.New("IndexError: list index out of range")
		}
		tb := b.d.Configs[g.Config].Tables[0]
		ib := bitLen(len(tb))
		ix, err := int2(pySlice(cb, a, a+ib))
		if err != nil {
			return nil, err
		}
		if ix == 0 {
			out = append(out, c)
			stat.Add("raw", 1)
			continue
		}
		if int(ix)-1 >= len(tb) {
			return nil, errors.New("IndexError: list index out of range")
		}
		C := pyc.FloorDiv(int64(e-a-ib), 3) - tb[ix-1]
		enc, err := encpos(v.XYZ, tb, C)
		if err != nil {
			return nil, err
		}
		nb := pySlice(cb, 0, a) + enc + pySlice(cb, e, len(cb))
		val, err := bits.Parse(nb)
		if err != nil {
			return nil, err
		}
		c.Value = val
		out = append(out, c)
		stat.Add("rebased", 1)
	}
	if b.ck != nil {
		tmp := *rec
		tmp.Chunks = out
		ch, _ := tmp.PyValue().(*pyc.Object).Get("chunks")
		b.emit("rebase_chunks", obj("t", g.T, "id", g.ID, "pseq", g.Pseq, "out", ch, "stat", counterDict(stat)))
	}
	return out, nil
}

// obj builds an ordered dict from key, value pairs.
func obj(kv ...any) *pyc.Object {
	o := pyc.NewObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
