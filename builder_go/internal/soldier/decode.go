// Package soldier is the soldier update convert (v3_convert_soldiers + soldier_py r201/r202/r210 + p2_rebase): every
// soldier update record of a decoded m-log (recorder_v3.to_v1_rows) -> one 'u' row {t, id, chunks, end, refs} with
// p2 rebased origin -> 0 and p8's ovec(8) re-encoded origin-zero, in log order. The rows are what v3_cl04c reads
// from soldier_rows.pkl; Go keeps them in memory (no chunk files, no resume, no spawn pool).
//
// Python's convert catches only ValueError per row (the row is dropped and counted as fail:<msg>); any other
// exception ended the convert process. Go keeps that split: a dropped row is a stat, every other Python exception is
// an error of Convert (a *capture.PyError with the Python class name).
//
// SOLDIER_CREATE_STUB_6D68B0 is not consulted here: soldier_py reads the p6 tail 6D68B0 natively, unconditionally
// (the stub switch lives in the wire harness of the cl04c create slicing, wire_py_small).
package soldier

import (
	"fmt"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/capture"
)

const (
	// NProp is soldier_py.NPROP.
	NProp = 17
	// GhostBits is soldier_py.GHOST_BITS: the ghost-reference width when the row's config has no create bookmark.
	GhostBits = 13
	// padBits is soldier_py.PAD: the harness zero-fills 512 bits past the payload.
	padBits = 512
)

// dropErr is a Python ValueError inside row_to_chunks_meta: convert() drops the row as 'fail:' + str(e)[:50].
type dropErr struct{ msg string }

func (e *dropErr) Error() string { return e.msg }

var errEmptyInt = &dropErr{"invalid literal for int() with base 2: ''"}

func pyErr(kind, msg string) error { return &capture.PyError{Kind: kind, Msg: msg} }

// stream is the bit string bin(int.from_bytes(b'\x01' + payload, 'big'))[3:] + PAD: n8 payload bits, then zeros up
// to n. Reads past n follow Python's slice rules (clipped; an empty int() slice is a ValueError).
type stream struct {
	p     []byte
	n8, n int64
	full  *bits.Bits // payload + PAD as Bits, built on the first body slice
}

func newStream(p []byte) *stream {
	return &stream{p: p, n8: 8 * int64(len(p)), n: 8*int64(len(p)) + padBits}
}

func (s *stream) bit(k int64) uint64 {
	if k >= s.n8 {
		return 0
	}
	return uint64(s.p[k>>3]>>(7-k&7)) & 1
}

// value is int(b[lo:hi], 2) for hi-lo <= 64 (hi already clipped to n, lo < hi).
func (s *stream) value(lo, hi int64) uint64 {
	var v uint64
	for k := lo; k < hi; k++ {
		v = v<<1 | s.bit(k)
	}
	return v
}

// slice is b[lo:hi] (Python clipping) as Bits.
func (s *stream) slice(lo, hi int64) bits.Bits {
	if s.full == nil {
		z := bits.Concat(bits.FromBytes(s.p), bits.Bits{}.Shl(padBits))
		s.full = &z
	}
	hi = min(hi, s.n)
	if lo >= hi {
		return bits.Bits{}
	}
	return s.full.Slice(int(lo), int(hi))
}

// ctx is soldier_py.ctx_from_row.
type ctx struct {
	table              []int64 // table[:count]
	ref, gb, lm, ibits int64
	tpad               []int64 // _R.T: table padded to 64 entries + one trailing 0 (T[-1] = 0)
}

func bl(x int64) int64 { // _bl: bit_length of a positive value, 0 for x <= 0
	n := int64(0)
	for ; x > 0; x >>= 1 {
		n++
	}
	return n
}

// event is one decode_meta event: an origin vector (ovec) or a ghost-reference read (gref).
type event struct {
	ovec        bool
	a, z        int64 // ovec: start (at the idx field) and end; gref: start, z = width
	idx, c, ibw int64
}

// reader is soldier_py._R.
type reader struct {
	s  *stream
	i  int64
	cx *ctx
	ev []event
}

func (r *reader) u(n int64) (uint64, error) {
	if n == 0 {
		return 0, nil
	}
	if n > 64 {
		return 0, fmt.Errorf("soldier: %d-bit read (> 64) not ported", n)
	}
	i := r.i
	r.i = i + n
	if i >= r.s.n {
		return 0, errEmptyInt
	}
	return r.s.value(i, min(i+n, r.s.n)), nil
}

func (r *reader) flag() (bool, error) { v, err := r.u(1); return v == 1, err }

