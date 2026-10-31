package pipeline

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/events"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/gate"
	"bc2replay/builder/internal/golden"
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/sections"
	"bc2replay/builder/internal/soldier"
	"bc2replay/builder/internal/wire"
)

// Exts are the 9 output extensions (v3_trial.EXTS order).
var Exts = []string{"fbgd", "fbgk", "fbgdd", "fbgdk", "fbidd", "fbidk", "fbcdd", "fbcdk", "txt"}

// Checkpoint receives the P2-E checkpoint records (maintainer: builder dev cl04c). stage is the golden stage name
// ('ckpt_' + position); rec is a golden-encodable value, valid only during the call (rows are mutated later);
// looseT marks a record holding a row whose 't' type Go does not track (see tkind).
type Checkpoint func(stage string, rec any, looseT bool)

// Inputs is one v3_cl04c run.
type Inputs struct {
	Log      string // the capture's m-log.bin, or the capture dir holding it
	Datapack string // data pack dir: templates_pack.json, mappack.json (BC2_DATAPACK_DIR)
	DemosDir string // dir of 9-file SRC sets (BC2_DEMOS_DIR); "" = map pack only
	MapPack  string // BC2_MAPPACK: "" auto (pack only when the 9-file set is not in DemosDir), "0" never, "1" always
	OutDir   string // output dir of cl04_<Suffix>.* (created when missing)
	Suffix   string
	Recipe   Recipe
	Workers  int    // soldier convert goroutines (0 = GOMAXPROCS; the output does not depend on it)
	Round    string // r287: V3_ROUND, the 1-based round to keep ("" = the one with the most packets)

	Checkpoint Checkpoint // nil = off (production)
}

// Lines are the cl04c stdout lines v3_trial parses (without the leading two spaces it adds); "" = not printed.
type Lines struct {
	Templates      string // 'templates N missing {...}'
	WireMulti      string
	WireDeletes    string
	WireMultiIDs   string
	WireRegistry   string
	TplNewBad      string
	CrebaseVsSrc   string
	WireCompanions string
	Last           string // the last stdout line (info, fbgd length, ids, creates)
}

// Result is what the run printed and wrote.
type Result struct {
	Stdout string // cl04c's stdout ('\n' after every line)
	Lines  Lines

	Src     string // SRC stem
	MapPack bool   // SRC from the map pack (else the 9-file set in DemosDir)

	TemplatesMissing []string // '0x..' classes without a template (sorted)
	Frames           int
	Keyframes        int
	FBGDBytes        int
	IDs              int
	Creates          int
	TplNewBad        int

	Files []string // written paths (Exts order)
}

const (
	soldierVT  = int64(wire.SoldierVT)
	playerVT   = 0x1431d14
	compVT     = 0x1431df8
	camVT      = 0x1431c0c
	c4VT       = 0x143aa80
	ac38VT     = 0x143ac38
	z383VT     = 0x14383cc
	registryVT = events.RegVT
	idBase     = 120 // r164: first demo ghost id
	leadFrames = 3
	keyEvery   = fbgd.KeyEvery
	joinRest   = 20 // key0 gap frames (t0 + 20 * DT_TICKS): SHIFTF moves rows after it
)

// dt is DT_TICKS as a variable: Python multiplies at run time, a Go constant expression would fold exactly.
var dt = fbgd.DTTicks

// SystemExit is a v3_cl04c raise SystemExit(msg) (rc 1, msg on stderr).
type SystemExit struct{ Msg string }

func (e *SystemExit) Error() string { return e.Msg }

type build struct {
	ctx context.Context
	in  Inputs
	r   *Recipe
	res *Result
	out []string
	ck  Checkpoint

	dec *capture.Decoded
	d   *wire.Log
	gi  map[*wire.Ghost]int // index in d.Ghosts

	rs       rowset
	pack     *sections.Pack
	tpl      fbgd.Templates
	tplOrder []fbgd.TypeKey
	st       *sections.State

	sites   int  // wire_sites records started (checkpoint site index)
	inSlice bool // inside a pipeline slice_record call (its site is already counted)

	mpe    *packEntry
	srcDir string
	srStat *fbgd.Counter // soldier pkl refs stats (_sr)

	joinKey string // r289: the resolved JOINKEY frame count ('auto' -> a number; "" = off) for the companions' _fo
}

func (b *build) say(args ...any) string {
	l := printLine(args...)
	b.out = append(b.out, l)
	return l
}

func (b *build) emit(stage string, rec any) {
	if b.ck != nil {
		b.ck("ckpt_"+stage, rec, false)
	}
}

// snapshot is a ckpt 'rows' record set: {head, n_rows}, then {row} per row.
func (b *build) snapshot(stage string, head any) {
	if b.ck == nil {
		return
	}
	b.ck("ckpt_"+stage, obj("head", head, "n_rows", len(b.rs.rows)), false)
	for _, r := range b.rs.rows {
		b.ck("ckpt_"+stage, obj("row", b.rs.rowValue(r)), b.rs.loose(r))
	}
}

