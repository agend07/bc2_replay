package captures

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/pyc"
)

// ---------- list (v3_captures.list_captures) ----------

// Roots is roots(extra): the --root dirs (default the work dir) + V3_CAPTURE_ROOTS (os.pathsep-separated).
func Roots(extra []string) []string {
	var rs []string
	for _, r := range extra {
		rs = append(rs, pyPath(r))
	}
	if len(rs) == 0 {
		rs = []string{WorkDir()}
	}
	for _, r := range strings.Split(os.Getenv("V3_CAPTURE_ROOTS"), string(os.PathListSeparator)) {
		if r != "" {
			rs = append(rs, pyPath(r))
		}
	}
	return rs
}

// CaptureDirs is capture_dirs(rs): resolved dirs v3-trial-* or with a capture.json, each once, sorted per root.
func CaptureDirs(rs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rs {
		if !isDir(r) {
			continue
		}
		ents, err := os.ReadDir(r)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Name())
		}
		sortNames(names)
		for _, n := range names {
			d := join(r, n)
			if !isDir(d) || !(strings.HasPrefix(n, "v3-trial-") || isFile(join(d, manifestName))) {
				continue
			}
			rd := resolve(d)
			if !seen[rd] {
				seen[rd] = true
				out = append(out, rd)
			}
		}
	}
	return out
}

// readManifest is v3_captures.read_manifest (v3_pov_list.read_manifest): nil without the file; error = ValueError text (bad JSON / not UTF-8).
func readManifest(dir string) (any, bool, error) {
	p := join(dir, manifestName)
	if !isFile(p) {
		return nil, false, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false, err
	}
	v, err := pyc.Loads(b)
	if err != nil {
		return nil, false, errors.New(jsonErrorText(b, err))
	}
	return v, true, nil
}

var pyjsonErr = regexp.MustCompile(`^pyjson: (.*): char (\d+)$`)

// jsonErrorText approximates str(json.JSONDecodeError): '<msg>: line L column C (char N)' (pyc's messages, Python's
// position format).
func jsonErrorText(b []byte, err error) string {
	m := pyjsonErr.FindStringSubmatch(err.Error())
	if m == nil {
		return strings.TrimPrefix(err.Error(), "pyjson: ")
	}
	bpos, _ := strconv.Atoi(m[2])
	prefix := []rune(string(b[:min(bpos, len(b))])) // pyc counts bytes, Python code points
	pos := len(prefix)
	line, col := 1, pos+1
	for i := pos - 1; i >= 0; i-- {
		if prefix[i] == '\n' {
			line = strings.Count(string(prefix), "\n") + 1
			col = pos - i
			break
		}
	}
	return fmt.Sprintf("%s: line %d column %d (char %d)", m[1], line, col, pos)
}

// srcOf is src_of(m): (src stem or nil, from the harvest index).
func srcOf(m any) (any, bool) {
	if v := get(m, "src"); truthy(v) {
		return v, false
	}
	lvl, ok := get(m, "level").(string)
	if !ok {
		return nil, false // .get(None) / an unhashable or non-str key never matches a JSON key
	}
	s := get(harvestIndex(), lvl)
	return s, truthy(s)
}

// harvestIndex is {level: stem}: OLD_DEMOS/companions_index.json, else the map pack index.
func harvestIndex() any {
	if b, err := os.ReadFile(join(OldDemosDir(), "companions_index.json")); err == nil {
		if v, err := pyc.Loads(b); err == nil {
			return v
		}
	}
	i, err := packIndex()
	if err != nil {
		return pyc.NewObject()
	}
	return i
}

// packEntry is v3_captures.pack_entry(stem): the map pack entry (set not complete in demos / old_demos) or nil.
func packEntry(stem string) *pyc.Object {
	e, err := packLookup(stem, DemosDir(), OldDemosDir())
	if err != nil {
		return nil
	}
	return e
}

// reason is why the capture cannot be built ("" = buildable).
func reason(cap string, m any, hasM bool) any {
	if !hasM {
		return "no capture.json"
	}
	if !isFile(join(cap, "m-log.bin")) {
		return "no m-log.bin"
	}
	src, _ := srcOf(m)
	if !truthy(src) {
		return "no data pack for " + pyStr(get(m, "level"))
	}
	s := pyStr(src)
	if !(complete(DemosDir(), s) || complete(OldDemosDir(), s) || packEntry(s) != nil) {
		return "src " + s + " not in demos, old_demos or the map pack"
	}
	return nil
}

func buildsOf(cap string) []any {
	l, _ := get(readJSON(join(cap, "builds.json")), "builds").([]any)
	return l
}

