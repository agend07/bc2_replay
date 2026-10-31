package events

// Port of v3_reg0 (r151/r187/r193/r195), v3_multi_slice.next_entries and the v3_cl04c REGWIRE loop (r136):
// the registry ghost (class 0x1432098) join state and updates read from the client wire.

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/fbgd"
)

const (
	// RegVT is v3_reg0.REG: the registry section class.
	RegVT = 0x1432098
	// PlayerVT is v3_reg0.PLAYER.
	PlayerVT = 0x1431d14
	// RegProps is parse_rows' default nprop.
	RegProps = 8
)

// regForms is FORMS: s1.p0 body length -> 13-bit ref positions.
var regForms = map[int][]int{27: {1}, 29: {1, 14}, 63: {1, 14, 28, 46}}

// RegRow is one parse_rows row (si, pj, L, body, ref positions); L = 1 is the null row '0'.
type RegRow struct {
	SI, PJ, L int
	Body      string
	Pos       []int
}

// Tracer observes the registry calls (maintainer exam): Call is made when a traced function starts, with its
// arguments, and returns the function to call with its results when it returns.
type Tracer interface {
	Call(fn string, args ...any) func(results ...any)
}

// Registry runs the registry functions over one decoded log; Trace may be nil.
type Registry struct {
	D     *capture.Decoded
	Trace Tracer
}

func (r *Registry) call(fn string, args ...any) func(...any) {
	if r.Trace == nil {
		return func(...any) {}
	}
	return r.Trace.Call(fn, args...)
}

// regForm is reg_form(seg) (r187): the s1.p0 body length of a registry row from '011' + body + 14 absent bits.
func regForm(seg string) int {
	if len(seg) >= 30 && !strings.Contains(seg[17:30], "1") {
		return 27
	}
	if len(seg) > 30 && seg[30] == '1' {
		return 29
	}
	return 63
}

// ParseRows is parse_rows(bits, c, nprop) (r195): the presence-mask walk from cursor c (s0 '0', then s1..s8
// presence, p0..p7 presence, present row body). -> (rows, end, "") or (nil, 0, reason).
func (r *Registry) ParseRows(b string, c, nprop int) (rows []RegRow, end int, why string) {
	done := r.call("parse_rows", b, c, nprop)
	defer func() { done(rows, end, why) }()
	if c >= len(b) || b[c] != '0' {
		return nil, 0, "s0 present or past end"
	}
	c++
	rows = []RegRow{}
	for si := 1; si <= 8; si++ {
		if c >= len(b) {
			return nil, 0, "past end"
		}
		c++
		if b[c-1] != '1' {
			continue
		}
		for pj := range nprop {
			if c >= len(b) {
				return nil, 0, "past end"
			}
			c++
			if b[c-1] != '1' {
				continue
			}
			if c >= len(b) {
				return nil, 0, "past end"
			}
			L, pos := 1, []int{}
			if b[c] != '0' {
				L = regForm("011" + slice(b, c, c+64))
				pos = regForms[L]
			}
			if c+L > len(b) {
				return nil, 0, "past end"
			}
			rows = append(rows, RegRow{si, pj, L, b[c : c+L], pos})
			c += L
		}
	}
	return rows, c, ""
}

// NextEntries is v3_multi_slice.next_entries(d): pseq -> its logged entry cursors, sorted (pseqs in first
// appearance order over ghosts then multi).
func (r *Registry) NextEntries() (map[uint32][]int, []uint32) {
	ent, order := entryCursors(r.D)
	for _, v := range ent {
		slices.Sort(v)
	}
	r.call("next_entries")(ent, order)
	return ent, order
}

// recKey is the (t, pseq, entry_cursor) sort key of v3_reg0.
func recKey(a, b *capture.Ghost) int {
	return cmp.Or(cmp.Compare(a.T, b.T), cmp.Compare(a.Pseq, b.Pseq), cmp.Compare(a.EntryCursor, b.EntryCursor))
}

// ViewerPlayer is viewer_player(d) (r193/r195): the wire id of the local player = the first player create whose
// create bits start 00000100, else the first player create; ok false when the log has none.
func (r *Registry) ViewerPlayer() (id int, ok bool) {
	done := r.call("viewer_player")
	defer func() { done(id, ok) }()
	var gs []*capture.Ghost
	for _, g := range r.D.Ghosts {
		if g.SectionVT == PlayerVT && g.Phase == 1 {
			gs = append(gs, g)
		}
	}
	slices.SortStableFunc(gs, recKey)
	for _, g := range gs {
		p := r.D.Packets[g.Pseq]
		if p == nil {
			return 0, false // KeyError in Python; a ghost without its packet is an orphan, never here
		}
		cc := int(g.CreateCur)
		if string(slice(payloadBits(p.Payload), cc, cc+8)) == "00000100" {
			return int(g.ID), true
		}
	}
	if len(gs) > 0 {
		return int(gs[0].ID), true
	}
	return 0, false
}