// loop observes one iteration of a ckpt 'loop' block: before() then after(i, key).
type loop struct {
	b     *build
	stage string
	stat  *fbgd.Counter
	n0    int
	s0    map[string]int
}

func (b *build) newLoop(stage string, stat *fbgd.Counter) *loop {
	if b.ck == nil {
		return nil
	}
	return &loop{b: b, stage: stage, stat: stat}
}

func (l *loop) before(nrows int) {
	if l == nil {
		return
	}
	l.n0 = nrows
	l.s0 = map[string]int{}
	for _, k := range l.stat.Keys() {
		l.s0[k] = l.stat.Get(k)
	}
}

func (l *loop) after(i int, key any, rows []*fbgd.Row) {
	if l == nil {
		return
	}
	ds := pyc.NewObject()
	for _, k := range l.stat.Keys() {
		if v := l.stat.Get(k); v != l.s0[k] {
			ds.Set(k, v-l.s0[k])
		}
	}
	add := rows[l.n0:]
	if len(add) == 0 && ds.Len() == 0 {
		return
	}
	loose := false
	for _, r := range add {
		loose = loose || l.b.rs.loose(r)
	}
	l.b.ck("ckpt_"+l.stage, obj("i", i, "key", key, "rows", l.b.rs.rowsValue(add), "dstat", ds), loose)
}

// packEntry is a mappack.json set entry: txt, creates, and the companion parts.
type packEntry struct {
	Txt     string            `json:"txt"`
	Creates []json.RawMessage `json:"creates"`
	comp    events.PackEntry
}

