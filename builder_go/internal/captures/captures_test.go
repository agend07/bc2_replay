package captures

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"bc2replay/builder/internal/pyc"
)

func TestPySplitlinesStrip(t *testing.T) {
	cases := map[string][]string{
		"":             nil,
		"a\nb\n":       {"a", "b"},
		"a\r\nb\rc":    {"a", "b", "c"},
		"a\n\nb":       {"a", "", "b"},
		"x\x1cy\u2028": {"x", "y"},
	}
	for in, want := range cases {
		if got := pySplitlines(in); !slices.Equal(got, want) {
			t.Errorf("splitlines(%q) = %q, want %q", in, got, want)
		}
	}
	if got := pyStrip("\x1c  a b \t\x1f"); got != "a b" {
		t.Errorf("strip = %q", got)
	}
}

func TestPySum(t *testing.T) {
	// CPython 3.12+: sum([0.1] * 10) == 1.0 (Neumaier), plain addition gives 0.9999999999999999
	v := make([]float64, 10)
	for i := range v {
		v[i] = 0.1
	}
	if got := pySum(v); got != 1.0 {
		t.Errorf("sum = %v", got)
	}
	if got := pySum([]float64{13.0, 18.5}); got != 31.5 {
		t.Errorf("sum = %v", got)
	}
}

func TestEstimateLine(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "m-summary.json"), `{"seq": 2802}`)
	if n, ph := estimate(dir, true, nil, 0); n != 20 || ph != nil { // round(2802 * 0.007) = round(19.614) = 20
		t.Errorf("estimate from packets = %d %v", n, ph)
	}
	write(t, filepath.Join(dir, "builds.json"), `{"builds": [{"stem": "a", "seconds": 31.6, "phases": {"soldiers": 9.5, "build": 13.0, "gate": 18.5}}]}`)
	n, ph := estimate(dir, false, nil, 0)
	if got := estimateLine(n, ph); got != "ESTIMATE 32 build=13.0 gate=18.5" {
		t.Errorf("estimate line = %q", got)
	}
	n, ph = estimate(dir, true, nil, 0)
	if got := estimateLine(n, ph); got != "ESTIMATE 41 soldiers=9.5 build=13.0 gate=18.5" {
		t.Errorf("estimate line = %q", got)
	}
}

func TestGateTextAndFailReason(t *testing.T) {
	out := "PHASE build\ntrial x\ngate vs cl04_1088 (1 s): 1007 ERROR (1007 unseen-* on 12 ghosts, 0 structural), 3 WARNING\nRESULT: OK  (cl04_x)\n"
	gc := gateCounts(out)
	if got := gateText(gc); got != "gate 1007 unseen on 12 ghosts, 0 structural" {
		t.Errorf("gate text = %q", got)
	}
	if s, _ := pyc.Dumps(gc, pyc.DumpOptions{}); s != `{"baseline": "cl04_1088", "errors": 1007, "unseen": 1007, "ghosts": 12, "structural": 0}` {
		t.Errorf("gate counts = %s", s)
	}
	clean := pyc.NewObject()
	clean.Set("errors", int64(0))
	if gateText(clean) != "gate clean" || gateText(nil) != "" {
		t.Error("gate clean / unknown")
	}
	fo := "PHASE gate\ngate vs b (2 s): 5 ERROR (3 unseen-* on 1 ghosts, 2 structural)\n  ERROR structural: 2 shape-mismatch\nRESULT: FAIL  (cl04_x)\n"
	if got := failReason(fo, ""); got != "ERROR structural: 2 shape-mismatch" {
		t.Errorf("fail reason = %q", got)
	}
	if got := failReason("PHASE build\n", "Traceback\nSystemExit: boom\r\n"); got != "SystemExit: boom" {
		t.Errorf("fail reason stderr = %q", got)
	}
	if got := failReason("", ""); got != "v3_trial gave no output" {
		t.Errorf("fail reason empty = %q", got)
	}
}

func TestSrcVerdict(t *testing.T) {
	cases := []struct{ line, v string }{
		{"crebase_vs_src matched game t3 4/0 t31 10/0 load t31 2/0", ""},
		{"crebase_vs_src matched game t3 4/0 t31 10/2 load t31 2/1", "FAIL"},
		{"crebase_vs_src matched game t31 10/2 unpaired t31 plausible 2 implausible 0", "WARN"},
		{"crebase_vs_src matched game t31 10/2 unpaired t31 plausible 1 implausible 1", "FAIL"},
		{"crebase_vs_src matched game t31 10/0 first unmatched game t31 9/9", ""},
	}
	for _, c := range cases {
		if v, msg := srcVerdict(c.line); v != c.v {
			t.Errorf("verdict(%q) = %q %q, want %q", c.line, v, msg, c.v)
		}
	}
	_, msg := srcVerdict(cases[1].line)
	if msg != "crebase_vs_src: 3 spawner (type 31) creates not in SRC (un-rebased origin vec3? r190/r192)" {
		t.Errorf("msg = %q", msg)
	}
}

