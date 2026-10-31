package sections

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/golden"
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// Maintainer exam against the golden stage dumps of the frozen Python builder (private; never shipped): Go's own
// decode of the dump's m-log, the patched create (State.Create) inside fbgd.BuildRows and the WIREMULTI step, then
//   - the wire_sites stream filtered to the kinds this package produces, record by record in call order;
//   - build_rows: every create equal to the dump's, then all rows and the stats (soldier / AC38 rows, templates and
//     initial states are the dump's: other packages' stages);
//   - next_entries (registry stage) and the 'wire multi' stats line of trial.txt when present.
// The data pack templates come from $BC2_DATAPACK_DIR or <golden root>/../datapack_oracle_r277.

// SiteKinds are the wire_sites kinds produced here, in report order.
var SiteKinds = []string{"camera_init", "create_rebase", "run_section", "chunks_of", "audit888_rebase", "vehsec"}

// KindCount is one site kind's tally.
type KindCount struct{ Py, Equal, Differ int }

// Check is one dump's exam result.
type Check struct {
	Name                  string
	Kinds                 map[string]*KindCount
	Tail                  int // trailing create_rebase records after the build (crebase_vs_src self-check; gate's)
	Creates, CreatesEqual int // build_rows creates (dump) / equal to the Go create
	PlainEqual            int // creates equal to plain create_chunk (the pre-port gap)
	Rows, RowsPy          int
	RowsEqual, StatsEqual bool
	NextEntries           string // "n/n" calls equal or "-"
	MultiStats            string // "equal", "DIFF" or "-" (no trial.txt line)
	Diffs                 []string
	Decode, Build, Multi  time.Duration // Go time (untraced): decode, build_rows (with creates), WIREMULTI
}

// OK reports whether everything compared equal.
func (c *Check) OK() bool {
	if !c.RowsEqual || !c.StatsEqual || c.CreatesEqual != c.Creates || c.MultiStats == "DIFF" || len(c.Diffs) > 0 {
		return false
	}
	for _, k := range c.Kinds {
		if k.Differ != 0 || k.Equal != k.Py {
			return false
		}
	}
	return !strings.Contains(c.NextEntries, "!")
}

func (c *Check) diff(format string, a ...any) {
	if len(c.Diffs) < 8 {
		c.Diffs = append(c.Diffs, fmt.Sprintf(format, a...))
	}
}

// DatapackDir is $BC2_DATAPACK_DIR, else <golden root>/../datapack_oracle_r277.
func DatapackDir(goldenRoot string) string {
	if v := os.Getenv("BC2_DATAPACK_DIR"); v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(goldenRoot), "datapack_oracle_r277")
}

// Pack is the data pack's templates: converted (Go) and raw (the decoded JSON, for the vehsec 'tpl' argument).
type Pack struct {
	T   fbgd.Templates
	Raw map[string]*pyc.Object
}

// LoadPack reads <dir>/templates_pack.json.
func LoadPack(dir string) (*Pack, error) {
	b, err := os.ReadFile(filepath.Join(dir, "templates_pack.json"))
	if err != nil {
		return nil, err
	}
	v, err := pyc.Loads(b)
	if err != nil {
		return nil, err
	}
	o, _ := v.(*pyc.Object)
	if o == nil {
		return nil, errors.New("templates_pack: not an object")
	}
	tv, _ := o.Get("templates")
	to, _ := tv.(*pyc.Object)
	if to == nil {
		return nil, errors.New("templates_pack: no templates")
	}
	p := &Pack{T: fbgd.Templates{}, Raw: map[string]*pyc.Object{}}
	for _, k := range to.Keys() {
		x, _ := to.Get(k)
		t, err := fbgd.TemplateOf(x)
		if err != nil {
			return nil, fmt.Errorf("template %s: %v", k, err)
		}
		p.T[k] = t
		p.Raw[k], _ = x.(*pyc.Object)
	}
	return p, nil
}

// eachRecord calls f with every untagged record of a dump file (header skipped); a missing file calls nothing.
// keep, when set, filters raw lines before decoding.
func eachRecord(path string, keep func(line []byte) bool, f func(*pyc.Object) error) error {
	fh, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	defer fh.Close()
	r, err := golden.NewReader(bufio.NewReaderSize(fh, 1<<20))
	if err != nil {
		return err
	}
	for {
		l, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if r.LineNo() == 1 || keep != nil && !keep(l) {
			continue
		}
		t, err := pyc.Loads(l)
		if err != nil {
			return err
		}
		u, err := golden.Untag(t)
		if err != nil {
			return err
		}
		o, _ := u.(*pyc.Object)
		if o == nil {
			return fmt.Errorf("%s: record is not an object", path)
		}
		if err := f(o); err != nil {
			return err
		}
	}
}

