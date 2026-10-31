package pipeline

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/pyc"
)

func TestRecipe(t *testing.T) {
	r := DefaultRecipe()
	if r.C4UPD != "1" || r.SHIFTF != "2000" || r.JOINKEY != "auto" || r.SRC != "patched_20261023_165152_405" {
		t.Fatalf("defaults: %+v", r)
	}
	if err := r.Apply([][2]string{{"C4UPD", ""}, {"SRCUNPAIRED", "1"}, {"PNAME", "17"}, {"SRCGHOSTS", "1"}}); err != nil {
		t.Fatal(err)
	}
	if r.C4UPD != "" || r.SRCUNPAIRED != "1" {
		t.Fatalf("overrides: %+v", r)
	}
	var mo *MaintainerOnly
	for _, kv := range [][2]string{{"COMPDUMP", "x"}, {"TPL", "x"}, {"V3_WIRE_ENGINE", "uc"}, {"V3_SOLDIER_ENGINE", "uc"}} {
		err := r.Set(kv[0], kv[1])
		if !errors.As(err, &mo) || !strings.Contains(err.Error(), "maintainer-only (Python builder)") {
			t.Errorf("%s=%s: %v", kv[0], kv[1], err)
		}
	}
	if err := r.Set("V3_WIRE_ENGINE", "py"); err != nil {
		t.Error(err)
	}
	d := RecipeDefaults()
	if d["TPL"] != nil || d["UPD"] == nil || len(d) != len(RecipeKeys()) {
		t.Errorf("RecipeDefaults: %d keys", len(d))
	}
	env := map[string]string{"C4UPD": "", "IDBASE": "130", "CUTF": "5"}
	r2, err := RecipeFromLookup(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil || r2.C4UPD != "" || r2.UPD == "" || r2 != func() Recipe { x := DefaultRecipe(); x.C4UPD = ""; return x }() {
		t.Errorf("RecipeFromLookup: %+v %v", r2, err)
	}
}

func TestRepr(t *testing.T) {
	c := &fbgd.Counter{}
	c.Add("a", 1)
	c.Add("b", 3)
	c.Add("c", 1)
	o := pyc.NewObject()
	o.Set("frames", 3)
	for _, x := range []struct {
		got, want string
	}{
		{printLine("wire multi", c), "wire multi {'a': 1, 'b': 3, 'c': 1}"},
		{repr(counterRepr{c}), "Counter({'b': 3, 'a': 1, 'c': 1})"},
		{repr(counterRepr{&fbgd.Counter{}}), "Counter()"},
		{printLine(tuple{15, 2}, tuple{1}, []any{}, pySet(nil), o, 1.5, nil), "(15, 2) (1,) [] set() {'frames': 3} 1.5 None"},
		{strRepr("it's"), `"it's"`},
		{strRepr("é\x01"), `'é\x01'`},
	} {
		if x.got != x.want {
			t.Errorf("got %s want %s", x.got, x.want)
		}
	}
}

func TestEncpos(t *testing.T) {
	tb := []int64{1, 3, 5, 7, 9, 11, 13}
	s, err := encpos([3]float64{1, -0.5, 0}, tb, 5)
	// k = 32, -16, 0: width 1 + 5 = 6 holds -32..31 (32 does not fit) -> index 2, width 3 + 5 = 8
	if err != nil || s != "010"+"00100000"+"11110000"+"00000000" {
		t.Errorf("encpos = %q, %v", s, err)
	}
	if _, err := encpos([3]float64{0.01, 0, 0}, tb, 5); err == nil || !strings.Contains(err.Error(), "inexact") {
		t.Errorf("inexact: %v", err)
	}
}

// TestDevCheckT05 runs the maintainer check on the trial-05 golden dump when the private dumps exist.
func TestDevCheckT05(t *testing.T) {
	root := capture.DefaultGolden(".")
	if root == "" {
		t.Skip("no golden dumps (private)")
	}
	ds, err := DevDumps(root, []string{"t05"})
	if err != nil || len(ds) == 0 {
		t.Skip("t05 dump absent")
	}
	if _, err := os.Stat(DevOptions{}.datapack(ds[0].Dir)); err != nil {
		t.Skip("data pack absent")
	}
	for _, r := range DevCheck(context.Background(), ds[0].Dir, ds[0].MLog, DevOptions{Work: t.TempDir()}) {
		if !r.OK() {
			t.Errorf("%s: ckpts %d/%d files %d/%d lines %d/%d err %v: %s %s %s", r.Name, r.Ckpts, r.CkptsPy, r.Files, r.FilesPy,
				r.Lines, r.LinesPy, r.Err, r.FirstBad, r.FileDiff, r.LineDiff)
		}
	}
}
