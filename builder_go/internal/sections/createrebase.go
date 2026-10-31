package sections

import (
	"math"

	"bc2replay/builder/internal/pyc"
)

// Port of v3_create_rebase (r159/r190): origin-zero re-encode of ghost-level creates that carry origin-relative
// vec3s (BitStream_ReadOriginRelVec3 50FE00 -> 50C7F0: idx3 + 3 x signed (TABLE[idx-1]+C) ints, value =
// origin + k/2^C). Create = 6-bit type + 1 + 12-bit variant (19) + body:
//
//	type 6 projectile:  P [vec3 C=5, 4 bits]  48  P [vec3 C=5]  P
//	type 3 vehicle:     vec3 C=5, the rest copied
//	type 31 spawner:    quaternion 50CBD0 N=8 (w-sign + 3 x (present [+8])) + vec3 C=10, nothing after
//
// Only the vec3s are re-encoded (smallest table entry that fits); the writer truncates toward zero: demo k_abs =
// O*2^C + k, minus 1 when k < 0 < k_abs, plus 1 when k > 0 > k_abs.

// Table is v3_create_rebase.TABLE (= v3_phys_p2.TABLE).
var Table = []int64{1, 3, 5, 7, 9, 11, 13}

// createTypes is TYPES: 6-bit create type -> layout.
var createTypes = map[uint64]string{6: "proj", 3: "veh", 31: "spawner"}

// VecInfo is rebase_vec's info dict: {idx: 0} for a raw-float vector, else {idx, idx_out, xyz}.
type VecInfo struct {
	Idx, IdxOut int
	XYZ         [3]float64
}

// PyValue is the info dict in Python key order.
func (v VecInfo) PyValue() any {
	o := pyc.NewObject()
	o.Set("idx", v.Idx)
	if v.Idx != 0 {
		o.Set("idx_out", v.IdxOut)
		o.Set("xyz", []any{v.XYZ[0], v.XYZ[1], v.XYZ[2]})
	}
	return o
}

// CreateInfo is rebase_create's info dict {kind, vecs}.
type CreateInfo struct {
	Kind string
	Vecs []VecInfo
}

// PyValue is the info dict in Python key order.
func (c *CreateInfo) PyValue() any {
	o := pyc.NewObject()
	o.Set("kind", c.Kind)
	vs := make([]any, len(c.Vecs))
	for i, v := range c.Vecs {
		vs[i] = v
	}
	o.Set("vecs", vs)
	return o
}

// readVec is _read_vec: (end, ks or nil for the raw-float form, idx).
func readVec(bits string, i, C int) (int, []int64, int) {
	idx := int(pyInt2(slice(bits, i, i+3)))
	i += 3
	if idx == 0 {
		return i + 96, nil, 0
	}
	w := int(Table[idx-1]) + C
	ks := make([]int64, 3)
	for k := range ks {
		v := int64(pyInt2(slice(bits, i, i+w)))
		i += w
		if v>>(w-1) != 0 {
			v -= 1 << w
		}
		ks[k] = v
	}
	return i, ks, idx
}

// encVec is _enc_vec: the smallest table entry whose width holds every k. big marks a value beyond int64 (fits
// nothing).
func encVec(ks []int64, big bool, C int, table []int64) (string, bool) {
	if big {
		return "", false
	}
	ib := bitLength(len(table))
	for i, q := range table {
		wd := int(q) + C
		if wd < 1 || wd > 63 {
			continue
		}
		fits := true
		for _, k := range ks {
			fits = fits && -(int64(1)<<(wd-1)) <= k && k < int64(1)<<(wd-1)
		}
		if fits {
			s := formatBin(uint64(i+1), ib)
			for _, k := range ks {
				s += formatBin(uint64(k)&(uint64(1)<<wd-1), wd)
			}
			return s, true
		}
	}
	return "", false
}

// rebaseVec is rebase_vec: re-encode the vec3 at bits[i:] origin-zero -> (new vec bits, end index, info).
func rebaseVec(bits string, i, C int, origin [3]float64, trunc bool) (string, int, VecInfo) {
	end, ks, idx := readVec(bits, i, C)
	if ks == nil {
		return slice(bits, i, end), end, VecInfo{}
	}
	out := make([]int64, 3)
	big := false
	for j, o := range origin {
		O := o * float64(int64(1)<<C)
		switch {
		case math.IsInf(O, 0):
			raise("OverflowError", "cannot convert float infinity to integer")
		case math.IsNaN(O):
			raise("ValueError", "cannot convert float NaN to integer")
		case O != math.Trunc(O):
			raise("ValueError", "origin not representable at C=%d: %s", C, pyc.Repr(o))
		case math.Abs(O) >= 1<<62: // int(O) + k fits no table width
			big = true
			continue
		}
		k := ks[j]
		a := int64(O) + k
		if trunc {
			if k < 0 && 0 < a {
				a--
			} else if k > 0 && 0 > a {
				a++
			}
		}
		out[j] = a
	}
	nb, ok := encVec(out, big, C, Table)
	if !ok {
		raise("ValueError", "rebase out of range")
	}
	info := VecInfo{Idx: idx, IdxOut: int(pyInt2(nb[:3]))}
	for j, a := range out {
		info.XYZ[j] = float64(a) / float64(int64(1)<<C)
	}
	return nb, end, info
}

// RebaseCreate is rebase_create(bits, origin, trunc): the whole create slice with its origin-relative vec3s
// re-encoded origin-zero; info nil when not applicable (short, flag bit 6 clear, or another create type).
func RebaseCreate(bits string, origin [3]float64, trunc bool) (out string, info *CreateInfo, err error) {
	defer catch(&err)
	if len(bits) < 19 || bits[6] != '1' {
		return bits, nil, nil
	}
	kind, ok := createTypes[pyInt2(bits[:6])]
	if !ok {
		return bits, nil, nil
	}
	info = &CreateInfo{Kind: kind, Vecs: []VecInfo{}}
	o, i := bits[:19], 19
	vec := func(C int) {
		nb, e, vi := rebaseVec(bits, i, C, origin, trunc)
		o += nb
		i = e
		info.Vecs = append(info.Vecs, vi)
	}
	switch kind {
	case "veh":
		vec(5)
		o += slice(bits, i, len(bits))
	case "spawner": // r190: 8C7400 = 50CBD0(N=8) quaternion, 50FE00(C=10) vec3, end
		j := i + 1
		for range 3 {
			j++
			if at(bits, j-1) == '1' {
				j += 8
			}
		}
		o += slice(bits, i, j)
		i = j
		vec(10)
		if i != len(bits) {
			raise("ValueError", "spawner create layout mismatch: %d consumed of %d", i, len(bits))
		}
	default: // proj
		p1 := at(bits, i)
		i++
		o += string(p1)
		if p1 == '1' {
			vec(5)
			o += slice(bits, i, i+4)
			i += 4
		}
		o += slice(bits, i, i+48)
		i += 48
		p2 := at(bits, i)
		i++
		o += string(p2)
		if p2 == '1' {
			vec(5)
		}
		o += slice(bits, i, i+1)
		i++
		if i != len(bits) {
			raise("ValueError", "projectile create layout mismatch: %d consumed of %d", i, len(bits))
		}
	}
	return o, info, nil
}
