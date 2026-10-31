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

	"bc2replay/builder/internal/capture"
)

// sRecord is one raw-log.bin S record (S_HEAD + name, padded to 4 bytes) for the r287 round tests.
func sRecord(flags, pseq, levelPtr uint32, name string) []byte {
	b := make([]byte, 4+52, 4+52+len(name)+3)
	for i, v := range []uint32{1, flags, 0x1000, 0, 13, 0, pseq, levelPtr, uint32(len(name))} {
		binary.LittleEndian.PutUint32(b[4+4*i:], v)
	}
	b = append(b, name...)
	b = append(b, make([]byte, (4-len(b)%4)%4)...)
	binary.LittleEndian.PutUint32(b, uint32(capture.TS)|uint32(len(b))<<8)
	return b
}

// twoRounds is cap_103557_15884's shape: round 1 = pseq 1..10416, round 2 = 10417..10423 (7 packets).
func twoRounds() []byte {
	v := uint32(capture.SLevelValid)
	return slices.Concat(sRecord(v, 0, 0xA0, "Levels/MP_A"), sRecord(0, 10416, 0, ""), sRecord(v, 10416, 0xB0, "Levels/MP_A"))
}

func TestRoundsJSONAndCacheNames(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "m-log.bin"), "x")
	write(t, filepath.Join(dir, "m-summary.json"), `{"seq": 10423}`)
	rs := roundsOf(dir)
	if len(rs) != 1 || rs[0].Packets != 10423 || !rs[0].Open {
		t.Fatalf("no raw-log: %+v", rs)
	}
	if n, err := capture.CacheName("soldier_rows.go.json", rs, ""); err != nil || n != "soldier_rows.go.json" {
		t.Errorf("single-round cache name %q %v", n, err)
	}
	write(t, filepath.Join(dir, "raw-log.bin"), string(twoRounds()))
	rs = roundsOf(dir)
	js := dumpsIndent1(roundsJSON(rs, "Levels/CAP"))
	for _, want := range []string{`"round": 1,`, `"hi": 10416,`, `"packets": 10416,`, `"seconds": 347.2,`, `"hi": null,`,
		`"packets": 7,`, `"seconds": 0.2,`, `"level": "Levels/MP_A"`} {
		if !strings.Contains(js, want) {
			t.Errorf("rounds json lacks %s:\n%s", want, js)
		}
	}
	if capture.DefaultOf(rs) != 1 || nonEmpty(rs) != 2 {
		t.Errorf("default %d, non-empty %d", capture.DefaultOf(rs), nonEmpty(rs))
	}
	if n, err := capture.CacheName("soldier_rows.go.json", rs, "2"); err != nil || n != "soldier_rows.go_r2.json" {
		t.Errorf("round 2 stamp name %q %v", n, err)
	}
	if n, _ := capture.CacheName("soldier_rows.go.json", rs, ""); n != "soldier_rows.go_r1.json" {
		t.Errorf("default stamp name %q", n)
	}
	if _, err := capture.CacheName("soldier_rows.go.json", rs, "3"); err == nil || err.Error() != "V3_ROUND=3: the capture has 2 round(s)" {
		t.Errorf("round 3: %v", err)
	}
	write(t, filepath.Join(dir, "raw-log.bin"), "\x01\x01\x00\x00bad!") // a broken raw-log.bin does not break list
	if rs := roundsOf(dir); rs != nil {
		t.Errorf("bad raw-log: %+v", rs)
	}
}

func TestEstimateRounds(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "m-log.bin"), "x")
	write(t, filepath.Join(dir, "m-summary.json"), `{"seq": 10423}`)
	write(t, filepath.Join(dir, "raw-log.bin"), string(twoRounds()))
	rs := roundsOf(dir)
	if n, _ := estimate(dir, true, rs, 2); n != 10 { // 7 packets -> EST_MIN
		t.Errorf("round 2 from packets: %d", n)
	}
	if n, _ := estimate(dir, true, rs, 1); n != 73 { // round(10416 * 0.007)
		t.Errorf("round 1 from packets: %d", n)
	}
	write(t, filepath.Join(dir, "builds.json"), `{"builds": [{"stem": "a", "seconds": 40}, {"stem": "b", "round": 2, "seconds": 12}]}`)
	if n, _ := estimate(dir, true, rs, 1); n != 40 { // an entry without round = the default round (1)
		t.Errorf("round 1 from builds: %d", n)
	}
	if n, _ := estimate(dir, true, rs, 2); n != 12 {
		t.Errorf("round 2 from builds: %d", n)
	}
}

