package capture

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bc2replay/builder/internal/pyc"
)

// retailToSteam is the inverse table (the generator refuses a non-injective one).
func retailToSteam(t *testing.T) map[uint32]uint32 {
	inv := map[uint32]uint32{}
	for s, r := range steamToRetail {
		if _, dup := inv[r]; dup {
			t.Fatalf("retail 0x%x mapped twice", r)
		}
		inv[r] = s
	}
	return inv
}

// frame builds one record: head (type | len<<8) + body, body padded to 4.
func frame(typ int, body []byte) Frame {
	for len(body)%4 != 0 {
		body = append(body, 0)
	}
	raw := binary.LittleEndian.AppendUint32(nil, uint32(typ)|uint32(4+len(body))<<8)
	return Frame{typ, append(raw, body...)}
}

func put(b []byte, off int, v uint32) []byte { binary.LittleEndian.PutUint32(b[off:], v); return b }

func TestExeBuild(t *testing.T) {
	if ExeBuild(ExeRetailSHA256) != ExeRetail || ExeBuild(ExeSteamSHA256) != ExeSteam || ExeBuild(strings.Repeat("0", 64)) != "" {
		t.Error("ExeBuild")
	}
	if len(steamToRetail) != 79 {
		t.Errorf("table has %d entries, want 79", len(steamToRetail))
	}
	for s, r := range steamToRetail {
		if _, ok := steamToRetail[r]; ok {
			t.Errorf("retail 0x%x is also a Steam key", r)
		}
		if s < steamImageLo || s >= steamImageHi {
			t.Errorf("steam 0x%x outside the image", s)
		}
	}
}

// TestNormalizeSteam: every listed field is translated (G ghost/section, M sections, B ghost, S dispatcher, C child
// +0/+16); 0, heap values, other fields and other record types are untouched.
func TestNormalizeSteam(t *testing.T) {
	g := make([]byte, gBodySize)
	put(g, 20, 0x143a458) // soldier ghost
	put(g, 24, 0x143a360) // soldier section
	put(g, 36, 0x143a458) // cache: not a vtable field
	gm := put(put(make([]byte, gBodySize), 20, 0x143e1e0), 24, 0)
	m := put(put(put(put(make([]byte, 16), 0, 3), 4, 0x1439824), 8, 0x14344a8), 12, 0x14344a8)
	b := put(make([]byte, bHeadSize), 8, 0x143a458)
	s := put(make([]byte, sHeadSize), 12, 0x1445a78)
	s0 := put(put(make([]byte, sHeadSize), 12, 0), 8, 0x02345678) // dispatcher 0, heap conn
	c := put(put(put(make([]byte, 40), 8, 0x1434c58), 24, 0x1434c3c), 12, 0x1434c58)
	p := put(make([]byte, pHeadSize), 24, 0x143a458) // P payload dword: not translated
	fr := []Frame{frame(TS, s), frame(TS, s0), frame(TC, c), frame(TP, p), frame(TG, g), frame(TG, gm), frame(TM, m), frame(TB, b)}
	n, err := NormalizeSteam(fr)
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Errorf("translated %d, want 10", n)
	}
	check := func(f Frame, off int, want uint32) {
		t.Helper()
		if v := u32(f.Body(), off); v != want {
			t.Errorf("%s +%d = 0x%x want 0x%x", typeName(f.Type), off, v, want)
		}
	}
	check(fr[0], 12, 0x147d218)
	check(fr[1], 12, 0)
	check(fr[1], 8, 0x02345678)
	check(fr[2], 8, 0x14320f0)
	check(fr[2], 24, 0x14320d4)
	check(fr[2], 12, 0x1434c58)
	check(fr[3], 24, 0x143a458)
	check(fr[4], 20, SoldierGhostVT)
	check(fr[4], 24, SoldierSectionVT)
	check(fr[4], 36, 0x143a458)
	check(fr[5], 20, 0x143b678)
	check(fr[5], 24, 0)
	check(fr[6], 4, 0x1436cbc)
	check(fr[6], 8, 0x1431940)
	check(fr[6], 12, 0x1431940)
	check(fr[7], 8, SoldierGhostVT)
}

