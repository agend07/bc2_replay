package pipeline

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/events"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/gate"
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// curSnap is one (create t, _cur) of cl04c's _cr list.
type curSnap struct {
	t   float64
	cur *idMap
}

// curSnaps is _cr = sorted(((r['t'], r['_cur']) for create rows with _cur), key=t) and the created ids (_ids).
func (b *build) curSnaps() ([]curSnap, map[int]bool) {
	var cr []curSnap
	ids := map[int]bool{}
	for _, r := range b.rs.rows {
		if r.K != 'c' {
			continue
		}
		ids[r.ID] = true
		if m := b.rs.mt(r); m.cur != nil {
			cr = append(cr, curSnap{r.T, m.cur})
		}
	}
	slices.SortStableFunc(cr, func(x, y curSnap) int { return cmp.Compare(x.t, y.t) })
	return cr, ids
}

// snapAt is next((c for t, c in reversed(_cr) if t <= at), _cr[0][1] if _cr else {}).
func snapAt(cr []curSnap, at float64) *idMap {
	for i := len(cr) - 1; i >= 0; i-- {
		if cr[i].t <= at {
			return cr[i].cur
		}
	}
	if len(cr) > 0 {
		return cr[0].cur
	}
	return newIDMap()
}

// mapVia is the cl04c mp closures: wire id o -> _m -> the snapshot's current demo id, 0 unless created.
func mapVia(idm, snap *idMap, ids map[int]bool, o int) int {
	mm, ok := idm.get(o)
	if !ok {
		return 0
	}
	n := mm
	if v, ok := snap.get(mm); ok {
		n = v
	}
	if !ids[n] {
		return 0
	}
	return n
}

// soldierRefs is the r202 block: the soldier pkl rows' ghost references -> demo ids through the id snapshots.
func (b *build) soldierRefs(idm *idMap) error {
	sr := &fbgd.Counter{}
	b.srStat = sr
	any := false
	for _, r := range b.rs.rows {
		if b.rs.mt(r).srefs != nil {
			any = true
			break
		}
	}
	if any {
		cr, ids := b.curSnaps()
		for _, r := range b.rs.rows {
			m := b.rs.mt(r)
			if m.srefs == nil {
				continue
			}
			snap := snapAt(cr, m.wt)
			chs := slices.Clone(r.Chunks)
			for _, pr := range m.srefs {
				i := slices.IndexFunc(chs, func(c fbgd.RowChunk) bool { return !c.Tuple && c.Key == fbgd.ChunkKey{Prop: pr.Prop} })
				if i < 0 {
					return fmt.Errorf("KeyError: %d", pr.Prop)
				}
				c := chs[i]
				bs := []byte(c.Value.String())
				refs := []fbgd.Ref{}
				for _, x := range pr.Refs {
					off, w := int(x.Off), int(x.Bits)
					o, err := int2(pySlice(string(bs), off, off+w))
					if err != nil {
						return err
					}
					n := mapVia(idm, snap, ids, int(o))
					if w < 63 && n >= 1<<w {
						n = 0
						sr.Add("unfit", 1)
					}
					if n != 0 {
						sr.Add("mapped", 1)
					} else {
						sr.Add("null", 1)
					}
					if off < 0 || off+w > len(bs) {
						return fmt.Errorf("pipeline: soldier ref at %d+%d outside its %d-bit chunk", off, w, len(bs))
					}
					copy(bs[off:off+w], bits.FormatBin(uint64(n), w))
					if n != 0 {
						refs = append(refs, fbgd.Ref{Target: n})
					}
				}
				v, err := bits.Parse(string(bs))
				if err != nil {
					return err
				}
				chs[i] = fbgd.RowChunk{Key: c.Key, Tuple: c.Tuple, Value: v, Refs: refs}
			}
			mw := b.rs.mw(r)
			mw.srefs, mw.wt = nil, 0
			r.Chunks = chs
			sr.Add("rows", 1)
		}
	}
	b.say("soldier pkl refs", sr)
	return nil
}