// rawHeader is a synthetic .bc2raw header (format 1, FILETIME of ts).
func rawHeader(ts time.Time) []byte {
	b := make([]byte, 128)
	copy(b, "BC2RAW\x00\x00")
	binary.LittleEndian.PutUint16(b[8:], 1)
	binary.LittleEndian.PutUint64(b[64:], uint64(ts.Unix())*10_000_000+116444736000000000)
	return b
}

func TestListAndPrune(t *testing.T) {
	root, raw, demos := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("BC2_WORK_DIR", root)
	t.Setenv("BC2_CAPTURES_DIR", raw)
	t.Setenv("BC2_DEMOS_DIR", demos)
	t.Setenv("BC2_OLD_DEMOS_DIR", filepath.Join(root, "no_old"))
	t.Setenv("BC2_DATAPACK_DIR", "")
	t.Setenv("V3_CAPTURE_ROOTS", "")
	capDir := filepath.Join(root, "cap_a")
	write(t, filepath.Join(capDir, "capture.json"), `{"version": 1, "level": "Levels/MP_X", "label": "", "paired": false, "recorded": "2026-10-27T19:15:02", "source": {"file": "A.bc2raw"}}`)
	write(t, filepath.Join(capDir, "m-log.bin"), "x")
	write(t, filepath.Join(capDir, "m-summary.json"), `{"seq": 100, "records": 5, "drops": 0, "bytes": 1}`)
	write(t, filepath.Join(root, "notacap", "x.txt"), "")
	write(t, filepath.Join(capDir, "builds.json"), `{"builds": [{"stem": "cl04_x"}, {"stem": "cl04_y", "round": 2}, {"stem": "cl04_x", "round": 1}, {"stem": "cl04_gone"}]}`)
	for _, st := range []string{"cl04_x", "cl04_y"} {
		for _, e := range EXTS {
			write(t, filepath.Join(demos, st+"."+e), "")
		}
	}
	old := time.Now().Add(-time.Hour)
	for i, n := range []string{"a.bc2raw", "b.bc2raw", "c.bc2raw"} {
		p := filepath.Join(raw, n)
		if err := os.WriteFile(p, rawHeader(old.Add(time.Duration(i)*time.Minute)), 0o666); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, old, old)
	}
	write(t, filepath.Join(raw, "bad.bc2raw"), "short")
	caps := List(nil)
	if len(caps) != 4 { // cap_a + b, c, bad (a is extracted: source.file matches case-insensitively)
		t.Fatalf("list: %d entries: %s", len(caps), ListJSON(caps))
	}
	js := ListJSON(caps)
	for _, want := range []string{`"reason": "no data pack for Levels/MP_X"`, `"seconds": 3.3`, `"reason": "bad .bc2raw: short header (5 bytes)"`,
		`"reason": "raw capture: extract first"`, `"default_round": 1`, `"default_round": null`, `"rounds": []`} {
		if !strings.Contains(js, want) {
			t.Errorf("list json lacks %s:\n%s", want, js)
		}
	}
	if b := dumpsIndent1(get(caps[0], "builds")); b != "[\n {\n  \"stem\": \"cl04_x\",\n  \"round\": null\n },\n {\n  \"stem\": \"cl04_y\",\n  \"round\": 2\n }\n]" {
		t.Errorf("builds: %s", b)
	}
	var lines []string
	say := func(l string) { lines = append(lines, l) }
	if rc := Prune(0, false, true, nil, say); rc != 1 || lines[0] != "RESULT: FAIL nothing to do: give --keep N (N >= 1) and/or --extracted" {
		t.Errorf("prune without a mode: %d %q", rc, lines)
	}
	lines = nil
	if rc := Prune(1, true, true, nil, say); rc != 0 {
		t.Fatalf("prune rc %d", rc)
	}
	want := []string{
		"WOULD-DELETE a.bc2raw 128 extracted -> cap_a",
		"KEEP c.bc2raw 128 newest 1",
		"WOULD-DELETE b.bc2raw 128 older than the newest 1",
	}
	got := strings.Join(lines, "\n")
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("prune lacks %q:\n%s", w, got)
		}
	}
	if !strings.HasPrefix(lines[len(lines)-1], "RESULT: OK would delete 2 of 4 raw files, 256 bytes, kept 1, skipped 1") {
		t.Errorf("prune result: %q", lines[len(lines)-1])
	}
	lines = nil
	Prune(1, true, false, nil, say)
	if exists(filepath.Join(raw, "a.bc2raw")) || exists(filepath.Join(raw, "b.bc2raw")) || !exists(filepath.Join(raw, "c.bc2raw")) {
		t.Errorf("prune deleted the wrong files: %q", lines)
	}
}

func TestLock(t *testing.T) {
	t.Setenv("BC2_WORK_DIR", t.TempDir())
	if r := takeLock(); r != "" {
		t.Fatal(r)
	}
	if r := takeLock(); !strings.HasPrefix(r, "another build is running (pid ") {
		t.Fatalf("second lock: %q", r)
	}
	releaseLock()
	write(t, lockPath(), "999999999") // a dead pid is taken over
	if r := takeLock(); r != "" {
		t.Fatal(r)
	}
	releaseLock()
}

