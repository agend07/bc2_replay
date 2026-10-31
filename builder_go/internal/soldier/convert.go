package soldier

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/golden"
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// Row is one converted soldier update row: dict(t, id, chunks, end, refs) of v3_convert_soldiers.convert.
type Row struct {
	T      uint64
	ID     int
	Chunks []Chunk // property order
	End    int64
	Refs   []PropRefs // properties with ghost-reference reads, property order; nil = {}
}

// PyValue is the row dict ({p: dict(value, bits)} chunks, {p: [(off, bits)]} refs).
func (r *Row) PyValue() any {
	o := pyc.NewObject()
	o.Set("t", r.T)
	o.Set("id", r.ID)
	var chunks any = pyc.NewObject() // {} (a dict without keys is a plain JSON object)
	if len(r.Chunks) > 0 {
		ps := make(golden.Pairs, len(r.Chunks))
		for i, c := range r.Chunks {
			v := pyc.NewObject()
			v.Set("value", c.Body)
			v.Set("bits", c.Body.Len())
			ps[i] = golden.Pair{K: c.Prop, V: v}
		}
		chunks = ps
	}
	o.Set("chunks", chunks)
	o.Set("end", r.End)
	var refs any = pyc.NewObject()
	if len(r.Refs) > 0 {
		ps := make(golden.Pairs, len(r.Refs))
		for i, pr := range r.Refs {
			l := make([]any, len(pr.Refs))
			for j, x := range pr.Refs {
				l[j] = []any{x.Off, x.Bits}
			}
			ps[i] = golden.Pair{K: pr.Prop, V: l}
		}
		refs = ps
	}
	o.Set("refs", refs)
	return o
}

// Stats is convert()'s collections.Counter summed over all rows, keys in first-seen order (the TOTAL line).
type Stats struct {
	keys []string
	m    map[string]int
}

func (s *Stats) add(k string, n int) {
	if s.m == nil {
		s.m = map[string]int{}
	}
	if _, ok := s.m[k]; !ok {
		s.keys = append(s.keys, k)
	}
	s.m[k] += n
}

// PyValue is dict(total).
func (s *Stats) PyValue() any {
	o := pyc.NewObject()
	for _, k := range s.keys {
		o.Set(k, s.m[k])
	}
	return o
}

// ------------------------------------------------------------------------------------------------- one row

var p2Table = []int64{1, 3, 5, 7, 9, 11, 13} // p2_rebase.TABLE (C = 7, IDXW = 3)

func structNeed(b []byte, off int) error {
	if len(b) < off+4 {
		return pyErr("struct.error", fmt.Sprintf("unpack_from requires a buffer of at least %d bytes for unpacking 4 "+
			"bytes at offset %d (actual buffer size is %d)", off+4, off, len(b)))
	}
	return nil
}

// ctxFromRow is soldier_py.ctx_from_row with ghost_bits = the config's create-bookmark width, else GhostBits.
func ctxFromRow(c *capture.Config, gb int64) (*ctx, error) {
	if err := structNeed(c.ChildRaw, 4); err != nil {
		return nil, err
	}
	if err := structNeed(c.ChildRaw, 12); err != nil {
		return nil, err
	}
	t := make([]int64, len(c.Tables[0]))
	for i, x := range c.Tables[0] {
		t[i] = int64(x)
	}
	tp := make([]int64, max(64, len(t))+1)
	copy(tp, t)
	return &ctx{table: t, tpad: tp, ibits: bl(int64(len(t))), gb: gb,
		lm: int64(binary.LittleEndian.Uint32(c.ChildRaw[4:])), ref: int64(binary.LittleEndian.Uint32(c.ChildRaw[12:]))}, nil
}

func f32(x float64) (float64, error) {
	y, err := pyc.F32(x)
	if err != nil {
		return 0, pyErr("OverflowError", "float too large to pack with f format")
	}
	return y, nil
}

