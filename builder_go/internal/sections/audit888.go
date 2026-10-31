package sections

import (
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// Port of v3_audit888.chunks_of / rebase (the parts WIREMULTI uses; run_section = wire.RunSection).

// AuOvec is one chunk-relative origin vector (a, b, xyz) = (start, end) relative to the body.
type AuOvec struct {
	A, B int
	XYZ  [3]float64
}

// AuChunk is one chunks_of entry: property p, body bits, body start b0, body-relative ref starts and ovs.
type AuChunk struct {
	P, B0 int
	Body  string
	Refs  []int
	Ovs   []AuOvec
}

// PyValue is dict(p, body, b0, refs, ovs) with ovs as (a, b, xyz) tuples.
func (c *AuChunk) PyValue() any {
	o := pyc.NewObject()
	o.Set("p", c.P)
	o.Set("body", c.Body)
	o.Set("b0", c.B0)
	rs := make([]any, len(c.Refs))
	for i, r := range c.Refs {
		rs[i] = r
	}
	o.Set("refs", rs)
	o.Set("ovs", OvsValue(c.Ovs))
	return o
}

// OvsValue is a list of (a, b, xyz) tuples.
func OvsValue(ovs []AuOvec) []any {
	l := make([]any, len(ovs))
	for i, v := range ovs {
		l[i] = []any{v.A, v.B, []any{v.XYZ[0], v.XYZ[1], v.XYZ[2]}}
	}
	return l
}

// ChunksOf is chunks_of(bits, pos, end, refs, ovs): the present properties of one section run (positions = the
// presence bits), with the references and origin vectors that lie inside each body.
func ChunksOf(bits string, pos []uint32, end uint32, refs []wire.Ref, ovs []wire.Ovec) (out []AuChunk, err error) {
	defer catch(&err)
	out = []AuChunk{}
	for pi, s32 := range pos {
		s := int(s32)
		e := int(end)
		if pi+1 < len(pos) {
			e = int(pos[pi+1])
		}
		if at(bits, s) != '1' {
			continue
		}
		c := AuChunk{P: pi, Body: slice(bits, s+1, e), B0: s + 1, Refs: []int{}, Ovs: []AuOvec{}}
		for _, r := range refs {
			if s < int(r.Start) && int(r.End) <= e {
				c.Refs = append(c.Refs, int(r.Start)-s-1)
			}
		}
		for _, v := range ovs {
			if s < int(v.Start) && int(v.End) <= e {
				c.Ovs = append(c.Ovs, AuOvec{int(v.Start) - s - 1, int(v.End) - s - 1, v.XYZ})
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// Rebase is v3_audit888.rebase(body, ovs, table) (r127): each origin-relative quantized vec3 re-encoded
// origin-zero in the same form (C from the width). ok false = None (no vectors, a raw-float form, an inexact value
// or no table width fits).
func Rebase(body string, ovs []AuOvec, table []int64) (out string, ok bool, err error) {
	defer catch(&err)
	if len(ovs) == 0 {
		return "", false, nil
	}
	ib := bitLength(len(table))
	last := 0
	for _, v := range ovs {
		ix := int(pyInt2(slice(body, v.A, v.A+ib)))
		if ix == 0 {
			return "", false, nil // raw float form
		}
		C := int(pyc.FloorDiv(int64(v.B-v.A-ib), 3) - pyIndex(table, ix-1))
		if C < 0 {
			raise("ValueError", "negative shift count")
		}
		if C > 62 { // Go-only limit: the readers' vectors have C <= 10
			raise("NotImplementedError", "audit888 rebase: precision C=%d", C)
		}
		scale := float64(int64(1) << C)
		ks := make([]int64, 3)
		big := false
		for j, x := range v.XYZ {
			k, fit := round(x * scale)
			ks[j], big = k, big || !fit
		}
		for j, k := range ks {
			if !big && float64(k)/scale != v.XYZ[j] {
				return "", false, nil
			}
		}
		enc, fits := encVec(ks, big, C, table)
		if !fits {
			return "", false, nil
		}
		out += slice(body, last, v.A) + enc
		last = v.B
	}
	return out + slice(body, last, len(body)), true, nil
}
