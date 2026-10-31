package gate

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"bc2replay/builder/internal/bits"
)

// A synthetic demo (no recorded data): keyframe {5: soldier, 7: spawner}, a delta deleting 7 and updating 5 with a
// reference to the dead 7, then a delta with an empty section flag (bit round trip) and a never-live reference.
type sw struct{ bits.Writer }

func (x *sw) put(v uint64, n int) {
	if err := x.Put(v, n); err != nil {
		panic(err)
	}
}

func (x *sw) chunk(idx, nbits int, val uint64, refs ...[2]int) {
	x.put(uint64(idx), 6)
	x.put(uint64(nbits), 16)
	x.put(val, nbits)
	x.put(uint64(len(refs)), 6)
	for _, r := range refs {
		x.put(uint64(r[0]), 16)
		x.put(uint64(r[1]), 1)
	}
}

func (x *sw) frame(marker int) []byte {
	p := x.Bytes()
	out := []byte{byte(marker), 0, 0, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(out[1:], uint32(len(p)))
	binary.LittleEndian.PutUint32(out[5:], uint32(x.Len()))
	return append(out, p...)
}

func synthDemo() []byte {
	var k sw
	k.put(1, 1)
	k.put(1, 16)
	k.put(5, 16)
	k.put(1, 16)
	k.chunk(0, 8, 10)
	k.put(2<<16, 32)
	k.put(3, 2)
	k.chunk(0, 3, 5)
	k.chunk(1, 4, 1, [2]int{7, 1})
	k.put(7, 16)
	k.put(0, 16)
	k.chunk(2, 20, 0b011111_1_000000000101_0)
	var d sw
	d.put(3, 31)
	d.put(4, 31)
	d.put(1, 1)
	d.put(0, 16)
	d.put(7, 16)
	d.put(1, 1)
	d.put(0, 16)
	d.put(5, 16)
	d.put(0, 1)
	d.put(1, 1)
	d.put(0, 1)
	d.put(1, 1)
	d.chunk(1, 5, 3, [2]int{7, 0})
	var e sw
	e.put(0, 62)
	e.put(0, 1)
	e.put(1, 1)
	e.put(1, 16)
	e.put(5, 16)
	e.put(0, 1)
	e.put(1, 1)
	e.put(0, 2)
	e.put(5, 16)
	e.put(0, 1)
	e.put(1, 1)
	e.put(1, 1)
	e.chunk(0, 3, 1, [2]int{99, 0})
	e.put(0, 1)
	data := append([]byte("EA\x00v1\x00"), k.frame(1)...)
	data = append(data, d.frame(2)...)
	data = append(data, e.frame(2)...)
	return append(data, 0)
}

const synthWhitelist = `{"header":{"gate_version":6,"lint_version":6,"corpus_demos":1,"corpus_sha":"x","date":"2026-10-29"},` +
	`"allowed":{"widths":{"[\"00001010/8\", -1, -1]":[8],"[\"00001010/8\", 0, 0]":[3],"[\"00001010/8\", 0, 1]":[4,6,9,2]},` +
	`"indices":{"[\"00001010/8\", -1, -1]":[0],"[\"00001010/8\", 0, 0]":[0],"[\"00001010/8\", 0, 1]":[1]},` +
	`"forms":{"[\"00001010/8\", -1, -1]":[[]],"[\"00001010/8\", 0, 0]":[[]],"[\"00001010/8\", 0, 1]":[[1],[0,1],[]]},` +
	`"shapes":{"00001010/8":[[2]],"00001010/9":[[2]]},"stale":{}}}`

// synthExpected is the frozen fbgd_gate.py stdout on the synthetic files (generated once by running the oracle on the
// files TestSynthetic writes when GATE_SYNTH_DUMP is set; the demo path is the only file-dependent text).
const synthExpected = `ERROR {"rule": "unseen-shape", "frame": 0, "ghost": 7, "signature": "01111110/*0", "shape": [], "create_width": 20, "allowed": []}
ERROR {"frame": 0, "ghost": 7, "signature": "01111110/*0", "section": -1, "create_width": 20, "property": -1, "width": 20, "index": 2, "refs": [], "rule": "unseen-indices", "allowed": []}
ERROR {"frame": 0, "ghost": 7, "signature": "01111110/*0", "section": -1, "create_width": 20, "property": -1, "width": 20, "index": 2, "refs": [], "rule": "unseen-forms", "allowed": []}
ERROR {"frame": 1, "ghost": 5, "signature": "00001010/8", "section": 0, "create_width": 8, "property": 1, "width": 5, "index": 1, "refs": [[7, 0]], "rule": "unseen-widths", "allowed": [4, 6, 2, 9]}
ERROR {"frame": 1, "ghost": 5, "signature": "00001010/8", "section": 0, "create_width": 8, "property": 1, "width": 5, "index": 1, "refs": [[7, 0]], "rule": "unseen-forms", "allowed": [[], [0, 1], [1]]}
ERROR {"frame": 1, "ghost": 5, "signature": "00001010/8", "section": 0, "create_width": 8, "property": 1, "width": 5, "index": 1, "refs": [[7, 0]], "rule": "unapproved-stale-reference", "target": 7}
ERROR {"rule": "duplicate-dirty", "frame": 2}
ERROR {"rule": "bit-roundtrip", "frame": 2}
ERROR {"frame": 2, "ghost": 5, "signature": "00001010/8", "section": 0, "create_width": 8, "property": 0, "width": 3, "index": 0, "refs": [[99, 0]], "rule": "unseen-forms", "allowed": [[]]}
ERROR {"frame": 2, "ghost": 5, "signature": "00001010/8", "section": 0, "create_width": 8, "property": 0, "width": 3, "index": 0, "refs": [[99, 0]], "rule": "never-live-reference", "target": 99}
ERROR {"rule": "missing-companion", "extension": "fbgk"}
ERROR {"rule": "missing-companion", "extension": "fbgdd"}
ERROR {"rule": "missing-companion", "extension": "fbgdk"}
ERROR {"rule": "missing-companion", "extension": "fbcdd"}
ERROR {"rule": "missing-companion", "extension": "fbcdk"}
ERROR {"rule": "missing-companion", "extension": "fbidd"}
ERROR {"rule": "missing-companion", "extension": "fbidk"}
ERROR {"rule": "missing-companion", "extension": "txt"}
WARNING {"rule": "no-baseline", "note": "supply --baseline for regression audit"}
GHOST {"ghost": 7, "signature": "01111110/*0", "shape": [], "frame": 0, "errors": 3, "rules": {"unseen-shape": 1, "unseen-indices": 1, "unseen-forms": 1}, "slots": 1, "create_width": 20, "corpus_widths": [], "same_sections": false, "known_create": false}
GHOST {"ghost": 5, "signature": "00001010/8", "shape": null, "frame": 1, "errors": 3, "rules": {"unseen-widths": 1, "unseen-forms": 2}, "slots": 2, "create_width": 8, "corpus_widths": [8, 9], "same_sections": false, "known_create": true}
SUMMARY: 18 ERROR (6 unseen-* on 2 ghosts, 12 structural), 0 INHERITED_ERROR, 1 WARNING; warning rules {'no-baseline': 1}
`

func TestSynthetic(t *testing.T) {
	dir := t.TempDir()
	if d := os.Getenv("GATE_SYNTH_DUMP"); d != "" {
		dir = d
	}
	demo := filepath.Join(dir, "synth.fbgd")
	wl := filepath.Join(dir, "wl.json")
	if err := os.WriteFile(demo, synthDemo(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wl, []byte(synthWhitelist), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	o, err := ParseArgs([]string{demo, "--no-baseline", "--whitelist", wl, "--verbose", "--json", filepath.Join(dir, "g.json")})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := Run(o, &out)
	if err != nil || rc != 1 {
		t.Fatalf("rc %d err %v", rc, err)
	}
	if got := out.String(); got != synthExpected {
		t.Errorf("stdout differs from the frozen gate:\n%s", got)
	}
	// --baseline-summary (frozen gate: 13 ERROR / 5 INHERITED on these files): structural errors are never inherited
	sum := filepath.Join(dir, "sum.json")
	os.WriteFile(sum, []byte(`{"version":6,"lint_version":6,"first_soldier":null,"corpus_sha":"x","forms":[`+
		`[["unseen-shape","01111110/*0",null,null,null,null,[],[]],false],`+
		`[["unseen-widths","01111110/*0",-1,-1,20,2,[],[]],false],[["unseen-indices","01111110/*0",-1,-1,20,2,[],[]],false],`+
		`[["unseen-forms","01111110/*0",-1,-1,20,2,[],[]],false],[["unseen-widths","00001010/8",0,1,5,1,[0],[]],false],`+
		`[["unseen-forms","00001010/8",0,1,5,1,[0],[]],false]]}`), 0o644)
	out.Reset()
	o, _ = ParseArgs([]string{demo, "--baseline-summary", sum, "--baseline-errors", "--whitelist=" + wl})
	if rc, err := Run(o, &out); rc != 1 || err != nil || !strings.Contains(out.String(),
		"SUMMARY: 13 ERROR (1 unseen-* on 1 ghosts, 12 structural), 5 INHERITED_ERROR, 1 WARNING; warning rules {'baseline-summary': 1}") {
		t.Errorf("summary partition: rc %d %v\n%s", rc, err, out.String())
	}
}

func TestParseArgs(t *testing.T) {
	o, err := ParseArgs([]string{"d.fbgd", "--verb", "--top=3", "--baseline-e", "--baseline-s", "s.json", "--cache", "c"})
	if err != nil || !o.Verbose || o.Top != 3 || !o.BaselineErrors || o.BaselineSummary != "s.json" || o.Demo != "d.fbgd" {
		t.Errorf("ParseArgs: %+v %v", o, err)
	}
	for _, bad := range [][]string{{"--baseline"}, {"--base", "x"}, {"--save-whitelist", "x"}, {"a", "b"}} {
		if _, err := ParseArgs(bad); err == nil {
			t.Errorf("ParseArgs(%q) accepted", bad)
		}
	}
	if rc, err := Run(Options{Demo: "x", NoBaseline: true, BaselineErrors: true}, &bytes.Buffer{}); rc != 2 || err == nil {
		t.Errorf("--no-baseline --baseline-errors: rc %d", rc)
	}
}

func TestPyPath(t *testing.T) {
	cases := map[string]string{"a/./b//c": "a/b/c", "./x": "x", "": ".", "/a/b/": "/a/b"}
	if runtime.GOOS == "windows" {
		cases = map[string]string{"C:/a/./b//c.fbgd": `C:\a\b\c.fbgd`, `C:\x\`: `C:\x`, "./x": "x", "": ".", "C:": "C:"}
	}
	for in, want := range cases {
		if got := PyPath(in); got != want {
			t.Errorf("PyPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOrders(t *testing.T) {
	if got := nearest(map[int]bool{1: true, 3: true, 5: true, 7: true, 9: true, 10: true}, 6); !slices.Equal(got, []int{5, 7, 3, 9}) {
		t.Errorf("nearest = %v", got)
	}
	// Python tuple order: (0,) < (0, 0) < (0, 1) < (1,)
	f := sortedForms(map[string]bool{formKey([]int{1}): true, formKey([]int{0, 1}): true, formKey([]int{0}): true, formKey([]int{0, 0}): true})
	want := [][]int{{0}, {0, 0}, {0, 1}, {1}}
	for i := range want {
		if !slices.Equal(f[i].([]int), want[i]) {
			t.Fatalf("sortedForms = %v", f)
		}
	}
}

func TestPositions(t *testing.T) {
	// v3_create_rebase.PAIRS demo-side spawner create (public source): header + one C=10 vec3
	q, ok := positions("01111110000101111010011010011001101100100110110110101010001010000011110011111111101111" + "00001010101")
	if !ok || q == nil || q.header != "0111111000010111101" || len(q.xyz) != 1 {
		t.Fatalf("positions: %+v %v", q, ok)
	}
	if q, ok := positions("0111111"); !ok || q != nil { // < 19 bits: ()
		t.Errorf("short create: %+v %v", q, ok)
	}
	if q, ok := positions("0111111000000000000101"); ok || q != nil { // spawner layout runs out: None
		t.Errorf("broken spawner: %+v %v", q, ok)
	}
}

// TestDanglingView (r284): a key whose player component p12 / camera p3 (component p0 target) names a ghost absent from
// the key is a dangling-view-reference; component p5 is not judged (same cases as the Python test_fbgd_gate).
func TestDanglingView(t *testing.T) {
	var k sw
	k.put(1, 1)
	k.put(2, 16)
	k.put(1, 16) // ghost 1
	k.put(1, 16)
	k.chunk(0, 7, 28)
	k.put(1<<16, 32)
	k.put(1, 1)
	k.chunk(0, 13, 0)
	k.put(5, 16) // component 5 of player 1
	k.put(1, 16)
	k.chunk(0, 20, 0b0001110<<13|1, [2]int{1, 0})
	k.put(13<<16, 32)
	k.put(0, 13)
	k.chunk(0, 13, 6, [2]int{6, 0})
	for i := 1; i < 12; i++ {
		if i == 5 {
			k.chunk(5, 13, 8, [2]int{8, 0})
		} else {
			k.chunk(i, 1, 0)
		}
	}
	k.chunk(12, 13, 9, [2]int{9, 0})
	k.put(6, 16) // camera 6
	k.put(1, 16)
	k.chunk(0, 7, 28)
	k.put(4<<16, 32)
	k.put(0, 4)
	for i := 0; i < 3; i++ {
		k.chunk(i, 1, 0)
	}
	k.chunk(3, 17, 7, [2]int{7, 0})
	data := append(append([]byte("company\x00version\x00"), k.frame(1)...), 0)
	res, err := scan(data, scanOpts{earlyLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	var got [][3]any
	for _, e := range res.errors {
		if r, _ := e.Get("rule"); r == ViewRule {
			g, _ := e.Get("ghost")
			p, _ := e.Get("property")
			x, _ := e.Get("target")
			got = append(got, [3]any{g, p, x})
		}
	}
	if want := [][3]any{{5, 12, 9}, {6, 3, 7}}; !slices.EqualFunc(got, want, func(a, b [3]any) bool { return a == b }) {
		t.Errorf("dangling-view-reference = %v, want %v", got, want)
	}
	if errorForm(res.errors[len(res.errors)-1]) != "" {
		t.Error("dangling-view-reference must be structural")
	}
}
