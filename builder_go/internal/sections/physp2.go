package sections

import (
	"strconv"

	"bc2replay/builder/internal/pyc"
)

// Port of v3_phys_p2 (r148/r165/r169): parser / rebaser of the physics section 0x1430b84 s1.p2 body (reader 8ABFD0)
// and p3 (78E3D0). Codecs from bytes: 50C7F0 vec3 (idx + 3 x (table[idx-1]+C)), 50BBE0 quaternion
// (1 + 3 x (1 + m(N-1))), 50B710 m(k) = k bits (k < 24) else 1 zero-flag bit [+23], 50C990 sparse vec3, 50CBD0
// low-precision quaternion (N < 25: 1 + 3 x (1 [+N])), inner blob (3 bits; 4 on /68 /76; none on /14-like layouts),
// tail = 1 option bit [+5].

// rd is a bit cursor over a '0'/'1' string with the two Python readers' overrun rules: v3_phys_p2.P (parseFirst:
// int(slice) before the length check, so a read at the very end raises int()'s ValueError) and v3_vehsec.W (length
// check first: 'overrun').
type rd struct {
	b          string
	i          int
	parseFirst bool
}

func (r *rd) u(n int) uint64 {
	var v uint64
	if r.parseFirst {
		if n != 0 {
			v = pyInt2(slice(r.b, r.i, r.i+n))
		}
		if r.i+n > len(r.b) {
			raise("ValueError", "overrun")
		}
	} else {
		if r.i+n > len(r.b) {
			raise("ValueError", "overrun")
		}
		if n != 0 {
			v = pyInt2(r.b[r.i : r.i+n])
		}
	}
	r.i += n
	return v
}

// opt is W.opt: a presence bit, then n bits when set.
func (r *rd) opt(n int) bool {
	if r.u(1) != 0 {
		r.u(n)
		return true
	}
	return false
}

// m is P.m (50B710 bit consumption).
func (r *rd) m(k int) {
	if k < 24 {
		r.u(k)
	} else if r.u(1) == 0 {
		r.u(23)
	}
}

// quat is P.quat: 50CBD0, N >= 25 -> 50BBE0 (1 + 3 x (1 + m(N-1))), else 1 + 3 x (1 [+N]).
func (r *rd) quat(N int) {
	r.u(1)
	for range 3 {
		if N >= 25 {
			r.u(1)
			r.m(N - 1)
		} else if r.u(1) != 0 {
			r.u(N)
		}
	}
}

// sparse is P.sparse (50C990): per-axis presence; the selector at the first present axis; sel 0 = 3 raw u32.
func (r *rd) sparse(table []int64, frac int) {
	ib := bitLength(len(table))
	w := -1
	for range 3 {
		if r.u(1) != 0 {
			if w < 0 {
				sel := int(r.u(ib))
				if sel == 0 {
					r.u(96)
					return
				}
				w = int(pyIndex(table, sel-1)) + frac
			}
			r.u(w)
		}
	}
}

func sx(v uint64, w int) int64 {
	if w > 0 && v>>(w-1) != 0 {
		return int64(v) - 1<<w
	}
	return int64(v)
}

// PhysShape is the parse(**shape) keywords: sparse vectors present, inner blob width.
type PhysShape struct {
	Sparse bool
	Inner  int
}

// StdShape is parse's default shape (sparse=True, inner=3).
var StdShape = PhysShape{true, 3}

// P2 is v3_phys_p2.parse's dict: start, end, flag, idx, C, vec (start, end of the vec3), xyz (nil = raw floats).
type P2 struct {
	Start, End, Flag, Idx, C int
	Vec                      [2]int
	XYZ                      *[3]float64
}

// PyValue is the dict in Python key order.
func (p *P2) PyValue() any {
	o := pyc.NewObject()
	o.Set("start", p.Start)
	o.Set("end", p.End)
	o.Set("flag", p.Flag)
	o.Set("idx", p.Idx)
	o.Set("C", p.C)
	o.Set("vec", []any{p.Vec[0], p.Vec[1]})
	if p.XYZ == nil {
		o.Set("xyz", nil)
	} else {
		o.Set("xyz", []any{p.XYZ[0], p.XYZ[1], p.XYZ[2]})
	}
	return o
}