// clientUpdates is the UPD client-updates loop: non-soldier (and pkl-uncovered soldier) updates sliced from the
// wire, UPDREBASE/C4UPD classes rebased, refs mapped.
func (b *build) clientUpdates(idm *idMap, tv map[uint32]bool, widths map[int]int64, soldCov map[[2]int64]bool) error {
	r := b.r
	cr, ids := b.curSnaps()
	us := &fbgd.Counter{}
	urv := map[uint32]bool{}
	var err error
	if on(r.REBASE) {
		if urv, err = hexSet(r.UPDREBASE, true); err != nil {
			return err
		}
		if on(r.C4UPD) {
			urv[c4VT] = true
		}
	}
	lp := b.newLoop("client_updates_rec", us)
	for i, g := range b.d.Ghosts {
		lp.before(len(b.rs.rows))
		if err := b.clientUpdate(g, idm, tv, widths, soldCov, cr, ids, us, urv); err != nil {
			return err
		}
		lp.after(i, []any{g.T, g.ID, g.Pseq, g.SectionVT}, b.rs.rows)
	}
	b.say("client updates", us)
	b.snapshot("client_updates", obj("stat", counterDict(us)))
	return nil
}

func (b *build) clientUpdate(g *wire.Ghost, idm *idMap, tv map[uint32]bool, widths map[int]int64, soldCov map[[2]int64]bool,
	cr []curSnap, ids map[int]bool, us *fbgd.Counter, urv map[uint32]bool) error {
	if g.Phase != 0 || !tv[g.SectionVT] {
		return nil
	}
	m, ok := idm.get(int(g.ID))
	if !ok {
		return nil
	}
	if int64(g.SectionVT) == soldierVT {
		if soldCov[[2]int64{int64(g.T), g.ID}] {
			us.Add("pkl", 1)
			return nil
		}
		us.Add("soldier_uncovered", 1)
	}
	rec, err := b.slice(g, widths)
	if err != nil {
		return err
	}
	if len(rec.OriginVectors) > 0 && rec.Error == "" && rec.Flags == 0 && urv[g.SectionVT] {
		out, err := b.rebaseChunks(rec, g, us)
		if err != nil {
			return err
		}
		if len(out) > 0 {
			nr := *rec
			nr.Chunks, nr.OriginVectors = out, []wire.Ovec{}
			rec = &nr
		}
	}
	if rec.Error != "" || rec.Flags != 0 || len(rec.OriginVectors) > 0 || len(rec.Chunks) == 0 {
		us.Add("skip", 1)
		return nil
	}
	snap := snapAt(cr, float64(g.T))
	id, ok := snap.get(m)
	if !ok {
		us.Add("noghost", 1)
		return nil
	}
	mp := func(t int) int {
		n := mapVia(idm, snap, ids, t)
		if n != 0 {
			us.Add("mapped", 1)
		} else {
			us.Add("null", 1)
		}
		return n
	}
	chs, err := fbgd.PropertyChunks(rec, g.SectionVT, mp, fbgd.DefaultConversion)
	if err != nil {
		return err
	}
	b.rs.add(&fbgd.Row{K: 'u', T: float64(g.T), ID: id, Chunks: chs}, tInt)
	us.Add("rows", 1)
	return nil
}