// sxFloat is float(_sx(int(v), w)): v is the (possibly clipped) slice, w the field width.
func sxFloat(v bits.Bits, w int64) (float64, error) {
	neg := int64(v.Len()) == w && int64(v.BitLen()) == w
	if w <= 63 {
		x, _ := v.Uint64()
		k := int64(x)
		if neg {
			k -= 1 << w
		}
		return float64(k), nil
	}
	k := v.Big()
	if neg {
		k.Sub(k, new(big.Int).Lsh(big.NewInt(1), uint(w)))
	}
	f, _ := new(big.Float).SetInt(k).Float64()
	if math.IsInf(f, 0) {
		return 0, pyErr("OverflowError", "int too large to convert to float")
	}
	return f, nil
}

// xyz is F32(F32(o) + F32(k) * 2**-c) per axis (p2_rebase.decode_vec / soldier_py.ovec_xyz).
func xyz(origin [3]float64, ks [3]float64, c int64) ([3]float64, error) {
	sc := 1.0 / float64(int64(1)<<c)
	var out [3]float64
	for i := range 3 {
		o, err := f32(origin[i])
		if err != nil {
			return out, err
		}
		k, err := f32(ks[i])
		if err != nil {
			return out, err
		}
		m := float64(k * sc) // no FMA fusion
		if out[i], err = f32(o + m); err != nil {
			return out, err
		}
	}
	return out, nil
}

// maxEncodeBits bounds one encoded int (Go only; Python would build the string).
const maxEncodeBits = 1 << 24

// twos is format(k & ((1 << w) - 1), '0%db' % w) for an integral k with -2**(w-1) <= k < 2**(w-1).
func twos(k float64, w int64) (bits.Bits, error) {
	if w <= 64 {
		return bits.FromUint(uint64(int64(k))&(math.MaxUint64>>(64-w)), int(w))
	}
	if w > maxEncodeBits {
		return bits.Bits{}, fmt.Errorf("soldier: %d-bit vector field not ported", w)
	}
	z, _ := new(big.Float).SetFloat64(k).Int(nil)
	if z.Sign() < 0 {
		z.Add(z, new(big.Int).Lsh(big.NewInt(1), uint(w)))
	}
	return bits.FromBig(z, int(w))
}

// encodeVec is p2_rebase.encode_vec(xyz, origin 0) / soldier_py.encode_ovec(xyz, table, C, idxw): the smallest idx
// whose T[idx-1]+C width fits all three rounded ints, else idx 0 + 3 raw f32. Returns the bits, the idx and the ints.
func encodeVec(x [3]float64, table []int64, c, idxw int64) (bits.Bits, int64, [3]float64, error) {
	var ks [3]float64
	for i := range 3 {
		fx, err := f32(x[i])
		if err != nil {
			return bits.Bits{}, 0, ks, err
		}
		v := fx * float64(int64(1)<<c)
		if math.IsInf(v, 0) {
			return bits.Bits{}, 0, ks, pyErr("OverflowError", "cannot convert float infinity to integer")
		}
		if math.IsNaN(v) {
			return bits.Bits{}, 0, ks, &dropErr{"cannot convert float NaN to integer"}
		}
		ks[i] = math.RoundToEven(v)
	}
	for i, t := range table {
		w := t + c
		lim := math.Ldexp(1, int(min(w-1, 2000)))
		if !(-lim <= ks[0] && ks[0] < lim && -lim <= ks[1] && ks[1] < lim && -lim <= ks[2] && ks[2] < lim) {
			continue
		}
		parts := []bits.Bits{}
		ib, err := bits.Parse(bits.FormatBin(uint64(i+1), int(idxw)))
		if err != nil {
			return bits.Bits{}, 0, ks, err
		}
		parts = append(parts, ib)
		for _, k := range ks {
			b, err := twos(k, w)
			if err != nil {
				return bits.Bits{}, 0, ks, err
			}
			parts = append(parts, b)
		}
		return bits.Concat(parts...), int64(i + 1), ks, nil
	}
	parts := []bits.Bits{bits.Bits{}.Shl(int(idxw))}
	for i := range 3 {
		u, _ := pyc.PackF32(x[i]) // x is a float32 value: never overflows
		b, _ := bits.FromUint(uint64(u), 32)
		parts = append(parts, b)
	}
	return bits.Concat(parts...), 0, ks, nil
}