// parseP2 is parse(bits, pos, origin, table, **shape); pos = the flag bit (right after the p2 presence bit).
func parseP2(bits string, pos int, origin [3]float64, table []int64, sh PhysShape) *P2 {
	p := &rd{b: bits, i: pos, parseFirst: true}
	flag := int(p.u(1))
	C := 7
	if flag != 0 {
		C = 10
	}
	ib := bitLength(len(table))
	idx := int(p.u(ib))
	r := &P2{Start: pos, Flag: flag, Idx: idx, C: C}
	vs := pos + 1
	if idx == 0 {
		p.u(96)
	} else {
		w := int(pyIndex(table, idx-1)) + C
		var ks [3]int64
		for j := range ks {
			ks[j] = sx(p.u(w), w)
		}
		var xyz [3]float64
		for j, k := range ks {
			xyz[j] = f32(f32(origin[j]) + f32(float64(k))/float64(int64(1)<<C))
		}
		r.XYZ = &xyz
	}
	r.Vec = [2]int{vs, p.i}
	if flag != 0 {
		p.quat(32)
	} else {
		p.quat(14)
	}
	if sh.Sparse {
		p.sparse(table, 5)
		p.sparse(table, 8)
	}
	p.u(sh.Inner) // inner blob (mode-2 read of [body+0xD8])
	if p.u(1) != 0 {
		p.u(5) // r165: tail option bit (8AC22C) -> 3 bits 50BF20 + 2 x 1 bit; 0 in violent-destruction updates
	}
	r.End = p.i
	return r
}

// encodeVec is encode_vec(xyz, C, origin, table): smallest table entry, ValueError('out of range').
func encodeVec(xyz [3]float64, C int, origin [3]float64, table []int64) string {
	ks := make([]int64, 3)
	big := false
	for j := range ks {
		k, ok := round((f32(xyz[j]) - f32(origin[j])) * float64(int64(1)<<C))
		ks[j], big = k, big || !ok
	}
	s, ok := encVec(ks, big, C, table)
	if !ok {
		raise("ValueError", "out of range")
	}
	return s
}

// q32Decode is _q32_decode: 50BBE0 N=32 -> (w sign, [x, y, z], end).
func q32Decode(bits string, pos int) (uint64, [3]float64, int) {
	p := &rd{b: bits, i: pos, parseFirst: true}
	ws := p.u(1)
	var v [3]float64
	for j := range v {
		sg := p.u(1)
		mag := 0.0
		if p.u(1) == 0 {
			mag = float64(p.u(23)) / float64(1<<23)
		}
		if sg != 0 {
			mag = -mag
		}
		v[j] = mag
	}
	return ws, v, p.i
}

// q14Encode is _q14_encode: 50CBD0 low path, w-sign bit + 3 x (present [+ 14-bit signed k = x * 8191]).
func q14Encode(ws uint64, v [3]float64) string {
	const N = 14
	const full = 1<<(N-1) - 1
	out := strconv.FormatUint(ws, 10)
	for _, x := range v {
		k, ok := round(x * full)
		if !ok {
			k = full
			if x < 0 {
				k = -full
			}
		}
		k = max(-full, min(full, k))
		if k == 0 {
			out += "0"
		} else {
			out += "1" + formatBin(uint64(k)&(1<<N-1), N)
		}
	}
	return out
}

// toDemoForm is to_demo_form(bits, pos, origin, table, **shape): the p2 body as the demo writes it (flag 0,
// position C=7 origin-zero, 14-bit quaternion, rest verbatim) and the parse dict.
func toDemoForm(bits string, pos int, origin [3]float64, table []int64, sh PhysShape) (string, *P2) {
	r := parseP2(bits, pos, origin, table, sh)
	if r.XYZ == nil {
		return slice(bits, pos, r.End), r
	}
	a, b := r.Vec[0], r.Vec[1]
	if r.Flag == 0 {
		return slice(bits, pos, a) + encodeVec(*r.XYZ, 7, [3]float64{}, table) + slice(bits, b, r.End), r
	}
	ws, v, qe := q32Decode(bits, b)
	return "0" + encodeVec(*r.XYZ, 7, [3]float64{}, table) + q14Encode(ws, v) + slice(bits, qe, r.End), r
}

// parseP3 is parse_p3 (78E3D0): 1 bit [+ vec3 relative to the decoded position, C=3, + 6 + 7 + 7 + 4] -> end.
func parseP3(bits string, pos int, table []int64) int {
	p := &rd{b: bits, i: pos, parseFirst: true}
	ib := bitLength(len(table))
	if p.u(1) != 0 {
		sel := int(p.u(ib))
		if sel == 0 {
			p.u(96)
		} else {
			p.u(3 * (int(pyIndex(table, sel-1)) + 3))
		}
		p.u(6 + 7 + 7 + 4)
	}
	return p.i
}

// ParseP2 is v3_phys_p2.parse (exported for tests / maintainers).
func ParseP2(bits string, pos int, origin [3]float64, table []int64, sh PhysShape) (r *P2, err error) {
	defer catch(&err)
	return parseP2(bits, pos, origin, table, sh), nil
}

// ToDemoForm is v3_phys_p2.to_demo_form.
func ToDemoForm(bits string, pos int, origin [3]float64, table []int64, sh PhysShape) (body string, r *P2, err error) {
	defer catch(&err)
	body, r = toDemoForm(bits, pos, origin, table, sh)
	return body, r, nil
}
