package wire

import (
	"fmt"
	mbits "math/bits"

	"bc2replay/builder/internal/pyc"
)

// ---------------------------------------------------------------------------------------------------- errors

// UcFail reproduces a Unicorn failure: RunReader returns Out.Err = Err with pos/end/flags as of the failure.
type UcFail struct{ Err string }

func (e UcFail) Error() string { return e.Err }

// ErrUnported is wire_py_core's NotImplementedError: the reader VA (0 when not reader-specific) has no port for the
// path the input takes (never exercised by the oracle corpus). A build must fail on it.
type ErrUnported struct {
	VA     uint32
	Reason string
}

func (e *ErrUnported) Error() string {
	if e.VA == 0 {
		return "wire: not ported: " + e.Reason
	}
	return fmt.Sprintf("wire: reader %#x not ported: %s", e.VA, e.Reason)
}

// ReaderError is any other Python exception a reader would raise (OverflowError from struct.pack('<f'), IndexError
// on the quantisation table, ValueError on an invalid captured state).
type ReaderError struct{ Msg string }

func (e *ReaderError) Error() string { return "wire: " + e.Msg }

// ---------------------------------------------------------------------------------------------------- run input

// State is the captured BitStream state (cursor, cache, dword_offset, flags).
type State struct{ Cursor, Cache, DwordOffset, Flags int64 }

// Inp is everything a run reads (wire_py_core.Inp: the Harness attributes + buffer/state). Plain = the AC38 Harness
// (kind 'plain'), otherwise WireHarness. Zero values are not the Python defaults: build inputs with NewInp.
// Not carried (no reader reads them; harness-only in Python): heap, skips, alloc_fixture.
type Inp struct {
	Plain         bool
	Entry         uint32
	Buf           []byte
	NBits         int64
	State         *State // nil = fresh stream (cursor 0)
	Generic       bool   // 50BDE0 hook records property boundaries
	SecVT         uint32
	Origin        [3]float64
	QN            int64
	QT            []int64 // nil = None
	ListMax       int64
	RefBits       int64
	CfgChild      []byte // nil = None
	SecFields     map[uint32]int64
	GhostBits     int64
	ResolveRefs   bool
	RefReal       bool
	PlayerFixture bool
	Stubs         StubSet
}

// NewInp returns an input with wire_py_core.Inp's defaults (kind 'wire').
func NewInp() *Inp {
	return &Inp{Generic: true, ListMax: 64, RefBits: 6, SecFields: map[uint32]int64{}, GhostBits: GhostBits,
		ResolveRefs: true, RefReal: true, Stubs: StubSet{}}
}

// ---------------------------------------------------------------------------------------------------- run output

// Ref is one C1E200 reference read {start, end, target}.
type Ref struct{ Start, End, Target uint32 }

// Ovec is one 50FE00 origin vector {start, end, xyz} (xyz = float32 values).
type Ovec struct {
	Start, End uint32
	XYZ        [3]float64
}

// Out is the Harness.run contract (wire_oracle._out). Refs/Ovs are nil in the plain kind (Python None).
type Out struct {
	Pos   []uint32
	End   uint32
	Flags uint32
	Err   string // "" = None
	Calls []uint32
	Refs  []Ref
	Ovs   []Ovec
	Q     []uint32 // q50c650 entry cursors
}

// ---------------------------------------------------------------------------------------------------- one run

type refObj uint8

const (
	refNone       refObj = iota // id 0
	refFake                     // WireHarness resolved: zeroed fake ghost (C1E21E hook)
	refUnresolved               // C1BBC0 stub called
)

// Rd is one reader run over inp (wire_py_core.Rd). Primitive names follow soldier_py._R + the native VAs.
type Rd struct {
	inp        *Inp
	buf        []byte
	dend       int // len(buf) + 16 zero bytes, rounded up to a dword (the harness' padded copy)
	nbits      int64
	cur, cache uint32
	ptr        int
	flags      uint32
	entry      uint32
	wire       bool
	t          []int64
	ib         int64
	out        Out
}