// viewerSlot is viewer_slot(rows, wire_pid): (si, pj) of the first non-null row naming wire_pid.
func viewerSlot(rows []RegRow, pid int) ([2]int, bool) {
	for _, x := range rows {
		if x.L > 1 {
			if v, err := intOf([]byte(x.Body), 1, 14); err == nil && int(v) == pid {
				return [2]int{x.SI, x.PJ}, true
			}
		}
	}
	return [2]int{}, false
}

// regChunk is a registry row chunk dict(value=int(body, 2), bits=L, refs) at (si, pj).
func regChunk(si, pj int, body string, refs []fbgd.Ref) (fbgd.RowChunk, error) {
	v, err := bits.Parse(body)
	if err != nil {
		return fbgd.RowChunk{}, err
	}
	return fbgd.RowChunk{Key: fbgd.ChunkKey{Sec: si, Prop: pj}, Tuple: true, Value: v, Refs: refs}, nil
}

// MapRows is map_rows(rows, mp, stat) (r195): rows -> chunks {(si, pj): chunk} in row order; every 13-bit ref goes
// through mp (wire id -> demo id or 0; stat 'mapped' / 'null'); a row whose vehicle ref (position >= 28) maps to 0
// is dropped ('skipped_null_vehicle'). A demo id >= 2**13 is an error (Python would widen the row).
func (r *Registry) MapRows(rows []RegRow, mp func(int) int, stat *fbgd.Counter) (ch []fbgd.RowChunk, err error) {
	done := r.call("map_rows", rows, mp, stat)
	defer func() { done(ch, err) }()
	ch = []fbgd.RowChunk{}
	for _, x := range rows {
		bl := []byte(x.Body)
		refs := []fbgd.Ref{}
		nv := 0
		for _, q := range x.Pos {
			if q+13 > len(bl) {
				return nil, fmt.Errorf("registry row (%d, %d): ref at %d past its %d bits", x.SI, x.PJ, q, len(bl))
			}
			o, _ := intOf(bl, q, q+13)
			n := 0
			if o != 0 {
				n = mp(int(o))
			}
			if n < 0 || n >= 1<<13 {
				return nil, fmt.Errorf("registry ref %d -> %d does not fit 13 bits", o, n)
			}
			copy(bl[q:q+13], fmt.Sprintf("%013b", n))
			if n != 0 {
				stat.Add("mapped", 1)
				refs = append(refs, fbgd.Ref{Target: n})
			} else {
				stat.Add("null", 1)
				if q >= 28 {
					nv++
				}
			}
		}
		if nv > 0 {
			stat.Add("skipped_null_vehicle", 1)
			continue
		}
		c, err := regChunk(x.SI, x.PJ, string(bl), refs)
		if err != nil {
			return nil, err
		}
		ch = append(ch, c)
	}
	return ch, nil
}

// Reg0 is reg0's dict: the registry join record and the viewer's row in it.
type Reg0 struct {
	T      uint64
	ID     int
	Player int
	Slot   [2]int
	Body   string
	Bits   int
	Pseq   uint32
	Cursor int
	Rows   []RegRow
	End    int
}

// Reg0 is reg0(d, viewer) (viewer < 0 = None: viewer_player(d)): the first registry create record's join state (64 rows,
// wire ids) and the viewer's reg(0) row; nil and the reason when unavailable.
func (r *Registry) Reg0(viewer int) (*Reg0, string, error) {
	ms := slices.Clone(r.D.Multi)
	slices.SortStableFunc(ms, recKey)
	var g *capture.Ghost
	for _, m := range ms {
		if len(m.Sections) == 0 {
			return nil, "", fmt.Errorf("IndexError: multi record %d (pseq %d) has no sections", m.ID, m.Pseq)
		}
		if m.Sections[0] == RegVT && m.Phase == 1 {
			g = m
			break
		}
	}
	if g == nil {
		return nil, "no registry create record on the wire", nil
	}
	p := r.D.Packets[g.Pseq]
	if p == nil {
		return nil, "", fmt.Errorf("KeyError: %d", g.Pseq)
	}
	b, c := string(payloadBits(p.Payload)), int(g.Cursor)
	rows, end, why := r.ParseRows(b, c, RegProps)
	if rows == nil {
		return nil, "join state: " + why, nil
	}
	ne, _ := r.NextEntries()
	for _, e := range ne[g.Pseq] {
		if e > int(g.EntryCursor) {
			if end > e {
				return nil, fmt.Sprintf("join state runs past the next entry (%d > %d)", end, e), nil
			}
			break
		}
	}
	if viewer < 0 {
		if v, ok := r.ViewerPlayer(); ok {
			viewer = v
		}
	}
	slot, ok := [2]int{}, false
	if viewer > 0 {
		slot, ok = viewerSlot(rows, viewer)
	}
	if !ok {
		v := "None"
		if viewer >= 0 {
			v = fmt.Sprint(viewer)
		}
		return nil, "no join row for viewer " + v, nil
	}
	i := slices.IndexFunc(rows, func(x RegRow) bool { return x.SI == slot[0] && x.PJ == slot[1] })
	x := rows[i]
	if x.L != 27 {
		return nil, fmt.Sprintf("viewer row (%d, %d) not reg(0): %s", slot[0], slot[1], x.Body), nil
	}
	return &Reg0{T: g.T, ID: int(g.ID), Player: viewer, Slot: slot, Body: x.Body, Bits: x.L, Pseq: g.Pseq, Cursor: c,
		Rows: rows, End: end}, "", nil
}

