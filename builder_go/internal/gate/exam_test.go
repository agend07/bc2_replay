package gate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestExam replays recorded frozen-gate runs in-process (P3 exam). It needs private files and skips without them:
// BC2_GATE_EXAM = a dir holding manifest.json ({"runs": [{"id", "args"}]}) and out/py/<id>/{gate.json, gate.txt, meta.json}
// written by the frozen fbgd_gate.py; BC2_GATE_EXAM_RUNS = optional comma list of run ids (default: all with a py result).
func TestExam(t *testing.T) {
	dir := os.Getenv("BC2_GATE_EXAM")
	if dir == "" {
		t.Skip("BC2_GATE_EXAM not set (private exam corpus)")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Skip(err)
	}
	var m struct {
		Runs []struct {
			ID   string
			Args []string
		}
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	only := map[string]bool{}
	for _, id := range strings.Split(os.Getenv("BC2_GATE_EXAM_RUNS"), ",") {
		if id != "" {
			only[id] = true
		}
	}
	tmp := t.TempDir()
	n := 0
	for _, r := range m.Runs {
		if len(only) > 0 && !only[r.ID] {
			continue
		}
		py := filepath.Join(dir, "out", "py", r.ID)
		want, err := os.ReadFile(filepath.Join(py, "gate.json"))
		if err != nil {
			continue
		}
		wantTxt, _ := os.ReadFile(filepath.Join(py, "gate.txt"))
		var meta struct{ RC int }
		mb, _ := os.ReadFile(filepath.Join(py, "meta.json"))
		json.Unmarshal(mb, &meta)
		out := filepath.Join(tmp, r.ID+".json")
		o, err := ParseArgs(append(append([]string{}, r.Args...), "--json", out))
		if err != nil {
			t.Fatal(err)
		}
		var stdout bytes.Buffer
		rc, err := Run(o, &stdout)
		if err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		got, _ := os.ReadFile(out)
		gotTxt := stdout.String()
		if runtime.GOOS == "windows" { // v3_trial's gate.txt = Path.write_text of the child's stdout
			gotTxt = strings.ReplaceAll(gotTxt, "\n", "\r\n")
		}
		if rc != meta.RC || !bytes.Equal(got, want) || gotTxt != string(wantTxt) {
			t.Errorf("%s: rc %d/%d json equal %v txt equal %v", r.ID, rc, meta.RC, bytes.Equal(got, want), gotTxt == string(wantTxt))
		}
		os.Remove(out)
		n++
	}
	t.Logf("%d runs equal to the frozen gate", n)
}