func newRd(inp *Inp) (*Rd, error) {
	n := len(inp.Buf) + 16
	r := &Rd{inp: inp, buf: inp.Buf, dend: n + (-n)&3, nbits: inp.NBits, entry: inp.Entry, wire: !inp.Plain}
	if st := inp.State; st == nil {
		r.cache, r.ptr = r.word(0), 4
	} else {
		if !(0 <= st.Cursor && st.Cursor <= inp.NBits && inp.NBits <= int64(len(inp.Buf))*8 && 0 <= st.DwordOffset &&
			st.DwordOffset <= int64(len(inp.Buf)) && st.DwordOffset%4 == 0) {
			return nil, &ReaderError{"invalid captured state"} // Harness.run validation, same message
		}
		r.cur, r.cache, r.ptr, r.flags = uint32(st.Cursor), uint32(st.Cache), int(st.DwordOffset), uint32(st.Flags)
	}
	// QTBL: entries past qt are 0; T[-1] (the dword before QTBL) = 0 (soldier_py).
	r.t = make([]int64, max(len(inp.QT), 65)+1)
	copy(r.t, inp.QT)
	if inp.QN > 0 {
		r.ib = int64(mbits.Len64(uint64(inp.QN)))
	}
	if r.wire {
		r.out.Refs, r.out.Ovs = []Ref{}, []Ovec{}
	}
	return r, nil
}

// word is the big-endian dword at byte offset p of the zero-padded buffer.
func (r *Rd) word(p int) uint32 {
	var w uint32
	for i := range 4 {
		w <<= 8
		if p+i < len(r.buf) {
			w |= uint32(r.buf[p+i])
		}
	}
	return w
}

// tAt is T[i] with Python indexing (i = -1 is the trailing 0; past the end is an IndexError).
func (r *Rd) tAt(i int64) int64 {
	if i == -1 {
		return 0
	}
	if i < 0 || i >= int64(len(r.t)) {
		panic(&ReaderError{"IndexError: list index out of range (quantisation table)"})
	}
	return r.t[i]
}

// u is 506660 BitStream_ReadBits (reverser confirmed; bytes 506660..5066FD) with the exact cache / next-dword model:
// n bits MSB-first. Past nbits the cursor still advances, the flags byte = 1 and the value is 0 (also for every
// later read). n follows Python int semantics (any width; masks as in the native code).
func (r *Rd) u(n int64) uint32 {
	c := int64(r.cur & 31)
	nw := uint32(int64(r.cur) + n)
	r.cur = nw
	if int64(nw) > r.nbits {
		r.flags = r.flags&^0xff | 1
		return 0
	}
	ebx := c + n - 32
	if ebx < 0 {
		esi := r.cache >> ((32 - n) & 31)
		r.cache <<= n & 31
		if n == 0 {
			return 0
		}
		return esi
	}
	esi := (r.cache >> c) << (ebx & 31)
	if r.ptr < r.dend {
		r.cache = r.word(r.ptr)
		r.ptr += 4
	}
	edi := r.cache
	esi |= (edi >> ((32 - ebx) & 31)) & (1<<(ebx&31) - 1)
	r.cache = edi << (ebx & 31)
	return esi
}

// pres is 50BDE0 BitStream_ReadPropertyBit (confirmed): 1 bit; generic harness: the cursor BEFORE the bit is a
// property boundary (Harness.on_code hook at 50BDE0) -> Out.Pos.
func (r *Rd) pres() bool {
	if r.inp.Generic {
		r.out.Pos = append(r.out.Pos, r.cur)
	}
	return r.u(1)&1 != 0
}

// b is 50BE90 ReadBit (soldier_py 'b'): 1 bit.
func (r *Rd) b() bool { return r.u(1)&1 != 0 }

// rng is 50BF30 ReadRange (soldier_py 'r'): bitlen(hi - lo) bits (0 when hi == lo) + lo.
func (r *Rd) rng(lo, hi int64) uint32 {
	return uint32(int64(r.u(int64(mbits.Len32(uint32(hi-lo))))) + lo)
}

// f31 is 50C0B0 BitStream_ReadFloat31 (confirmed): 31 bits as a float (value unused by every reader).
func (r *Rd) f31() { r.u(31) }

// f32 is 50C090 (soldier_py 'f32'): 32 bits as a float (value unused by every reader).
func (r *Rd) f32() { r.u(32) }

// m is 50B710 BitStream_ReadScaledUnit (confirmed; 50C110 / 50C120 jump here): k < 24: k bits; else 1 flag bit,
// flag 0 -> 23 more bits.
func (r *Rd) m(k int64) {
	if k < 24 {
		r.u(k)
	} else if r.u(1) == 0 {
		r.u(23)
	}
}

// s is 50C140 (soldier_py 's'): 1 sign bit + m(k - 1).
func (r *Rd) s(k int64) { r.u(1); r.m(k - 1) }

// raw3 is 50B8D0 / 50C3E0 BitStream_ReadRawVec3 (probable): 3 x 32 raw bits -> 3 floats.
func (r *Rd) raw3() [3]float64 {
	var v [3]float64
	for i := range v {
		v[i] = pyc.UnpackF32(r.u(32))
	}
	return v
}

