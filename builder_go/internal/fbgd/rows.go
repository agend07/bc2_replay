package fbgd

// Port of the row side of v3_assemble (create_chunk, build_rows, initial_property_state, property_chunks) and
// rows_to_fbgd.delnull. ac38_rows is wire.AC38Rows; templates() / write_all / main (the pre-cl04c demo911 path) are
// not ported.

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

const (
	// DeleteAfter is v3_assemble.DELETE_AFTER: a ghost is deleted 3 s after its last record (if the log goes on).
	DeleteAfter = 3 * 10_000_000
	// SoldierVT is v3_assemble.SOLDIER (= recorder_v3.SOLDIER_SECTION_VT).
	SoldierVT = wire.SoldierVT
)

// Counter is a collections.Counter with insertion order (stats dicts printed by cl04c).
type Counter struct {
	keys []string
	m    map[string]int
}

// Add is c[k] += n (n == 0 still creates the key, like Counter's += 0).
func (c *Counter) Add(k string, n int) {
	if c.m == nil {
		c.m = map[string]int{}
	}
	if _, ok := c.m[k]; !ok {
		c.keys = append(c.keys, k)
	}
	c.m[k] += n
}

// Keys are the keys in insertion order.
func (c *Counter) Keys() []string { return c.keys }

// Get is c[k].
func (c *Counter) Get(k string) int { return c.m[k] }

// PyValue is dict(c).
func (c *Counter) PyValue() any {
	o := pyc.NewObject()
	for _, k := range c.keys {
		o.Set(k, c.m[k])
	}
	return o
}

// CreateChunk is v3_assemble.create_chunk: the create slice bits [create_cur, cursor) of the record's packet as
// dict(index=32, bits=cursor-create_cur, value=int(b or 0), refs=[]).
func CreateChunk(d *wire.Log, g *wire.Ghost) (OutChunk, error) {
	p := d.Packets[g.Pseq]
	if p == nil {
		return OutChunk{}, fmt.Errorf("KeyError: %d", g.Pseq)
	}
	b := bits.FromBytes(p.Payload).Slice(int(g.CreateCur), int(g.Cursor))
	w := int(g.Cursor - g.CreateCur)
	if b.Len() != w { // a cursor past the payload: Python keeps bits = w with the shorter body's value
		v, err := bits.FromBig(b.Big(), w)
		if err != nil {
			return OutChunk{}, fmt.Errorf("create chunk of %d bits holds %d: %w", w, b.Len(), err)
		}
		b = v
	}
	return OutChunk{Index: 32, Value: b, Refs: []Ref{}}, nil
}

// InitialKey is the initial_states key (t, id).
type InitialKey struct {
	T  uint64
	ID int64
}

// BuildRow is a build_rows output row: Row plus the fields cl04c pops later (_initial, _vt on initial-state rows,
// syn on synthetic deletes).
type BuildRow struct {
	Row
	Initial any    // the initial_states record (rows 'u' at a create's t; Chunks empty until property_chunks)
	VT      uint32 // _vt of an initial-state row
	Syn     bool
}