// envPipe is fakePipe recording V3_ROUND of the process env at each call.
type envPipe struct {
	fakePipe
	rounds []string
}

func (e *envPipe) Build(ctx context.Context, in Inputs) (Result, error) {
	e.rounds = append(e.rounds, os.Getenv(roundEnv))
	return e.fakePipe.Build(ctx, in)
}

func TestBuildRounds(t *testing.T) {
	root, demos := t.TempDir(), t.TempDir()
	t.Setenv("BC2_WORK_DIR", root)
	t.Setenv("BC2_DEMOS_DIR", demos)
	t.Setenv("BC2_OLD_DEMOS_DIR", filepath.Join(root, "old"))
	t.Setenv("BC2_DATAPACK_DIR", "")
	t.Setenv(roundEnv, "")
	os.Unsetenv(roundEnv)
	capDir := filepath.Join(root, "v3-trial-07-x")
	write(t, filepath.Join(capDir, "capture.json"), `{"version": 1, "level": "Levels/MP_X", "src": "srcstem", "paired": true}`)
	write(t, filepath.Join(capDir, "m-log.bin"), "x")
	write(t, filepath.Join(capDir, "m-summary.json"), `{"seq": 10423}`)
	write(t, filepath.Join(capDir, "raw-log.bin"), string(twoRounds()))
	write(t, filepath.Join(capDir, "soldier_rows_r2.pkl"), "p") // round 2's rows present, round 1's not
	for _, e := range EXTS {
		write(t, filepath.Join(demos, "srcstem."+e), "Level: Levels/MP_X\n")
	}
	run := func(o BuildOpts) (int, string, *envPipe) {
		var out bytes.Buffer
		p := &envPipe{}
		o.Capture = capDir
		rc := Build(context.Background(), p, o, func(l string) { out.WriteString(l + "\n") })
		return rc, out.String(), p
	}
	k := func(n int) *int { return &n }
	if rc, text, _ := run(BuildOpts{Round: k(3)}); rc != 1 || !strings.Contains(text, "RESULT: FAIL --round 3: the capture has 2 round(s)\n") {
		t.Errorf("round 3: %d\n%s", rc, text)
	}
	rc, text, p := run(BuildOpts{Round: k(2)})
	for _, want := range []string{
		"  round 2 of 2: pseq 10416..end, 7 packets (0.2 s)\n",
		"STEP 2/2 build cl04_c07r2_1 (", "PROGRESS build 1/2\n", // soldiers skipped: soldier_rows_r2.pkl
	} {
		if !strings.Contains(text, want) {
			t.Errorf("round 2 transcript lacks %q:\n%s", want, text)
		}
	}
	if rc != 1 || !slices.Equal(p.rounds, []string{"2"}) || os.Getenv(roundEnv) != "" {
		t.Errorf("round 2: rc %d, pipeline V3_ROUND %q, after %q", rc, p.rounds, os.Getenv(roundEnv))
	}
	tt, _ := os.ReadFile(filepath.Join(capDir, "builds", "cl04_c07r2_1.trial.txt"))
	if !strings.Contains(string(tt), "round 2 of 2: pseq 10416..end (7 packets)") || !strings.Contains(string(tt), "soldier rows: present (1 B)") {
		t.Errorf("trial.txt:\n%s", tt)
	}
	_, text, _ = run(BuildOpts{}) // default = round 1 (most packets): soldiers run, round-1 suffix
	for _, want := range []string{"  round 1 of 2: pseq 0..10416, 10416 packets (347.2 s)\n", "STEP 2/2 build cl04_c07r1_1 (", "PROGRESS soldiers 1/3\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("default transcript lacks %q:\n%s", want, text)
		}
	}
}