// f32of is soldier_py._f32 (struct '<f' round trip); a finite value that rounds to infinity is Python's OverflowError.
func f32of(x float64) float64 {
	y, err := pyc.F32(x)
	if err != nil {
		panic(&ReaderError{"OverflowError: float too large to pack with f format"})
	}
	return y
}

// sx is soldier_py._sx: v as a w-bit two's complement value.
func sx(v uint32, w int64) int64 {
	if w-1 < 32 && v>>(w-1) != 0 {
		return int64(v) - 1<<w
	}
	return int64(v)
}

// ovec is 50C7F0 BitStream_ReadVec3RelBase (confirmed; bytes 50C81B..50C950): idx = bitlen(qn) bits; idx 0 -> raw3
// (base not applied); else 3 x signed (T[idx-1] + C) bits, xyz = f32(f32(base) + f32(k) * 2^-C).
func (r *Rd) ovec(C int64, base [3]float64) [3]float64 {
	idx := r.u(r.ib)
	if idx == 0 {
		return r.raw3()
	}
	w := (r.tAt(int64(idx)-1) + C) & m32
	sc := 1.0 / float64(int64(1)<<C)
	var out [3]float64
	for i, o := range base {
		v := r.u(w)
		k := 0.0
		if w != 0 {
			k = f32of(float64(sx(v, w)))
		}
		out[i] = f32of(f32of(o) + float64(k*sc))
	}
	return out
}

// originVec is 50FE00 BitStream_ReadOriginRelVec3 (confirmed): ovec(C) relative to the stream origin. WireHarness
// records {start, end, xyz} (hooks 50FE00 / 50FE4B) -> Out.Ovs.
func (r *Rd) originVec(C int64) [3]float64 {
	start := r.cur
	xyz := r.ovec(C, r.inp.Origin)
	if r.wire {
		r.out.Ovs = append(r.out.Ovs, Ovec{start, r.cur, xyz})
	}
	return xyz
}

// sparse is 50C990 BitStream_ReadSparseVectorWithBase (confirmed, r201 correction): x: 1 [+ sel(ib); sel 0 -> raw3,
// stop; else T[sel-1]+F]; y, z: 1 [+ sel if no earlier axis present] + T[sel-1]+F (no sel-0 escape: T[-1] = 0).
func (r *Rd) sparse(F int64) {
	var w int64
	have := false
	if r.u(1) != 0 {
		sel := r.u(r.ib)
		if sel == 0 {
			r.raw3()
			return
		}
		w, have = r.tAt(int64(sel)-1)+F, true
		r.u(w)
	}
	for range 2 {
		if r.u(1) != 0 {
			if !have {
				w, have = r.tAt(int64(r.u(r.ib))-1)+F, true
			}
			r.u(w)
		}
	}
}

// gref is C1E200 ClientGhostManager_ReadGhostReference (confirmed): ghost_bits bits, 16-bit id; WireHarness records
// {start, end, target}. -> (target, obj). C4_REF_DROP drop: reader 8F4890 + nonzero id -> UcFail(ERR_BUDGET), after
// the C1BBC0 stub on the unresolved path (r277 order: Unicorn calls ['c1bbc0'] then stops).
func (r *Rd) gref() (uint32, refObj) {
	if !r.inp.RefReal {
		panic(&ErrUnported{r.entry, "ghost reference without ref_real (not on the default path)"})
	}
	start := r.cur
	v := r.u(r.inp.GhostBits) & 0xffff
	if r.wire {
		r.out.Refs = append(r.out.Refs, Ref{start, r.cur, v})
	}
	if v == 0 {
		return 0, refNone
	}
	drop := r.entry == 0x8f4890
	if r.wire && r.inp.ResolveRefs {
		if drop {
			panic(UcFail{ErrBudget})
		}
		return v, refFake
	}
	r.stub(0xc1bbc0)
	if drop {
		panic(UcFail{ErrBudget})
	}
	return v, refUnresolved
}

// cfg is the config-width index (7FF060 singleton +8 -> +0xC = ref_bits, raw 50BF20): not a ghost reference.
func (r *Rd) cfg() uint32 { return r.u(r.inp.RefBits) }

// qunit is 50C650 BitStream_ReadQuantizedUnitVec (probable; bytes 50C67B..50C7CA, r274 §5): ONE n-bit read; code >=
// 2^n - 2 -> pole vector, else QTAB division + x87 (no further stream reads). Records the entry cursor (q50c650).
// Always (old-Unicorn x87 quirk, AC38_DROP_X87=drop): plain Harness + non-pole -> UcFail(ERR_EXCEPTION) with the cursor after the read.
func (r *Rd) qunit(n int64) {
	r.out.Q = append(r.out.Q, r.cur)
	v := r.u(n)
	pole := v >= uint32(int64(1)<<(n&31)-2)
	if !pole && !r.wire {
		panic(UcFail{ErrException})
	}
}

