package soldier

import (
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/golden"
)

// vector is one synthetic row of testdata/soldier/vectors.json.gz (gen/gen_soldier_vectors.py, frozen CPython).
type vector struct {
	Payload    string
	Cursor     uint32
	Limit      uint32
	Child      string
	Table      []uint32
	OriginBits [3]uint32 `json:"origin_bits"`
	GhostBits  int64     `json:"ghost_bits"`
	T          uint64
	ID         uint16
	EndHint    *int64 `json:"end_hint"`
	Stats      []string
	Row        *string
	Error      *string
}

func TestVectors(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "soldier", "vectors.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var vs []vector
	if err := json.NewDecoder(gz).Decode(&vs); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for n, v := range vs {
		p, _ := hex.DecodeString(v.Payload)
		child, _ := hex.DecodeString(v.Child)
		g := &capture.Ghost{T: v.T, ID: v.ID, Cursor: v.Cursor, OriginBits: v.OriginBits}
		if v.EndHint != nil {
			g.EndHint, g.HasEndHint = *v.EndHint, true
		}
		r := capture.V1Row{G: g, P: &capture.Packet{Limit: v.Limit, Payload: p},
			C: &capture.Config{ChildRaw: child, Tables: [2][]uint32{v.Table, nil}}}
		var res rowResult
		if cx, err := ctxFromRow(r.C, v.GhostBits); err != nil {
			res.err = err
		} else {
			res = convertOne(newStream(p), &r, cx)
		}
		var got, want string
		switch {
		case res.err != nil:
			var pe *capture.PyError
			if errors.As(res.err, &pe) {
				got = "error " + pe.Kind
			} else {
				got = "error go: " + res.err.Error()
			}
		case res.ok:
			l, err := golden.Line(&res.row)
			if err != nil {
				t.Fatal(err)
			}
			got = l
		}
		switch {
		case v.Error != nil:
			want = "error " + map[string]string{"error": "struct.error"}[*v.Error]
			if want == "error " {
				want += *v.Error
			}
		case v.Row != nil:
			want = *v.Row
		}
		if got != want || res.err == nil && !slices.Equal(res.stats, v.Stats) {
			if bad++; bad <= 10 {
				t.Errorf("vector %d:\n  go: %s %q\n  py: %s %q", n, got, res.stats, want, v.Stats)
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d/%d vectors differ", bad, len(vs))
	}
	t.Logf("%d vectors", len(vs))
}
