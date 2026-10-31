package wire_test

import (
	"os"
	"strings"
	"testing"

	"bc2replay/builder/internal/wire/wirecheck"
)

// TestCorpus replays the private r274 Unicorn oracle corpus ($BC2_WIRE_CORPUS, default the private sibling repo's
// work/private/wire_oracle): every run must be equal (r277: 81,055/81,055, diag 0).
// Skipped when the corpus is absent (CI). With
// $BC2_WIRE_DECODED (decoded m-logs, private) the site wrappers are replayed end to end too.
func TestCorpus(t *testing.T) {
	dir := wirecheck.DefaultCorpus(".")
	names := wirecheck.Available(dir, nil)
	if dir == "" || len(names) == 0 {
		t.Skip("wire oracle corpus absent (set " + wirecheck.CorpusEnv + ")")
	}
	decoded := os.Getenv(wirecheck.DecodedEnv)
	runs, sites, err := wirecheck.Load(dir, names, decoded != "")
	if err != nil {
		t.Fatal(err)
	}
	rep := wirecheck.Check(runs, 0, 3)
	rep.Logs = names
	var b strings.Builder
	rep.Print(&b, true)
	t.Log("\n" + b.String())
	if rep.Total.Differ != 0 || rep.Total.Unported != 0 {
		t.Errorf("differ %d unported %d", rep.Total.Differ, rep.Total.Unported)
	}
	if rep.Total.Diag != 0 || len(names) == len(wirecheck.Logs) && rep.Total.Runs != 81055 {
		t.Errorf("diag %d runs %d (want 0 / 81055)", rep.Total.Diag, rep.Total.Runs)
	}
	if decoded == "" {
		t.Log("site exam skipped (set " + wirecheck.DecodedEnv + ")")
		return
	}
	srep, err := wirecheck.CheckSites(decoded, names, runs, sites, 3)
	if err != nil {
		t.Fatal(err)
	}
	b.Reset()
	srep.Print(&b)
	t.Log("\n" + b.String())
	if !srep.OK() {
		t.Error("site exam: differ")
	}
}