// registry is the REG0WIRE / REGWIRE block (or the synthetic pre-r136 rows): returns the viewer's player row and id.
func (b *build) registry(idm *idMap, regid int, pl []*fbgd.Row) (*fbgd.Row, int, error) {
	r := b.r
	inv := map[int]int{}
	for _, k := range idm.keys {
		inv[idm.m[k]] = k
	}
	reg := &events.Registry{D: b.dec}
	vw, vwOK := reg.ViewerPlayer()
	// r300l: the viewer is the player the (single) component 0x1431df8 create refs (r300k survey: always the
	// recording user); the r193 first-create-bits rule is the fallback (it picks the wrong player on MP_007 r2).
	vp := componentOwner(b.rs.rows, pl)
	if vp != nil {
		w := -1
		if x, ok := inv[vp.ID]; ok {
			w = x
		}
		b.say(fmt.Sprintf("viewer: component owner id=%d (wire %d)", vp.ID, w))
	} else {
		vp = pl[0]
		for _, x := range pl {
			if w, ok := inv[x.ID]; ok && vwOK && w == vw {
				vp = x
				break
			}
		}
		b.say(fmt.Sprintf("WARN viewer: no component create refs a player; fallback ViewerPlayer id=%d (wire %d, ok %v)",
			vp.ID, vw, vwOK))
	}
	pid := vp.ID
	cr, ids := b.curSnaps()
	rmap := func(t float64) func(int) int {
		snap := snapAt(cr, t)
		return func(o int) int { return mapVia(idm, snap, ids, o) }
	}
	regChunk := func(sid int) []fbgd.RowChunk {
		s := "1" + bits.FormatBin(uint64(pid), 13) + bits.FormatBin(uint64(sid), 13)
		if sid != 0 {
			s += "10"
		}
		v, _ := bits.Parse(s)
		refs := []fbgd.Ref{}
		for _, x := range []int{pid, sid} {
			if x != 0 {
				refs = append(refs, fbgd.Ref{Target: x})
			}
		}
		return []fbgd.RowChunk{{Key: fbgd.ChunkKey{Sec: 1, Prop: 0}, Tuple: true, Value: v, Refs: refs}}
	}
	var r0 []*fbgd.Row
	if on(r.REG0WIRE) {
		j0 := &fbgd.Counter{}
		wp := -1
		if w, ok := inv[vp.ID]; ok {
			wp = w
		}
		rows, why, err := reg.JoinRows(regid, pid, wp, rmap(vp.T), j0)
		if err != nil {
			return nil, 0, err
		}
		if why != "" {
			b.say("reg0: unavailable:", why)
		}
		r0 = rows
		if len(r0) > 0 {
			ks := make([]tuple, 0, len(r0[0].Chunks))
			for _, c := range r0[0].Chunks {
				ks = append(ks, tuple{c.Key.Sec, c.Key.Prop})
			}
			slices.SortFunc(ks, func(x, y tuple) int {
				return cmp.Or(cmp.Compare(x[0].(int), y[0].(int)), cmp.Compare(x[1].(int), y[1].(int)))
			})
			l := make([]any, len(ks))
			for i, k := range ks {
				l[i] = k
			}
			b.say("registry join rows", l, j0)
		}
	}
	if len(r0) > 0 {
		for _, x := range r0 {
			b.rs.add(x, tInt)
		}
	} else {
		b.rs.add(&fbgd.Row{K: 'u', T: vp.T + float64(int64(2*dt)), ID: regid, Chunks: regChunk(0)}, b.rs.mt(vp).tf)
	}
	if on(r.REGWIRE) {
		if err := b.regWire(reg, regid, rmap); err != nil {
			return nil, 0, err
		}
	} else {
		var sols []*fbgd.Row
		for _, x := range b.rs.rows {
			if x.K == 'c' && x.Type == fbgd.TypeKey(soldierVT) {
				sols = append(sols, x)
			}
		}
		for _, s := range sols {
			b.rs.add(&fbgd.Row{K: 'u', T: s.T + float64(int64(dt)), ID: regid, Chunks: regChunk(s.ID)}, b.rs.mt(s).tf)
			var best *fbgd.Row
			for _, x := range b.rs.rows {
				if x.K == 'd' && x.ID == s.ID && x.T > s.T && (best == nil || x.T < best.T) {
					best = x
				}
			}
			if best != nil {
				b.rs.add(&fbgd.Row{K: 'u', T: best.T, ID: regid, Chunks: regChunk(0)}, b.rs.mt(best).tf)
			}
		}
	}
	b.say("registry updates for player", pid, "registry row", regid)
	if b.ck != nil {
		var vv any
		if vwOK {
			vv = vw
		}
		b.snapshot("registry", obj("pid", pid, "regid", regid, "viewer_wire", vv, "r0", len(r0)))
	}
	return vp, pid, nil
}

// componentOwner is the viewer's player row: of the component (compVT) creates in rows, the earliest (by T) whose
// create refs hit a row of pl; the first pl row it refs wins. Create refs are output ids here (remapped in Run),
// the same space as pl[].ID. nil when no component create refs a player.
func componentOwner(rows, pl []*fbgd.Row) *fbgd.Row {
	byID := map[int]*fbgd.Row{}
	for _, x := range pl {
		if _, ok := byID[x.ID]; !ok {
			byID[x.ID] = x
		}
	}
	var best, bp *fbgd.Row
	for _, x := range rows {
		if x.K != 'c' || x.Type != fbgd.TypeKey(int64(compVT)) || (best != nil && x.T >= best.T) {
			continue
		}
		for _, r := range x.Create.Refs {
			if p, ok := byID[r.Target]; ok {
				best, bp = x, p
				break
			}
		}
	}
	return bp
}

