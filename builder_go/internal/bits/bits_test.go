package bits

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

// vector is one case of testdata/bits/bits.json (gen/gen_pyc_vectors.py, frozen CPython).
type vector struct {
	N        int
	V        string
	Body     string
	BitLen   int `json:"bit_length"`
	FmtW     int `json:"fmt_w"`
	Fmt      string
	ShrK     int `json:"shr_k"`
	Shr, Shl string
	MaskM    int `json:"mask_m"`
	Mask     string
	Sl       [2]int
	SlBody   string `json:"sl_body"`
	CatN     int    `json:"cat_n"`
	CatV     string `json:"cat_v"`
	Cat      string
	Tobytes  string
	Payload  *string
	BitsOf   string `json:"bits_of"`
	Join08   string
}

func bigOf(t *testing.T, s string) *big.Int {
	z, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad int %q", s)
	}
	return z
}

func TestVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "bits", "bits.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Cases []vector }
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Cases {
		if c.Payload != nil {
			p, _ := hex.DecodeString(*c.Payload)
			if s := FromBytes(p).String(); s != c.BitsOf || s != c.Join08 {
				t.Errorf("FromBytes(%s) = %s, want %s", *c.Payload, s, c.BitsOf)
			}
			continue
		}
		b, err := Parse(c.Body)
		if err != nil || b.Len() != c.N || b.Big().Cmp(bigOf(t, c.V)) != 0 || b.String() != c.Body {
			t.Fatalf("Parse(%q) = %v %v", c.Body, b, err)
		}
		if fb, err := FromBig(bigOf(t, c.V), c.N); err != nil || !fb.Equal(b) {
			t.Errorf("FromBig(%s, %d) = %v %v", c.V, c.N, fb, err)
		}
		if b.BitLen() != c.BitLen {
			t.Errorf("bit_length %d, want %d", b.BitLen(), c.BitLen)
		}
		if s := FormatBinBig(b.Big(), c.FmtW); s != c.Fmt {
			t.Errorf("format(%s, '0%db') = %s, want %s", c.V, c.FmtW, s, c.Fmt)
		}
		if u, ok := b.Uint64(); ok {
			if s := FormatBin(u, c.FmtW); s != c.Fmt {
				t.Errorf("FormatBin(%d, %d) = %s, want %s", u, c.FmtW, s, c.Fmt)
			}
		}
		if s := b.Shr(c.ShrK).Big().String(); s != c.Shr {
			t.Errorf("%s >> %d = %s, want %s", c.V, c.ShrK, s, c.Shr)
		}
		if s := b.Shl(c.ShrK); s.Big().String() != c.Shl || s.Len() != c.N+c.ShrK {
			t.Errorf("%s << %d = %s, want %s", c.V, c.ShrK, s.Big(), c.Shl)
		}
		if s := b.Low(c.MaskM).Big().String(); s != c.Mask {
			t.Errorf("%s & mask(%d) = %s, want %s", c.V, c.MaskM, s, c.Mask)
		}
		mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(c.MaskM)), big.NewInt(1))
		if mb, err := FromBig(mask, c.MaskM); err != nil || b.And(mb).Big().String() != c.Mask {
			t.Errorf("And mask(%d) = %v, want %s", c.MaskM, b.And(mb).Big(), c.Mask)
		}
		if s := b.Slice(c.Sl[0], c.Sl[1]).String(); s != c.SlBody {
			t.Errorf("body[%d:%d] = %s, want %s", c.Sl[0], c.Sl[1], s, c.SlBody)
		}
		b2, _ := FromBig(bigOf(t, c.CatV), c.CatN)
		if cat := b.Append(b2); cat.Big().String() != c.Cat || cat.Len() != c.N+c.CatN || cat.String() != c.Body+b2.String() {
			t.Errorf("concat = %s, want %s", cat.Big(), c.Cat)
		}
		if h := b.Hex(); h != c.Tobytes {
			t.Errorf("tobytes = %s, want %s", h, c.Tobytes)
		}
		if fh, err := FromHex(c.N, c.Tobytes); err != nil || !fh.Equal(b) {
			t.Errorf("FromHex(%d, %s) = %v %v", c.N, c.Tobytes, fh, err)
		}
	}
	t.Logf("%d cases", len(v.Cases))
}

// TestReaderWriter: random fields written MSB first read back equal, and match slices of the bit string.
func TestReaderWriter(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for it := 0; it < 2000; it++ {
		var w Writer
		var widths []int
		var vals []Bits
		for k := rng.IntN(20); k > 0; k-- {
			n := rng.IntN(150)
			body := make([]byte, n)
			for i := range body {
				body[i] = '0' + byte(rng.IntN(2))
			}
			b, _ := Parse(string(body))
			if n <= 64 {
				u, _ := b.Uint64()
				if err := w.Put(u, n); err != nil {
					t.Fatal(err)
				}
			} else {
				w.PutBits(b)
			}
			widths, vals = append(widths, n), append(vals, b)
		}
		all := w.Bits()
		if all.Len() != w.Len() || all.String() != Concat(vals...).String() {
			t.Fatal("writer concat mismatch")
		}
		r := NewReader(w.Bytes(), w.Len())
		pos := 0
		for i, n := range widths {
			var got Bits
			if n <= 64 && i%2 == 0 {
				u, err := r.Read(n)
				if err != nil {
					t.Fatal(err)
				}
				got, _ = FromUint(u, n)
			} else {
				var err error
				if got, err = r.ReadBits(n); err != nil {
					t.Fatal(err)
				}
			}
			if !got.Equal(vals[i]) || got.String() != all.Slice(pos, pos+n).String() {
				t.Fatalf("field %d: %s != %s", i, got, vals[i])
			}
			pos += n
		}
		if r.Remaining() != 0 {
			t.Fatal("bits left")
		}
		if _, err := r.Read(1); err != ErrShort {
			t.Fatal("expected ErrShort")
		}
	}
}

func TestFitChecks(t *testing.T) {
	if _, err := FromUint(8, 3); err == nil {
		t.Fatal("8 fits 3 bits?")
	}
	if b, err := FromUint(0, 0); err != nil || b.String() != "" || b.Hex() != "" {
		t.Fatal("empty body")
	}
	if FormatBin(0, 0) != "0" || FormatBin(5, 1) != "101" {
		t.Fatal("format quirks")
	}
}
