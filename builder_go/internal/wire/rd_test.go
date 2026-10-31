package wire

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"bc2replay/builder/internal/bits"
)

// Synthetic vectors only (no capture data): the corpus exam is corpus_test.go.

func bitBuf(t *testing.T, s string) ([]byte, int64) {
	t.Helper()
	s = strings.ReplaceAll(s, " ", "")
	b, err := bits.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), int64(len(s))
}

func rdOf(t *testing.T, inp *Inp) *Rd {
	t.Helper()
	r, err := newRd(inp)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// naive MSB-first read of buf bits [pos, pos+n).
func naive(buf []byte, pos, n int) uint32 {
	var v uint32
	for k := pos; k < pos+n; k++ {
		v = v<<1 | uint32(buf[k/8]>>(7-k%8)&1)
	}
	return v
}

// The 506660 model equals a plain MSB-first reader for every in-bounds read, from a fresh stream and from a captured
// state (cursor, cache = dword at cursor shifted, next dword) as run_section builds it.
func TestReadBitsModel(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := range 400 {
		buf := make([]byte, 4+rng.IntN(60))
		for i := range buf {
			buf[i] = byte(rng.Uint32())
		}
		nbits := int64(len(buf) * 8)
		inp := NewInp()
		inp.Buf, inp.NBits = buf, nbits
		pos := 0
		if trial%2 == 1 {
			pos = rng.IntN(len(buf) * 4)
			wd := pos / 32
			cache := naive(append(buf, 0, 0, 0, 0), wd*32, 32) << (pos % 32)
			inp.State = &State{int64(pos), int64(cache), int64(wd*4 + 4), 0}
			if inp.State.DwordOffset > int64(len(buf)) {
				continue
			}
		}
		r := rdOf(t, inp)
		for {
			n := rng.IntN(33)
			if pos+n > int(nbits) {
				break
			}
			if got, want := r.u(int64(n)), naive(buf, pos, n); got != want {
				t.Fatalf("trial %d pos %d n %d: got %#x want %#x", trial, pos, n, got, want)
			}
			pos += n
			if r.cur != uint32(pos) || r.flags != 0 {
				t.Fatalf("cursor %d flags %d, want %d 0", r.cur, r.flags, pos)
			}
		}
	}
}

// Past nbits the cursor still advances, the flags byte becomes 1 and every later read returns 0.
func TestReadPastEnd(t *testing.T) {
	buf, _ := bitBuf(t, "1111 1111 1111 1111 1111 1111 1111 1111")
	inp := NewInp()
	inp.Buf, inp.NBits = buf, 10
	r := rdOf(t, inp)
	if v := r.u(8); v != 0xff {
		t.Fatalf("first read %#x", v)
	}
	if v := r.u(8); v != 0 || r.cur != 16 || r.flags != 1 {
		t.Fatalf("past end: v %#x cur %d flags %d", v, r.cur, r.flags)
	}
	if v := r.u(0); v != 0 || r.cur != 16 {
		t.Fatalf("u(0): v %#x cur %d", v, r.cur)
	}
}

// sparse: y/z with sel 0 take T[-1] = 0 (no raw escape): width = F.
func TestSparseSelZero(t *testing.T) {
	// qn 1 -> ib 1. x absent (0); y present (1) + sel 0 (0) + F=3 bits (101); z absent (0).
	buf, n := bitBuf(t, "0 1 0 101 0 1")
	inp := NewInp()
	inp.Buf, inp.NBits, inp.QN, inp.QT = buf, n, 1, []int64{7}
	r := rdOf(t, inp)
	r.sparse(3)
	if r.cur != 7 {
		t.Fatalf("cursor %d, want 7", r.cur)
	}
}

func TestRegistry(t *testing.T) {
	for _, va := range Known() {
		if !IsPorted(va) {
			t.Errorf("%#x not ported", va)
		}
	}
	if len(Readers) != len(Known()) {
		t.Errorf("Readers %d, Known %d", len(Readers), len(Known()))
	}
	inp := NewInp()
	inp.Entry = 0x12345
	var un *ErrUnported
	if _, err := RunReader(inp); !errors.As(err, &un) || un.VA != 0x12345 {
		t.Fatalf("unknown VA: %v", err)
	}
	if _, err := SectionReader(0x1); !errors.As(err, &un) {
		t.Fatalf("unknown vt: %v", err)
	}
}

// AC38 (plain kind): a non-pole 50C650 code fails ERR_EXCEPTION right after the 6-bit read (old-Unicorn x87 quirk).
func TestAC38DropX87(t *testing.T) {
	// p0 absent, p1 absent, p2 present: 78E3D0 b=1, ovec idx (ib 3) = 000 -> raw3 96 bits, qunit 6 bits = 000001
	// (non-pole), u7 u7 m4; p3 absent.
	s := "0 0 1 1 000" + strings.Repeat("0", 96) + "000001" + "0000000" + "0000000" + "0000" + "0"
	buf, n := bitBuf(t, s)
	inp := NewInp()
	inp.Plain, inp.Entry, inp.Buf, inp.NBits, inp.QN, inp.QT = true, 0x8f1b20, buf, n, 7, []int64{1, 3, 5, 7, 9, 11, 13}
	inp.Stubs = PlainStubs()
	o, err := RunReader(inp)
	if err != nil || o.Err != ErrException || o.End != 3+1+3+96+6 || o.Refs != nil || o.Ovs != nil {
		t.Fatalf("drop: %+v %v", o, err)
	}
}

// C4 8F4890: a nonzero ghost reference fails ERR_BUDGET (unresolved: after the C1BBC0 stub call).
func TestC4RefDrop(t *testing.T) {
	// p0 absent; p1 present, attached 1, ref 13 bits = 5, b 0, raw3 96, quat(25) = w 1 + 3 x (sign 1 + m(24) flag 1
	// = no mantissa); p2..p4 absent.
	s := "0 1 1 0000000000101 0" + strings.Repeat("0", 96) + "0 11 11 11" + "0 0 0"
	buf, n := bitBuf(t, s)
	inp := NewInp()
	inp.Entry, inp.Buf, inp.NBits, inp.Stubs = 0x8f4890, buf, n, WireStubs(false)
	o, err := RunReader(inp)
	if err != nil || o.Err != ErrBudget || len(o.Refs) != 1 || o.Refs[0].Target != 5 || len(o.Calls) != 0 {
		t.Fatalf("resolved drop: %+v %v", o, err)
	}
	inp.ResolveRefs = false
	o, _ = RunReader(inp)
	if o.Err != ErrBudget || len(o.Calls) != 1 || o.Calls[0] != 0xc1bbc0 {
		t.Fatalf("unresolved drop: %+v", o)
	}
}

// The soldier reader 8C07E0 answers 6D68B0 with a stub (call recorded), like the old Unicorn harness.
func TestSoldier6D68B0(t *testing.T) {
	inp := NewInp()
	inp.Entry, inp.Stubs = 0x8c07e0, WireStubs(false)
	r := rdOf(t, inp)
	if !r.stub(0x6d68b0) || len(r.out.Calls) != 1 {
		t.Fatal("6D68B0 not stubbed")
	}
}

// 89F6B0 is answered by the WireHarness hook without a recorded call; the plain kind records it.
func TestStub89F6B0(t *testing.T) {
	inp := NewInp()
	inp.Stubs = WireStubs(false)
	r := rdOf(t, inp)
	if !r.stub(0x89f6b0) || len(r.out.Calls) != 0 {
		t.Fatal("wire: 89F6B0 recorded")
	}
	inp.Plain = true
	r = rdOf(t, inp)
	if !r.stub(0x89f6b0) || len(r.out.Calls) != 1 {
		t.Fatal("plain: 89F6B0 not recorded")
	}
}

// originVec: idx 1 of qt [1] -> 3 x (1 + 5) signed bits, xyz = f32(origin + k / 32); the ovs record has start/end.
func TestOriginVec(t *testing.T) {
	buf, n := bitBuf(t, "1 000011 111111 100000")
	inp := NewInp()
	inp.Buf, inp.NBits, inp.QN, inp.QT, inp.Origin = buf, n, 1, []int64{1}, [3]float64{10, 0.5, -1}
	r := rdOf(t, inp)
	xyz := r.originVec(5)
	if want := [3]float64{10 + 3.0/32, 0.5 - 1.0/32, -1 - 1}; xyz != want {
		t.Fatalf("xyz %v want %v", xyz, want)
	}
	if len(r.out.Ovs) != 1 || r.out.Ovs[0].Start != 0 || r.out.Ovs[0].End != uint32(n) {
		t.Fatalf("ovs %+v", r.out.Ovs)
	}
}

func TestEncodePosition(t *testing.T) {
	s, err := EncodePosition([3]float64{1, -0.5, 2.03125}, []int64{1, 3})
	// round(x*32) = 32, -16, 65: width 1+5 = 6 is too narrow for 65; width 3+5 = 8 fits -> idx 2 (2 bits).
	want := "10" + "00100000" + "11110000" + "01000001"
	if err != nil || s != want {
		t.Fatalf("%q %v, want %q", s, err, want)
	}
	s, _ = EncodePosition([3]float64{0.01, 0, 0}, []int64{1})
	if len(s) != 1+96 || s[:1] != "0" {
		t.Fatalf("raw fallback %q", s)
	}
}

// create_fields memo: ticket create u16/u5 tail -> +0x50/+0x54 for the create and later updates of the same id;
// a record that is not one of Log.Ghosts (a copy) gets {} like Python's id()-keyed memo.
func TestCreateFieldsAndSlice(t *testing.T) {
	// packet bits: entry 0..13 = id 5 in 13 bits + isNew 1, create body 14..35 = 1 + u16 0x00ff + u5 3.
	body := bits.FormatBin(5, 13) + "1" + "1" + bits.FormatBin(0xff, 16) + bits.FormatBin(3, 5)
	upd := "1" + "1" // 7E8900 record: p0 present + 1 bit
	buf, _ := bitBuf(t, body+upd+strings.Repeat("0", 64-len(body)-len(upd)))
	child := make([]byte, 32)
	child[4], child[12] = 64, 6
	create := &Ghost{ID: 5, Phase: 1, SectionVT: TicketVT, EntryCursor: 0, CreateCur: 14, Cursor: 36}
	update := &Ghost{ID: 5, Phase: 0, SectionVT: TicketVT, Cursor: 36}
	d := &Log{Packets: map[int64]*Packet{0: {Payload: buf, Limit: 38}}, Ghosts: []*Ghost{create, update},
		Configs: []*Config{{ChildRaw: child, Tables: [2][]int64{{1, 3}, {}}}}}
	w, err := InferIDWidths(d)
	if err != nil || w[0] != 13 {
		t.Fatalf("widths %v %v", w, err)
	}
	for _, g := range []*Ghost{create, update} {
		if f, err := d.CreateFields(g); err != nil || f[0x50] != 0xff || f[0x54] != 3 {
			t.Fatalf("fields %v %v", f, err)
		}
	}
	cp := *update
	if f, _ := d.CreateFields(&cp); len(f) != 0 {
		t.Fatalf("copy: %v", f)
	}
	g := &Ghost{ID: 7, Phase: 0, SectionVT: 0x143010c, Cursor: 36, Cache: uint32(naive(append(buf, 0, 0, 0, 0), 32, 32) << 4),
		DwordOffset: 8}
	d.Ghosts = append(d.Ghosts, g)
	res, err := SliceRecord(d, g, w)
	if err != nil || res.End != 38 || res.Properties != 1 || len(res.Chunks) != 1 || res.Chunks[0].Value.String() != "1" {
		t.Fatalf("slice %+v %v", res, err)
	}
}