// recorded is the manifest 'recorded', else the m-log.bin mtime as local ISO seconds; nil without either.
func recorded(cap string, m any) any {
	if v := get(m, "recorded"); truthy(v) {
		return pyStr(v)
	}
	st, err := os.Stat(join(cap, "m-log.bin"))
	if err != nil {
		return nil
	}
	return isoLocal(st.ModTime())
}

// entry is v3_captures.entry(cap) (cap resolved).
func entry(cap string) *pyc.Object {
	m, hasM, err := readManifest(cap)
	var bad any
	if err != nil {
		m, hasM, bad = nil, false, "bad capture.json: "+err.Error()
	}
	s := readJSON(join(cap, "m-summary.json"))
	seq := get(s, "seq")
	mt := truthy(m)
	var src any
	if mt {
		src, _ = srcOf(m)
	}
	var stems, seen []any // r287: {"stem", "round"} (round null = built before r287)
	for _, b := range buildsOf(cap) {
		st := get(b, "stem")
		dup := false
		for _, x := range seen {
			if x == st {
				dup = true
			}
		}
		if !dup && complete(DemosDir(), pyStr(st)) {
			seen = append(seen, st)
			o := pyc.NewObject()
			o.Set("stem", st)
			o.Set("round", get(b, "round"))
			stems = append(stems, o)
		}
	}
	if stems == nil {
		stems = []any{}
	}
	why := bad
	if why == nil {
		why = reason(cap, m, hasM)
	}
	var seconds any
	if seq != nil {
		if f, ok := num(seq); ok {
			r, _ := pyc.RoundFloat(f/fps, 1)
			seconds = r
		}
	}
	o := pyc.NewObject()
	o.Set("kind", "capture")
	o.Set("dir", cap)
	o.Set("name", pathName(cap))
	o.Set("level", get(m, "level"))
	o.Set("label", get(m, "label"))
	o.Set("seconds", seconds)
	o.Set("packets", seq)
	o.Set("records", get(s, "records"))
	o.Set("drops", get(s, "drops"))
	o.Set("bytes", get(s, "bytes"))
	o.Set("recorded", recorded(cap, m))
	o.Set("buildable", why == nil)
	o.Set("reason", why)
	o.Set("src", src)
	if mt {
		o.Set("paired", get(m, "paired"))
	} else {
		o.Set("paired", nil)
	}
	ref := get(m, "reference")
	if !truthy(ref) {
		ref = nil
	}
	o.Set("reference", get(ref, "stem"))
	o.Set("builds", stems)
	if mt {
		o.Set("manifest", join(cap, manifestName))
	} else {
		o.Set("manifest", nil)
	}
	rs := roundsOf(cap) // r287
	o.Set("rounds", roundsJSON(rs, get(m, "level")))
	if k := capture.DefaultOf(rs); k > 0 {
		o.Set("default_round", int64(k))
	} else {
		o.Set("default_round", nil)
	}
	return o
}

const rawReason = "raw capture: extract first"

// rawEntry is v3_captures.raw_entry(p).
func rawEntry(p string) *pyc.Object {
	var hdr *capture.Header
	var bad any
	f, err := os.Open(p)
	if err == nil {
		buf := make([]byte, capture.HeaderSize)
		n, rerr := readFull(f, buf)
		f.Close()
		if rerr != nil {
			bad = "bad .bc2raw: " + osErrorText(rerr)
		} else if h, perr := capture.ParseHeader(buf[:n]); perr != nil {
			bad = "bad .bc2raw: " + perr.Error()
		} else {
			hdr = h
		}
	} else {
		bad = "bad .bc2raw: " + osErrorText(err)
	}
	var size, rec any
	st, err := os.Stat(p)
	if err == nil {
		size = st.Size()
	}
	if hdr != nil && hdr.Recorded != "" {
		rec = hdr.Recorded
	} else if err == nil {
		rec = isoLocal(st.ModTime())
	}
	lvl := ""
	if hdr != nil {
		lvl = capture.LevelOf(p)
	}
	why := bad
	if why == nil {
		why = rawReason
	}
	o := pyc.NewObject()
	o.Set("kind", "raw")
	o.Set("name", pathName(p))
	o.Set("path", p)
	o.Set("bytes", size)
	o.Set("recorded", rec)
	o.Set("level", lvl)
	o.Set("buildable", false)
	o.Set("reason", why)
	o.Set("builds", []any{})
	o.Set("rounds", []any{})
	o.Set("default_round", nil)
	return o
}

