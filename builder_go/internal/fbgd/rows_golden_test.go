package fbgd_test

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/golden"
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// records returns the untagged records of a dump file (header skipped).
func records(t *testing.T, path string) []*pyc.Object {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := gzip.NewReader(f); err != nil {
		t.Fatal(err)
	}
	f.Seek(0, io.SeekStart)
	r, err := golden.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var out []*pyc.Object
	for {
		l, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if r.LineNo() == 1 {
			continue
		}
		tr, err := pyc.Loads(l)
		if err != nil {
			t.Fatal(err)
		}
		u, err := golden.Untag(tr)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, u.(*pyc.Object))
	}
}

func get(o *pyc.Object, k string) any { v, _ := o.Get(k); return v }

// updateRow converts {t, id, chunks, ...} (soldier_rows / ac38 row) to a 'u' Row.
func updateRow(t *testing.T, o *pyc.Object) *fbgd.Row {
	u := pyc.NewObject()
	u.Set("k", "u")
	for _, k := range []string{"t", "id", "chunks"} {
		u.Set(k, get(o, k))
	}
	r, err := fbgd.RowOf(u)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func line(t *testing.T, v any) string {
	t.Helper()
	l, err := golden.Line(v)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func chunkStr(c fbgd.OutChunk) string { return fmt.Sprintf("%d %s %v", c.Index, c.Value.Key(), c.Refs) }

func rowStr(t *testing.T, r *fbgd.Row, initial any, vt uint32, syn bool) string {
	s := fmt.Sprintf("%c t=%v id=%d type=%#v vt=%x syn=%v", r.K, r.T, r.ID, r.Type, vt, syn)
	if r.K == 'c' {
		s += " create=" + chunkStr(r.Create)
	}
	for _, c := range r.Chunks {
		s += fmt.Sprintf(" [%v %v %s %v]", c.Key, c.Tuple, c.Value.Key(), c.Refs)
	}
	if initial != nil {
		s += " initial=" + line(t, initial)
	}
	return s
}

// TestBuildRowsGolden: BuildRows over Go's decode of each dump's m-log, with the dump's soldier_rows, ac38 rows,
// templates and initial states, equals the build_rows stage (rows, stats).
func TestBuildRowsGolden(t *testing.T) {
	root := capture.DefaultGolden(".")
	if root == "" {
		t.Skip("golden dumps absent (set " + capture.GoldenEnv + ")")
	}
	var want []string
	if n := os.Getenv("BC2_GO_GOLDEN_NAMES"); n != "" {
		want = strings.Split(n, ",")
	}
	dumps, err := capture.GoldenDumps(root, want)
	if err != nil || len(dumps) == 0 {
		t.Skip("golden dumps absent")
	}
	for _, gd := range dumps {
		if testing.Short() && gd.Name != "t05" {
			continue
		}
		t.Run(gd.Name, func(t *testing.T) {
			raw, err := os.ReadFile(gd.MLog)
			if err != nil {
				t.Fatal(err)
			}
			dec, err := capture.Load(raw)
			if err != nil {
				t.Fatal(err)
			}
			log := capture.WireLog(dec)
			var sold, ac []*fbgd.Row
			for _, o := range records(t, filepath.Join(gd.Dir, "soldier_rows.jsonl.gz")) {
				sold = append(sold, updateRow(t, o))
			}
			for _, o := range records(t, filepath.Join(gd.Dir, "ac38.jsonl.gz")) {
				ac = append(ac, updateRow(t, get(o, "row").(*pyc.Object)))
			}
			vts := map[uint32]bool{}
			initial := map[fbgd.InitialKey]any{}
			creates := map[fbgd.InitialKey]fbgd.OutChunk{} // (t, id) -> the dump's create (first row of that key)
			var wantRows []string
			var wantStats string
			for _, o := range records(t, filepath.Join(gd.Dir, "build_rows.jsonl.gz")) {
				switch {
				case get(o, "tpl") != nil:
					if k, ok := get(o, "tpl").([]any)[0].(int64); ok {
						vts[uint32(k)] = true
					}
				case get(o, "initial_state") != nil:
					kv := get(o, "initial_state").([]any)
					tid := kv[0].([]any)
					initial[fbgd.InitialKey{T: uint64(tid[0].(int64)), ID: tid[1].(int64)}] = kv[1]
				case get(o, "row") != nil:
					ro := get(o, "row").(*pyc.Object)
					r, err := fbgd.RowOf(ro)
					if err != nil {
						t.Fatal(err)
					}
					var vt uint32
					if v, ok := ro.Get("_vt"); ok {
						vt = uint32(v.(int64))
					}
					syn, _ := ro.Get("syn")
					s, _ := syn.(bool)
					wantRows = append(wantRows, rowStr(t, r, get(ro, "_initial"), vt, s))
					if r.K == 'c' {
						k := fbgd.InitialKey{T: uint64(r.T), ID: int64(r.ID)}
						if _, ok := creates[k]; !ok {
							creates[k] = r.Create
						}
					}
				case get(o, "stats") != nil:
					wantStats = line(t, get(o, "stats"))
				}
			}
			// cl04c's patched create_chunk (camera_create, CREBASE, CUT) is not ported here: the creates come from the
			// dump's create rows; the plain CreateChunk must agree with them for vts the patch leaves alone.
			plain := 0
			create := func(d *wire.Log, g *wire.Ghost) (fbgd.OutChunk, error) {
				c, ok := creates[fbgd.InitialKey{T: g.T, ID: g.ID}]
				if !ok {
					return fbgd.CreateChunk(d, g)
				}
				if p, err := fbgd.CreateChunk(d, g); err == nil && chunkStr(p) == chunkStr(c) {
					plain++
				}
				return c, nil
			}
			rows, stats, err := fbgd.BuildRows(log, func(vt uint32) bool { return vts[vt] }, sold, ac, initial, create)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(wantRows) {
				t.Errorf("rows: go %d py %d", len(rows), len(wantRows))
			}
			for i := range min(len(rows), len(wantRows)) {
				if g := rowStr(t, &rows[i].Row, rows[i].Initial, rows[i].VT, rows[i].Syn); g != wantRows[i] {
					t.Fatalf("row %d:\n go %.300s\n py %.300s", i, g, wantRows[i])
				}
			}
			if g := line(t, stats); g != wantStats {
				t.Errorf("stats: go %s py %s", g, wantStats)
			}
			t.Logf("%d rows (%d of %d creates equal to plain create_chunk), stats %s", len(rows), plain, len(creates), wantStats)
		})
	}
}
