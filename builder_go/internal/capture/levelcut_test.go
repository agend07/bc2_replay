package capture

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// sRec is one S record (head + S_HEAD body, empty name) for the r282 level-cut tests.
func sRec(flags, pseq, levelPtr uint32) []byte {
	b := make([]byte, 4+sHeadSize)
	binary.LittleEndian.PutUint32(b, uint32(TS)|uint32(len(b))<<8)
	for i, v := range []uint32{1, flags, 0x1000, 0, 13, 0, pseq, levelPtr, 0} {
		binary.LittleEndian.PutUint32(b[4+4*i:], v)
	}
	return b
}

func TestLevelCut(t *testing.T) {
	join := func(rs ...[]byte) []byte { return slices.Concat(rs...) }
	for _, c := range []struct {
		name string
		raw  []byte
		want uint32
	}{
		{"none", nil, 0},
		{"first load only (late S)", join(sRec(0, 0, 0), sRec(SLevelValid, 7, 0xA0)), 0},
		{"ghost bits re-emit, same level", join(sRec(SLevelValid, 0, 0xA0), sRec(SLevelValid, 9, 0xA0)), 0},
		{"unload + reload (cap_164317)", join(sRec(0, 0, 0), sRec(SLevelValid, 0, 0xA0), sRec(0, 18, 0), sRec(SLevelValid, 18, 0xB0)), 18},
		{"pointer change without unload", join(sRec(SLevelValid, 0, 0xA0), sRec(SLevelValid, 30, 0xB0)), 30},
		{"last change wins", join(sRec(SLevelValid, 0, 0xA0), sRec(0, 5, 0), sRec(SLevelValid, 6, 0xA0), sRec(0, 40, 0)), 40},
	} {
		got, err := LevelCut(c.raw)
		if err != nil || got != c.want {
			t.Errorf("%s: LevelCut = %d, %v; want %d", c.name, got, err, c.want)
		}
	}
}

func TestCut(t *testing.T) {
	d := &Decoded{Packets: map[uint32]*Packet{}}
	for _, s := range []uint32{3, 1, 2, 5, 4} {
		d.Packets[s] = &Packet{Seq: s}
		d.PacketSeqs = append(d.PacketSeqs, s)
		d.Ghosts = append(d.Ghosts, &Ghost{Pseq: s})
		d.Multi = append(d.Multi, &Ghost{Pseq: s, Sections: []uint32{}})
	}
	d.Cut(2)
	if !slices.Equal(d.PacketSeqs, []uint32{3, 5, 4}) || len(d.Packets) != 3 || d.Packets[1] != nil || d.Packets[2] != nil {
		t.Fatalf("packets after Cut(2): %v (%d)", d.PacketSeqs, len(d.Packets))
	}
	for _, gs := range [][]*Ghost{d.Ghosts, d.Multi} {
		var ps []uint32
		for _, g := range gs {
			ps = append(ps, g.Pseq)
		}
		if !slices.Equal(ps, []uint32{3, 5, 4}) {
			t.Fatalf("ghosts after Cut(2): %v", ps)
		}
	}
}

// pRec is one P record (head + P_HEAD body, empty payload) with P seq seq, for the r287 round tests.
func pRec(seq uint32) []byte {
	b := make([]byte, 4+pHeadSize)
	binary.LittleEndian.PutUint32(b, uint32(TP)|uint32(len(b))<<8)
	binary.LittleEndian.PutUint32(b[4+12:], seq)
	return b
}

// sNamed is sRec with a level name (name length at S_HEAD +32, the record padded to 4 bytes).
func sNamed(flags, pseq, levelPtr uint32, name string) []byte {
	b := append(sRec(flags, pseq, levelPtr), name...)
	b = append(b, make([]byte, (4-len(b)%4)%4)...)
	binary.LittleEndian.PutUint32(b, uint32(TS)|uint32(len(b))<<8)
	binary.LittleEndian.PutUint32(b[4+32:], uint32(len(name)))
	return b
}

