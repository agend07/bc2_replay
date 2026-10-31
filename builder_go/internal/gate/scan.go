package gate

import (
	"fmt"
	"strconv"

	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/pyc"
)

// earlyKey is the (frame, gid, kind, section, property) key of fbgd_gate.scan's `early` chunks.
type earlyKey struct {
	frame, ghost int
	kind         string
	si, pi       int
}

// scanResult is fbgd_gate.scan's result minus observations/shapes/stale (those feed the corpus collector directly).
type scanResult struct {
	errors       []*pyc.Object
	keys         []int
	frames       int
	early        map[earlyKey]*fbgd.Chunk // only with scanOpts.early
	firstSoldier int                      // -1 = None
}

type scanOpts struct {
	allowed *Whitelist // nil = no whitelist checks (corpus / baseline-regression scans)
	collect *collector // corpus mode: feed observations, shapes, stale forms
	early   bool       // keep the load-phase chunks (only regression reads them)
	// earlyLimit >= 0: keep early chunks of frames < earlyLimit only (regression never reads past the baseline's
	// load phase); -1 = no extra bound
	earlyLimit int
}

// event is one (gid, kind, g) of a frame: kind 'key' / 'new' carry a ghost, 'upd' carries changes.
type event struct {
	gid     int
	kind    string
	ghost   *fbgd.Ghost
	changes []fbgd.Change
}

