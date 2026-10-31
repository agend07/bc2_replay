package events

import (
	"os"
	"strings"
	"testing"

	"bc2replay/builder/internal/capture"
)

// TestGoldenEvents runs the maintainer exam over every private golden dump (skipped when absent; BC2_GO_GOLDEN_NAMES
// = comma list restricts it). The template(SRC) dumps need BC2_DEMOS_DIR (or BC2_ARCHIVE_DIR); without it they
// stop after registry and are reported, not failed.
func TestGoldenEvents(t *testing.T) {
	root := capture.DefaultGolden(".")
	if root == "" {
		t.Skip("no golden dumps")
	}
	var want []string
	if v := os.Getenv("BC2_GO_GOLDEN_NAMES"); v != "" {
		want = strings.Split(v, ",")
	}
	dumps, err := capture.GoldenDumps(root, want)
	if err != nil || len(dumps) == 0 {
		t.Skip("no golden dumps with their m-log", err)
	}
	opt := DefaultOptions(root)
	for _, g := range dumps {
		c, err := CheckDump(g, opt)
		if err != nil {
			t.Errorf("%s: %v", g.Name, err)
			continue
		}
		t.Logf("%-24s %s, files %d/6, load %.0f ms, deletes %.1f ms, registry %.1f ms, companions %.1f ms", g.Name,
			c.Summary(), c.Files, ms(c.Decode.Seconds()), ms(c.Deletes.Seconds()), ms(c.Registry.Seconds()),
			ms(c.Companions.Seconds()))
		for _, s := range c.Stages {
			if s.Diff != "" {
				t.Errorf("%s: %s %s", g.Name, s.Stage, s.Diff)
			}
		}
		if c.Skip != "" {
			t.Logf("%s: skipped: %s", g.Name, c.Skip)
			continue
		}
		if c.Files != 6 {
			t.Errorf("%s: %d/6 companion files equal", g.Name, c.Files)
		}
	}
}

func ms(s float64) float64 { return s * 1000 }