// quat is 50CBB0 BitStream_ReadQuaternionN_Thunk -> 50BBE0 NetworkReader_ReadQuaternion (both confirmed; bytes
// 50BBE0..50BC6B): w sign 1, then x/y/z each sign 1 + m(N-1). Wire kind only (sqrt via the C521BC shim).
func (r *Rd) quat(N int64) {
	if !r.wire {
		panic(&ErrUnported{r.entry, "50BBE0 in the plain Harness: _CIsqrt thunk not shimmed"})
	}
	r.u(1)
	for range 3 {
		r.u(1)
		r.m(N - 1)
	}
}

// quatN is 50CBD0 BitStream_ReadQuaternionN (confirmed; bytes 50CBD0..50CD56): N >= 25 -> 50BBE0 (= quat); N < 25 ->
// inline 50CC2B..50CCE9: w sign u(1), then x/y/z each present u(1) [+ u(N)]. Wire kind only.
func (r *Rd) quatN(N int64) {
	if N >= 25 {
		r.quat(N)
		return
	}
	if !r.wire {
		panic(&ErrUnported{r.entry, "50CBD0 in the plain Harness: _CIsqrt thunk not shimmed"})
	}
	r.u(1)
	for range 3 {
		if r.u(1)&1 != 0 {
			r.u(N)
		}
	}
}

// physP3 is 78E3D0 PhysicsSection_ReadP3 (confirmed; bytes 78E3D0..78E463): b(); set -> 50C7F0 ovec(C=3, base) +
// 50C650 qunit(6) + u(7) + u(7) + 50C120 m(4). Used by 8EBCE0 p2, 8F0900 p1, 8F1B20 p2.
func (r *Rd) physP3(base [3]float64) {
	if r.b() {
		r.ovec(3, base)
		r.qunit(6)
		r.u(7)
		r.u(7)
		r.m(4)
	}
}

// string514390 is 514390 BitStream_ReadString (bytes 514390..5143FB): u(10) length n, n x u(8), then the EASTL
// assign 4245B0 (player-fixture stub). Callers stub their own second 4245B0 assign.
func (r *Rd) string514390() {
	n := r.u(10)
	for range n {
		r.u(8)
	}
	r.stub(0x4245b0)
}

// stub reports a call to va: true when the harness stubs it (call recorded; the reader then skips the callee's
// effects), false when Unicorn would run it natively. WireHarness answers 89F6B0 in its own hook (not recorded).
func (r *Rd) stub(va uint32) bool {
	if _, ok := r.inp.Stubs[va]; !ok {
		return false
	}
	if !(r.wire && va == 0x89f6b0) {
		r.out.Calls = append(r.out.Calls, va)
	}
	return true
}

// tail is the common reader epilogue `7FF060() -> [cfg+0x40] -> C118C0 -> apply(...)`: two stub calls; the native
// apply (8E5A30 / 8E7E70 / 8EF310 / 8E7D20 / ...) has no stream argument.
func (r *Rd) tail() { r.stub(0x7ff060); r.stub(0xc118c0) }

// unported raises ErrUnported for the current reader.
func (r *Rd) unported(format string, a ...any) {
	panic(&ErrUnported{r.entry, fmt.Sprintf(format, a...)})
}

// ---------------------------------------------------------------------------------------------------- dispatch

// IsPorted reports whether va has a Go reader.
func IsPorted(va uint32) bool { return Readers[va] != nil }

// RunReader is one emulation (wire_py_core.run_reader): UcFail -> Out.Err; ErrUnported / ReaderError -> error.
func RunReader(inp *Inp) (out Out, err error) {
	fn := Readers[inp.Entry]
	if fn == nil {
		return Out{}, &ErrUnported{inp.Entry, "unknown reader"}
	}
	r, err := newRd(inp)
	if err != nil {
		return Out{}, err
	}
	defer func() {
		switch e := recover().(type) {
		case nil:
		case UcFail:
			r.out.End, r.out.Flags, r.out.Err = r.cur, r.flags, e.Err
			out, err = r.out, nil
		case *ErrUnported:
			out, err = Out{}, e
		case *ReaderError:
			out, err = Out{}, e
		default:
			panic(e)
		}
	}()
	fn(r)
	r.out.End, r.out.Flags = r.cur, r.flags
	return r.out, nil
}