// BuildRows is v3_assemble.build_rows(d, tpl, sold, ac, initial_states): create rows (+ delete 400,000 ticks before a
// re-create), initial-state rows, synthetic soldier creates, the soldier and AC38 update rows (sold, ac: 'u' rows,
// chunks shared, not copied) and synthetic deletes DELETE_AFTER after each id's last record. hasTpl is 'vt in tpl'.
// initial values are tested '!= nil' (Python 'if rec'): never store a typed nil pointer there.
// create is the module-global create_chunk build_rows calls: cl04c patches it (camera creates, CREBASE, CUT); nil =
// CreateChunk.
func BuildRows(d *wire.Log, hasTpl func(vt uint32) bool, sold, ac []*Row, initial map[InitialKey]any,
	create func(*wire.Log, *wire.Ghost) (OutChunk, error)) ([]*BuildRow, *Counter, error) {
	if create == nil {
		create = CreateChunk
	}
	gs := d.Ghosts
	if len(gs) == 0 {
		return nil, nil, errors.New("ValueError: max() iterable argument is empty")
	}
	tEnd := gs[0].T
	for _, g := range gs {
		tEnd = max(tEnd, g.T)
	}
	var rows []*BuildRow
	stats := &Counter{}
	var firstSold *OutChunk
	for _, g := range gs {
		if g.Phase == 1 && g.SectionVT == SoldierVT && g.Cursor-g.CreateCur == 217 {
			c, err := create(d, g)
			if err != nil {
				return nil, nil, err
			}
			firstSold = &c
			break
		}
	}
	alive := map[int64]uint32{}
	last := map[int64]uint64{}
	var lastOrder []int64
	for _, g := range gs {
		gid, vt := g.ID, g.SectionVT
		if !hasTpl(vt) {
			stats.Add(fmt.Sprintf("skip_type_%x", vt), 0)
			continue
		}
		if g.Phase == 1 {
			if _, ok := alive[gid]; ok { // >1 frame earlier: delete+new of one id inside a frame is not readable
				rows = append(rows, &BuildRow{Row: Row{K: 'd', T: float64(int64(g.T) - 400_000), ID: int(gid)}})
			}
			c, err := create(d, g)
			if err != nil {
				return nil, nil, err
			}
			rows = append(rows, &BuildRow{Row: Row{K: 'c', T: float64(g.T), ID: int(gid), Type: int64(vt), Create: c}})
			alive[gid] = vt
			stats.Add("create", 1)
			if rec := initial[InitialKey{g.T, gid}]; rec != nil {
				rows = append(rows, &BuildRow{Row: Row{K: 'u', T: float64(g.T), ID: int(gid)}, Initial: rec, VT: vt})
				stats.Add("initial_state", 1)
			}
		} else if a, ok := alive[gid]; !ok || a != vt {
			if vt == SoldierVT && !ok && firstSold != nil {
				c := *firstSold
				rows = append(rows, &BuildRow{Row: Row{K: 'c', T: float64(g.T), ID: int(gid), Type: int64(vt), Create: c}})
				alive[gid] = vt
				stats.Add("synthetic_soldier_create", 1)
			} else {
				stats.Add("no_create_dropped", 1)
				continue
			}
		}
		if _, ok := last[gid]; !ok {
			lastOrder = append(lastOrder, gid)
		}
		last[gid] = g.T
	}
	for _, g := range gs {
		if !hasTpl(g.SectionVT) {
			stats.Add("unsupported_records", 1)
		}
	}
	for _, src := range [2][]*Row{sold, ac} {
		for _, r := range src {
			rows = append(rows, &BuildRow{Row: Row{K: 'u', T: r.T, ID: r.ID, Chunks: r.Chunks}})
		}
	}
	for _, gid := range lastOrder {
		if t := last[gid]; t+DeleteAfter < tEnd {
			rows = append(rows, &BuildRow{Row: Row{K: 'd', T: float64(t + DeleteAfter), ID: int(gid)}, Syn: true})
			stats.Add("delete", 1)
		}
	}
	return rows, stats, nil
}

// overlaps is the origin-vector test of initial_property_state: v.start < body_start + bits and v.end > body_start.
func overlaps(c *wire.Chunk, ovs []wire.Ovec) bool {
	for _, v := range ovs {
		if int64(v.Start) < c.BodyStart+int64(c.Value.Len()) && int64(v.End) > c.BodyStart {
			return true
		}
	}
	return false
}

// InitialPropertyState is v3_assemble.initial_property_state: the record with only the chunks no origin vector
// overlaps (origin vectors are never copied or rebased here); nil for an error / flagged record or when no chunk is
// left.
func InitialPropertyState(rec *wire.SliceResult) *wire.SliceResult {
	if rec.Error != "" || rec.Flags != 0 {
		return nil
	}
	var chunks []wire.Chunk
	for i := range rec.Chunks {
		if !overlaps(&rec.Chunks[i], rec.OriginVectors) {
			chunks = append(chunks, rec.Chunks[i])
		}
	}
	if len(chunks) == 0 {
		return nil
	}
	out := *rec
	out.Chunks = chunks
	return &out
}