// lookupPack is mappack.lookup(stem, demos): the map pack entry to use, or nil (= the 9-file set).
func lookupPack(datapack, demos, mode, stem string) (*packEntry, error) {
	if mode == "0" {
		return nil, nil
	}
	path := ""
	if datapack != "" {
		path = filepath.Join(datapack, "mappack.json")
	}
	var raw []byte
	if path != "" {
		raw, _ = os.ReadFile(path)
	}
	complete := func() bool {
		if demos == "" {
			return false
		}
		for _, e := range Exts {
			if st, err := os.Stat(filepath.Join(demos, stem+"."+e)); err != nil || !st.Mode().IsRegular() {
				return false
			}
		}
		return true
	}
	if raw == nil {
		if mode == "1" {
			if path == "" {
				path = "BC2_DATAPACK_DIR/mappack.json"
			}
			return nil, &SystemExit{"BC2_MAPPACK=1 but no " + path}
		}
		return nil, nil
	}
	var p struct {
		Format int                        `json:"format"`
		Sets   map[string]json.RawMessage `json:"sets"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Format != 1 {
		return nil, fmt.Errorf("ValueError: %s: map pack format %d, want 1", path, p.Format)
	}
	s, ok := p.Sets[stem]
	switch {
	case mode == "1" && !ok:
		return nil, &SystemExit{"BC2_MAPPACK=1: " + stem + " not in the map pack"}
	case mode != "1" && (!ok || complete()):
		return nil, nil
	}
	e := &packEntry{}
	if err := json.Unmarshal(s, e); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(s, &e.comp); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *packEntry) creates() ([]gate.PackCreate, error) {
	out := make([]gate.PackCreate, len(e.Creates))
	for i, c := range e.Creates {
		var a []json.RawMessage
		if err := json.Unmarshal(c, &a); err != nil || len(a) != 2 {
			return nil, fmt.Errorf("map pack create %.40s", c)
		}
		if err := json.Unmarshal(a[0], &out[i].Bits); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(a[1], &out[i].Hex); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Run is v3_cl04c.py <OutDir> <Suffix> with V3_LOG = in.Log: the 9 files cl04_<Suffix>.*, the stdout v3_trial reads, and the typed summary. On an error Result still holds the stdout so far.
func Run(ctx context.Context, in Inputs) (Result, error) {
	b := &build{ctx: ctx, in: in, r: &in.Recipe, res: &Result{}, ck: in.Checkpoint, rs: rowset{m: map[*fbgd.Row]*meta{}}}
	err := b.run()
	if err != nil {
		var pe *wire.ErrUnported
		if errors.As(err, &pe) {
			err = fmt.Errorf("FAIL unported reader: %w", err)
		}
	}
	b.res.Stdout = strings.Join(b.out, "\n")
	if len(b.out) > 0 {
		b.res.Stdout += "\n"
	}
	return *b.res, err
}

func (b *build) check() error { return b.ctx.Err() }

func (b *build) run() error {
	r, res := b.r, b.res
	res.Src = r.SRC
	mpe, err := lookupPack(b.in.Datapack, b.in.DemosDir, b.in.MapPack, r.SRC)
	if err != nil {
		return err
	}
	b.mpe, b.srcDir = mpe, b.in.DemosDir
	res.MapPack = mpe != nil
	src := "files"
	if mpe != nil {
		src = "map pack"
	}
	b.say("src", r.SRC+":", src)
	if mpe != nil && !on(r.WIRECOMP) {
		return &SystemExit{"map pack SRC: WIRECOMP= needs the 9-file SRC set in BC2_DEMOS_DIR (maintainer mode, r268)"}
	}
	st, err := sections.NewState(r.Lookup)
	if err != nil {
		return err
	}
	b.st = st
	base := filepath.Join(b.in.OutDir, "cl04_"+b.in.Suffix)
	for _, e := range Exts {
		if _, err := os.Stat(base + "." + e); err == nil {
			return errors.New("FileExistsError: refusing to overwrite an existing replay/sidecar")
		}
	}
	mlog := b.in.Log
	if st, err := os.Stat(mlog); err == nil && st.IsDir() {
		mlog = filepath.Join(mlog, "m-log.bin")
	}
	var lv capture.Levels
	if b.dec, lv, err = capture.LoadFile(mlog, b.in.Round); err != nil {
		return err
	}
	if lv.ConfigInjected {
		b.say("config: none in the log, default injected (r291)")
	}
	if k := lv.Kept; k != nil { // r282/r287: one round (V3_ROUND, else the one with the most packets)
		var hi, first, last any
		if !k.Open {
			hi = k.Hi
		}
		if len(b.dec.PacketSeqs) > 0 {
			first, last = slices.Min(b.dec.PacketSeqs), slices.Max(b.dec.PacketSeqs)
		}
		cuts := make([]any, len(lv.Cuts))
		for i, c := range lv.Cuts {
			cuts[i] = c
		}
		b.say("level change: round", lv.Round, "kept pseq range", tuple{k.Lo, hi}, "cuts", cuts, "kept pseq", first, "..", last)
	}
	b.d = capture.WireLog(b.dec)
	b.gi = make(map[*wire.Ghost]int, len(b.d.Ghosts))
	for i, g := range b.d.Ghosts {
		b.gi[g] = i
	}
	if b.ck != nil {
		b.traceSites()
	}
	if err := b.check(); err != nil {
		return err
	}

	// templates: data pack classes of the phase-1 ghosts; server-id refs in their defaults -> null
	if b.in.Datapack == "" {
		return errors.New("no data pack (templates_pack.json): set BC2_DATAPACK_DIR")
	}
	if b.pack, err = sections.LoadPack(b.in.Datapack); err != nil {
		return err
	}
	b.tpl = fbgd.Templates{}
	miss := map[string]bool{}
	for _, g := range b.d.Ghosts {
		if g.Phase != 1 {
			continue
		}
		vt := int64(g.SectionVT)
		if _, ok := b.tpl[vt]; ok {
			continue
		}
		if t, ok := b.pack.T[fmt.Sprintf("0x%x", g.SectionVT)]; ok {
			nt, err := nullTemplate(t)
			if err != nil {
				return err
			}
			b.tpl[vt] = nt
			b.tplOrder = append(b.tplOrder, vt)
		} else {
			miss[fmt.Sprintf("%#x", g.SectionVT)] = true
		}
	}
	for k := range miss {
		res.TemplatesMissing = append(res.TemplatesMissing, k)
	}
	slices.Sort(res.TemplatesMissing)
	ms := "set()"
	if len(miss) > 0 {
		q := make([]string, len(res.TemplatesMissing))
		for i, k := range res.TemplatesMissing {
			q[i] = strRepr(k)
		}
		ms = "{" + strings.Join(q, ", ") + "}"
	}
	res.Lines.Templates = b.say("templates", len(b.tpl), "missing", ms)

	// soldier rows (v3_convert_soldiers, in memory) and the AC38 rows
	srows, _, err := soldier.Convert(b.dec, b.in.Workers)
	if err != nil {
		return fmt.Errorf("soldier convert: %w", err)
	}
	sold := make([]*fbgd.Row, len(srows))
	soldCov := map[[2]int64]bool{}
	srefs := map[*fbgd.RowChunk][]soldier.PropRefs{}
	for i := range srows {
		s := &srows[i]
		row := &fbgd.Row{K: 'u', T: float64(s.T), ID: s.ID, Chunks: make([]fbgd.RowChunk, len(s.Chunks))}
		for j, c := range s.Chunks {
			row.Chunks[j] = fbgd.RowChunk{Key: fbgd.ChunkKey{Prop: c.Prop}, Value: c.Body}
		}
		sold[i] = row
		soldCov[[2]int64{int64(s.T), int64(s.ID)}] = true
		if len(s.Refs) > 0 && len(row.Chunks) > 0 {
			srefs[&row.Chunks[0]] = s.Refs
		}
	}
	for _, g := range b.d.Ghosts { // ac38_rows: one wire site per AC38 update record (before the UPD loop)
		if g.Phase == 0 && g.SectionVT == ac38VT {
			b.sites++
		}
	}
	arows, err := wire.AC38Rows(b.d)
	if err != nil {
		return fmt.Errorf("ac38 rows: %w", err)
	}
	ac := make([]*fbgd.Row, len(arows))
	for i, a := range arows {
		row := &fbgd.Row{K: 'u', T: float64(a.T), ID: int(a.ID), Chunks: make([]fbgd.RowChunk, len(a.Chunks))}
		for j, c := range a.Chunks {
			row.Chunks[j] = fbgd.RowChunk{Key: fbgd.ChunkKey{Prop: c.Index}, Value: c.Value}
		}
		ac[i] = row
	}
	b.say("wire engine", "py")
	if err := b.check(); err != nil {
		return err
	}

	// UPD: initial states of single-section wire classes; REBASE: their one-vec3 chunks re-encoded origin-zero
	initial := map[fbgd.InitialKey]any{}
	var widths map[int]int64
	var tv map[uint32]bool
	upd := on(r.UPD)
	if upd {
		if widths, err = wire.InferIDWidths(b.d); err != nil {
			return err
		}
		if tv, err = hexSet(r.UPD, false); err != nil {
			return err
		}
		for _, g := range b.d.Ghosts {
			if g.Phase != 1 || !tv[g.SectionVT] {
				continue
			}
			sr, err := b.slice(g, widths)
			if err != nil {
				return err
			}
			rec := fbgd.InitialPropertyState(sr)
			if rec != nil {
				initial[fbgd.InitialKey{T: g.T, ID: g.ID}] = rec
			}
			if b.ck != nil {
				var rv any
				if rec != nil {
					rv = rec
				}
				b.emit("upd_initial", obj("t", g.T, "id", g.ID, "vt", g.SectionVT, "rec", rv))
			}
		}
	}
	rebase := on(r.REBASE)
	if rebase {
		if !upd {
			return errors.New("NameError: name '_tv' is not defined")
		}
		rbv, err := hexSet(r.REBASE, false)
		if err != nil {
			return err
		}
		rb := &fbgd.Counter{}
		for _, g := range b.d.Ghosts {
			if g.Phase != 1 || !tv[g.SectionVT] || !rbv[g.SectionVT] {
				continue
			}
			sr, err := b.slice(g, widths)
			if err != nil {
				return err
			}
			if sr.Error != "" || sr.Flags != 0 {
				continue
			}
			out, err := b.rebaseChunks(sr, g, rb)
			if err != nil {
				return err
			}
			k := fbgd.InitialKey{T: g.T, ID: g.ID}
			if len(out) > 0 {
				ns := *sr
				ns.Chunks, ns.OriginVectors = out, []wire.Ovec{}
				initial[k] = &ns
			}
			if b.ck != nil {
				var sv any
				if x := initial[k]; x != nil {
					sv = x
				}
				b.emit("rebase", obj("t", g.T, "id", g.ID, "stored", len(out) > 0, "state", sv))
			}
		}
		b.say("rebase", rb)
	}
	if err := b.check(); err != nil {
		return err
	}

	// build_rows with the patched create (camera create, CREBASE, CUT, AA80B0)
	hasTpl := func(vt uint32) bool { _, ok := b.tpl[int64(vt)]; return ok }
	brs, stt, err := fbgd.BuildRows(b.d, hasTpl, sold, ac, initial, b.st.Create)
	if err != nil {
		return fmt.Errorf("build_rows: %w", err)
	}
	b.say(stt)
	for _, br := range brs {
		row := &br.Row
		b.rs.rows = append(b.rs.rows, row)
		if br.Initial != nil {
			m := b.rs.mw(row)
			m.initial, _ = br.Initial.(*wire.SliceResult)
			m.vt = br.VT
		}
		if br.Syn {
			b.rs.mw(row).syn = true
		}
		if row.K == 'u' && len(row.Chunks) > 0 { // r202: pkl rows with ghost references (wire t kept)
			if sr, ok := srefs[&row.Chunks[0]]; ok {
				m := b.rs.mw(row)
				m.srefs, m.wt = sr, row.T
			}
		}
	}
	// final deletes of non-soldier ids: no real delete info -> keep alive
	lastc := map[int]float64{}
	typ := map[int]fbgd.TypeKey{}
	for _, row := range b.rs.rows {
		if row.K == 'c' {
			lastc[row.ID] = row.T
			typ[row.ID] = row.Type
		}
	}
	b.rs.rows = slices.DeleteFunc(b.rs.rows, func(x *fbgd.Row) bool {
		return x.K == 'd' && x.T > lastc[x.ID] && typ[x.ID] != fbgd.TypeKey(soldierVT)
	})
	b.snapshot("final_deletes", pyc.NewObject())

	// WIREMULTI
	var wreg *int64
	if on(r.WIREMULTI) {
		ww, err := wire.InferIDWidths(b.d)
		if err != nil {
			return err
		}
		if b.ck != nil {
			lp := &loop{b: b, stage: "wiremulti_rec"}
			var cur *wire.Ghost
			curI := 0
			b.st.Item = func(i int, g *wire.Ghost, mr *sections.MultiResult) {
				if cur != nil {
					lp.after(curI, []any{cur.T, cur.Pseq, cur.EntryCursor, cur.ID, cur.Phase}, mr.Rows)
				}
				cur, curI = g, i
				if g != nil {
					lp.stat = mr.Stats
					lp.before(len(mr.Rows))
				}
			}
		}
		mr, err := sections.WireMulti(b.d, b.st, b.pack.T, b.tpl, ww)
		b.st.Item = nil
		if err != nil {
			return fmt.Errorf("wire multi: %w", err)
		}
		b.rs.rows = append(b.rs.rows, mr.Rows...)
		for _, k := range mr.Added {
			b.tplOrder = append(b.tplOrder, k)
		}
		if mr.HasReg {
			x := mr.Reg
			wreg = &x
		}
		res.Lines.WireMulti = b.say("wire multi", mr.Stats)
		if len(mr.NoTemplate) > 0 {
			b.say("no_template keys", fmt.Sprint(mr.NoTemplate)) // r300c diagnostics: wanted keys the pack lacks
		}
		if b.ck != nil {
			wmt := golden.Pairs{}
			for _, e := range mr.WMT {
				wmt = append(wmt, golden.Pair{K: e.ID, V: e.Key})
			}
			var wv any = pyc.NewObject()
			if len(wmt) > 0 {
				wv = wmt
			}
			var rg any
			if wreg != nil {
				rg = *wreg
			}
			b.snapshot("wiremulti", obj("stat", counterDict(mr.Stats), "wmt", wv, "wreg", rg, "tpl", b.tplKeys()))
		}
	}
	b.say("create rebase", b.st.Crs)
	b.emit("create_rebase", obj("stat", counterDict(b.st.Crs)))
	if err := b.check(); err != nil {
		return err
	}

	// WIREDEL
	if on(r.WIREDEL) {
		dl, dst, err := events.Deletes(b.dec, events.DeleteWidth)
		if err != nil {
			return fmt.Errorf("wire deletes: %w", err)
		}
		ri := map[int]bool{}
		for _, row := range b.rs.rows {
			ri[row.ID] = true
		}
		wn := &fbgd.Counter{}
		for _, x := range dl {
			for _, id := range x.IDs {
				if ri[id] {
					b.rs.add(&fbgd.Row{K: 'd', T: float64(x.T - 1), ID: id}, tInt)
					wn.Add("rows", 1)
				} else {
					wn.Add("not_in_rows", 1)
				}
			}
		}
		synSol := func(x *fbgd.Row) bool {
			return x.K == 'd' && b.rs.mt(x).syn && typ[x.ID] == fbgd.TypeKey(soldierVT)
		}
		var sy []*fbgd.Row
		for _, x := range b.rs.rows {
			if synSol(x) {
				sy = append(sy, x)
			}
		}
		b.rs.rows = slices.DeleteFunc(b.rs.rows, synSol)
		res.Lines.WireDeletes = b.say("wire deletes", dst, wn, "synthetic soldier deletes dropped", len(sy))
		if b.ck != nil {
			b.snapshot("wiredel", obj("stat", counterDict(dst), "wn", counterDict(wn), "syn_dropped", b.rs.rowsValue(sy)))
		}
	}
	if err := b.check(); err != nil {
		return err
	}

	// demo ids: sorted wire ids from idBase, then sort by t; a re-create of an id gets a fresh id
	ib := idBase
	idm := newIDMap()
	{
		var ids []int
		seen := map[int]bool{}
		for _, row := range b.rs.rows {
			if !seen[row.ID] {
				seen[row.ID] = true
				ids = append(ids, row.ID)
			}
		}
		slices.Sort(ids)
		for k, id := range ids {
			idm.set(id, ib+k)
		}
	}
	wmi := pyc.NewObject()
	for _, row := range b.rs.rows {
		if s, ok := row.Type.(string); ok && row.K == 'c' {
			n, _ := idm.get(row.ID)
			if strings.HasPrefix(s, "WM") {
				wmi.Set(fmt.Sprintf("%s:%d", s[2:], row.ID), n)
			}
		}
	}
	res.Lines.WireMultiIDs = b.say("wire multi ids", wmi)
	for _, row := range b.rs.rows {
		row.ID, _ = idm.get(row.ID)
	}
	slices.SortStableFunc(b.rs.rows, func(x, y *fbgd.Row) int { return cmp.Compare(x.T, y.T) })
	cur, seen, nxt := newIDMap(), map[int]bool{}, ib+len(idm.keys)
	nr := make([]*fbgd.Row, 0, len(b.rs.rows))
	for _, row := range b.rs.rows {
		o := row.ID
		if row.K == 'c' {
			if seen[o] {
				cur.set(o, nxt)
				nxt++
			} else {
				cur.set(o, o)
			}
			seen[o] = true
		}
		if n, ok := cur.get(o); ok {
			row.ID = n
			nr = append(nr, row)
			if row.K == 'c' || b.rs.mt(row).initial != nil {
				b.rs.mw(row).cur = cur.clone()
			}
		}
	}
	b.rs.rows = nr
	b.snapshot("renumber", obj("ib", ib, "m", idm, "nxt", nxt))
	wireID := func(t int) (int, bool) { return idm.get(t) }

	// initial states -> property chunks (+ Z383), create refs remapped
	fin := map[int]bool{}
	for _, row := range b.rs.rows {
		if row.K == 'c' {
			fin[row.ID] = true
		}
	}
	stat := &fbgd.Counter{}
	for _, row := range b.rs.rows {
		m := b.rs.mt(row)
		if m.initial != nil {
			snap := m.cur
			mp := func(t int) int {
				n := 0
				if w, ok := wireID(t); ok {
					n = w
					if v, ok := snap.get(w); ok {
						n = v
					}
				}
				if !fin[n] {
					return 0
				}
				return n
			}
			chs, err := fbgd.PropertyChunks(m.initial, m.vt, mp, fbgd.DefaultConversion)
			if err != nil {
				return err
			}
			ivt := m.vt
			mw := b.rs.mw(row)
			mw.initial, mw.vt = nil, 0
			if on(r.Z383) && ivt == z383VT {
				for i := range chs {
					if chs[i].Key == (fbgd.ChunkKey{Prop: 1}) && !chs[i].Tuple && chs[i].Value.Len() == 65 {
						v, _ := bits.FromBig(oneShl64, 65)
						chs[i].Value = v
					}
				}
			}
			row.Chunks = chs
		}
		if row.K != 'c' {
			continue
		}
		pos := fbgd.CreatePositions(&row.Create)
		if len(pos) == 0 {
			continue
		}
		snap := m.cur
		mp := func(t int) int {
			n := 0
			if w, ok := wireID(t); ok {
				n = w
				if v, ok := snap.get(w); ok {
					n = v
				}
			}
			if !fin[n] {
				n = 0
			}
			if n != 0 {
				stat.Add("mapped", 1)
			} else {
				stat.Add("null", 1)
			}
			return n
		}
		if row.Create, err = fbgd.Remap(row.Create, pos, mp); err != nil {
			return err
		}
	}
	b.say("create refs", stat)
	b.snapshot("create_refs", obj("stat", counterDict(stat)))

	// owner-bound ghosts (create refs a soldier) die with the target
	type dtt struct {
		t  float64
		tf tkind
	}
	dts := map[int][]dtt{}
	for _, row := range b.rs.rows {
		if row.K == 'd' {
			dts[row.ID] = append(dts[row.ID], dtt{row.T, b.rs.mt(row).tf})
		}
	}
	for _, row := range slices.Clone(b.rs.rows) {
		if row.K != 'c' {
			continue
		}
		for _, ref := range row.Create.Refs {
			var best *dtt
			for k := range dts[ref.Target] {
				x := &dts[ref.Target][k]
				if x.t >= row.T && (best == nil || x.t < best.t) {
					best = x
				}
			}
			if best != nil && row.Type != fbgd.TypeKey(soldierVT) {
				b.rs.add(&fbgd.Row{K: 'd', T: best.t, ID: row.ID}, best.tf)
				stat.Add("owner_delete", 1)
			}
		}
	}
	b.say("owner deletes", stat.Get("owner_delete"))
	b.snapshot("owner_deletes", obj("stat", counterDict(stat)))
	types := &fbgd.Counter{}
	for _, row := range b.rs.rows {
		if row.K == 'c' {
			if v, ok := row.Type.(int64); ok {
				types.Add(fmt.Sprintf("%#x", v), 1)
			} else {
				types.Add(fmt.Sprint(row.Type), 1)
			}
		}
	}
	b.say(counterRepr{types})
	if len(b.rs.rows) == 0 {
		return errors.New("ValueError: min() iterable argument is empty")
	}
	t0, t0f := b.rs.rows[0].T, b.rs.mt(b.rs.rows[0]).tf
	for _, row := range b.rs.rows[1:] {
		if row.T < t0 {
			t0, t0f = row.T, b.rs.mt(row).tf
		}
	}
	var jk float64
	if on(r.JOINKEY) {
		var n int
		if strings.ToLower(strings.TrimSpace(r.JOINKEY)) == "auto" { // r289 (default): the wire join burst + 1
			var cf []int
			for _, row := range b.rs.rows {
				if row.K == 'c' {
					q, err := pyc.FloatFloorDiv(row.T-t0, dt)
					if err != nil {
						return err
					}
					cf = append(cf, int(q))
				}
			}
			n = fbgd.JoinFoldFrame(cf, fbgd.JoinGap, fbgd.JoinMin, fbgd.JoinMax) + 1
		} else {
			var err error
			if n, err = parseInt("JOINKEY", r.JOINKEY); err != nil {
				return err
			}
		}
		b.say("joinkey", strings.TrimSpace(r.JOINKEY), "fold frame", n-1)
		b.joinKey = strconv.Itoa(n)
		jk = t0 + float64(float64(n)*dt)
		jn := 0
		for _, row := range b.rs.rows {
			if (row.K == 'c' || row.K == 'u') && t0 < row.T && row.T < jk {
				row.T = t0
				b.setTF(row, t0f)
				jn++
			}
		}
		b.say("join batch moved to t0", jn)
		b.snapshot("joinkey", obj("t0", timeValue(t0, t0f), "jk", jk, "jn", jn))
	}
	{
		var sc []float64
		for _, row := range b.rs.rows {
			if row.K == 'c' && row.Type == fbgd.TypeKey(soldierVT) {
				sc = append(sc, (row.T-t0)/1e7)
			}
		}
		slices.Sort(sc)
		l := make([]any, len(sc))
		for i, x := range sc {
			v, err := pyc.RoundFloat(x, 1)
			if err != nil {
				return err
			}
			l[i] = v
		}
		b.say("soldier create times", l)
	}
	if err := b.soldierRefs(idm); err != nil {
		return err
	}
	b.snapshot("soldier_refs", obj("t0", timeValue(t0, t0f), "stat", counterDict(b.srStat)))
	if err := b.check(); err != nil {
		return err
	}
	if upd {
		if err := b.clientUpdates(idm, tv, widths, soldCov); err != nil {
			return err
		}
	}
	if on(r.DELNULL) {
		types := map[int]fbgd.TypeKey{}
		for _, row := range b.rs.rows {
			if row.K == 'c' {
				types[row.ID] = row.Type
			}
		}
		n0 := len(b.rs.rows)
		dn := &fbgd.Counter{}
		// r284: slots = VIEW_SLOTS (component p12, camera p3), like v3_cl04c
		rows, hits, err := fbgd.DelNull(b.rs.rows, types, fbgd.TypeKey(soldierVT), dn, fbgd.ViewSlots, dt)
		if err != nil {
			return err
		}
		b.rs.rows = rows
		for _, x := range rows[n0:] {
			b.rs.mw(x).tf = tLoose
		}
		t00 := minT(b.rs.rows)
		var hl []any
		for _, h := range hits[:min(20, len(hits))] {
			q, _ := pyc.FloatFloorDiv(h.T-t00, dt)
			hl = append(hl, tuple{int64(q), h.Holder, h.Prop, h.Deleted})
		}
		if hl == nil {
			hl = []any{}
		}
		b.say("delnull", dn, "(row bin, holder, prop, deleted)", hl)
		if b.ck != nil {
			hv := make([]any, len(hits))
			for i, h := range hits {
				hv[i] = []any{h.T, h.Holder, h.Prop, h.Deleted}
			}
			b.snapshot("delnull", obj("dn", counterDict(dn), "dl", hv))
		}
	}
	if err := b.check(); err != nil {
		return err
	}

	// registry (REG0WIRE join row, REGWIRE updates), VIEWERID
	var regid, pid int
	hasReg := wreg != nil
	var pl []*fbgd.Row
	var vp *fbgd.Row
	if hasReg {
		regid, _ = idm.get(int(*wreg))
		for _, row := range b.rs.rows {
			if row.K == 'c' && row.Type == fbgd.TypeKey(int64(playerVT)) {
				pl = append(pl, row)
			}
		}
		if len(pl) > 0 {
			if vp, pid, err = b.registry(idm, regid, pl); err != nil {
				return err
			}
		}
	}
	if on(r.VIEWERID) && hasReg && len(pl) > 0 {
		if err := b.viewerID(vp, pl, pid, regid); err != nil {
			return err
		}
	}
	tgap := t0 + float64(joinRest*dt)
	var sh int64
	if on(r.SHIFTF) {
		n, err := parseInt("SHIFTF", r.SHIFTF)
		if err != nil {
			return err
		}
		sh = int64(float64(n) * dt)
		for _, row := range b.rs.rows {
			if row.T > tgap {
				row.T += float64(sh)
			}
		}
		b.snapshot("shiftf", obj("sh", sh))
	}
	if err := b.check(); err != nil {
		return err
	}

	// assemble + checks + files
	data, k, info, err := fbgd.Assemble(b.rs.rows, b.tpl, t0, false)
	if err != nil {
		return fmt.Errorf("assemble: %w", err)
	}
	res.Frames, res.Keyframes, res.TplNewBad, res.FBGDBytes = info.Frames, info.Keyframes, len(info.TplNewBad), len(data)
	tb := []any{}
	for _, x := range info.TplNewBad[:min(5, len(info.TplNewBad))] {
		tb = append(tb, tuple{x[0], x[1]})
	}
	res.Lines.TplNewBad = b.say("tplnew_bad", len(info.TplNewBad), "first (frame, id)", tb)
	res.Lines.CrebaseVsSrc = b.say(b.crebaseVsSrc(data))
	files := []string{base + ".fbgd", base + ".fbgk"}
	if err := os.MkdirAll(b.in.OutDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(files[0], data, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(files[1], k, 0o644); err != nil {
		return err
	}
	srcBase := filepath.Join(b.srcDir, r.SRC)
	if !on(r.WIRECOMP) {
		for _, ext := range Exts[2:8] {
			raw, err := os.ReadFile(srcBase + "." + ext)
			if err != nil {
				return err
			}
			if err := os.WriteFile(base+"."+ext, raw, 0o644); err != nil {
				return err
			}
			files = append(files, base+"."+ext)
		}
	}
	var txt []byte
	if mpe != nil {
		txt = []byte(mpe.Txt)
	} else if txt, err = os.ReadFile(srcBase + ".txt"); err != nil {
		return err
	}
	if err := os.WriteFile(base+".txt", txt, 0o644); err != nil {
		return err
	}
	if on(r.WIRECOMP) {
		cf, err := b.companions(t0, info, base)
		if err != nil {
			return err
		}
		files = append(files, cf...)
	}
	files = append(files, base+".txt")
	res.Files = orderFiles(files)
	creates := 0
	for _, row := range b.rs.rows {
		if row.K == 'c' {
			creates++
		}
	}
	res.IDs, res.Creates = len(idm.keys), creates
	res.Lines.Last = b.say(obj("frames", info.Frames, "keyframes", info.Keyframes), len(data), "ids", len(idm.keys), "creates", creates)
	return nil
}

var oneShl64 = new(big.Int).Lsh(big.NewInt(1), 64)

func orderFiles(files []string) []string {
	ext := func(p string) int { return slices.Index(Exts, strings.TrimPrefix(filepath.Ext(p), ".")) }
	files = slices.Compact(files)
	slices.SortStableFunc(files, func(a, c string) int { return ext(a) - ext(c) })
	return slices.Compact(files)
}

func minT(rows []*fbgd.Row) float64 {
	t := math.Inf(1)
	for _, r := range rows {
		t = min(t, r.T)
	}
	return t
}

func (b *build) setTF(r *fbgd.Row, tf tkind) {
	if tf == tInt && b.rs.m[r] == nil {
		return
	}
	b.rs.mw(r).tf = tf
}

func (b *build) tplKeys() []any {
	out := make([]any, len(b.tplOrder))
	for i, k := range b.tplOrder {
		out[i] = k
	}
	return out
}

// nullTemplate is the template with server-id refs in its defaults nulled (remap_server_chunk(c, lambda o: 0)).
func nullTemplate(t *fbgd.Template) (*fbgd.Template, error) {
	nt := &fbgd.Template{Sections: make([]fbgd.TplSection, len(t.Sections))}
	for i, s := range t.Sections {
		ns := fbgd.TplSection{Header: s.Header, Mask: s.Mask, Defaults: make([]fbgd.OutChunk, len(s.Defaults))}
		for j, c := range s.Defaults {
			nc, err := fbgd.RemapServerChunk(c, func(int) int { return 0 })
			if err != nil {
				return nil, err
			}
			ns.Defaults[j] = nc
		}
		nt.Sections[i] = ns
	}
	return nt, nil
}

// slice is _slice(d, g, _w) (wire_py_core.slice_record_py): one wire site.
func (b *build) slice(g *wire.Ghost, widths map[int]int64) (*wire.SliceResult, error) {
	b.sites++
	b.inSlice = true
	defer func() { b.inSlice = false }()
	return wire.SliceRecord(b.d, g, widths)
}

// traceSites counts the wire sites of the sections calls and records every create_fields call (ckpt_create_ctx).
// A create_fields call inside a sections call (run_section, camera_init) is held until that site's trace: Python's
// run_section passes a copy of the multi record with section_vt = the section read (its key shows that vt).
func (b *build) traceSites() {
	var pending []*pyc.Object
	b.st.Trace = func(s sections.Site) {
		for _, rec := range pending {
			if s.Kind == "run_section" && len(s.Args) > 1 {
				k, _ := rec.Get("key")
				k.([]any)[4] = s.Args[1]
			}
			b.ck("ckpt_create_ctx", rec, false)
		}
		pending = nil
		b.sites++
	}
	b.d.OnCreateFields = func(g *wire.Ghost, built bool, res map[uint32]int64) {
		site := b.sites // inside a sections call: its site is counted when it returns
		if b.inSlice {
			site = b.sites - 1
		}
		var gv any
		if i, ok := b.gi[g]; ok {
			gv = []any{"ghosts", i}
		}
		rec := obj("g", gv, "key", []any{g.T, g.ID, g.Pseq, g.Phase, g.SectionVT}, "built", built, "site", site,
			"result", fieldsValue(res))
		if !b.inSlice {
			if built {
				rec.Set("ctx", b.ctxValue())
			}
			pending = append(pending, rec)
			return
		}
		if built {
			rec.Set("ctx", b.ctxValue())
		}
		b.ck("ckpt_create_ctx", rec, false)
	}
}

func (b *build) ctxValue() []any {
	ctx := []any{}
	for i, x := range b.d.Ghosts {
		if f, ok := b.d.CreateCtx(x); ok && f != nil {
			ctx = append(ctx, []any{[]any{"ghosts", i}, fieldsValue(f)})
		}
	}
	return ctx
}

func fieldsValue(f map[uint32]int64) any {
	if len(f) == 0 {
		return pyc.NewObject()
	}
	ks := make([]uint32, 0, len(f))
	for k := range f {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	ps := make(golden.Pairs, len(ks))
	for i, k := range ks {
		ps[i] = golden.Pair{K: k, V: f[k]}
	}
	return ps
}