func obj(kv ...any) *pyc.Object {
	o := pyc.NewObject()
	for i := 0; i < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// refsValue is a chunk's refs as json sees them: [[target, flag], ...].
type refsValue []fbgd.Ref

func (r refsValue) PyValue() any {
	out := make([]any, len(r))
	for i, x := range r {
		out[i] = []int{x.Target, x.Flag}
	}
	return out
}

// islot is a slot with the signature interned (cheap map key).
type islot struct{ sig, si, pi int32 }

const nGhost = 1 << 16 // ghost ids are u16

// scanner holds fbgd_gate.scan's per-file state; ghost-indexed sets are arrays (ids are u16).
type scanner struct {
	o        scanOpts
	res      *scanResult
	live     [nGhost]bool
	liveList []int
	dead     [nGhost]bool
	sigOf    [nGhost]int32 // interned signature + 1 (0 = never created: types[gid] KeyError)
	cwidth   [nGhost]int
	sigs     []string
	sigID    map[string]int32
	cache    map[islot]*slotSets
	mismatch map[string]bool
	empty    *slotSets
	form     []byte
	frame    int
}

func (s *scanner) intern(sig string) int32 {
	id, ok := s.sigID[sig]
	if !ok {
		id = int32(len(s.sigs))
		s.sigs = append(s.sigs, sig)
		s.sigID[sig] = id
	}
	return id
}

func (s *scanner) opts(k islot) *slotSets {
	ss, ok := s.cache[k]
	if !ok {
		ss = s.o.allowed.slots[slot{s.sigs[k.sig], int(k.si), int(k.pi)}]
		if ss == nil {
			ss = s.empty
		}
		s.cache[k] = ss
	}
	return ss
}

func (s *scanner) setLive(ids []int) {
	for _, id := range s.liveList {
		s.live[id] = false
	}
	s.liveList = s.liveList[:0]
	for _, id := range ids {
		if !s.live[id] {
			s.live[id] = true
			s.liveList = append(s.liveList, id)
		}
	}
}

func (s *scanner) addLive(id int) {
	if !s.live[id] {
		s.live[id] = true
		s.liveList = append(s.liveList, id)
	}
}

func (s *scanner) dropLive(id int) {
	if !s.live[id] {
		return
	}
	s.live[id] = false
	for i, x := range s.liveList {
		if x == id {
			s.liveList[i] = s.liveList[len(s.liveList)-1]
			s.liveList = s.liveList[:len(s.liveList)-1]
			break
		}
	}
}

func (s *scanner) errorf(kv ...any) { s.res.errors = append(s.res.errors, obj(kv...)) }

// scan is fbgd_gate.scan(data, allowed): parse every frame, check the bit round trip, liveness, shapes and the whitelist.
func scan(data []byte, o scanOpts) (*scanResult, error) {
	_, raw, err := fbgd.Frames(data)
	if err != nil {
		return nil, err
	}
	end := fbgd.PrologEnd(data)
	if len(raw) > 0 {
		last := raw[len(raw)-1]
		end = last.Off + 9 + last.Size
	}
	if end < 0 || end > len(data) || string(data[end:]) != "\x00" {
		return nil, &fbgd.PyError{Class: "ValueError", Msg: "missing terminator or trailing/unparsed bytes"}
	}
	s := &scanner{o: o, res: &scanResult{firstSoldier: -1}, sigID: map[string]int32{}, cache: map[islot]*slotSets{},
		mismatch: map[string]bool{},
		empty:    &slotSets{widths: map[int]bool{}, indices: map[int]bool{}, forms: map[string]bool{}, stale: map[string]bool{}}}
	if o.early {
		s.res.early = map[earlyKey]*fbgd.Chunk{}
	}
	state := fbgd.State{}
	var keyLive []int // ids made live by keyframes since the last delta frame (dead -= live is applied lazily)
	var events []event
	var keyGhosts []*fbgd.Ghost // this frame's keyframe ghosts (nil on a delta)
	wasLive := map[int]bool{}
	for frame, fr := range raw {
		s.frame = frame
		if len(fr.Payload) != fr.Size || fr.BitPos > fr.Size*8 || fr.Size != (fr.BitPos+7)/8 {
			return nil, &fbgd.PyError{Class: "ValueError", Msg: fmt.Sprintf("frame %d: invalid payload size/bit count", frame)}
		}
		r := fbgd.NewReader(fr.Payload, fr.BitPos)
		events = events[:0]
		keyGhosts = nil
		clear(wasLive)
		canonical := true
		switch fr.Marker {
		case 1:
			gs, err := fbgd.Keyframe(r)
			if err != nil {
				return nil, err
			}
			keyGhosts = gs
			ids := make([]int, len(gs))
			seen := map[int]bool{}
			for i, g := range gs {
				ids[i] = g.ID
				seen[g.ID] = true
				events = append(events, event{gid: g.ID, kind: "key", ghost: g})
			}
			if len(seen) != len(ids) {
				s.errorf("rule", "duplicate-key-id", "frame", frame)
			}
			if frame > 0 {
				same := len(seen) == len(s.liveList)
				for id := range seen {
					same = same && s.live[id]
				}
				if !same {
					s.errorf("rule", "key-live-mismatch", "frame", frame)
				}
			}
			for _, g := range gs {
				wasLive[g.ID] = s.live[g.ID]
			}
			state = fbgd.State{}
			for _, g := range gs {
				state[g.ID] = g.Counts()
			}
			s.setLive(ids)
			keyLive = append(keyLive, ids...)
			s.res.keys = append(s.res.keys, fr.Off)
		case 2:
			d, err := fbgd.Delta(r, state)
			if err != nil {
				return nil, err
			}
			canonical = !d.EmptySection
			seen := map[int]bool{}
			for _, x := range d.Dirty {
				kind := "upd"
				if x.New {
					kind = "new"
					if s.live[x.ID] {
						s.errorf("rule", "duplicate-create", "frame", frame, "ghost", x.ID)
					}
					wasLive[x.ID] = s.live[x.ID]
				} else if !s.live[x.ID] {
					s.errorf("rule", "update-nonlive", "frame", frame, "ghost", x.ID)
				}
				seen[x.ID] = true
				events = append(events, event{gid: x.ID, kind: kind, ghost: x.Ghost, changes: x.Changes})
			}
			if len(seen) != len(events) {
				s.errorf("rule", "duplicate-dirty", "frame", frame)
			}
			for _, g := range d.Deleted {
				if !s.live[g] {
					s.errorf("rule", "delete-nonlive", "frame", frame)
					break
				}
			}
			for _, x := range d.Dirty {
				if x.New {
					s.addLive(x.ID)
				}
			}
			for _, g := range d.Deleted {
				s.dropLive(g)
			}
			for _, g := range d.Deleted {
				s.dead[g] = true
			}
			// dead.difference_update(live): only ghosts made live since the last delta frame can be in both
			for _, id := range keyLive {
				if s.live[id] {
					s.dead[id] = false
				}
			}
			keyLive = keyLive[:0]
			for _, x := range d.Dirty {
				if x.New && s.live[x.ID] {
					s.dead[x.ID] = false
				}
			}
		default:
			return nil, &fbgd.PyError{Class: "ValueError", Msg: fmt.Sprintf("frame %d: unknown marker %d", frame, fr.Marker)}
		}
		if r.Pos() != fr.BitPos || !canonical {
			s.errorf("rule", "bit-roundtrip", "frame", frame)
		}
		// Register forward creates before looking at references.
		for i := range events {
			e := &events[i]
			if e.kind != "upd" {
				s.create(e, wasLive[e.gid])
			}
		}
		for i := range events {
			e := &events[i]
			sid := s.sigOf[e.gid]
			if sid == 0 {
				return nil, fbgd.KeyError(e.gid)
			}
			sid--
			if e.kind == "upd" {
				for j := range e.changes {
					c := &e.changes[j]
					s.chunk(e, sid, c.Section, c.Property, &c.Chunk)
				}
				continue
			}
			g := e.ghost
			s.chunk(e, sid, -1, -1, &g.Create)
			for si := range g.Sections {
				for pi := range g.Sections[si].Chunks {
					s.chunk(e, sid, si, pi, &g.Sections[si].Chunks[pi])
				}
			}
		}
		if fr.Marker == 1 {
			s.danglingView(keyGhosts)
		}
	}
	s.res.frames = len(raw)
	return s.res, nil
}

// ViewRule is fbgd_gate.VIEW_RULE (r284).
const ViewRule = "dangling-view-reference"

// danglingView is fbgd_gate.dangling_view (r284): a structural ERROR for each ref of a player component's p12 (create =
// 20 bits, class bits 0001110 + ref13 player) or of its camera's p3 (the component's p0 target in the key) that names a
// ghost absent from the key. Server demos never write it; component/camera p5 are not judged (server demos leave them).
func (s *scanner) danglingView(gs []*fbgd.Ghost) {
	byID := make(map[int]*fbgd.Ghost, len(gs))
	for _, g := range gs {
		byID[g.ID] = g
	}
	check := func(g *fbgd.Ghost, prop int) {
		if len(g.Sections) == 0 || prop >= len(g.Sections[0].Chunks) {
			return
		}
		c := &g.Sections[0].Chunks[prop]
		for _, ref := range c.Refs {
			if ref.Target != 0 && !s.live[ref.Target] {
				s.res.errors = append(s.res.errors, with(s.where(g.ID, s.sigOf[g.ID]-1, 0, prop, c), "rule", ViewRule, "target", ref.Target))
			}
		}
	}
	for _, g := range gs {
		if g.Create.Bits != 20 || g.Create.Top(7) != 0b0001110 {
			continue
		}
		check(g, 12)
		if len(g.Sections) == 0 || len(g.Sections[0].Chunks) == 0 {
			continue
		}
		for _, cam := range g.Sections[0].Chunks[0].Refs {
			if cg := byID[cam.Target]; cg != nil {
				check(cg, 3)
			}
		}
	}
}

// create registers a key/new ghost: signature, create width, shape check, first soldier.
func (s *scanner) create(e *event, wasLive bool) {
	sig := fbgd.Signature(e.ghost)
	s.sigOf[e.gid] = s.intern(sig) + 1
	s.cwidth[e.gid] = e.ghost.Create.Bits
	shape := e.ghost.Shape()
	if s.o.collect != nil {
		s.o.collect.shape(sig, shape)
	}
	if s.o.allowed != nil {
		if rule := shapeRule(sig, shape, s.o.allowed); rule != "" {
			mk := strconv.Itoa(e.gid) + "|" + sig + "|" + shapeKey(shape)
			if !(rule == "shape-mismatch" && s.mismatch[mk]) {
				if rule == "shape-mismatch" {
					s.mismatch[mk] = true
				}
				s.errorf("rule", rule, "frame", s.frame, "ghost", e.gid, "signature", sig, "shape", shape,
					"create_width", s.cwidth[e.gid], "allowed", shapesValue(s.o.allowed.sortedShapes(sig)))
			}
		}
	}
	if s.res.firstSoldier < 0 && s.frame > 0 && !wasLive && fbgd.Soldier(e.ghost) {
		s.res.firstSoldier = s.frame
	}
}

// chunk is the per-chunk body of fbgd_gate.scan: whitelist checks, reference liveness, early chunks.
func (s *scanner) chunk(e *event, sid int32, si, pi int, c *fbgd.Chunk) {
	s.form = s.form[:0]
	for _, r := range c.Refs {
		s.form = append(s.form, byte(r.Flag))
	}
	var where *pyc.Object // built once, on the first error of this chunk
	mk := func() *pyc.Object {
		if where == nil {
			where = s.where(e.gid, sid, si, pi, c)
		}
		return where
	}
	k := islot{sid, int32(si), int32(pi)}
	if s.o.collect != nil {
		s.o.collect.observe(slot{s.sigs[sid], si, pi}, c.Bits, c.Index, string(s.form))
	}
	if s.o.allowed != nil {
		ss := s.opts(k)
		if !(si == -1 && fbgd.VariableWidth(s.sigs[sid])) && !ss.widths[c.Bits] {
			s.res.errors = append(s.res.errors, with(mk(), "rule", "unseen-widths", "allowed", nearest(ss.widths, c.Bits)))
		}
		if !ss.indices[c.Index] {
			s.res.errors = append(s.res.errors, with(mk(), "rule", "unseen-indices", "allowed", sortedInts(ss.indices)))
		}
		if !ss.forms[string(s.form)] {
			s.res.errors = append(s.res.errors, with(mk(), "rule", "unseen-forms", "allowed", sortedForms(ss.forms)))
		}
	}
	for _, ref := range c.Refs {
		if ref.Target == 0 || s.live[ref.Target] {
			continue
		}
		if s.dead[ref.Target] {
			if s.o.collect != nil {
				s.o.collect.staleForm(slot{s.sigs[sid], si, pi}, string(s.form))
			}
			if s.o.allowed != nil && !s.opts(k).stale[string(s.form)] {
				s.res.errors = append(s.res.errors, with(mk(), "rule", "unapproved-stale-reference", "target", ref.Target))
			}
		} else {
			s.res.errors = append(s.res.errors, with(mk(), "rule", "never-live-reference", "target", ref.Target))
		}
	}
	if s.res.early != nil && (s.res.firstSoldier < 0 || s.frame < s.res.firstSoldier) &&
		(s.o.earlyLimit < 0 || s.frame < s.o.earlyLimit) {
		s.res.early[earlyKey{s.frame, e.gid, e.kind, si, pi}] = c
	}
}

func (s *scanner) where(gid int, sid int32, si, pi int, c *fbgd.Chunk) *pyc.Object {
	return obj("frame", s.frame, "ghost", gid, "signature", s.sigs[sid], "section", si,
		"create_width", s.cwidth[gid], "property", pi, "width", c.Bits, "index", c.Index, "refs", refsValue(c.Refs))
}

// with is dict(where, k=v, ...): a copy of where with the extra keys appended (or replaced in place).
func with(where *pyc.Object, kv ...any) *pyc.Object {
	o := pyc.NewObject()
	for _, k := range where.Keys() {
		v, _ := where.Get(k)
		o.Set(k, v)
	}
	for i := 0; i < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

func shapesValue(s [][]int) []any {
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}

// nearest is sorted(options, key=lambda x: (abs(x - value), x))[:4].
func nearest(m map[int]bool, value int) []int {
	out := sortedInts(m)
	abs := func(x int) int {
		if x < 0 {
			return -x
		}
		return x
	}
	// stable insertion sort by distance keeps the ascending x order for equal distances
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && abs(out[j]-value) < abs(out[j-1]-value); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out[:min(4, len(out))]
}

// shapeRule is fbgd_gate.shape_rule: "" (allowed), "shape-mismatch" (known signature) or "unseen-shape".
func shapeRule(sig string, shape []int, w *Whitelist) string {
	known := w.shapes[sig]
	if _, ok := known[shapeKey(shape)]; ok {
		return ""
	}
	if len(known) > 0 {
		return "shape-mismatch"
	}
	return "unseen-shape"
}