func TestLevelCuts(t *testing.T) {
	join := func(rs ...[]byte) []byte { return slices.Concat(rs...) }
	for _, c := range []struct {
		name string
		raw  []byte
		want []uint32
	}{
		{"none", nil, nil},
		{"one reload (cap_103557_15884)", join(sNamed(SLevelValid, 0, 0xA0, "L"), sRec(0, 10416, 0), sNamed(SLevelValid, 10416, 0xA0, "L")), []uint32{10416}},
		{"duplicates collapsed", join(sRec(SLevelValid, 0, 0xA0), sRec(0, 18, 0), sRec(SLevelValid, 18, 0xB0), sRec(SLevelValid, 18, 0xC0)), []uint32{18}},
		{"every change", join(sRec(SLevelValid, 0, 0xA0), sRec(0, 5, 0), sRec(SLevelValid, 6, 0xA0), sRec(0, 40, 0)), []uint32{5, 40}},
	} {
		got, err := LevelCuts(c.raw)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: LevelCuts = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

func TestLevelRange(t *testing.T) {
	seqs := func(lo, hi uint32) []uint32 {
		var out []uint32
		for q := lo; q <= hi; q++ {
			out = append(out, q)
		}
		return out
	}
	for _, c := range []struct {
		name  string
		pseqs []uint32
		cuts  []uint32
		round int
		want  Range
	}{
		{"no cut", seqs(1, 9), nil, 1, Range{0, 0, true}},
		{"first round longest (cap_103557_15884)", seqs(1, 10423), []uint32{10416}, 1, Range{0, 10416, false}},
		{"last round longest", seqs(1, 50), []uint32{18}, 2, Range{18, 0, true}},
		{"tie -> later", seqs(1, 8), []uint32{4}, 2, Range{4, 0, true}},
		{"empty middle round", slices.Concat(seqs(1, 3), seqs(6, 7)), []uint32{3, 5}, 1, Range{0, 3, false}},
	} {
		if k := DefaultRound(c.pseqs, c.cuts); k != c.round {
			t.Errorf("%s: DefaultRound = %d; want %d", c.name, k, c.round)
		}
		if got := LevelRange(c.pseqs, c.cuts); got != c.want {
			t.Errorf("%s: LevelRange = %+v; want %+v", c.name, got, c.want)
		}
	}
	if s := Segments([]uint32{3, 5}); len(s) != 3 || s[1] != (Range{3, 5, false}) || s[1].Has(3) || !s[1].Has(5) || !s[2].Has(99) {
		t.Errorf("Segments([3 5]) = %+v", s)
	}
}

func TestRounds(t *testing.T) {
	seq := func(n int64) *int64 { return &n }
	raw := slices.Concat(sNamed(SLevelValid, 0, 0xA0, "Levels/A"), sRec(0, 10416, 0), sNamed(SLevelValid, 10416, 0xB0, "Levels/B"))
	got, err := Rounds(raw, seq(10423))
	want := []Round{{Range{0, 10416, false}, 10416, true, "Levels/A"}, {Range{10416, 0, true}, 7, true, "Levels/B"}}
	if err != nil || !slices.Equal(got, want) || DefaultOf(got) != 1 {
		t.Errorf("Rounds = %+v, %v; want %+v", got, err, want)
	}
	got, err = Rounds(nil, seq(30))
	if err != nil || !slices.Equal(got, []Round{{Range{0, 0, true}, 30, true, ""}}) || DefaultOf(got) != 1 {
		t.Errorf("Rounds(no raw-log) = %+v, %v", got, err)
	}
	// no m-summary seq: the open round's packets are unknown (counted as 0 for the default)
	got, _ = Rounds(raw, nil)
	if len(got) != 2 || got[1].HasPackets || DefaultOf(got) != 1 {
		t.Errorf("Rounds(no seq) = %+v", got)
	}
	// unnamed S -> no level; tie -> later; empty round between two cuts counts
	got, _ = Rounds(slices.Concat(sRec(SLevelValid, 0, 0xA0), sRec(0, 4, 0), sRec(SLevelValid, 4, 0xA0), sRec(0, 9, 0)), seq(13))
	if len(got) != 3 || got[0].Level != "" || got[1].Packets != 5 || got[2].Packets != 4 || DefaultOf(got) != 2 {
		t.Errorf("Rounds(unnamed) = %+v", got)
	}
	got, _ = Rounds(slices.Concat(sRec(SLevelValid, 0, 0xA0), sRec(0, 4, 0)), seq(8))
	if DefaultOf(got) != 2 {
		t.Errorf("tie: DefaultOf = %d", DefaultOf(got))
	}
	for _, c := range []struct {
		v    string
		want int
		ok   bool
	}{{"", 2, true}, {"1", 1, true}, {"2", 2, true}, {"3", 0, false}, {"0", 0, false}, {"x", 0, false}, {"+1", 0, false}} {
		k, err := RoundOf(got, c.v)
		if k != c.want || (err == nil) != c.ok {
			t.Errorf("RoundOf(%q) = %d, %v", c.v, k, err)
		}
	}
	if _, err := RoundOf(got, "3"); err == nil || err.Error() != "V3_ROUND=3: the capture has 2 round(s)" {
		t.Errorf("RoundOf text: %v", err)
	}
}

func TestLoadFileRound(t *testing.T) {
	dir := t.TempDir()
	var mlog []byte
	for q := uint32(1); q <= 10; q++ {
		mlog = append(mlog, pRec(q)...)
	}
	write := func(n string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("m-log.bin", mlog)
	ml := filepath.Join(dir, "m-log.bin")
	d, lv, err := LoadFile(ml, "")
	if err != nil || lv.Kept != nil || lv.Round != 1 || len(d.PacketSeqs) != 10 {
		t.Fatalf("no raw-log: %v %+v %d", err, lv, len(d.PacketSeqs))
	}
	write("raw-log.bin", slices.Concat(sRec(SLevelValid, 0, 0xA0), sRec(0, 7, 0), sRec(SLevelValid, 7, 0xA0)))
	for _, c := range []struct {
		round  string
		k      int
		lo, hi uint32
	}{{"", 1, 1, 7}, {"1", 1, 1, 7}, {"2", 2, 8, 10}} {
		d, lv, err := LoadFile(ml, c.round)
		if err != nil || lv.Kept == nil || lv.Round != c.k || !slices.Equal(lv.Cuts, []uint32{7}) || slices.Min(d.PacketSeqs) != c.lo || slices.Max(d.PacketSeqs) != c.hi {
			t.Errorf("round %q: %v %+v %v", c.round, err, lv, d.PacketSeqs)
		}
	}
	if _, _, err := LoadFile(ml, "3"); err == nil {
		t.Errorf("round 3 of 2: no error")
	}
}