// fakePipe writes the 9 files and answers like the Python core enough for the driver protocol.
type fakePipe struct{ calls []string }

func (f *fakePipe) RecipeDefaults() (map[string]*string, error) {
	u := "0x1"
	return map[string]*string{"UPD": &u, "TPL": nil}, nil
}

func (f *fakePipe) Soldiers(_ context.Context, in Inputs) (Result, error) {
	f.calls = append(f.calls, "soldiers")
	return Result{Stdout: "TOTAL 3 rows\n"}, os.WriteFile(filepath.Join(filepath.Dir(in.Log), "soldier_rows.pkl"), []byte("p"), 0o666)
}

func (f *fakePipe) Build(_ context.Context, in Inputs) (Result, error) {
	f.calls = append(f.calls, "build "+in.Suffix+" "+kvLine(in.Env))
	return Result{RC: 1, Stderr: "boom\n"}, nil
}

func TestBuildProtocolEarlyFail(t *testing.T) {
	root, demos := t.TempDir(), t.TempDir()
	t.Setenv("BC2_WORK_DIR", root)
	t.Setenv("BC2_DEMOS_DIR", demos)
	t.Setenv("BC2_OLD_DEMOS_DIR", filepath.Join(root, "old"))
	t.Setenv("BC2_DATAPACK_DIR", "")
	capDir := filepath.Join(root, "v3-trial-07-x")
	write(t, filepath.Join(capDir, "capture.json"), `{"version": 1, "level": "Levels/MP_X", "src": "srcstem", "paired": true, "env": {"A": 1}}`)
	write(t, filepath.Join(capDir, "m-log.bin"), "x")
	for _, e := range EXTS {
		write(t, filepath.Join(demos, "srcstem."+e), "Level: Levels/MP_X\n")
	}
	var out bytes.Buffer
	p := &fakePipe{}
	rc := Build(context.Background(), p, BuildOpts{Capture: capDir, Env: []string{"UPD+=0x2"}}, func(l string) { out.WriteString(l + "\n") })
	text := out.String()
	if rc != 1 {
		t.Fatalf("rc %d\n%s", rc, text)
	}
	for _, want := range []string{
		"STEP 1/2 resolve v3-trial-07-x\n",
		"  level Levels/MP_X, src srcstem (paired), env A=1 UPD+=0x2\n",
		"ESTIMATE 10\n",
		"STEP 2/2 build cl04_c07_1 (v3_trial: build, gate; ~0.5-3 min)\n",
		"PROGRESS soldiers 1/3\n", "PROGRESS build 2/3\n",
		"  build: FAIL rc=1 (0 s): boom\n",
		"RESULT: FAIL cl04_c07_1: build: FAIL rc=1 (0 s): boom\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript lacks %q:\n%s", want, text)
		}
	}
	if !slices.Equal(p.calls, []string{"soldiers", "build c07_1 A=1 UPD=0x1,0x2"}) {
		t.Errorf("calls %q", p.calls)
	}
	tt, _ := os.ReadFile(filepath.Join(capDir, "builds", "cl04_c07_1.trial.txt"))
	if !strings.HasPrefix(osTextUndo(string(tt)), "PHASE soldiers\nPHASE build\nPHASE done\ntrial v3-trial-07-x  log 1 B") {
		t.Errorf("trial.txt:\n%s", tt)
	}
	if exists(lockPath()) {
		t.Error("lock left behind")
	}
}

func osTextUndo(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o666); err != nil {
		t.Fatal(err)
	}
}

// stampPipe is fakePipe with the Go pipeline's SoldierFile (a stamp in place of soldier_rows.pkl).
type stampPipe struct{ fakePipe }

func (s *stampPipe) SoldierFile(capDir string) string { return filepath.Join(capDir, "stamp.json") }

func TestSoldierFile(t *testing.T) {
	d := t.TempDir()
	if got, want := soldierFile(&fakePipe{}, d), join(d, "soldier_rows.pkl"); got != want {
		t.Fatalf("fallback %q, want %q", got, want)
	}
	sp := &stampPipe{}
	if got, want := soldierFile(sp, d), filepath.Join(d, "stamp.json"); got != want {
		t.Fatalf("method %q, want %q", got, want)
	}
	write(t, filepath.Join(d, "soldier_rows.pkl"), "p") // a pkl alone does not count for the stamp pipeline
	if p := plan(sp, d, false, false, nil, 0); !slices.Contains(p, "soldiers") {
		t.Fatalf("plan %v without the stamp should convert", p)
	}
	write(t, filepath.Join(d, "stamp.json"), "{}")
	if p := plan(sp, d, true, false, nil, 0); slices.Contains(p, "soldiers") || !slices.Contains(p, "compare") {
		t.Fatalf("plan %v with the stamp", p)
	}
}