// twoMaps is cap_20261031_002547_52296's shape (r294): round 1 = a short MP_A pre-round, round 2 = MP_B on the
// same connection (map rotation in one client process).
func twoMaps() []byte {
	v := uint32(capture.SLevelValid)
	return slices.Concat(sRecord(v, 0, 0xA0, "Levels/MP_A"), sRecord(0, 158, 0, ""), sRecord(v, 158, 0xB0, "Levels/MP_B"))
}

// TestBuildRoundOtherMap: the built round's level picks the SRC set, not capture.json's (first-session) level;
// an explicit capture.json "src" (and "paired") is for the first map only.
func TestBuildRoundOtherMap(t *testing.T) {
	root, demos := t.TempDir(), t.TempDir()
	old := filepath.Join(root, "old")
	t.Setenv("BC2_WORK_DIR", root)
	t.Setenv("BC2_DEMOS_DIR", demos)
	t.Setenv("BC2_OLD_DEMOS_DIR", old)
	t.Setenv("BC2_DATAPACK_DIR", "")
	os.Unsetenv(roundEnv)
	capDir := filepath.Join(root, "cap_x")
	write(t, filepath.Join(capDir, "capture.json"), `{"version": 1, "level": "Levels/MP_A", "src": "srca", "paired": true}`)
	write(t, filepath.Join(capDir, "m-log.bin"), "x")
	write(t, filepath.Join(capDir, "m-summary.json"), `{"seq": 11620}`)
	write(t, filepath.Join(capDir, "raw-log.bin"), string(twoMaps()))
	write(t, filepath.Join(capDir, "soldier_rows_r1.pkl"), "p") // rows present: the build phase runs directly
	write(t, filepath.Join(capDir, "soldier_rows_r2.pkl"), "p")
	write(t, filepath.Join(old, "companions_index.json"), `{"Levels/MP_A": "srca", "Levels/MP_B": "srcb"}`)
	for _, e := range EXTS {
		write(t, filepath.Join(demos, "srca."+e), "Level: Levels/MP_A\n")
		write(t, filepath.Join(demos, "srcb."+e), "Level: Levels/MP_B\n")
	}
	run := func(o BuildOpts) (string, *fakePipe) {
		var out bytes.Buffer
		p := &fakePipe{}
		o.Capture = capDir
		Build(context.Background(), p, o, func(l string) { out.WriteString(l + "\n") })
		return out.String(), p
	}
	text, p := run(BuildOpts{}) // default = round 2 (most packets) = MP_B: srcb from the index, unpaired
	for _, want := range []string{
		"  round 2 is Levels/MP_B (capture.json: Levels/MP_A): src from the map pack for that level\n",
		"  level Levels/MP_B, src srcb (unpaired), env SRCUNPAIRED=1\n",
		"  round 2 of 2: pseq 158..end, 11462 packets (382.1 s)\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("round 2 transcript lacks %q:\n%s", want, text)
		}
	}
	if len(p.calls) != 1 || !strings.HasPrefix(p.calls[0], "build ") || !strings.Contains(p.calls[0], "SRCUNPAIRED=1") {
		t.Errorf("calls %q", p.calls)
	}
	k := 1
	text, _ = run(BuildOpts{Round: &k}) // round 1 = the manifest's map: srca, paired, as before
	if !strings.Contains(text, "  level Levels/MP_A, src srca (paired), env -\n") || strings.Contains(text, "capture.json:") {
		t.Errorf("round 1 transcript:\n%s", text)
	}
	write(t, filepath.Join(old, "companions_index.json"), `{"Levels/MP_A": "srca"}`)
	if text, _ = run(BuildOpts{}); !strings.Contains(text, "RESULT: FAIL no data pack for Levels/MP_B\n") {
		t.Errorf("no MP_B set:\n%s", text)
	}
	if text, _ = run(BuildOpts{Src: "srca"}); !strings.Contains(text, "RESULT: FAIL src srca is Levels/MP_A, round 2 is Levels/MP_B\n") {
		t.Errorf("--src of another map:\n%s", text)
	}
}