// regWire is the REGWIRE loop (r136/r171/r195): every registry update record parsed up to the next logged entry
// (else the packet limit), refs mapped at the record's time.
func (b *build) regWire(reg *events.Registry, regid int, rmap func(float64) func(int) int) error {
	ne, _ := reg.NextEntries()
	rs := &fbgd.Counter{}
	lp := b.newLoop("regwire_rec", rs)
	for i, g := range b.dec.Multi {
		lp.before(len(b.rs.rows))
		if g.Phase != 0 {
			continue
		}
		if len(g.Sections) == 0 {
			return errors.New("IndexError: list index out of range")
		}
		if g.Sections[0] != registryVT {
			continue
		}
		p := b.dec.Packets[g.Pseq]
		if p == nil {
			return fmt.Errorf("KeyError: %d", g.Pseq)
		}
		end := int(p.Limit)
		for _, e := range ne[g.Pseq] {
			if e > int(g.EntryCursor) {
				end = e
				break
			}
		}
		bs := pySlice(bits.FromBytes(p.Payload).String(), 0, end)
		rw, c, why := reg.ParseRows(bs, int(g.Cursor), events.RegProps)
		if why != "" {
			rs.Add("unknown_form", 1)
		} else {
			if c != end {
				rs.Add("run_on", 1)
			}
			ch, err := reg.MapRows(rw, rmap(float64(g.T)), rs)
			if err != nil {
				return err
			}
			if len(ch) > 0 {
				b.rs.add(&fbgd.Row{K: 'u', T: float64(g.T), ID: regid, Chunks: ch}, tInt)
				rs.Add("rows", 1)
				for _, k := range ch {
					rs.Add(fmt.Sprintf("slot%d%d", k.Key.Sec, k.Key.Prop), 1)
				}
			}
		}
		lp.after(i, []any{g.T, int(g.ID), g.Pseq, g.EntryCursor}, b.rs.rows)
	}
	b.res.Lines.WireRegistry = b.say("wire registry", rs)
	return nil
}

// viewerID is the r203 VIEWERID block: the viewer's player id field (create bits [7:15], p7) renumbered, its
// registry slot swapped with the target's; the other player holding id nw takes the viewer's old id (r300l).
func (b *build) viewerID(vp *fbgd.Row, pl []*fbgd.Row, pid, regid int) error {
	cs := vp.Create.Value.String()
	oldV, err := int2(pySlice(cs, 7, 15))
	if err != nil {
		return err
	}
	nw, err := parseInt("VIEWERID", b.r.VIEWERID)
	if err != nil {
		return err
	}
	old := int(oldV)
	vs := &fbgd.Counter{}
	if old != nw {
		sl := func(i int) fbgd.ChunkKey { // wire player id -> registry slot (1 + (i-1)//8, (i-1)%8)
			return fbgd.ChunkKey{Sec: 1 + int(pyc.FloorDiv(int64(i-1), 8)), Prop: int(pyc.Mod(int64(i-1), 8))}
		}
		a, bb := sl(old), sl(nw)
		if nw < 0 || nw > 255 {
			return fmt.Errorf("pipeline: VIEWERID %d does not fit 8 bits", nw)
		}
		v, err := bits.Parse(pySlice(cs, 0, 7) + bits.FormatBin(uint64(nw), 8) + pySlice(cs, 15, len(cs)))
		if err != nil {
			return err
		}
		vp.Create.Value = v
		vs.Add("create", 1)
		ov, _ := bits.FromUint(uint64(old), 8)
		nv, _ := bits.FromUint(uint64(nw), 8)
		if err := swapPlayerID(b.rs.rows, pl, vp, old, nw, vs); err != nil {
			return err
		}
		for _, x := range b.rs.rows {
			if x.K != 'u' {
				continue
			}
			if x.ID == pid {
				for i := range x.Chunks {
					c := &x.Chunks[i]
					if c.Key == (fbgd.ChunkKey{Prop: 7}) && c.Value.Len() == 8 && c.Value.Equal(ov) {
						c.Value = nv
						vs.Add("p7", 1)
					}
				}
			} else if x.ID == regid {
				hit := false
				for _, c := range x.Chunks {
					if c.Tuple && (c.Key == a || c.Key == bb) {
						hit = true
					}
				}
				if hit {
					for i := range x.Chunks {
						c := &x.Chunks[i]
						if c.Tuple && c.Key == a {
							c.Key = bb
						} else if c.Tuple && c.Key == bb {
							c.Key = a
						}
					}
					vs.Add("reg_swapped", 1)
				}
			}
		}
	}
	b.say("viewer id", old, "->", nw, vs)
	if b.ck != nil {
		b.snapshot("viewerid", obj("old", old, "new", nw, "stat", counterDict(vs)))
	}
	return nil
}