// JoinRows is v3_reg0.rows(d, regid, player_row, wire_player, mp, stat): the registry row of the join state. pid is
// the viewer's create row id after renumbering, wirePlayer its wire id (< 0 = None: same as pid). The viewer's row carries
// pid at its real slot; with mp the other occupied join rows are written too, refs mapped. Unavailable -> no rows
// and the reason (Python prints 'reg0: unavailable: <why>').
func (r *Registry) JoinRows(regid, pid, wirePlayer int, mp func(int) int, stat *fbgd.Counter) (rows []*fbgd.Row, why string, err error) {
	done := r.call("rows", regid, pid, wirePlayer, mp, stat)
	defer func() { done(rows, why, err) }()
	wp := wirePlayer
	if wp < 0 {
		wp = pid
	}
	g, why, err := r.Reg0(wp)
	if err != nil || g == nil {
		return []*fbgd.Row{}, why, err
	}
	if pid < 0 || pid >= 1<<13 {
		return nil, "", fmt.Errorf("registry viewer id %d does not fit 13 bits", pid)
	}
	ch := []fbgd.RowChunk{}
	if mp != nil {
		var in []RegRow
		for _, x := range g.Rows {
			if x.L > 1 && (x.SI != g.Slot[0] || x.PJ != g.Slot[1]) {
				in = append(in, x)
			}
		}
		if stat == nil {
			stat = &fbgd.Counter{}
		}
		if ch, err = r.MapRows(in, mp, stat); err != nil {
			return nil, "", err
		}
	}
	c, err := regChunk(g.Slot[0], g.Slot[1], fmt.Sprintf("1%013b%013d", pid, 0), []fbgd.Ref{{Target: pid}})
	if err != nil {
		return nil, "", err
	}
	ch = append(ch, c) // the viewer's slot is not among the map_rows input: a new key
	slices.SortStableFunc(ch, func(a, b fbgd.RowChunk) int {
		return cmp.Or(cmp.Compare(a.Key.Sec, b.Key.Sec), cmp.Compare(a.Key.Prop, b.Key.Prop))
	})
	return []*fbgd.Row{{K: 'u', T: float64(g.T), ID: regid, Chunks: ch}}, "", nil
}

// RegistryUpdates is the v3_cl04c REGWIRE loop (r136/r171/r195): every registry update record (multi, phase 0,
// first section the registry) parsed up to the next logged entry (else the packet limit) and mapped through
// mpAt(t); stat gets unknown_form, run_on, mapped/null/skipped_null_vehicle, rows and slot<si><pj>.
func (r *Registry) RegistryUpdates(regid int, mpAt func(t uint64) func(int) int, stat *fbgd.Counter) ([]*fbgd.Row, error) {
	ne, _ := r.NextEntries()
	var out []*fbgd.Row
	for _, g := range r.D.Multi {
		if g.Phase != 0 {
			continue
		}
		if len(g.Sections) == 0 {
			return nil, fmt.Errorf("IndexError: multi record %d (pseq %d) has no sections", g.ID, g.Pseq)
		}
		if g.Sections[0] != RegVT {
			continue
		}
		p := r.D.Packets[g.Pseq]
		if p == nil {
			return nil, fmt.Errorf("KeyError: %d", g.Pseq)
		}
		end := int(p.Limit)
		for _, e := range ne[g.Pseq] {
			if e > int(g.EntryCursor) {
				end = e
				break
			}
		}
		rw, c, _ := r.ParseRows(string(slice(payloadBits(p.Payload), 0, end)), int(g.Cursor), RegProps)
		if rw == nil {
			stat.Add("unknown_form", 1)
			continue
		}
		if c != end {
			stat.Add("run_on", 1)
		}
		ch, err := r.MapRows(rw, mpAt(g.T), stat)
		if err != nil {
			return nil, err
		}
		if len(ch) == 0 {
			continue
		}
		out = append(out, &fbgd.Row{K: 'u', T: float64(g.T), ID: regid, Chunks: ch})
		stat.Add("rows", 1)
		for _, k := range ch {
			stat.Add(fmt.Sprintf("slot%d%d", k.Key.Sec, k.Key.Prop), 1)
		}
	}
	return out, nil
}
