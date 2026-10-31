package events

// Maintainer exam: Go's deletes / registry / companions from Go's own decode of the dump's m-log plus the dump's
// inputs (summary env and src, assemble t0 and frame count, the dumped registry call args, the map pack or the SRC
// companion set) against the frozen Python's golden stages wire_deletes, registry and wire_companions, then the six
// companion files against outputs.json. Private data; never shipped.

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/golden"
	"bc2replay/builder/internal/pyc"
)

// ---------------------------------------------------------------------------------------------- golden values

func obj(kv ...any) *pyc.Object {
	o := pyc.NewObject()
	for i := 0; i < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// dict is a non-str-keyed Python dict: {"$dict": ...}, or {} when empty (an empty dict has no non-str key).
func dict(ps golden.Pairs) any {
	if len(ps) == 0 {
		return pyc.NewObject()
	}
	return ps
}

func counterValue(c *fbgd.Counter) any {
	if c == nil {
		return nil
	}
	return c.PyValue()
}

func copyCounter(c *fbgd.Counter) *fbgd.Counter {
	if c == nil {
		return nil
	}
	n := &fbgd.Counter{}
	for _, k := range c.Keys() {
		n.Add(k, c.Get(k))
	}
	return n
}

func (c CompEvent) PyValue() any  { return []any{c.I, c.El, c.V} }
func (g GroupEvent) PyValue() any { return []any{g.G, g.J} }

func (x RegRow) PyValue() any {
	pos := make([]any, len(x.Pos))
	for i, p := range x.Pos {
		pos[i] = p
	}
	return []any{x.SI, x.PJ, x.L, x.Body, pos}
}

func listOf[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func chunksValue(ch []fbgd.RowChunk) any {
	ps := make(golden.Pairs, len(ch))
	for i, c := range ch {
		refs := make([]any, len(c.Refs))
		for k, r := range c.Refs {
			refs[k] = []any{r.Target, r.Flag}
		}
		ps[i] = golden.Pair{K: []any{c.Key.Sec, c.Key.Prop}, V: obj("value", c.Value, "bits", c.Value.Len(), "refs", refs)}
	}
	return dict(ps)
}

func rowValue(r *fbgd.Row) any {
	return obj("k", string(r.K), "t", int64(r.T), "id", r.ID, "chunks", chunksValue(r.Chunks))
}

func cfgValue(c *Config) any {
	ce := make(golden.Pairs, len(c.Composite.Entries))
	for i, e := range c.Composite.Entries {
		ce[i] = golden.Pair{K: i, V: []any{e[0], e[1]}}
	}
	ge := make(golden.Pairs, len(c.Group.Entries))
	for i, e := range c.Group.Entries {
		ge[i] = golden.Pair{K: i, V: e}
	}
	im := c.Impact
	return golden.Pairs{
		{K: 0, V: obj("max_index", im.MaxIndex, "id_width", im.IDWidth, "vector_widths", listOf(im.VectorWidths),
			"fraction_bits", im.FractionBits, "magnitude_width", im.MagnitudeWidth, "raw_vector_mode", im.RawVectorMode)},
		{K: 1, V: obj("id_width", c.Composite.IDWidth, "entries", dict(ce))},
		{K: 2, V: obj("id_width", c.Group.IDWidth, "entries", dict(ge))},
	}
}

func recsValue(rs []Rec) any {
	out := make([]any, len(rs))
	for i, r := range rs {
		out[i] = []any{int(r.M), r.S}
	}
	return out
}

func setValue(s GroupSet) golden.Set {
	out := make(golden.Set, 0, len(s))
	for j := range s {
		out = append(out, j)
	}
	return out
}

func templateValue(t *Template) any {
	hdr, recs := pyc.NewObject(), pyc.NewObject()
	for _, ext := range Exts {
		hdr.Set(ext, t.Hdr[ext])
	}
	for _, ext := range t.RecExts {
		recs.Set(ext, recsValue(t.Recs[ext]))
	}
	c0 := make(golden.Pairs, len(t.Comp0.Keys))
	for i, k := range t.Comp0.Keys {
		c0[i] = golden.Pair{K: []any{k.I, k.El}, V: t.Comp0.M[k]}
	}
	g0 := make([]any, len(t.Grp0))
	for i, s := range t.Grp0 {
		g0[i] = setValue(s)
	}
	return obj("cfg", cfgValue(t.Cfg), "prolog", t.Prolog, "hdr", hdr, "recs", recs, "dt", t.DT, "comp0", dict(c0),
		"grp0", g0, "burst", t.Burst, "imp_key", t.ImpKey)
}

func eventsValue(e Events) any { return []any{listOf(e.Comp), listOf(e.Grp)} }

func deltasValue(d *Deltas) any {
	ps := make(golden.Pairs, len(d.Keys))
	for i, L := range d.Keys {
		ps[i] = golden.Pair{K: L, V: eventsValue(*d.M[L])}
	}
	return dict(ps)
}

func wireValue(rows []WireRow) any {
	ps := make(golden.Pairs, len(rows))
	for i, r := range rows {
		ps[i] = golden.Pair{K: r.Pseq, V: obj("t", r.T, "start", r.Start, "end", r.End, "impact_bits", r.ImpactBits,
			"comp", listOf(r.Comp), "grp", listOf(r.Grp))}
	}
	return dict(ps)
}

// ---------------------------------------------------------------------------------------------- dump inputs

// recipe holds the v3_cl04c RECIPE defaults the exam needs (summary env overrides them).
var recipe = map[string]string{"JOINKEY": "6", "SHIFTF": "2000", "REGWIRE": "1", "REG0WIRE": "1"}

type summary struct {
	Src string            `json:"src"`
	Env map[string]string `json:"env"`
}

func (s *summary) env(k string) string {
	if v, ok := s.Env[k]; ok {
		return v
	}
	return recipe[k]
}

// readRecords returns the untagged records of a dump (header skipped), up to max (0 = all).
func readRecords(path string, max int) ([]*pyc.Object, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := golden.NewReader(f)
	if err != nil {
		return nil, err
	}
	var out []*pyc.Object
	for max == 0 || len(out) < max {
		l, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if r.LineNo() == 1 {
			continue
		}
		t, err := pyc.Loads(l)
		if err != nil {
			return nil, err
		}
		u, err := golden.Untag(t)
		if err != nil {
			return nil, err
		}
		o, ok := u.(*pyc.Object)
		if !ok {
			return nil, fmt.Errorf("%s line %d: not an object", path, r.LineNo())
		}
		out = append(out, o)
	}
	return out, nil
}

func getInt(o *pyc.Object, k string) (int64, error) {
	v, _ := o.Get(k)
	switch x := v.(type) {
	case int64:
		return x, nil
	case float64:
		return int64(x), nil
	}
	return 0, fmt.Errorf("%q: want int, got %T", k, v)
}

// assembleInputs reads t0 (first assemble_args record) and frames - keyframes (assemble_out info).
func assembleInputs(dir string) (t0 float64, nd int, err error) {
	a, err := readRecords(filepath.Join(dir, "assemble_args.jsonl.gz"), 1)
	if err != nil || len(a) == 0 {
		return 0, 0, fmt.Errorf("assemble_args: %v", err)
	}
	switch v, _ := a[0].Get("t0"); x := v.(type) {
	case int64:
		t0 = float64(x)
	case float64:
		t0 = x
	default:
		return 0, 0, fmt.Errorf("assemble_args t0: %T", v)
	}
	o, err := readRecords(filepath.Join(dir, "assemble_out.jsonl.gz"), 1)
	if err != nil || len(o) == 0 {
		return 0, 0, fmt.Errorf("assemble_out: %v", err)
	}
	iv, _ := o[0].Get("info")
	info, _ := iv.(*pyc.Object)
	if info == nil {
		return 0, 0, errors.New("assemble_out: no info")
	}
	f, err := getInt(info, "frames")
	if err != nil {
		return 0, 0, err
	}
	k, err := getInt(info, "keyframes")
	return t0, int(f - k), err
}

// ---------------------------------------------------------------------------------------------- registry trace

// regTrace builds the registry records like the dumper (call-start order, results filled at return). The calls'
// mp closures are not dumped: map_rows uses the wire -> demo id table read from the dumped call's own input rows
// and result (the 13-bit field at every ref position before and after), unseen ids -> 0.
type regTrace struct {
	recs      []*pyc.Object
	playerRow any // the dumped rows() player_row (an input)
	mpTables  []map[int]int
	nmap      int
	cur       map[int]int
}

func (t *regTrace) mp(o int) int { return t.cur[o] }

func (t *regTrace) Call(fn string, args ...any) func(...any) {
	rec := obj("i", len(t.recs), "fn", fn)
	t.recs = append(t.recs, rec)
	switch fn {
	case "viewer_player", "next_entries":
		rec.Set("args", pyc.NewObject())
	case "parse_rows":
		b := args[0].(string)
		h := sha1.Sum([]byte(b))
		rec.Set("args", obj("bits_sha1", hex.EncodeToString(h[:]), "bits_len", len(b), "c", args[1], "nprop", args[2]))
	case "map_rows":
		if t.nmap < len(t.mpTables) {
			t.cur = t.mpTables[t.nmap]
		} else {
			t.cur = nil
		}
		t.nmap++
		rec.Set("args", obj("rows", listOf(args[0].([]RegRow)), "stat_before", counterValue(args[2].(*fbgd.Counter))))
	case "rows":
		wp := any(nil)
		if w := args[2].(int); w >= 0 {
			wp = w
		}
		mp, _ := args[3].(func(int) int)
		rec.Set("args", obj("regid", args[0], "player_row", t.playerRow, "wire_player", wp, "mp", mp != nil,
			"stat_before", counterValue(copyCounter(args[4].(*fbgd.Counter)))))
	}
	return func(res ...any) {
		switch fn {
		case "viewer_player":
			if res[1].(bool) {
				rec.Set("result", res[0])
			} else {
				rec.Set("result", nil)
			}
		case "parse_rows":
			if rows := res[0].([]RegRow); rows != nil {
				rec.Set("result", []any{listOf(rows), res[1]})
			} else {
				rec.Set("result", []any{nil, res[2]})
			}
		case "next_entries":
			ent, order := res[0].(map[uint32][]int), res[1].([]uint32)
			ps := make(golden.Pairs, len(order))
			for i, s := range order {
				ps[i] = golden.Pair{K: s, V: listOf(ent[s])}
			}
			rec.Set("result", dict(ps))
		case "map_rows":
			rec.Set("result", chunksValue(res[0].([]fbgd.RowChunk)))
			rec.Set("after", obj("stat", counterValue(args[2].(*fbgd.Counter))))
		case "rows":
			rows := res[0].([]*fbgd.Row)
			out := make([]any, len(rows))
			for i, r := range rows {
				out[i] = rowValue(r)
			}
			rec.Set("result", out)
			rec.Set("after", obj("stat", counterValue(args[4].(*fbgd.Counter))))
		}
	}
}

// mpTable reads one dumped map_rows call: wire id -> demo id at every ref position of every kept row.
func mpTable(rec *pyc.Object) (map[int]int, error) {
	m := map[int]int{}
	av, _ := rec.Get("args")
	args, _ := av.(*pyc.Object)
	if args == nil {
		return nil, errors.New("map_rows: no args")
	}
	rv, _ := args.Get("rows")
	rows, _ := rv.([]any)
	out := map[[2]int64][]byte{}
	res, _ := rec.Get("result")
	if ps, ok := res.(golden.Pairs); ok {
		for _, p := range ps {
			k, _ := p.K.([]any)
			c, _ := p.V.(*pyc.Object)
			if len(k) != 2 || c == nil {
				return nil, errors.New("map_rows: bad result item")
			}
			n, err := getInt(c, "bits")
			if err != nil {
				return nil, err
			}
			var s string
			switch v, _ := c.Get("value"); x := v.(type) {
			case int64:
				s = fmt.Sprintf("%0*b", n, x)
			case *big.Int:
				s = fmt.Sprintf("%0*s", n, x.Text(2))
			default:
				return nil, fmt.Errorf("map_rows: value %T", v)
			}
			out[[2]int64{k[0].(int64), k[1].(int64)}] = []byte(s)
		}
	}
	for _, it := range rows {
		x, _ := it.([]any)
		if len(x) != 5 {
			return nil, errors.New("map_rows: bad row")
		}
		body := []byte(x[3].(string))
		after, ok := out[[2]int64{x[0].(int64), x[1].(int64)}]
		if !ok {
			continue
		}
		for _, q := range x[4].([]any) {
			p := int(q.(int64))
			o, err1 := intOf(body, p, p+13)
			n, err2 := intOf(after, p, p+13)
			if err1 != nil || err2 != nil {
				return nil, errors.New("map_rows: ref past the row")
			}
			m[int(o)] = int(n)
		}
	}
	return m, nil
}

// ---------------------------------------------------------------------------------------------- the exam

// Options locate the inputs that are not in a dump: the frozen data pack (mappack.json) and the folder holding the
// SRC 9-file sets (the template() dumps).
type Options struct {
	DataPack, Demos string
}

// DefaultOptions is $BC2_DATAPACK_DIR, else <golden root>/../datapack_oracle_r277; $BC2_DEMOS_DIR, else
// $BC2_ARCHIVE_DIR/demos.
func DefaultOptions(root string) Options {
	o := Options{DataPack: os.Getenv("BC2_DATAPACK_DIR"), Demos: os.Getenv("BC2_DEMOS_DIR")}
	if o.DataPack == "" && root != "" {
		o.DataPack = filepath.Join(filepath.Dir(root), "datapack_oracle_r277")
	}
	if a := os.Getenv("BC2_ARCHIVE_DIR"); o.Demos == "" && a != "" {
		o.Demos = filepath.Join(a, "demos")
	}
	return o
}

// Check is one dump's exam.
type Check struct {
	Stages                                []capture.StageCheck
	Files                                 int    // companion files sha-equal to outputs.json (of 6); -1 = not reached
	Skip                                  string // why a stage was not examined
	Decode, Deletes, Registry, Companions time.Duration
}

// OK: every examined stage equal and all six files.
func (c *Check) OK() bool {
	for _, s := range c.Stages {
		if s.Diff != "" {
			return false
		}
	}
	return c.Files == 6 && c.Skip == ""
}

// CheckDump examines one dump (see the file comment).
func CheckDump(g capture.GoldenDump, opt Options) (*Check, error) {
	c := &Check{Files: -1}
	t := time.Now()
	raw, err := os.ReadFile(g.MLog)
	if err != nil {
		return nil, err
	}
	d, err := capture.Load(raw)
	if err != nil {
		return nil, err
	}
	c.Decode = time.Since(t)
	sb, err := os.ReadFile(filepath.Join(g.Dir, "summary.json"))
	if err != nil {
		return nil, err
	}
	var sum summary
	if err := json.Unmarshal(sb, &sum); err != nil {
		return nil, err
	}
	stage := func(name string, gen func(emit func(any) error) error) error {
		p := filepath.Join(g.Dir, name+".jsonl.gz")
		if _, err := os.Stat(p); err != nil {
			return nil
		}
		s, err := capture.CompareStage(p, name, gen)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		c.Stages = append(c.Stages, s)
		return nil
	}

	// wire_deletes
	t = time.Now()
	dl, st, err := Deletes(d, DeleteWidth)
	c.Deletes = time.Since(t)
	if err != nil {
		return c, fmt.Errorf("deletes: %w", err)
	}
	if err := stage("wire_deletes", func(emit func(any) error) error {
		return emit(obj("call", 0, "args", []any{}, "kw", pyc.NewObject(), "result", []any{listOf(dl), st.PyValue()}))
	}); err != nil {
		return c, err
	}

	// registry: viewer_player, rows (REG0WIRE, args from the dump), the REGWIRE loop
	if p := filepath.Join(g.Dir, "registry.jsonl.gz"); fileExists(p) {
		recs, err := readRecords(p, 0)
		if err != nil {
			return c, err
		}
		tr := &regTrace{}
		var rowsArgs *pyc.Object
		for _, r := range recs {
			switch fn, _ := r.Get("fn"); fn {
			case "map_rows":
				m, err := mpTable(r)
				if err != nil {
					return c, err
				}
				tr.mpTables = append(tr.mpTables, m)
			case "rows":
				if rowsArgs == nil {
					a, _ := r.Get("args")
					rowsArgs, _ = a.(*pyc.Object)
				}
			}
		}
		t = time.Now()
		reg := &Registry{D: d, Trace: tr}
		reg.ViewerPlayer()
		if rowsArgs != nil {
			pr, _ := rowsArgs.Get("player_row")
			tr.playerRow = pr
			pro, _ := pr.(*pyc.Object)
			if pro == nil {
				return c, errors.New("registry rows: no player_row")
			}
			regid, err1 := getInt(rowsArgs, "regid")
			pid, err2 := getInt(pro, "id")
			if err := errors.Join(err1, err2); err != nil {
				return c, err
			}
			wp := int64(-1)
			if v, _ := rowsArgs.Get("wire_player"); v != nil {
				wp = v.(int64)
			}
			var mp func(int) int
			if v, _ := rowsArgs.Get("mp"); v == true {
				mp = tr.mp
			}
			stat := &fbgd.Counter{}
			if sbv, _ := rowsArgs.Get("stat_before"); sbv != nil {
				so, _ := sbv.(*pyc.Object)
				for _, k := range so.Keys() {
					v, _ := getInt(so, k)
					stat.Add(k, int(v))
				}
			} else {
				stat = nil
			}
			if _, _, err := reg.JoinRows(int(regid), int(pid), int(wp), mp, stat); err != nil {
				return c, fmt.Errorf("registry rows: %w", err)
			}
			if sum.env("REGWIRE") != "" {
				if _, err := reg.RegistryUpdates(int(regid), func(uint64) func(int) int { return tr.mp }, &fbgd.Counter{}); err != nil {
					return c, fmt.Errorf("registry updates: %w", err)
				}
			}
		}
		c.Registry = time.Since(t)
		if err := stage("registry", func(emit func(any) error) error {
			for _, r := range tr.recs {
				if err := emit(r); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return c, err
		}
	}

	// wire_companions + the six files
	p := filepath.Join(g.Dir, "wire_companions.jsonl.gz")
	if !fileExists(p) {
		return c, nil
	}
	first, err := readRecords(p, 1)
	if err != nil || len(first) == 0 {
		return c, fmt.Errorf("wire_companions: %v", err)
	}
	fn, _ := first[0].Get("fn")
	t0, nd, err := assembleInputs(g.Dir)
	if err != nil {
		return c, err
	}
	t = time.Now()
	var tpl *Template
	var tplArgs any = pyc.NewObject()
	switch fn {
	case "template_from_pack":
		e, err := LoadPackEntry(filepath.Join(opt.DataPack, "mappack.json"), sum.Src)
		if err != nil {
			return c, err
		}
		if e == nil {
			return c, fmt.Errorf("map pack: no set %s", sum.Src)
		}
		if tpl, err = TemplateFromPack(e); err != nil {
			return c, fmt.Errorf("template_from_pack: %w", err)
		}
	case "template":
		if opt.Demos == "" {
			c.Skip = "template(SRC) needs BC2_DEMOS_DIR"
			return c, nil
		}
		if tpl, err = TemplateFiles(filepath.Join(opt.Demos, sum.Src)); err != nil {
			return c, fmt.Errorf("template: %w", err)
		}
		tplArgs = obj("base", sum.Src)
	default:
		return c, fmt.Errorf("wire_companions: first call %v", fn)
	}
	fo, err := NewFrameOf(t0, sum.env("JOINKEY"), sum.env("SHIFTF"))
	if err != nil {
		return c, err
	}
	key0, deltas, wrows, err := WireEvents(d, tpl.Cfg, fo.Of)
	if err != nil {
		return c, fmt.Errorf("wire_events: %w", err)
	}
	recs, err := Build(tpl, key0, deltas, nd)
	if err != nil {
		return c, fmt.Errorf("build: %w", err)
	}
	files, err := Encode(tpl, recs)
	if err != nil {
		return c, err
	}
	c.Companions = time.Since(t)
	cfg := cfgValue(tpl.Cfg)
	if err := stage("wire_companions", func(emit func(any) error) error {
		build := pyc.NewObject()
		for _, ext := range Exts {
			build.Set(ext, recsValue(recs[ext]))
		}
		for i, r := range []*pyc.Object{
			obj("fn", fn, "args", tplArgs, "result", templateValue(tpl)),
			obj("fn", "destruction.wire", "args", obj("cfg", cfg), "result", wireValue(wrows)),
			obj("fn", "wire_events", "args", obj("cfg", cfg, "frame_of", "v3_cl04c._fo (closure, not dumped)"),
				"result", []any{eventsValue(key0), deltasValue(deltas)}),
			obj("fn", "build", "args", obj("key0", eventsValue(key0), "deltas", deltasValue(deltas), "n", nd), "result", build),
		} {
			rec := obj("call", i)
			for _, k := range r.Keys() {
				v, _ := r.Get(k)
				rec.Set(k, v)
			}
			if err := emit(rec); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return c, err
	}
	outs, err := fbgd.LoadOutputs(g.Dir)
	if err != nil {
		return c, err
	}
	c.Files = 0
	for _, f := range files {
		if sumLen(f.Data) == outs[f.Ext] {
			c.Files++
		}
		if sumLen(f.Idx) == outs[fbgd.CompanionIndexExt(f.Ext)] {
			c.Files++
		}
	}
	return c, nil
}

func sumLen(b []byte) fbgd.SumLen {
	h := sha256.Sum256(b)
	return fbgd.SumLen{Len: len(b), Sha: hex.EncodeToString(h[:])}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// StageNames lists the examined stages (for tables).
var StageNames = []string{"wire_deletes", "registry", "wire_companions"}

// Summary is a one-line form of a Check's stage results.
func (c *Check) Summary() string {
	var parts []string
	for _, s := range c.Stages {
		r := fmt.Sprintf("%s %d", s.Stage, s.Lines)
		if s.Diff != "" {
			r += " DIFF"
		}
		parts = append(parts, r)
	}
	return strings.Join(parts, ", ")
}