func readFull(f *os.File, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		k, err := f.Read(buf[n:])
		n += k
		if err != nil {
			if n > 0 || k == 0 {
				return n, nil // EOF: a short read is parse_header's 'short header'
			}
			return n, err
		}
	}
	return n, nil
}

// rawSources is {lower-case source.file: capture dir} over capture entries; a dir with m-log.bin wins.
func rawSources(dirs []string) map[string]string {
	out := map[string]string{}
	for _, d := range dirs {
		m, _, err := readManifest(d)
		if err != nil || !truthy(m) {
			m = nil
		}
		src := get(m, "source")
		if _, ok := src.(*pyc.Object); !ok {
			continue
		}
		f := get(src, "file")
		if !truthy(f) {
			continue
		}
		k := strings.ToLower(pyStr(f))
		if old, ok := out[k]; !ok || !isFile(join(old, "m-log.bin")) {
			out[k] = d
		}
	}
	return out
}

// rawFiles are the *.bc2raw files directly in RawDir(), sorted by name.
func rawFiles() []string {
	d := RawDir()
	if d == "" || !isDir(d) {
		return nil
	}
	ents, err := os.ReadDir(d)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if len(n) >= len(".bc2raw") && strings.EqualFold(n[len(n)-len(".bc2raw"):], ".bc2raw") {
			names = append(names, n)
		}
	}
	sortNames(names)
	var out []string
	for _, n := range names {
		if p := join(d, n); isFile(p) {
			out = append(out, p)
		}
	}
	return out
}

// List is list_captures(extra_roots): capture entries, then the raws no listed capture.json names as source.file.
func List(extraRoots []string) []any {
	dirs := CaptureDirs(Roots(extraRoots))
	var out []any
	for _, d := range dirs {
		out = append(out, entry(d))
	}
	done := rawSources(dirs)
	for _, p := range rawFiles() {
		if _, ok := done[strings.ToLower(pathName(p))]; !ok {
			out = append(out, rawEntry(p))
		}
	}
	if out == nil {
		out = []any{}
	}
	return out
}

// ListJSON is print(json.dumps(caps, ensure_ascii=False, indent=1)) without the final newline.
func ListJSON(caps []any) string { return dumpsIndent1(caps) }

// ListText is the non --json table.
func ListText(caps []any) []string {
	var out []string
	for _, c := range caps {
		name, lvl := pyStr(get(c, "name")), get(c, "level")
		l := "-"
		if truthy(lvl) {
			l = pyStr(lvl)
		}
		if get(c, "kind") == "raw" {
			b := get(c, "bytes")
			if !truthy(b) {
				b = int64(0)
			}
			out = append(out, fmt.Sprintf("%s %s %s bytes  %s", ljust(name, 28), ljust(l, 20), rjust(pyStr(b), 10), pyStr(get(c, "reason"))))
			continue
		}
		st := "buildable"
		if !truthy(get(c, "buildable")) {
			st = pyStr(get(c, "reason"))
		}
		mmss := "-"
		if s, ok := num(get(c, "seconds")); ok {
			n := int64(s)
			mmss = fmt.Sprintf("%d:%02d", pyc.FloorDiv(n, 60), n-60*pyc.FloorDiv(n, 60))
		}
		pk := get(c, "packets")
		if !truthy(pk) {
			pk = "-"
		}
		dr := get(c, "drops")
		if !truthy(dr) {
			dr = int64(0)
		}
		line := fmt.Sprintf("%s %s %s %s pk %s drops  %s", ljust(name, 28), ljust(l, 20), rjust(mmss, 6), rjust(pyStr(pk), 6), pyStr(dr), st)
		if rs, _ := get(c, "rounds").([]any); len(rs) > 1 { // r287
			parts := make([]string, len(rs))
			for i, r := range rs {
				parts[i] = pyStr(get(r, "packets"))
			}
			line += fmt.Sprintf("  rounds %s (default %s)", strings.Join(parts, " "), pyStr(get(c, "default_round")))
		}
		if b, _ := get(c, "builds").([]any); len(b) > 0 {
			parts := make([]string, len(b))
			for i, x := range b {
				parts[i] = pyStr(get(x, "stem"))
			}
			line += "  builds " + strings.Join(parts, ",")
		}
		out = append(out, line)
	}
	return out
}

func ljust(s string, n int) string {
	if k := len([]rune(s)); k < n {
		return s + strings.Repeat(" ", n-k)
	}
	return s
}

func rjust(s string, n int) string {
	if k := len([]rune(s)); k < n {
		return strings.Repeat(" ", n-k) + s
	}
	return s
}