// t is T[idx - 1] (idx >= 0; T[-1] = 0); past the padded table -> IndexError.
func (r *reader) t(idx int64) (int64, error) {
	if idx == 0 {
		return 0, nil
	}
	if idx-1 >= int64(len(r.cx.tpad)) {
		return 0, pyErr("IndexError", "list index out of range")
	}
	return r.cx.tpad[idx-1], nil
}

func (r *reader) m(k int64) error { // 50B710
	if k < 24 {
		r.i += k
		return nil
	}
	f, err := r.flag()
	if err == nil && !f {
		r.i += 23
	}
	return err
}

func (r *reader) sm(k int64) error { r.i++; return r.m(k - 1) } // 50C140

func (r *reader) ovec(c int64) error { // 50C7F0
	a := r.i
	idx, err := r.u(r.cx.ibits)
	if err != nil {
		return err
	}
	if idx == 0 {
		r.i += 96
	} else {
		t, err := r.t(int64(idx))
		if err != nil {
			return err
		}
		r.i += 3 * (t + c)
	}
	r.ev = append(r.ev, event{ovec: true, a: a, z: r.i, idx: int64(idx), c: c, ibw: r.cx.ibits})
	return nil
}

func (r *reader) gref() { r.ev = append(r.ev, event{a: r.i, z: r.cx.gb}); r.i += r.cx.gb }

func (r *reader) sparse(f int64) error { // 50C990
	w, have := int64(0), false
	x, err := r.flag()
	if err != nil {
		return err
	}
	if x {
		sel, err := r.u(r.cx.ibits)
		if err != nil {
			return err
		}
		if sel == 0 {
			r.i += 96
			return nil
		}
		t, err := r.t(int64(sel))
		if err != nil {
			return err
		}
		w, have = t+f, true
		r.i += w
	}
	for range 2 {
		y, err := r.flag()
		if err != nil {
			return err
		}
		if y {
			if !have {
				sel, err := r.u(r.cx.ibits)
				if err != nil {
					return err
				}
				t, err := r.t(int64(sel))
				if err != nil {
					return err
				}
				w, have = t+f, true
			}
			r.i += w
		}
	}
	return nil
}

func p0(r *reader) error {
	r.i += 3 + 3 + 4
	f, err := r.flag()
	if f {
		r.i += 2
	}
	r.i++
	return err
}

func p1(r *reader) error {
	v, err := r.u(31)
	if err != nil {
		return err
	}
	if v == 0 {
		sel, err := r.u(3)
		if err != nil {
			return err
		}
		if sel == 4 {
			r.gref()
			r.i += 4
		}
		r.i += 96
	}
	f, err := r.flag()
	if f {
		r.i += r.cx.ref // cfg: a config index, not a ghost reference
	}
	return err
}

func p2(r *reader) error {
	r.i += 4
	f, err := r.flag()
	if err != nil {
		return err
	}
	if f {
		if err := r.ovec(7); err != nil {
			return err
		}
	}
	r.i++
	for range 3 {
		if err := r.sparse(5); err != nil {
			return err
		}
	}
	return nil
}

func p3(r *reader) error {
	f, err := r.flag()
	if err != nil {
		return err
	}
	if f {
		v, err := r.u(1)
		if err != nil {
			return err
		}
		r.i += int64(8*v + 8)
	}
	if f, err = r.flag(); err != nil {
		return err
	}
	if f {
		r.i += 4
	}
	v, err := r.u(4)
	if v == 3 {
		r.i += 64
	}
	r.i++
	return err
}

func p4(r *reader) error { v, err := r.u(4); r.i += 28 * int64(v); return err }

func p6(r *reader) error {
	if err := r.sm(12); err != nil {
		return err
	}
	if err := r.sm(12); err != nil {
		return err
	}
	r.i++ // 7F0400
	v, err := r.u(3)
	if err != nil {
		return err
	}
	r.i++ // 8026D0(0)
	if v == 1 || v == 2 {
		r.i += 5
	}
	f, err := r.flag()
	if err != nil {
		return err
	}
	if f {
		if err := r.m(8); err != nil {
			return err
		}
	}
	for range 3 { // 8C0AE2 bit -> 6D68B0(0): A, B (always native here)
		if f, err = r.flag(); err != nil || !f {
			return err
		}
	}
	if f, err = r.flag(); err != nil {
		return err
	}
	if f {
		r.i += 16
	}
	if f, err = r.flag(); err != nil {
		return err
	}
	if f {
		if err := r.sparse(5); err != nil {
			return err
		}
	}
	if f, err = r.flag(); err != nil {
		return err
	}
	if f {
		if err := r.m(24); err != nil {
			return err
		}
	}
	if f, err = r.flag(); f {
		r.i += 3
	}
	return err
}

