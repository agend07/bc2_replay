package soldier

import (
	"testing"

	"bc2replay/builder/internal/capture"
)

// TestGoldenSoldierRows: Convert over Go's decode of every golden dump's m-log == the dump's soldier_rows stage.
// Skips when the private dumps are absent.
func TestGoldenSoldierRows(t *testing.T) {
	root := capture.DefaultGolden(".")
	if root == "" {
		t.Skip("no golden dumps (set " + capture.GoldenEnv + ")")
	}
	dumps, err := capture.GoldenDumps(root, nil)
	if err != nil || len(dumps) == 0 {
		t.Skip("no golden dumps under", root, err)
	}
	if testing.Short() {
		dumps = dumps[:1]
	}
	// one worker: the output must not depend on the worker count
	if c, err := CheckDump(dumps[0], 1); err != nil || c.Diff != "" {
		t.Errorf("%s (1 worker): %v %s", dumps[0].Name, err, c.Diff)
	}
	for _, g := range dumps {
		c, err := CheckDump(g, 0)
		if err != nil {
			t.Errorf("%s: %v", g.Name, err)
			continue
		}
		t.Logf("%-28s in %7d out %7d equal %7d convert %-12v TOTAL %s (py line found: %v)", g.Name, c.In, c.Out, c.Lines,
			c.Convert, c.Total, c.TotalPy != "")
		if c.Diff != "" {
			t.Errorf("%s: %s", g.Name, c.Diff)
		}
	}
}