// p2Rebase is p2_rebase.rebase(body, origin, (0, 0, 0)).
func p2Rebase(body bits.Bits, origin [3]float64) (bits.Bits, error) {
	n := body.Len()
	if n < 5 {
		return body, pyErr("IndexError", "string index out of range")
	}
	if b, _ := body.Slice(4, 5).Uint64(); b != 1 {
		return body, nil
	}
	is := body.Slice(5, 8)
	if is.Len() == 0 {
		return body, errEmptyInt
	}
	idx, _ := is.Uint64()
	if idx == 0 {
		return body, nil
	}
	w := p2Table[idx-1] + 7
	var ks [3]float64
	for k := range int64(3) {
		s := body.Slice(int(8+w*k), int(8+w*(k+1)))
		if s.Len() == 0 {
			return body, errEmptyInt
		}
		ks[k], _ = sxFloat(s, w)
	}
	x, err := xyz(origin, ks, 7)
	if err != nil {
		return body, err
	}
	enc, _, _, err := encodeVec(x, p2Table, 7, 3)
	if err != nil {
		return body, err
	}
	return bits.Concat(body.Slice(0, 5), enc, body.Slice(int(8+3*w), n)), nil
}

// rowResult is one row's outcome: the row (ok) and the stat keys it counted, in order.
type rowResult struct {
	row   Row
	ok    bool
	stats []string
	err   error
}

// convertOne is v3_convert_soldiers.convert for one (g, v1 row): soldier_py.row_to_chunks_meta + the per-row checks.
func convertOne(s *stream, v *capture.V1Row, cx *ctx) rowResult {
	var res rowResult
	out, st, err := rowToChunksMeta(s, v, cx)
	if err != nil {
		var de *dropErr
		if errors.As(err, &de) {
			m := de.msg
			if len(m) > 50 {
				m = m[:50]
			}
			res.stats = append(res.stats, "fail:"+m)
			return res
		}
		res.err = err
		return res
	}
	g := v.G
	if g.HasEndHint && out.End > g.EndHint+capture.GAP {
		res.stats = append(res.stats, "overrun_dropped")
		return res
	}
	if g.HasEndHint && g.EndHint+capture.GAP != out.End {
		res.stats = append(res.stats, "gap_unrecorded_between")
	}
	for _, c := range out.Chunks {
		if c.Prop != 2 || c.Body.Len() < 5 {
			continue
		}
		if b, _ := c.Body.Slice(4, 5).Uint64(); b == 1 {
			if i := c.Body.Slice(5, 8); i.Len() > 0 && i.IsZero() {
				res.stats = append(res.stats, "p2_idx0_out")
			}
		}
	}
	res.stats = append(res.stats, st...)
	if out.Refs != nil {
		res.stats = append(res.stats, "refs_rows")
	}
	res.stats = append(res.stats, "ok")
	res.row, res.ok = out, true
	return res
}