func get(o *pyc.Object, k string) any { v, _ := o.Get(k); return v }

func ghostKey(pseq, entry, cursor, phase, id int64) []any {
	return []any{pseq, entry, cursor, phase, id}
}

func pyGhostKey(v any) any {
	o, _ := v.(*pyc.Object)
	if o == nil {
		return v
	}
	n := func(k string) int64 { x, _ := get(o, k).(int64); return x }
	return ghostKey(n("pseq"), n("entry_cursor"), n("cursor"), n("phase"), n("id"))
}

func line(v any) string {
	l, err := golden.Line(v)
	if err != nil {
		return "unencodable: " + err.Error()
	}
	return l
}

// excOf is the 'Type: message' of a Go error (wire errors carry the Python message after their prefix).
func excOf(err error) string {
	var re *wire.ReaderError
	if errors.As(err, &re) {
		return re.Msg
	}
	return err.Error()
}

// siteRec is the comparable form of a site: {site, args, result | exc}.
func siteRec(kind string, args *pyc.Object, result any, exc string, isExc bool) string {
	o := pyc.NewObject()
	o.Set("site", kind)
	o.Set("args", args)
	if isExc {
		o.Set("exc", exc)
	} else {
		o.Set("result", result)
	}
	return line(o)
}

// siteRenderer renders a traced Go call like the dumper's wrapper did (ghosts as their key, bit strings as sha1 + length).
type siteRenderer struct {
	pack           *Pack
	lastBits, lsha string
}

func (sr *siteRenderer) sha(bs string) string {
	if bs != sr.lastBits || sr.lsha == "" {
		h := sha1.Sum([]byte(bs))
		sr.lastBits, sr.lsha = bs, hex.EncodeToString(h[:])
	}
	return sr.lsha
}

func xyzList(v [3]float64) []any { return []any{v[0], v[1], v[2]} }

func (sr *siteRenderer) render(s Site) string {
	a := pyc.NewObject()
	g := func(x any) any {
		gh := x.(*wire.Ghost)
		return ghostKey(gh.Pseq, gh.EntryCursor, gh.Cursor, gh.Phase, gh.ID)
	}
	result := s.Result
	switch s.Kind {
	case "camera_init":
		a.Set("g", g(s.Args[0]))
		a.Set("width", nil)
	case "create_rebase":
		a.Set("bits", s.Args[0])
		a.Set("origin", xyzList(s.Args[1].([3]float64)))
		a.Set("trunc", s.Args[2])
	case "run_section":
		gh := s.Args[0].(*wire.Ghost)
		a.Set("g", g(gh))
		a.Set("vt", s.Args[1])
		a.Set("cur", s.Args[2])
		if w, ok := s.Args[3].(map[int]int64)[gh.Config]; ok {
			a.Set("width", w)
		} else {
			a.Set("width", nil)
		}
	case "chunks_of":
		bs := s.Args[0].(string)
		a.Set("bits_sha1", sr.sha(bs))
		a.Set("bits_len", len(bs))
		sv := (&wire.SectionResult{Positions: s.Args[1].([]uint32), End: s.Args[2].(uint32), Refs: s.Args[3].([]wire.Ref),
			Ovs: s.Args[4].([]wire.Ovec)}).PyValue().([]any)
		a.Set("pos", sv[0])
		a.Set("end", sv[1])
		a.Set("refs", sv[2])
		a.Set("ovs", sv[3])
		if cs, ok := s.Result.([]AuChunk); ok {
			l := make([]any, len(cs))
			for i := range cs {
				l[i] = &cs[i]
			}
			result = l
		}
	case "audit888_rebase":
		a.Set("body", s.Args[0])
		a.Set("ovs", OvsValue(s.Args[1].([]AuOvec)))
		a.Set("table", s.Args[2])
	case "vehsec":
		bs := s.Args[0].(string)
		a.Set("bits_sha1", sr.sha(bs))
		a.Set("bits_len", len(bs))
		a.Set("cur", s.Args[1])
		a.Set("vt", s.Args[2])
		a.Set("count", s.Args[3])
		a.Set("origin", xyzList(s.Args[4].([3]float64)))
		a.Set("table", s.Args[5])
		a.Set("cfg", s.Args[6])
		a.Set("rebase", s.Args[7])
		ref := s.Args[8].(TplRef)
		var tpl any
		if raw := sr.pack.Raw[ref.Key]; raw != nil {
			if ss, ok := get(raw, "sections").([]any); ok && ref.Sec < len(ss) {
				tpl = ss[ref.Sec]
			}
		}
		a.Set("tpl", tpl)
		a.Set("layout", s.Args[9])
	}
	if s.Err != nil {
		return siteRec(s.Kind, a, nil, excOf(s.Err), true)
	}
	return siteRec(s.Kind, a, result, "", false)
}