func p7(r *reader) error {
	if err := r.m(32); err != nil {
		return err
	}
	if err := r.sm(32); err != nil {
		return err
	}
	r.i++
	return nil
}

func p8(r *reader) error {
	f, err := r.flag()
	if err != nil || !f {
		return err
	}
	if err := r.ovec(8); err != nil {
		return err
	}
	r.i += 1 + 31
	return nil
}

func p9(r *reader) error {
	anyp := false
	for range 5 {
		f, err := r.flag()
		if err != nil {
			return err
		}
		if f {
			r.i += 16
			anyp = true
		}
	}
	r.i += 8
	if !anyp {
		return nil
	}
	f, err := r.flag()
	if err != nil || !f {
		return err
	}
	w := bl(r.cx.lm - 1)
	n, err := r.u(w)
	if err != nil {
		return err
	}
	r.i += (int64(n) + 1) * w
	return nil
}

func p11(r *reader) error {
	r.i += 1 + 3
	f, err := r.flag()
	if f {
		r.i += r.cx.ref
	}
	return err
}

func p13(r *reader) error {
	f, err := r.flag()
	if f {
		r.i += 31
	}
	return err
}

func p14(r *reader) error {
	f, err := r.flag()
	if f {
		r.gref()
	}
	return err
}

func skip(n int64) func(*reader) error { return func(r *reader) error { r.i += n; return nil } }

var body = [NProp]func(*reader) error{p0, p1, p2, p3, p4, skip(12), p6, p7, p8, p9, skip(13), p11, skip(1), p13, p14,
	skip(31), skip(1)}

// Chunk is one present property: Body is the chunk body (Len = 'bits', value = 'value').
type Chunk struct {
	Prop int
	Body bits.Bits
}

// Ref is one ghost-reference read: bit offset in the chunk body and width.
type Ref struct{ Off, Bits int64 }

// PropRefs are the ghost-reference reads of one property.
type PropRefs struct {
	Prop int
	Refs []Ref
}

// ovecMeta is a decode_meta ovec entry: offsets relative to the chunk body and the 3 signed ints as floats (the raw
// f32 patterns of idx 0 are never used: row_to_chunks_meta skips idx 0).
type ovecMeta struct {
	off, end, idx, idxw, c int64
	ks                     [3]float64 // float(k) of the signed ints (idx != 0; only kept for p8)
}

// decoded is soldier_py._decode(want_meta=True) restricted to what row_to_chunks_meta uses.
type decoded struct {
	chunks []Chunk
	end    int64
	refs   []PropRefs
	p8ovec []ovecMeta
}

// decodeMeta is soldier_py.decode_meta(bits, pos, ctx).
func decodeMeta(s *stream, pos int64, cx *ctx) (*decoded, error) {
	r := &reader{s: s, i: pos, cx: cx}
	out := &decoded{}
	for p := range NProp {
		i := r.i
		r.i = i + 1
		if i >= s.n {
			return nil, pyErr("IndexError", "string index out of range")
		}
		if s.bit(i) == 0 {
			continue
		}
		r.ev = r.ev[:0]
		if err := body[p](r); err != nil {
			return nil, err
		}
		out.chunks = append(out.chunks, Chunk{p, s.slice(i+1, r.i)})
		var refs []Ref
		for _, e := range r.ev {
			if !e.ovec {
				refs = append(refs, Ref{e.a - i - 1, e.z})
				continue
			}
			om, err := ovecKs(s, e, p == 8)
			if err != nil {
				return nil, err
			}
			if p == 8 {
				om.off, om.end = e.a-i-1, e.z-i-1
				out.p8ovec = append(out.p8ovec, om)
			}
		}
		if refs != nil {
			out.refs = append(out.refs, PropRefs{p, refs})
		}
	}
	out.end = r.i
	return out, nil
}

// ovecKs evaluates the ks of an ovec event like _decode: three int() slices (ValueError when one is empty); the
// values are only computed when keep (p8, idx != 0).
func ovecKs(s *stream, e event, keep bool) (ovecMeta, error) {
	om := ovecMeta{idx: e.idx, idxw: e.ibw, c: e.c}
	w := int64(32)
	if e.idx != 0 {
		w = (e.z - e.a - e.ibw) / 3
	}
	for k := range int64(3) {
		lo := e.a + e.ibw + w*k
		if lo >= s.n || w <= 0 {
			return om, errEmptyInt
		}
		if !keep || e.idx == 0 {
			continue
		}
		f, err := sxFloat(s.slice(lo, lo+w), w)
		if err != nil {
			return om, err
		}
		om.ks[k] = f
	}
	return om, nil
}
