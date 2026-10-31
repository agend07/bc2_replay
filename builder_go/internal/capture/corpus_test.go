package capture

import (
	"os"
	"slices"
	"testing"
)

// goldenLogs are the oracle logs with a golden stage dump (the wire corpus logs minus t06: its dump went with SRCGHOSTS, r290).
var goldenLogs = []string{"t01", "t03", "t04", "t05", "t07", "t08", "t09", "cap_173136", "cap_181502", "cap_182734"}

// TestGoldenDumps compares Go's parse / decode / to_v1_rows with the frozen Python's golden stage dumps of the 10
// oracle logs (private: $BC2_GO_GOLDEN or the private sibling repo; skipped when absent or with -short).
func TestGoldenDumps(t *testing.T) {
	root := DefaultGolden(".")
	if root == "" || testing.Short() {
		t.Skip("golden dumps absent (set " + GoldenEnv + ") or -short")
	}
	dumps, err := GoldenDumps(root, goldenLogs)
	if err != nil {
		t.Fatal(err)
	}
	if len(dumps) == 0 {
		t.Skip("no complete golden dump")
	}
	for _, g := range dumps {
		t.Run(g.Name, func(t *testing.T) {
			t.Parallel()
			res, _, _, err := CheckDump(g)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range res {
				if c.Diff != "" {
					t.Errorf("%s: %s", c.Stage, c.Diff)
				}
			}
			if len(res) != 3 {
				t.Errorf("%d stages, want 3", len(res))
			}
		})
	}
}

// BenchmarkLoadT09 is read + v3_types.load + to_v1_rows of trial 09 (private; skipped when absent).
func BenchmarkLoadT09(b *testing.B) {
	root := DefaultGolden(".")
	dumps, _ := GoldenDumps(root, []string{"t09"})
	if root == "" || len(dumps) == 0 || !slices.ContainsFunc(dumps, func(g GoldenDump) bool { return g.Name == "t09" }) {
		b.Skip("t09 golden dump absent")
	}
	for b.Loop() {
		raw, err := os.ReadFile(dumps[0].MLog)
		if err != nil {
			b.Fatal(err)
		}
		d, err := Load(raw)
		if err != nil {
			b.Fatal(err)
		}
		_ = ToV1Rows(d)
	}
}