// pySite is the comparable form of a dump record (ghost args projected to their key).
func pySite(o *pyc.Object) string {
	args, _ := get(o, "args").(*pyc.Object)
	if args == nil {
		args = pyc.NewObject()
	}
	if g, ok := args.Get("g"); ok {
		args.Set("g", pyGhostKey(g))
	}
	exc, isExc := get(o, "exc").(string)
	kind, _ := get(o, "site").(string)
	return siteRec(kind, args, get(o, "result"), exc, isExc)
}

func chunkStr(c fbgd.OutChunk) string { return fmt.Sprintf("%d %s %v", c.Index, c.Value.Key(), c.Refs) }

func rowStr(r *fbgd.Row, initial any, vt uint32, syn bool) string {
	s := fmt.Sprintf("%c t=%v id=%d type=%#v vt=%x syn=%v", r.K, r.T, r.ID, r.Type, vt, syn)
	if r.K == 'c' {
		s += " create=" + chunkStr(r.Create)
	}
	for _, c := range r.Chunks {
		s += fmt.Sprintf(" [%v %v %s %v]", c.Key, c.Tuple, c.Value.Key(), c.Refs)
	}
	if initial != nil {
		s += " initial=" + line(initial)
	}
	return s
}

func updateRow(o *pyc.Object) (*fbgd.Row, error) {
	u := pyc.NewObject()
	u.Set("k", "u")
	for _, k := range []string{"t", "id", "chunks"} {
		u.Set(k, get(o, k))
	}
	return fbgd.RowOf(u)
}

