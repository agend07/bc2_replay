package fbgd

import (
	"encoding/binary"
	"errors"
	"testing"

	"bc2replay/builder/internal/bits"
)

// synthetic stream helpers (no demo data): MSB-first fields as fbgd_write puts them.
type w struct{ bits.Writer }

func (x *w) put(v uint64, n int) {
	if err := x.Put(v, n); err != nil {
		panic(err)
	}
}

func (x *w) chunk(idx, nbits int, val uint64, refs ...Ref) {
	x.put(uint64(idx), 6)
	x.put(uint64(nbits), 16)
	x.put(val, nbits)
	x.put(uint64(len(refs)), 6)
	for _, r := range refs {
		x.put(uint64(r.Target), 16)
		x.put(uint64(r.Flag), 1)
	}
}

func frame(marker int, x *w) []byte {
	p := x.Bytes()
	out := []byte{byte(marker), 0, 0, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(out[1:], uint32(len(p)))
	binary.LittleEndian.PutUint32(out[5:], uint32(x.Len()))
	return append(out, p...)
}

func TestReadSynthetic(t *testing.T) {
	var k w
	k.put(1, 1)
	k.put(1, 16) // two ghosts
	k.put(5, 16) // ghost 5: soldier create, one section of two chunks
	k.put(1, 16)
	k.chunk(0, 8, 10)
	k.put(2<<16, 32)
	k.put(3, 2)
	k.chunk(0, 3, 5)
	k.chunk(1, 70, 1, Ref{7, 1})
	k.put(7, 16) // ghost 7: type-31 spawner create, no sections
	k.put(0, 16)
	k.chunk(2, 20, 0b011111_1_000000000101_0)
	var d w
	d.put(3, 31)
	d.put(4, 31)
	d.put(1, 1) // deleted: [7]
	d.put(0, 16)
	d.put(7, 16)
	d.put(1, 1) // dirty: 5 upd s0 p1, 9 new
	d.put(1, 16)
	d.put(5, 16)
	d.put(0, 1)
	d.put(1, 1)
	d.put(0, 1)
	d.put(1, 1)
	d.chunk(1, 2, 3)
	d.put(9, 16)
	d.put(1, 1)
	d.put(0, 16)
	d.chunk(0, 0, 0)
	var e w // empty section flag: the one non-canonical form
	e.put(0, 62)
	e.put(0, 1)
	e.put(1, 1)
	e.put(0, 16)
	e.put(5, 16)
	e.put(0, 1)
	e.put(1, 1)
	e.put(0, 2)
	data := append([]byte("EA\x00v1\x00"), frame(1, &k)...)
	data = append(data, frame(2, &d)...)
	data = append(data, frame(2, &e)...)
	data = append(data, 0)

	ver, fr, err := Frames(data)
	if err != nil || string(ver) != "v1" || len(fr) != 3 {
		t.Fatalf("Frames: %q %d %v", ver, len(fr), err)
	}
	r := NewReader(fr[0].Payload, fr[0].BitPos)
	gs, err := Keyframe(r)
	if err != nil || len(gs) != 2 || r.Pos() != fr[0].BitPos {
		t.Fatalf("Keyframe: %d ghosts, pos %d/%d, %v", len(gs), r.Pos(), fr[0].BitPos, err)
	}
	if !Soldier(gs[0]) || Soldier(gs[1]) {
		t.Errorf("Soldier: %v %v", Soldier(gs[0]), Soldier(gs[1]))
	}
	if s := Signature(gs[0]); s != "00001010/8" {
		t.Errorf("Signature(soldier) = %q", s)
	}
	if s := Signature(gs[1]); s != "01111110/*0" {
		t.Errorf("Signature(spawner) = %q", s)
	}
	c := gs[0].Sections[0].Chunks[1]
	if c.Bits != 70 || c.Value().Int64() != 1 || len(c.Refs) != 1 || c.Refs[0] != (Ref{7, 1}) || c.BitString()[69] != '1' {
		t.Errorf("70-bit chunk: %+v %s", c, c.Value())
	}
	st := State{5: gs[0].Counts(), 7: gs[1].Counts()}
	r = NewReader(fr[1].Payload, fr[1].BitPos)
	dd, err := Delta(r, st)
	if err != nil || r.Pos() != fr[1].BitPos || dd.EmptySection {
		t.Fatalf("Delta: %+v pos %d/%d %v", dd, r.Pos(), fr[1].BitPos, err)
	}
	if dd.DT != 3 || dd.Sim != 4 || len(dd.Deleted) != 1 || len(dd.Dirty) != 2 || !dd.Dirty[1].New ||
		dd.Dirty[0].Changes[0].Property != 1 {
		t.Errorf("Delta content: %+v", dd)
	}
	if _, ok := st[7]; ok {
		t.Error("deleted ghost still in state")
	}
	if _, ok := st[9]; !ok {
		t.Error("new ghost not in state")
	}
	r = NewReader(fr[2].Payload, fr[2].BitPos)
	dd, err = Delta(r, st)
	if err != nil || !dd.EmptySection {
		t.Errorf("empty section not flagged: %+v %v", dd, err)
	}
	// upd of an unknown ghost = KeyError(gid); a short read = EOFError with fbgd_read's message
	_, err = Delta(NewReader(fr[2].Payload, fr[2].BitPos), State{})
	var pe *PyError
	if !errors.As(err, &pe) || pe.Class != "KeyError" || pe.Msg != "5" || pe.Repr() != "KeyError(5)" {
		t.Errorf("KeyError: %v", err)
	}
	_, err = Keyframe(NewReader(fr[0].Payload, 10))
	if !errors.As(err, &pe) || pe.Class != "EOFError" || pe.Msg != "read 16 at 1 past 10" {
		t.Errorf("EOFError: %v", err)
	}
}

func TestFramesErrors(t *testing.T) {
	for _, c := range []struct {
		data       string
		class, msg string
	}{
		{"abc", "ValueError", "subsection not found"},
		{"a\x00b\x00", "IndexError", "index out of range"},
		{"a\x00b\x00\x01\x02", "error",
			"unpack_from requires a buffer of at least 13 bytes for unpacking 8 bytes at offset 5 (actual buffer size is 6)"},
	} {
		_, _, err := Frames([]byte(c.data))
		var pe *PyError
		if !errors.As(err, &pe) || pe.Class != c.class || pe.Msg != c.msg {
			t.Errorf("Frames(%q) = %v, want %s %s", c.data, err, c.class, c.msg)
		}
	}
}

func TestStrRepr(t *testing.T) {
	for in, want := range map[string]string{`a`: `'a'`, `a'b`: `"a'b"`, `a'"b`: `'a\'"b'`, `C:\x`: `'C:\\x'`} {
		if got := StrRepr(in); got != want {
			t.Errorf("StrRepr(%s) = %s, want %s", in, got, want)
		}
	}
}
