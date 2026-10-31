package sections

import (
	"os"
	"strings"
	"testing"

	"bc2replay/builder/internal/capture"
)

// TestGoldenDumps is the maintainer exam (skipped when the private dumps are absent): every dump's wire_sites kinds
// of this package, build_rows with the patched create, next_entries and the WIREMULTI stats. -short: t05 only;
// BC2_GO_GOLDEN_NAMES=a,b restricts the dumps.
func TestGoldenDumps(t *testing.T) {
	root := capture.DefaultGolden(".")
	if root == "" {
		t.Skip("golden dumps absent (set " + capture.GoldenEnv + ")")
	}
	var names []string
	if n := os.Getenv("BC2_GO_GOLDEN_NAMES"); n != "" {
		names = strings.Split(n, ",")
	} else if testing.Short() {
		names = []string{"t05"}
	}
	if _, err := os.Stat(DatapackDir(root)); err != nil {
		t.Skip("data pack absent")
	}
	checks, err := CheckRoot(root, names)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) == 0 {
		t.Skip("golden dumps absent")
	}
	for _, c := range checks {
		t.Logf("%s: creates %d/%d (plain %d), rows %d/%d %v stats %v, next_entries %s, wire multi %s, tail %d; "+
			"decode %.0f ms, build_rows %.0f ms, wiremulti %.0f ms",
			c.Name, c.CreatesEqual, c.Creates, c.PlainEqual, c.Rows, c.RowsPy, c.RowsEqual, c.StatsEqual, c.NextEntries,
			c.MultiStats, c.Tail, c.Decode.Seconds()*1000, c.Build.Seconds()*1000, c.Multi.Seconds()*1000)
		for _, k := range SiteKinds {
			x := c.Kinds[k]
			t.Logf("  %-16s %6d py %6d equal %6d differ", k, x.Py, x.Equal, x.Differ)
		}
		for _, d := range c.Diffs {
			t.Errorf("%s: %s", c.Name, d)
		}
		if !c.OK() {
			t.Errorf("%s: not equal", c.Name)
		}
	}
}