// pyRepr renders a Counter like Python's repr(dict(c)) (str keys, int values).
func pyRepr(c *fbgd.Counter) string {
	parts := make([]string, len(c.Keys()))
	for i, k := range c.Keys() {
		parts[i] = fmt.Sprintf("'%s': %d", k, c.Get(k))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

var siteIndex = regexp.MustCompile(`^\{"i":(\d+),`)

var multiLine = regexp.MustCompile(`(?m)^\s*wire multi (\{[^}]*\})\s*$`)

// CheckDump runs the exam on one dump dir with the data pack templates pack.
func CheckDump(dir string, pack *Pack) (*Check, error) {
	c := &Check{Name: filepath.Base(dir), Kinds: map[string]*KindCount{}, NextEntries: "-", MultiStats: "-"}
	for _, k := range SiteKinds {
		c.Kinds[k] = &KindCount{}
	}
	var sum struct {
		Capture string
		Env     map[string]string
	}
	b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &sum); err != nil {
		return nil, err
	}
	st, err := NewState(func(k string) (string, bool) { v, ok := sum.Env[k]; return v, ok })
	if err != nil {
		return nil, err
	}
	t0 := time.Now()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(dir)), sum.Capture, "m-log.bin"))
	if err != nil {
		return nil, err
	}
	dec, err := capture.Load(raw)
	if err != nil {
		return nil, err
	}
	log := capture.WireLog(dec)
	c.Decode = time.Since(t0)

	// build_rows inputs from the dump (other stages) and the expected output
	var sold, ac []*fbgd.Row
	if err := eachRecord(filepath.Join(dir, "soldier_rows.jsonl.gz"), nil, func(o *pyc.Object) error {
		r, err := updateRow(o)
		sold = append(sold, r)
		return err
	}); err != nil {
		return nil, err
	}
	if err := eachRecord(filepath.Join(dir, "ac38.jsonl.gz"), nil, func(o *pyc.Object) error {
		ro, _ := get(o, "row").(*pyc.Object)
		if ro == nil {
			return errors.New("ac38 record without row")
		}
		r, err := updateRow(ro)
		ac = append(ac, r)
		return err
	}); err != nil {
		return nil, err
	}
	vts := map[uint32]bool{}
	tpl := fbgd.Templates{}
	initial := map[fbgd.InitialKey]any{}
	var wantRows, wantCreates []string
	var wantStats string
	if err := eachRecord(filepath.Join(dir, "build_rows.jsonl.gz"), nil, func(o *pyc.Object) error {
		switch {
		case get(o, "tpl") != nil:
			kv, _ := get(o, "tpl").([]any)
			if len(kv) == 2 {
				if k, ok := kv[0].(int64); ok {
					vts[uint32(k)] = true
					tpl[k] = nil // only 'WM...' entries are read by WIREMULTI
				}
			}
		case get(o, "initial_state") != nil:
			kv := get(o, "initial_state").([]any)
			tid := kv[0].([]any)
			initial[fbgd.InitialKey{T: uint64(tid[0].(int64)), ID: tid[1].(int64)}] = kv[1]
		case get(o, "row") != nil:
			ro := get(o, "row").(*pyc.Object)
			r, err := fbgd.RowOf(ro)
			if err != nil {
				return err
			}
			var vt uint32
			if v, ok := ro.Get("_vt"); ok {
				vt = uint32(v.(int64))
			}
			syn, _ := get(ro, "syn").(bool)
			wantRows = append(wantRows, rowStr(r, get(ro, "_initial"), vt, syn))
			if r.K == 'c' {
				wantCreates = append(wantCreates, chunkStr(r.Create))
			}
		case get(o, "stats") != nil:
			wantStats = line(get(o, "stats"))
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Go: build_rows with the patched create, then WIREMULTI; every traced site rendered in call order
	sr := &siteRenderer{pack: pack}
	var goSites []string
	var goKinds []string
	st.Trace = func(s Site) { goSites = append(goSites, sr.render(s)); goKinds = append(goKinds, s.Kind) }
	hasTpl := func(vt uint32) bool { return vts[vt] }
	stB, _ := NewState(func(k string) (string, bool) { v, ok := sum.Env[k]; return v, ok })
	t1 := time.Now()
	if _, _, err := fbgd.BuildRows(log, hasTpl, sold, ac, initial, stB.Create); err != nil {
		return nil, fmt.Errorf("build_rows: %w", err)
	}
	c.Build = time.Since(t1) // untraced
	rows, stats, err := fbgd.BuildRows(log, hasTpl, sold, ac, initial, st.Create)
	if err != nil {
		return nil, fmt.Errorf("build_rows: %w", err)
	}
	gotCreates := 0
	for _, r := range rows {
		if r.K == 'c' {
			if gotCreates < len(wantCreates) && chunkStr(r.Create) == wantCreates[gotCreates] {
				c.CreatesEqual++
			}
			gotCreates++
		}
	}
	c.Creates = len(wantCreates)
	if gotCreates != len(wantCreates) {
		c.diff("creates: go %d py %d", gotCreates, len(wantCreates))
	}
	if prows, _, err := fbgd.BuildRows(log, hasTpl, sold, ac, initial, nil); err == nil { // the pre-port gap
		k := 0
		for _, r := range prows {
			if r.K == 'c' {
				if k < len(wantCreates) && chunkStr(r.Create) == wantCreates[k] {
					c.PlainEqual++
				}
				k++
			}
		}
	}
	c.Rows, c.RowsPy = len(rows), len(wantRows)
	c.RowsEqual = len(rows) == len(wantRows)
	for i := range min(len(rows), len(wantRows)) {
		if g := rowStr(&rows[i].Row, rows[i].Initial, rows[i].VT, rows[i].Syn); g != wantRows[i] {
			c.RowsEqual = false
			c.diff("build_rows row %d:\n go %.300s\n py %.300s", i, g, wantRows[i])
			break
		}
	}
	c.StatsEqual = line(stats) == wantStats
	if !c.StatsEqual {
		c.diff("build_rows stats: go %s py %s", line(stats), wantStats)
	}

	widths, err := wire.InferIDWidths(log)
	if err != nil {
		return nil, err
	}
	// production timing: an untraced run on a copy of the state and template dict
	st0, _ := NewState(func(k string) (string, bool) { v, ok := sum.Env[k]; return v, ok })
	tpl0 := fbgd.Templates{}
	for k, v := range tpl {
		tpl0[k] = v
	}
	t2 := time.Now()
	if _, err := WireMulti(log, st0, pack.T, tpl0, widths); err != nil {
		return nil, fmt.Errorf("wiremulti: %w", err)
	}
	c.Multi = time.Since(t2)
	mr, err := WireMulti(log, st, pack.T, tpl, widths)
	if err != nil {
		return nil, fmt.Errorf("wiremulti: %w", err)
	}
	if tt, err := os.ReadFile(filepath.Join(dir, "trial.txt")); err == nil {
		if m := multiLine.FindSubmatch(tt); m != nil {
			c.MultiStats = "equal"
			if g := pyRepr(mr.Stats); g != string(m[1]) {
				c.MultiStats = "DIFF"
				c.diff("wire multi stats:\n go %s\n py %s", g, m[1])
			}
		}
	}

	// wire_sites: the dump's records of these kinds, in order, against the Go trace
	want := map[string]bool{}
	for _, k := range SiteKinds {
		want[k] = true
	}
	gi := 0
	firstUpd := int64(-1) // i of the first upd_update site: the client-update loop runs after WIREMULTI
	keep := func(l []byte) bool {
		h := l[:min(len(l), 48)]
		if firstUpd < 0 && strings.Contains(string(h), `"site":"upd_update"`) {
			if m := siteIndex.FindSubmatch(h); m != nil {
				firstUpd, _ = strconv.ParseInt(string(m[1]), 10, 64)
			}
		}
		for _, k := range SiteKinds {
			if strings.Contains(string(h), `"site":"`+k+`"`) {
				return true
			}
		}
		return false
	}
	if err := eachRecord(filepath.Join(dir, "wire_sites.jsonl.gz"), keep, func(o *pyc.Object) error {
		kind, _ := get(o, "site").(string)
		if !want[kind] {
			return nil
		}
		kc := c.Kinds[kind]
		if gi >= len(goSites) {
			i, _ := get(o, "i").(int64)
			if kind == "create_rebase" && i > firstUpd && isZeroOrigin(get(get(o, "args").(*pyc.Object), "origin")) {
				c.Tail++ // after the build: crebase_vs_src reads SRC creates at origin zero (gate's port)
				return nil
			}
			kc.Py++
			kc.Differ++
			c.diff("wire_sites %s i=%v: Go stream ended", kind, get(o, "i"))
			return nil
		}
		kc.Py++
		p := pySite(o)
		if goSites[gi] == p {
			kc.Equal++
		} else {
			kc.Differ++
			c.diff("wire_sites i=%v (go #%d %s):\n go %.700s\n py %.700s", get(o, "i"), gi, goKinds[gi], goSites[gi], p)
		}
		gi++
		return nil
	}); err != nil {
		return nil, err
	}
	if gi < len(goSites) {
		c.diff("wire_sites: Go has %d more records (first %s)", len(goSites)-gi, goKinds[gi])
		c.Kinds[goKinds[gi]].Differ++
	}

	// next_entries (registry stage)
	ne := line(NewNextEntries(log))
	n, eq := 0, 0
	if err := eachRecord(filepath.Join(dir, "registry.jsonl.gz"), func(l []byte) bool {
		return strings.Contains(string(l[:min(len(l), 64)]), `"next_entries"`)
	}, func(o *pyc.Object) error {
		if get(o, "fn") != "next_entries" {
			return nil
		}
		n++
		if line(get(o, "result")) == ne {
			eq++
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if n > 0 {
		c.NextEntries = fmt.Sprintf("%d/%d", eq, n)
		if eq != n {
			c.NextEntries += "!"
			c.diff("next_entries: %d of %d calls differ", n-eq, n)
		}
	}
	return c, nil
}

func isZeroOrigin(v any) bool {
	l, ok := v.([]any)
	if !ok || len(l) != 3 {
		return false
	}
	for _, x := range l {
		if f, ok := x.(float64); !ok || f != 0 {
			return false
		}
	}
	return true
}

// CheckRoot runs CheckDump on dir, or on every dump under it (a dir with summary.json is one dump).
func CheckRoot(dir string, names []string) ([]*Check, error) {
	var dirs []string
	if _, err := os.Stat(filepath.Join(dir, "summary.json")); err == nil {
		dirs = []string{dir}
	} else {
		ds, err := capture.GoldenDumps(dir, names)
		if err != nil {
			return nil, err
		}
		for _, d := range ds {
			dirs = append(dirs, d.Dir)
		}
	}
	if len(dirs) == 0 {
		return nil, nil
	}
	pack, err := LoadPack(DatapackDir(filepath.Dir(dirs[0])))
	if err != nil {
		return nil, fmt.Errorf("data pack: %w", err)
	}
	var out []*Check
	for _, d := range dirs {
		c, err := CheckDump(d, pack)
		if err != nil {
			return out, fmt.Errorf("%s: %w", filepath.Base(d), err)
		}
		out = append(out, c)
	}
	return out, nil
}
