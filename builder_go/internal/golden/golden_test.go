package golden

import (
	"bytes"
	"io"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/pyc"
)

func readAll(t *testing.T, gz []byte) []string {
	t.Helper()
	r, err := NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for {
		l, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(l))
	}
}

// TestPythonSample: every line of the dump written by gen/golden_ref.py (frozen CPython) decodes, untags and
// re-encodes in Go to the same bytes, and a Go-written dump of those values has identical decompressed lines.
func TestPythonSample(t *testing.T) {
	gz, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "sample.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	lines := readAll(t, gz)
	var buf bytes.Buffer
	var w *Writer
	for i, l := range lines {
		tree, err := pyc.Loads([]byte(l))
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		v, err := Untag(tree)
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		got, err := Line(v)
		if err != nil || got != l {
			t.Fatalf("line %d:\n got %s %v\nwant %s", i+1, got, err, l)
		}
		if i == 0 {
			h := v.(*pyc.Object)
			stage, _ := h.Get("stage")
			meta, _ := h.Get("meta")
			if w, err = NewWriter(&buf, stage.(string), meta.(*pyc.Object)); err != nil {
				t.Fatal(err)
			}
		} else if err := w.Write(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, buf.Bytes()); strings.Join(got, "\n") != strings.Join(lines, "\n") {
		t.Fatal("Go dump differs from the Python dump")
	}
	if d, err := Diff(bytes.NewReader(gz), bytes.NewReader(buf.Bytes()), Options{}); err != nil || d != nil {
		t.Fatalf("Diff = %v %v", d, err)
	}
	t.Logf("%d lines", len(lines))
}

func TestTreeRules(t *testing.T) {
	big200, _ := new(big.Int).SetString("b680873fe674dc58b0374f469fe3e01edada6bb5639dfcfbd4", 16)
	b200, _ := bits.FromBig(big200, 200)
	b13, _ := bits.FromUint(4097, 13)
	dollar := pyc.NewObject()
	dollar.Set("$ref", 1)
	cases := []struct {
		v    any
		want string
	}{
		{[]any{int64(1<<53 - 1), int64(1 << 53), int64(-1<<53 + 1), int64(-1 << 53)},
			`[9007199254740991,{"$int":"20000000000000"},-9007199254740991,{"$int":"-20000000000000"}]`},
		{b200, `{"$int":"b680873fe674dc58b0374f469fe3e01edada6bb5639dfcfbd4"}`},
		{b13, `4097`},
		{[]any{0.0, math.Copysign(0, -1), math.Inf(1)}, `[{"$f64":"0000000000000000"},{"$f64":"8000000000000000"},{"$f64":"7ff0000000000000"}]`},
		{[]byte{0, 255}, `{"$bytes":"00ff"}`},
		{dollar, `{"$dict":[["$ref",1]]}`},
		{Pairs{{[]any{1, "el"}, 5}}, `{"$dict":[[[1,"el"],5]]}`},
		{Set{3, "x", 1}, `{"$set":["x",1,3]}`},
		{"é\U0001F600\x7f", `"\u00e9\ud83d\ude00\u007f"`},
	}
	for _, c := range cases {
		if got, err := Line(c.v); err != nil || got != c.want {
			t.Errorf("Line(%v) = %s %v, want %s", c.v, got, err, c.want)
		}
	}
	if _, err := Line(map[string]any{"a": 1}); err == nil {
		t.Error("unordered map accepted")
	}
}

func dumpOf(t *testing.T, recs ...any) []byte {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	return buf.Bytes()
}

func obj(kv ...any) *pyc.Object {
	o := pyc.NewObject()
	for i := 0; i < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

func TestDiff(t *testing.T) {
	nan2 := math.Float64frombits(0xfff8000000000000) // math.NaN() is 0x7ff8000000000001
	base := dumpOf(t, obj("a", 1), obj("rows", []any{obj("bits", 3, "value", 5), obj("f", math.NaN())}))
	cases := []struct {
		other []byte
		want  string
	}{
		{dumpOf(t, obj("a", 1), obj("rows", []any{obj("bits", 3, "value", 5), obj("f", math.NaN())})), ""},
		{dumpOf(t, obj("a", 1), obj("rows", []any{obj("bits", 3, "value", 5), obj("f", nan2)})), ""},
		{dumpOf(t, obj("a", 1), obj("rows", []any{obj("bits", 3, "value", 4), obj("f", math.NaN())})), "line 3 $.rows[0].value: A=5 B=4"},
		{dumpOf(t, obj("a", 1), obj("rows", []any{obj("value", 5, "bits", 3), obj("f", math.NaN())})), "line 3 $.rows[0]: A=keys bits,value B=keys value,bits"},
		{dumpOf(t, obj("a", 1), obj("rows", []any{obj("bits", 3, "value", 5)})), "line 3 $.rows: A=len 2 B=len 1"},
		{dumpOf(t, obj("a", 1)), "line 3 $: A=" + `{"rows":[{"bits":3,"value":5},{"f":{"$f64":"7ff8000000000001"}}]}` + " B=<end of dump>"},
		{dumpOf(t, obj("a", 1.0), obj("rows", nil)), `line 2 $.a: A=1 B={"$f64":"3ff0000000000000"}`},
	}
	for i, c := range cases {
		d, err := Diff(bytes.NewReader(base), bytes.NewReader(c.other), Options{})
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if d != nil {
			got = d.String()
		}
		if got != c.want {
			t.Errorf("case %d: %q, want %q", i, got, c.want)
		}
	}
	if d, _ := Diff(bytes.NewReader(base), bytes.NewReader(cases[1].other), Options{StrictNaN: true}); d == nil {
		t.Error("StrictNaN missed a payload difference")
	}
}