// Conversion holds property_chunks' keyword switches (cl04c _conversion: WidenP12 false, CameraP5Flag true).
type Conversion struct {
	WidenP12     bool // legacy 979 bisect: widen a 13-bit non-null component p12 to 17 bits (selector 0)
	CameraP5Flag bool // camera p5's reference-table flag (property-defined, not a payload bit)
}

// DefaultConversion is cl04c's _conversion (r119 / r115).
var DefaultConversion = Conversion{WidenP12: false, CameraP5Flag: true}

const (
	componentVT = 0x1431df8
	cameraVT    = 0x1431c0c
)

// PropertyChunks is v3_assemble.property_chunks(rec, vt, mapfn, **conv): sliced wire properties -> 'u' row chunks
// {prop: dict(value, bits, refs)} in record order (a repeated property keeps its first position). Ref positions are
// the 13-bit reads relative to the body; refs are remapped through mapfn.
// Component p12 (r118): null = 13 bits, non-null = ref13 + selector4 (17); a 13-bit non-null slice is rejected unless
// WidenP12; a p12 whose target maps to null is narrowed to the 13-bit null form. Component p5 (r167) whose target is
// unmapped -> the 1-bit null form.
func PropertyChunks(rec *wire.SliceResult, vt uint32, mapfn func(int) int, conv Conversion) ([]RowChunk, error) {
	var out []RowChunk
	put := func(prop int, c OutChunk) {
		rc := RowChunk{Key: ChunkKey{0, prop}, Value: c.Value, Refs: c.Refs}
		for i := range out {
			if out[i].Key.Prop == prop {
				out[i] = rc
				return
			}
		}
		out = append(out, rc)
	}
	for i := range rec.Chunks {
		c := &rec.Chunks[i]
		prop := c.Property
		ch := OutChunk{Value: c.Value, Refs: []Ref{}}
		pos := make([]int, len(c.References))
		for k, r := range c.References {
			pos[k] = int(int64(r.Start) - c.BodyStart)
		}
		if vt == componentVT && prop == 12 {
			if (ch.Value.Len() != 13 && ch.Value.Len() != 17) || !slices.Equal(pos, []int{0}) {
				return nil, errors.New("ValueError: unsupported component p12 representation")
			}
			if ch.Value.Len() == 13 && !ch.Value.IsZero() {
				if !conv.WidenP12 {
					return nil, errors.New("ValueError: 13-bit non-null p12: unresolved-ref slice artifact")
				}
				ch.Value = ch.Value.Shl(4)
			}
		}
		flag := 0
		if conv.CameraP5Flag && vt == cameraVT && prop == 5 {
			flag = 1
		}
		res := ch
		if len(pos) > 0 {
			ch.Refs = make([]Ref, len(pos))
			for k := range ch.Refs {
				ch.Refs[k] = Ref{0, flag}
			}
			var err error
			if res, err = Remap(ch, pos, mapfn); err != nil {
				return nil, err
			}
		}
		if vt == componentVT && prop == 12 && len(res.Refs) == 0 {
			res = OutChunk{Value: bits.Bits{}, Refs: []Ref{}}
			res.Value, _ = bits.FromUint(0, 13)
		}
		if vt == componentVT && prop == 5 && len(pos) > 0 && len(res.Refs) == 0 { // r167: cl04_1046 crash at 827CBB
			res = OutChunk{Refs: []Ref{}}
			res.Value, _ = bits.FromUint(0, 1)
		}
		put(prop, res)
	}
	return out, nil
}

// DelNullSlots are rows_to_fbgd.DELNULL_SLOTS (r205): ref slots that must not name a deleted (non-soldier) ghost, with
// their null forms (bits, value): component p12 13:0, p5 1:0; camera p3 17:0.
var DelNullSlots = map[uint32]map[int][2]int{componentVT: {12: {13, 0}, 5: {1, 0}}, cameraVT: {3: {17, 0}}}

// ViewSlots are rows_to_fbgd.VIEW_SLOTS (r284): the slots the default DELNULL pass uses (non-soldier deletes, as r205):
// component p12 and camera p3. Server demos clear exactly these 1-4 frames before the delete of what they name;
// component/camera p5 stay on deleted ghosts in server demos and are never nulled.
var ViewSlots = map[uint32]map[int][2]int{componentVT: {12: {13, 0}}, cameraVT: {3: {17, 0}}}