// rowToChunksMeta is soldier_py.row_to_chunks_meta (marker is always 1 here) -> the row and the p8 stat keys.
// The rebased p8 body is not read back by the decoder: the read-back's only effects are the p8 stats (computed
// directly) and an assertion that holds by construction (same table, same idx width).
func rowToChunksMeta(s *stream, v *capture.V1Row, cx *ctx) (Row, []string, error) {
	row := Row{T: v.G.T, ID: int(v.G.ID)}
	d, err := decodeMeta(s, int64(v.G.Cursor), cx)
	if err != nil {
		return row, nil, err
	}
	if d.end > int64(v.P.Limit) {
		return row, nil, &dropErr{fmt.Sprintf("framing failed: end %d > limit %d", d.end, v.P.Limit)}
	}
	var origin [3]float64
	for i, b := range v.G.OriginBits {
		origin[i] = pyc.UnpackF32(b)
	}
	at := map[int]int{}
	for i, c := range d.chunks {
		at[c.Prop] = i
	}
	if i, ok := at[2]; ok {
		if d.chunks[i].Body, err = p2Rebase(d.chunks[i].Body, origin); err != nil {
			return row, nil, err
		}
	}
	var st []string
	for _, ov := range d.p8ovec {
		if ov.idx == 0 {
			continue // raw floats: origin not applied
		}
		wire, err := xyz(origin, ov.ks, ov.c)
		if err != nil {
			return row, nil, err
		}
		b8 := d.chunks[at[8]].Body
		enc, idxTo, ks, err := encodeVec(wire, cx.table, ov.c, ov.idxw)
		if err != nil {
			return row, nil, err
		}
		d.chunks[at[8]].Body = bits.Concat(b8.Slice(0, int(ov.off)), enc, b8.Slice(int(ov.end), b8.Len()))
		demo := wire // idx 0: the raw f32 read back = wire
		if idxTo != 0 {
			if demo, err = xyz([3]float64{}, ks, ov.c); err != nil {
				return row, nil, err
			}
		}
		st = append(st, "p8_rebased", fmt.Sprintf("p8_idx%d->%d", ov.idx, idxTo))
		if max(math.Abs(wire[0]-demo[0]), math.Abs(wire[1]-demo[1]), math.Abs(wire[2]-demo[2])) > 1.0/256 {
			st = append(st, "p8_inexact")
		}
	}
	row.Chunks, row.End, row.Refs = d.chunks, d.end, d.refs
	return row, st, nil
}

// ------------------------------------------------------------------------------------------------- convert

// Convert is v3_convert_soldiers.main on a decoded m-log (capture.Load: ghosts carry end_hint): the soldier update
// rows in log order and the TOTAL stats. workers <= 0 = GOMAXPROCS; the output does not depend on it. An error is a
// Python exception that ended the convert (the first one in row order).
func Convert(d *capture.Decoded, workers int) ([]Row, *Stats, error) {
	if !d.Hinted {
		return nil, nil, errors.New("soldier: decoded log without end_hint (use capture.Load)")
	}
	widths, err := wire.InferIDWidths(capture.WireLog(d))
	if err != nil {
		return nil, nil, err
	}
	v1 := capture.ToV1Rows(d)
	res := make([]rowResult, len(v1))
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	const block = 1024
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(workers, (len(v1)+block-1)/block) {
		wg.Go(func() {
			ctxs := map[*capture.Config]*ctx{}
			var s *stream
			var sp *capture.Packet
			for {
				lo := int(next.Add(block)) - block
				if lo >= len(v1) {
					return
				}
				for i := lo; i < min(lo+block, len(v1)); i++ {
					v := &v1[i]
					if v.P != sp {
						s, sp = newStream(v.P.Payload), v.P
					}
					gb, ok := widths[v.G.Config]
					if !ok {
						gb = GhostBits
					}
					cx, ok := ctxs[v.C] // one config = one table, one ghost-id width
					if !ok {
						var e error
						if cx, e = ctxFromRow(v.C, gb); e != nil {
							res[i].err = e
							continue
						}
						ctxs[v.C] = cx
					}
					res[i] = convertOne(s, v, cx)
				}
			}
		})
	}
	wg.Wait()
	rows := make([]Row, 0, len(v1))
	st := &Stats{}
	for i := range res {
		if res[i].err != nil {
			return nil, nil, res[i].err
		}
		for _, k := range res[i].stats {
			st.add(k, 1)
		}
		if res[i].ok {
			rows = append(rows, res[i].row)
		}
	}
	return rows, st, nil
}