// swapPlayerID gives every player row of pl other than vp whose create id field (bits [7:15]) is nw the id old:
// its create field and its 8-bit p7 updates equal to nw (r300l: two players never share a wire player id).
func swapPlayerID(rows, pl []*fbgd.Row, vp *fbgd.Row, old, nw int, vs *fbgd.Counter) error {
	ov, err := bits.FromUint(uint64(old), 8)
	if err != nil {
		return err
	}
	nv, err := bits.FromUint(uint64(nw), 8)
	if err != nil {
		return err
	}
	for _, o := range pl {
		if o == vp {
			continue
		}
		ocs := o.Create.Value.String()
		oi, err := int2(pySlice(ocs, 7, 15))
		if err != nil || int(oi) != nw {
			continue
		}
		v, err := bits.Parse(pySlice(ocs, 0, 7) + bits.FormatBin(uint64(old), 8) + pySlice(ocs, 15, len(ocs)))
		if err != nil {
			return err
		}
		o.Create.Value = v
		vs.Add("swap_create", 1)
		for _, x := range rows {
			if x.K != 'u' || x.ID != o.ID {
				continue
			}
			for i := range x.Chunks {
				c := &x.Chunks[i]
				if c.Key == (fbgd.ChunkKey{Prop: 7}) && c.Value.Len() == 8 && c.Value.Equal(nv) {
					c.Value = ov
					vs.Add("swap_p7", 1)
				}
			}
		}
	}
	return nil
}

// crebaseVsSrc is the r192/r207 print-only self-check line (never fails the build).
func (b *build) crebaseVsSrc(data []byte) string {
	var idx *gate.SrcIndex
	var err error
	if b.mpe != nil {
		var cs []gate.PackCreate
		if cs, err = b.mpe.creates(); err == nil {
			idx, err = gate.SrcIndexCreates(cs)
		}
	} else {
		var raw []byte
		if raw, err = os.ReadFile(filepath.Join(b.srcDir, b.r.SRC) + ".fbgd"); err == nil {
			idx, err = gate.SrcIndexBytes(raw)
		}
	}
	if err == nil {
		var res *gate.SrcResult
		if res, err = gate.CheckSrcCreates(data, idx, !on(b.r.SRCUNPAIRED)); err == nil {
			return res.Line(5)
		}
	}
	return "crebase_vs_src error " + strRepr(err.Error())
}

// companions is the WIRECOMP block (r143): the destruction companions from the wire stream, SRC (map pack or files)
// supplying the header / level-start state.
func (b *build) companions(t0 float64, info fbgd.Info, base string) ([]string, error) {
	var tp *events.Template
	var err error
	if b.mpe != nil {
		tp, err = events.TemplateFromPack(&b.mpe.comp)
	} else {
		tp, err = events.TemplateFiles(filepath.Join(b.srcDir, b.r.SRC))
	}
	if err != nil {
		return nil, err
	}
	fo, err := events.NewFrameOf(t0, b.joinKey, b.r.SHIFTF)
	if err != nil {
		return nil, err
	}
	of := fo.Of
	if b.ck != nil {
		of = func(t uint64) (int, error) {
			n, err := fo.Of(t)
			if err == nil {
				b.emit("fo", obj("args", []any{t}, "result", n))
			}
			return n, err
		}
	}
	key0, deltas, _, err := events.WireEvents(b.dec, tp.Cfg, of)
	if err != nil {
		return nil, err
	}
	nd := info.Frames - info.Keyframes
	past := 0
	for _, L := range deltas.Keys {
		if L >= nd {
			past++
		}
	}
	b.res.Lines.WireCompanions = b.say("wire companions: key0 events", tuple{len(key0.Comp), len(key0.Grp)}, "delta frames",
		len(deltas.Keys), "dropped past end", past)
	recs, err := events.Build(tp, key0, deltas, nd)
	if err != nil {
		return nil, err
	}
	files, err := events.Encode(tp, recs)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		p := base + "." + f.Ext
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			return nil, err
		}
		q := base + "." + fbgd.CompanionIndexExt(f.Ext)
		if err := os.WriteFile(q, f.Idx, 0o644); err != nil {
			return nil, err
		}
		out = append(out, p, q)
	}
	return out, nil
}