// TestNormalizeSteamUnknown: an image value missing from the table (a new vtable, or a retail value in a Steam capture)
// is a hard error naming the record, field and value; a short body is left to Decode.
func TestNormalizeSteamUnknown(t *testing.T) {
	for _, c := range []struct {
		f    Frame
		want string
	}{
		{frame(TG, put(make([]byte, gBodySize), 24, 0x1431000)), "record 1 (G) section_vt 0x1431000 is not in the Steam vtable table"},
		{frame(TG, put(make([]byte, gBodySize), 20, SoldierGhostVT)), "record 1 (G) ghost_vt 0x14378f0"},
		{frame(TM, put(put(put(make([]byte, 12), 0, 2), 4, 0x1439824), 8, 0x1436cbc)), "record 1 (M) sections[1] 0x1436cbc"},
		{frame(TS, put(make([]byte, sHeadSize), 12, 0x147d218)), "record 1 (S) dispatcher_vt 0x147d218"},
		{frame(TB, put(make([]byte, bHeadSize), 8, 0x1500000)), "record 1 (B) ghost_vt 0x1500000"},
		{frame(TC, put(make([]byte, 40), 24, 0x1d6fffc)), "record 1 (C) child_raw+16 0x1d6fffc"},
	} {
		fr := []Frame{frame(TT, make([]byte, tBodySize)), c.f}
		_, err := NormalizeSteam(fr)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("err %v, want %q", err, c.want)
		}
	}
	// short bodies / an M count past the body: nothing to translate, no panic
	short := []Frame{frame(TG, make([]byte, 20)), frame(TM, put(make([]byte, 8), 0, 99)), frame(TS, []byte{1, 2, 3, 4})}
	if n, err := NormalizeSteam(short); n != 0 || err != nil {
		t.Errorf("short: %d %v", n, err)
	}
	if n, err := NormalizeSteam([]Frame{frame(TM, put(put(make([]byte, 8), 0, 99), 4, 0x1439824))}); n != 1 || err != nil {
		t.Errorf("M count past body: %d %v", n, err)
	}
}

// steamify rewrites a retail .bc2raw into a Steam one: header hash and every vtable field retail -> Steam.
func steamify(t *testing.T, raw []byte) ([]byte, int) {
	inv := retailToSteam(t)
	out := bytes.Clone(raw)
	sha, _ := hex.DecodeString(ExeSteamSHA256)
	copy(out[12:44], sha)
	fr, _ := ReadFrames(out[HeaderSize:])
	n := 0
	for _, f := range fr {
		b := f.Body()
		swap := func(off int) {
			if off+4 <= len(b) {
				if s, ok := inv[u32(b, off)]; ok {
					put(b, off, s)
					n++
				}
			}
		}
		switch f.Type {
		case TG:
			swap(20)
			swap(24)
		case TB:
			swap(8)
		case TS:
			swap(12)
		case TC:
			swap(8)
			swap(24)
		case TM:
			if len(b) >= 4 {
				for k := 0; k < int(u32(b, 0)) && 8+4*k <= len(b); k++ {
					swap(4 + 4*k)
				}
			}
		}
	}
	return out, n
}

// TestExtractSteam: extract of a steamified copy of a retail capture writes byte-identical m-log.bin / raw-log.bin
// and records exe {build steam, vt_translated N}; the retail original records exe {build retail, vt_translated 0}.
func TestExtractSteam(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(vec, "synth.bc2raw"))
	if err != nil {
		t.Fatal(err)
	}
	// synth.bc2raw: give its G records retail vtables so the test translates something
	retail := bytes.Clone(src)
	rsha, _ := hex.DecodeString(ExeRetailSHA256)
	copy(retail[12:44], rsha)
	fr, _ := ReadFrames(retail[HeaderSize:])
	for _, f := range fr {
		if b := f.Body(); f.Type == TG && len(b) >= gBodySize {
			put(b, 20, SoldierGhostVT)
			if u32(b, 24) != 0 {
				put(b, 24, SoldierSectionVT)
			}
		}
	}
	steam, n := steamify(t, retail)
	if n == 0 {
		t.Fatal("steamify changed nothing")
	}
	dir := t.TempDir()
	rp, sp := filepath.Join(dir, "r.bc2raw"), filepath.Join(dir, "s.bc2raw")
	os.WriteFile(rp, retail, 0o666)
	os.WriteFile(sp, steam, 0o666)
	mr, err := Extract(rp, filepath.Join(dir, "r"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := Extract(sp, filepath.Join(dir, "s"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"m-log.bin", "raw-log.bin"} {
		a, _ := os.ReadFile(filepath.Join(dir, "r", f))
		b, _ := os.ReadFile(filepath.Join(dir, "s", f))
		if !bytes.Equal(a, b) {
			t.Errorf("%s differs", f)
		}
	}
	exe := func(m *pyc.Object) string {
		e, _ := m.Get("exe")
		s, _ := pyc.Dumps(e, pyc.DumpOptions{})
		return s
	}
	if got, want := exe(ms), `{"build": "steam", "vt_translated": `+itoa(n)+`}`; got != want {
		t.Errorf("steam exe %s want %s", got, want)
	}
	if got := exe(mr); got != `{"build": "retail", "vt_translated": 0}` {
		t.Errorf("retail exe %s", got)
	}
	// without the table entry the same capture fails
	bad := bytes.Clone(steam)
	frb, _ := ReadFrames(bad[HeaderSize:])
	for _, f := range frb {
		if f.Type == TG {
			put(f.Body(), 20, 0x1431000)
			break
		}
	}
	os.WriteFile(sp, bad, 0o666)
	if _, err := Extract(sp, filepath.Join(dir, "bad"), "", ""); err == nil || !strings.Contains(err.Error(), "ghost_vt 0x1431000 is not in the Steam vtable table") {
		t.Errorf("unknown vtable: %v", err)
	}
}

func itoa(n int) string { s, _ := pyc.Dumps(n, pyc.DumpOptions{}); return s }