// DelNullHit is one delnull result (t, holder, prop, deleted id).
type DelNullHit struct {
	T            float64
	Holder, Prop int
	Deleted      int
}

type slotKey struct{ holder, prop int }

type slotVal struct {
	seq     int
	targets map[int]bool
	t       float64
}

// DelNull is rows_to_fbgd.delnull(rows, types, soldier, stat, slots, dt): for every delete row of a non-soldier ghost
// X, null the listed ref slots of live holders whose current value names X with one 'u' row at max(delete t - dt, t
// of the row that set the slot, holder create t). types = demo id -> create type (int64 vt or string). The rows are
// appended to rows (returned); stat counts per 'p<prop>'. slots nil = DelNullSlots, dt = DTTicks in cl04c.
func DelNull(rows []*Row, types map[int]TypeKey, soldier TypeKey, stat *Counter, slots map[uint32]map[int][2]int, dt float64) ([]*Row, []DelNullHit, error) {
	if slots == nil {
		slots = DelNullSlots
	}
	slotsOf := func(g int) (map[int][2]int, bool) {
		t, ok := types[g]
		if !ok {
			return nil, false
		}
		vt, ok := t.(int64)
		if !ok || vt < 0 || vt > math.MaxUint32 {
			return nil, false
		}
		s, ok := slots[uint32(vt)]
		return s, ok
	}
	cur := map[slotKey]*slotVal{} // insertion-ordered by seq
	seq := 0
	ct := map[int]float64{}
	dropHolder := func(g int) {
		for k := range cur {
			if k.holder == g {
				delete(cur, k)
			}
		}
	}
	sorted := slices.Clone(rows)
	slices.SortStableFunc(sorted, byT)
	var out []*Row
	var hits []DelNullHit
	for _, r := range sorted {
		g := r.ID
		switch r.K {
		case 'c':
			ct[g] = r.T
			dropHolder(g)
		case 'u':
			sl, ok := slotsOf(g)
			if !ok {
				continue
			}
			for _, c := range r.Chunks {
				if c.Key.Sec != 0 {
					continue
				}
				if _, ok := sl[c.Key.Prop]; !ok {
					continue
				}
				ts := map[int]bool{}
				for _, x := range c.Refs {
					if x.Target != 0 {
						ts[x.Target] = true
					}
				}
				k := slotKey{g, c.Key.Prop}
				if v := cur[k]; v != nil {
					v.targets, v.t = ts, r.T
				} else {
					seq++
					cur[k] = &slotVal{seq, ts, r.T}
				}
			}
		case 'd':
			dropHolder(g)
			if types[g] == soldier {
				continue
			}
			keys := make([]slotKey, 0, len(cur))
			for k := range cur {
				keys = append(keys, k)
			}
			slices.SortFunc(keys, func(a, b slotKey) int { return cur[a].seq - cur[b].seq })
			snap := make([]slotVal, len(keys))
			for i, k := range keys {
				snap[i] = *cur[k]
			}
			for i, k := range keys {
				if !snap[i].targets[g] {
					continue
				}
				sl, _ := slotsOf(k.holder)
				form, ok := sl[k.prop]
				if !ok {
					return nil, nil, fmt.Errorf("KeyError: %d", k.prop)
				}
				t1 := snap[i].t
				c1, ok := ct[k.holder]
				if !ok {
					c1 = t1
				}
				tn := max(r.T-dt, t1, c1)
				v, err := bits.FromUint(uint64(form[1]), form[0])
				if err != nil {
					return nil, nil, err
				}
				out = append(out, &Row{K: 'u', T: tn, ID: k.holder, Chunks: []RowChunk{{Key: ChunkKey{0, k.prop}, Value: v, Refs: []Ref{}}}})
				hits = append(hits, DelNullHit{T: tn, Holder: k.holder, Prop: k.prop, Deleted: g})
				cur[k].targets, cur[k].t = map[int]bool{}, tn
				if stat != nil {
					stat.Add(fmt.Sprintf("p%d", k.prop), 1)
				}
			}
		}
	}
	return append(rows, out...), hits, nil
}
