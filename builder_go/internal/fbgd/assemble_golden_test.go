package fbgd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/fbgd"
)

// goldenDirs lists the private golden dumps with an assemble_args stage ($BC2_GO_GOLDEN or the sibling repo);
// $BC2_GO_GOLDEN_NAMES (comma list) restricts them. Empty when absent (CI): the exams skip.
func goldenDirs(t testing.TB) []string {
	root := capture.DefaultGolden(".")
	if root == "" {
		return nil
	}
	dirs, _ := filepath.Glob(filepath.Join(root, "*", "assemble_args.jsonl.gz"))
	want := os.Getenv("BC2_GO_GOLDEN_NAMES")
	var out []string
	for _, d := range dirs {
		d = filepath.Dir(d)
		if want == "" || strings.Contains(","+want+",", ","+filepath.Base(d)+",") {
			out = append(out, d)
		}
	}
	return out
}

// TestAssembleGolden: Assemble from every assemble_args dump equals assemble_out (fbgd/fbgk sha, info) and the
// companion files encoded from wire_companions equal outputs.json. Slow on the full set (~4 min); -short runs t05.
func TestAssembleGolden(t *testing.T) {
	dirs := goldenDirs(t)
	if len(dirs) == 0 {
		t.Skip("golden dumps absent (set " + capture.GoldenEnv + ")")
	}
	for _, d := range dirs {
		if testing.Short() && filepath.Base(d) != "t05" {
			continue
		}
		c, err := fbgd.CheckAssemble(d)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(d), err)
			continue
		}
		t.Logf("%-28s rows %7d fbgd %v fbgk %v info %v comp %d/6 assemble %v", filepath.Base(d), c.Rows, c.FBGD, c.FBGK, c.Info,
			c.Companions, c.Run)
		if !c.OK() {
			t.Errorf("%s: fbgd %v fbgk %v info %v (go %s py %s) companions %d/6", filepath.Base(d), c.FBGD, c.FBGK, c.Info,
				c.GotInfo, c.WantInfo, c.Companions)
		}
	}
}

// BenchmarkAssembleGolden times Assemble alone on $BC2_GO_GOLDEN_NAMES (default t09). Assemble moves row times in
// place, so each iteration loads fresh rows (load untimed).
func BenchmarkAssembleGolden(b *testing.B) {
	if os.Getenv("BC2_GO_GOLDEN_NAMES") == "" {
		os.Setenv("BC2_GO_GOLDEN_NAMES", "t09")
	}
	dirs := goldenDirs(b)
	if len(dirs) == 0 {
		b.Skip("golden dumps absent")
	}
	for b.Loop() {
		b.StopTimer()
		a, err := fbgd.LoadAssembleArgs(dirs[0])
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, _, _, err := fbgd.Assemble(a.Rows, a.Tpl, a.T0, a.KeySnap); err != nil {
			b.Fatal(err)
		}
	}
}
