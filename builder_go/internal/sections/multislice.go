package sections

import (
	"fmt"
	"slices"
	"strconv"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/golden"
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// NextEntries is v3_multi_slice.next_entries(d): per packet seq (first-appearance order over ghosts then multi),
// the sorted entry cursors of its records.
type NextEntries struct {
	Order []int64
	By    map[int64][]int64
}

// NewNextEntries computes next_entries(d).
func NewNextEntries(d *wire.Log) *NextEntries {
	ne := &NextEntries{By: map[int64][]int64{}}
	for _, gs := range [2][]*wire.Ghost{d.Ghosts, d.Multi} {
		for _, g := range gs {
			if _, ok := ne.By[g.Pseq]; !ok {
				ne.Order = append(ne.Order, g.Pseq)
			}
			ne.By[g.Pseq] = append(ne.By[g.Pseq], g.EntryCursor)
		}
	}
	for _, v := range ne.By {
		slices.Sort(v)
	}
	return ne
}

// PyValue is the {pseq: [cursors]} dict ($dict: int keys).
func (ne *NextEntries) PyValue() any {
	if len(ne.Order) == 0 {
		return pyc.NewObject()
	}
	ps := make(golden.Pairs, len(ne.Order))
	for i, k := range ne.Order {
		ps[i] = golden.Pair{K: k, V: ne.By[k]}
	}
	return ps
}

const registryVT = 0x1432098 // player registry: create only here; its s1.p0 updates come from REGWIRE

// MultiResult is the WIREMULTI step's output: rows to append (creates with 'WM...' type keys, 'u' rows with
// (section, property) chunks), the 'wire multi' stats, the registry ghost id (_wreg) and the template keys added to
// tpl in order.
type MultiResult struct {
	Rows   []*fbgd.Row
	Stats  *fbgd.Counter
	Reg    int64
	HasReg bool
	Added  []string
	// WMT is cl04c's _wmt {wire id: template key} in dict order (every listed create, also the no_template ones).
	WMT []WMEntry
	// NoTemplate (r300c, diagnostics only) counts the no_template creates per wanted key (base key + "/vN").
	NoTemplate map[string]int
}

// WMEntry is one _wmt item.
type WMEntry struct {
	ID  int64
	Key string
}

// tplKey is cl04c _wmkey(g, cb) (r164): 'WM<vt>/<nsec>', variant-qualified when the pack has that key.
func tplKey(g *wire.Ghost, cb string, pack fbgd.Templates) string {
	k := fmt.Sprintf("WM%x/%d", g.Sections[0], len(g.Sections))
	if len(cb) >= 19 {
		if t := pyInt2(cb[:6]); (t == 3 || t == 6) && cb[6] == '1' {
			kv := k + "/v" + strconv.FormatUint(pyInt2(cb[7:19]), 10)
			if _, ok := pack[kv]; ok {
				return kv
			}
		}
	}
	return k
}

// variantOf is the "/v<N>" suffix tplKey would use for create bits cb ("" when the create carries no variant).
func variantOf(cb string) string {
	if len(cb) >= 19 {
		if t := pyInt2(cb[:6]); (t == 3 || t == 6) && cb[6] == '1' {
			return "/v" + strconv.FormatUint(pyInt2(cb[7:19]), 10)
		}
	}
	return ""
}

// WireMulti is cl04c's WIREMULTI step (r135..r189): the multi-section ghosts of the listed classes sliced from the
// wire, in (t, pseq, entry_cursor) order. Creates (CREBASE re-encoded) become 'c' rows typed by their template key
// (added to tpl from pack, setdefault); every present section is read by its Python codec (Section, physics s1.p2
// rebased) when the layout has a template, else by wire.RunSection + ChunksOf (refs dropped, origin vectors rebased
// or raw kept); the physics section stops a non-walkable record. pack is the data pack's templates, tpl the build's
// template dict (both keyed by TypeKey strings for 'WM...'). widths is infer_id_widths(d).
func WireMulti(d *wire.Log, st *State, pack, tpl fbgd.Templates, widths map[int]int64) (res *MultiResult, err error) {
	// r300c: a reader failure names the record it happened in (new-map triage: which class/variant/section overruns).
	var curG *wire.Ghost
	var curKey string
	curSi, curVT, curCur := -1, uint32(0), 0
	variant := map[int64]string{} // wire id -> "/vN" of its create (the key tplKey wanted when the pack lacks it)
	var walked []string           // "si:vt@start+present" of the sections walked so far in the current record
	defer func() {
		if err != nil && curG != nil {
			limit := int64(-1)
			if p := d.Packets[curG.Pseq]; p != nil {
				limit = p.Limit
			}
			err = fmt.Errorf("record t=%d pseq=%d id=%d phase=%d class=%#x/%d key=%q variant=%q section=%d vt=%#x cursor=%d entry=%d packet_limit=%d walked=%v: %w",
				curG.T, curG.Pseq, curG.ID, curG.Phase, curG.Sections[0], len(curG.Sections), curKey, variant[curG.ID], curSi,
				curVT, curCur, curG.EntryCursor, limit, walked, err)
		}
	}()
	defer catch(&err)
	res = &MultiResult{Stats: &fbgd.Counter{}}
	ws := res.Stats
	listed := func(g *wire.Ghost) bool {
		s0 := g.Sections[0]
		for _, k := range st.WireMulti {
			if k.VT == s0 && (k.NSec == 0 || k.NSec == len(g.Sections)) {
				return true
			}
		}
		return false
	}
	created := map[int64]bool{} // _wa
	keyOf := map[int64]string{} // _wmt
	ms := slices.Clone(d.Multi)
	slices.SortStableFunc(ms, func(a, b *wire.Ghost) int {
		switch {
		case a.T != b.T:
			return cmpU(a.T, b.T)
		case a.Pseq != b.Pseq:
			return cmpI(a.Pseq, b.Pseq)
		}
		return cmpI(a.EntryCursor, b.EntryCursor)
	})
	if st.Item != nil {
		defer func() {
			if err == nil {
				st.Item(-1, nil, res)
			}
		}()
	}
	for i, g := range ms {
		if st.Item != nil {
			st.Item(i, g, res)
		}
		if len(g.Sections) == 0 {
			raise("IndexError", "list index out of range")
		}
		if !listed(g) {
			continue
		}
		curG, curKey, curSi, curVT, curCur, walked = g, keyOf[g.ID], -1, 0, int(g.Cursor), nil
		bs, err := st.payloadBits(d, g)
		if err != nil {
			return nil, err
		}
		if g.Config < 0 || g.Config >= len(d.Configs) {
			raise("IndexError", "list index out of range")
		}
		table := d.Configs[g.Config].Tables[0]
		ib := bitLength(len(table))
		s0 := g.Sections[0]
		switch {
		case g.Phase == 1:
			cb, err := st.RebaseCreate(s0, slice(bs, int(g.CreateCur), int(g.Cursor)), g)
			if err != nil {
				return nil, err
			}
			tp := tplKey(g, cb, pack)
			variant[g.ID], curKey = variantOf(cb), tp
			if _, ok := keyOf[g.ID]; ok {
				for k := range res.WMT {
					if res.WMT[k].ID == g.ID {
						res.WMT[k].Key = tp
					}
				}
			} else {
				res.WMT = append(res.WMT, WMEntry{g.ID, tp})
			}
			keyOf[g.ID] = tp
			t, ok := pack[tp]
			if ok && st.SkipVariants[variant[g.ID]] {
				ok = false // r300c triage knob WIREMULTI_SKIPV: drop the creates of these variants (counted as no_template)
			}
			if !ok {
				ws.Add("no_template", 1)
				if res.NoTemplate == nil {
					res.NoTemplate = map[string]int{}
				}
				res.NoTemplate[fmt.Sprintf("WM%x/%d%s", s0, len(g.Sections), variant[g.ID])]++
				delete(created, g.ID) // r291: a re-used wire id must not keep its earlier create (its updates would be sliced)
				continue
			}
			if _, ok := tpl[tp]; !ok {
				tpl[tp] = t
				res.Added = append(res.Added, tp)
			}
			c, err := chunkOf(cb)
			if err != nil {
				return nil, err
			}
			res.Rows = append(res.Rows, &fbgd.Row{K: 'c', T: float64(g.T), ID: int(g.ID), Type: tp, Create: c})
			created[g.ID] = true
			ws.Add("create", 1)
			if s0 == registryVT {
				res.Reg, res.HasReg = g.ID, true
			}
		case !created[g.ID]:
			ws.Add("no_create", 1)
			continue
		case s0 == registryVT:
			continue // reader 0x1431b7c reads 0 bits offline; REGWIRE decodes these (r136)
		}
		cur := int(g.Cursor)
		var ch []fbgd.RowChunk
		failed := false
		put := func(si, pi int, body string) {
			v, err := bits.Parse(body)
			if err != nil {
				panic(err)
			}
			k := fbgd.ChunkKey{Sec: si, Prop: pi}
			for i := range ch {
				if ch[i].Key == k {
					ch[i].Value = v
					return
				}
			}
			ch = append(ch, fbgd.RowChunk{Key: k, Tuple: true, Value: v})
		}
		tp := keyOf[g.ID]
		_, inTpl := tpl[tp]
		walk := Walkable(g.Sections) && inTpl
	sections:
		for si, vt := range g.Sections {
			cur++
			curKey, curSi, curVT, curCur = tp, si, vt, cur
			walked = append(walked, fmt.Sprintf("%d:%#x@%d%c", si, vt, cur-1, at(bs, cur-1)))
			if at(bs, cur-1) != '1' {
				continue
			}
			if PY[vt] && inTpl && (walk || vt != VTPhys) {
				secs := tpl[tp].Sections
				if si >= len(secs) {
					raise("IndexError", "list index out of range")
				}
				ts := &secs[si]
				layout := LayoutKey(g.Sections) // r300f: the second /7 shape has its own physics p2 shape
				cfg := WheelCfg[[2]int{layout, si}]
				origin := OriginOf(g)
				r, err := Section(bs, cur, vt, len(ts.Defaults), origin, table, cfg, true, ts, layout)
				st.trace("vehsec", r, err, bs, cur, vt, len(ts.Defaults), origin, table, cfg, true, TplRef{tp, si}, layout)
				if err != nil {
					if !IsValueError(err) {
						return nil, err
					}
					failed = true
					ws.Add("vs_fail", 1)
					break sections
				}
				cur = r.End
				for _, c := range r.Chunks {
					put(si, c.P, c.Body)
				}
				if r.P2 != nil {
					ws.Add(fmt.Sprintf("vs_p2_flag%d", r.P2.Flag), 1)
				}
				continue
			}
			if vt == VTPhys {
				ws.Add("phys_stop", 1) // handoff102: keep the sections sliced so far (r133)
				break
			}
			sr, err := wire.RunSection(d, g, vt, int64(cur), widths)
			st.trace("run_section", sr, err, g, vt, cur, widths)
			if err != nil {
				return nil, err
			}
			if sr.Err != "" {
				failed = true
				break
			}
			cs, err := ChunksOf(bs, sr.Positions, sr.End, sr.Refs, sr.Ovs)
			st.trace("chunks_of", cs, err, bs, sr.Positions, sr.End, sr.Refs, sr.Ovs)
			if err != nil {
				return nil, err
			}
			for _, c := range cs {
				body := c.Body
				if len(c.Refs) > 0 {
					ws.Add("ref_dropped", 1) // 405/888 vehicles carry no refs (r129)
					continue
				}
				if len(c.Ovs) > 0 {
					rb, ok, err := Rebase(body, c.Ovs, table)
					if st.Trace != nil {
						var rv any
						if ok {
							rv = rb
						}
						st.trace("audit888_rebase", rv, err, body, c.Ovs, table)
					}
					if err != nil {
						return nil, err
					}
					switch {
					case ok:
						body = rb
						ws.Add("rebased", 1)
					case allRaw(body, c.Ovs, ib):
						ws.Add("raw_vec", 1) // raw float: kept (as REBASE)
					default:
						ws.Add("vec_dropped", 1)
						continue
					}
				}
				put(si, c.P, body)
			}
			cur = int(sr.End)
		}
		if failed {
			ws.Add("slice_fail", 1)
			continue
		}
		if len(ch) > 0 {
			res.Rows = append(res.Rows, &fbgd.Row{K: 'u', T: float64(g.T), ID: int(g.ID), Chunks: ch})
			if g.Phase != 0 {
				ws.Add("init", 1)
			} else {
				ws.Add("upd", 1)
			}
		}
		if len(ch) > 0 && g.Phase != 0 { // diagnostic: wire initial state vs the template baseline
			secs := tpl[tp].Sections
			for _, c := range ch {
				if c.Key.Sec >= len(secs) || c.Key.Prop >= len(secs[c.Key.Sec].Defaults) {
					raise("IndexError", "list index out of range")
				}
				if c.Value.Equal(secs[c.Key.Sec].Defaults[c.Key.Prop].Value) {
					ws.Add("init_eq_tpl", 1)
				} else {
					ws.Add("init_ne_tpl", 1)
				}
			}
		}
	}
	return res, nil
}

// allRaw is all(int(body[a:a+ib], 2) == 0 for a, _, _ in ovs) (short-circuit: an empty slice raises only when reached).
func allRaw(body string, ovs []AuOvec, ib int) bool {
	for _, v := range ovs {
		if pyInt2(slice(body, v.A, v.A+ib)) != 0 {
			return false
		}
	}
	return true
}

func cmpU(a, b uint64) int {
	if a < b {
		return -1
	}
	return 1
}

func cmpI(a, b int64) int {
	if a < b {
		return -1
	}
	return 1
}
