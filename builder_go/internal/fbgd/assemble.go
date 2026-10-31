package fbgd

// Port of rows_to_fbgd.assemble / tplnew_bad: v2 recorder rows (create / update / delete +
// timestamp) -> .fbgd + .fbgk. Frame n = bin [t0+n*dt, t0+(n+1)*dt), dt = 1/30 s in 100 ns ticks; frame n % 151 == 0
// is a keyframe (full state after that bin's changes), the others are deltas.
//
// Not ported (no caller): template_from_ghost, the single-template form (cl04c always passes per-type templates),
// assemble's base= and nframes= arguments and the FOLLOW_INIT=False switch (pre-r181 bug, r182 test only).

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/pyc"
)

const (
	// DTBits is the delta frame's f31 dt (1/30 s).
	DTBits = 1023969417
	// DTTicks is 10_000_000 / 30 as a Python float (correctly rounded, like the Go constant conversion).
	DTTicks float64 = 10_000_000.0 / 30
	// KeyEvery: frame n % KeyEvery == 0 is a keyframe; the .fbgk interval is KeyEvery - 1.
	KeyEvery = 151
)

// r289 JOINKEY=auto (rows_to_fbgd.JOIN_GAP / JOIN_MIN / JOIN_MAX, join_fold_frame).
const (
	JoinGap = 5
	JoinMin = 5
	JoinMax = 19
)

// JoinFoldFrame is rows_to_fbgd.join_fold_frame: the last frame (from t0) of the wire join burst = the last frame holding
// a create before the first run of >= gap create-free frames, clamped to [lo, hi]; negative frames are ignored.
func JoinFoldFrame(createFrames []int, gap, lo, hi int) int {
	fs := slices.Clone(createFrames)
	slices.Sort(fs)
	end := 0
	for _, f := range fs {
		if f < 0 {
			continue
		}
		if f-end > gap {
			break
		}
		end = f
	}
	return min(max(end, lo), hi)
}

// Prolog is the client demo version prolog.
var Prolog = []byte("EA Digital Illusions CE AB\x00ROMEPC133769\x00")

// TypeKey is a template key / create row 'type': int64 (a section vt) or string ('WM...', 'SRC<id>'). Ints must be
// int64 so that equal Python keys are equal map keys.
type TypeKey = any

// ChunkKey is rows_to_fbgd._ckey: (section, property); a plain property index p is (0, p).
type ChunkKey struct{ Sec, Prop int }

// RowChunk is one entry of a 'u' row's chunks {p: dict(value, bits[, refs])}. Tuple records whether the Python key
// was a (section, property) tuple rather than an int (it does not change the output).
type RowChunk struct {
	Key   ChunkKey
	Tuple bool
	Value bits.Bits
	Refs  []Ref
}

// Row is one assemble input row: K 'c' (create: Type, Create), 'u' (dirty chunks: Chunks), 'd' (delete). T is the
// time in 100 ns ticks; Python mixes ints and floats there, all exact in a float64 below 2**53. Assemble moves T of
// rows in keyframe bins in place, like Python (the row dicts are shared with the caller).
type Row struct {
	K      byte
	T      float64
	ID     int
	Type   TypeKey
	Create OutChunk
	Chunks []RowChunk
}

// TplSection is a template section: header, mask (width = property count) and the default chunk of each property.
type TplSection struct {
	Header   uint32
	Mask     bits.Bits
	Defaults []OutChunk
}

// Count is the section's property count.
func (s *TplSection) Count() int { return s.Mask.Len() }

// Template is a ghost type template ({'sections': [...]}).
type Template struct{ Sections []TplSection }

// Templates is the per-type template dict.
type Templates map[TypeKey]*Template

// Info is assemble's info dict: frames, keyframes, tplnew_bad [(frame, id)] (r182 self-check).
type Info struct {
	Frames, Keyframes int
	TplNewBad         [][2]int
}

// PyValue is the Python dict {'frames', 'keyframes', 'tplnew_bad': [(frame, id)]} (golden form).
func (i Info) PyValue() any {
	o := pyc.NewObject()
	o.Set("frames", i.Frames)
	o.Set("keyframes", i.Keyframes)
	tb := make([]any, len(i.TplNewBad))
	for k, x := range i.TplNewBad {
		tb[k] = []any{x[0], x[1]}
	}
	o.Set("tplnew_bad", tb)
	return o
}

// bin is int((t - t0) // DT_TICKS) with CPython float floordiv.
func bin(t, t0 float64) int {
	q, _ := pyc.FloatFloorDiv(t-t0, DTTicks)
	return int(q)
}

func byT(a, b *Row) int { return cmp.Compare(a.T, b.T) }

func indexOf(s []int, g int) int { return slices.Index(s, g) }

// remove is list.remove(g) guarded by 'g in list' (first occurrence).
func remove(s []int, g int) []int {
	if i := indexOf(s, g); i >= 0 {
		return slices.Delete(s, i, i+1)
	}
	return s
}

// setChange is dirty[g][key] = chunk on a slice kept sorted by (section, property).
func setChange(s []OutChange, c OutChange) []OutChange {
	i, found := slices.BinarySearchFunc(s, c, changeCmp)
	if found {
		s[i] = c
		return s
	}
	return slices.Insert(s, i, c)
}

type liveGhost struct {
	seq    int // dict position: assigned on insert, kept on re-assign
	create OutChunk
	props  map[ChunkKey]OutChunk
	typ    TypeKey
	ini    map[ChunkKey]OutChunk // the create's initial-state chunks (r182)
}

// errKey is Python's KeyError for a template type.
func errKey(k TypeKey) error { return fmt.Errorf("KeyError: %#v", k) }

// baseline is rows_to_fbgd._baseline: live state -> keyframe/new ghost (unset properties from the template).
func baseline(gid int, st *liveGhost, tpl Templates) (*OutGhost, error) {
	t, ok := tpl[st.typ]
	if !ok {
		return nil, errKey(st.typ)
	}
	g := &OutGhost{ID: gid, Create: st.create, Sections: make([]OutSection, len(t.Sections))}
	for si := range t.Sections {
		s := &t.Sections[si]
		chunks := make([]OutChunk, s.Count())
		for p := range chunks {
			if c, ok := st.props[ChunkKey{si, p}]; ok {
				chunks[p] = c
			} else if p < len(s.Defaults) {
				chunks[p] = s.Defaults[p]
			} else {
				return nil, errors.New("IndexError: list index out of range")
			}
		}
		g.Sections[si] = OutSection{s.Header, s.Mask, chunks}
	}
	return g, nil
}

// tplNewBad is rows_to_fbgd.tplnew_bad for one delta NEW: written from template defaults only although its create
// had an initial-state row that differs from them (cl04_1062 ghost 279, r178). bl = the NEW's baseline.
func tplNewBad(bl *OutGhost, st *liveGhost, tpl Templates) (bool, error) {
	if len(st.ini) == 0 {
		return false, nil
	}
	tp := tpl[st.typ]
	dflt := func(si, pi int) (bits.Bits, error) {
		if si >= len(tp.Sections) || pi >= len(tp.Sections[si].Defaults) {
			return bits.Bits{}, errors.New("IndexError: list index out of range")
		}
		return tp.Sections[si].Defaults[pi].Value, nil
	}
	for si, s := range bl.Sections {
		for pi, c := range s.Chunks {
			d, err := dflt(si, pi)
			if err != nil {
				return false, err
			}
			if !c.Value.Equal(d) {
				return false, nil
			}
		}
	}
	for k, c := range st.ini {
		if k.Sec < len(tp.Sections) && k.Prop < tp.Sections[k.Sec].Count() {
			d, err := dflt(k.Sec, k.Prop)
			if err != nil {
				return false, err
			}
			if !c.Value.Equal(d) {
				return true, nil
			}
		}
	}
	return false, nil
}

// Assemble is rows_to_fbgd.assemble(rows, tpl, t0=t0, keysnap=keysnap) -> (fbgd, fbgk, info).
// keysnap (r220): updates in a keyframe bin also move to the previous bin.
func Assemble(rows []*Row, tpl Templates, t0 float64, keysnap bool) ([]byte, []byte, Info, error) {
	var info Info
	if len(rows) == 0 {
		return nil, nil, info, errors.New("ValueError: no rows")
	}
	rs := slices.Clone(rows)
	slices.SortStableFunc(rs, byT) // stable: row order kept inside a bin
	// r182: create row -> its initial-state chunks (rows of that id at the create's t, before any move)
	ini := map[*Row]map[ChunkKey]OutChunk{}
	lc := map[int]*Row{}
	for _, r := range rs {
		switch r.K {
		case 'c':
			lc[r.ID] = r
		case 'u':
			c := lc[r.ID]
			if c == nil || c.T != r.T {
				continue
			}
			m := ini[c]
			if m == nil {
				m = map[ChunkKey]OutChunk{}
				ini[c] = m
			}
			for _, ch := range r.Chunks {
				m[ch.Key] = OutChunk{Index: ch.Key.Prop, Value: ch.Value, Refs: ch.Refs}
			}
		}
	}
	// A delete inside a keyframe bin would be lost (a keyframe has no delete list): move it to the previous bin; a
	// create there would appear in the key with no delta create (r153/r159): same move. r181: the create's
	// initial-state rows (same t, after it) follow it.
	moved := map[int][2]float64{} // id -> (create t before the move, after)
	for _, r := range rs {
		if r.K == 'u' {
			if m, ok := moved[r.ID]; ok && m[0] == r.T {
				r.T = m[1]
				continue
			}
		}
		b := bin(r.T, t0)
		if (r.K == 'd' || r.K == 'c' || keysnap) && b > 0 && b%KeyEvery == 0 {
			nt := t0 + float64(float64(b)*DTTicks)
			nt -= 1
			if r.K == 'c' {
				moved[r.ID] = [2]float64{r.T, nt}
			}
			r.T = nt
		}
	}
	slices.SortStableFunc(rs, byT)
	bins := map[int][]*Row{}
	maxb := math.MinInt
	for _, r := range rs {
		b := bin(r.T, t0)
		bins[b] = append(bins[b], r)
		maxb = max(maxb, b)
	}
	n := maxb + 1

	live := map[int]*liveGhost{}
	seq := 0
	counts := map[int][]int{}
	var frames []OutFrame
	dirty := map[int][]OutChange{} // per frame: id -> chunks by (section, property), last write wins
	for f := 0; f < n; f++ {
		var news, deleted, order []int
		clear(dirty)
		for _, r := range bins[f] {
			g := r.ID
			switch {
			case r.K == 'c':
				st := &liveGhost{create: r.Create, props: map[ChunkKey]OutChunk{}, typ: r.Type, ini: ini[r]}
				if old := live[g]; old != nil {
					st.seq = old.seq
				} else {
					seq++
					st.seq = seq
				}
				live[g] = st
				news = append(news, g)
				delete(dirty, g)
				order = append(remove(order, g), g)
			case r.K == 'd' && live[g] != nil:
				delete(live, g)
				delete(dirty, g)
				order = remove(order, g)
				if indexOf(news, g) >= 0 {
					news = remove(news, g) // created and deleted inside one bin
				} else {
					deleted = append(deleted, g)
				}
			case r.K == 'u' && live[g] != nil:
				isNew := indexOf(news, g) >= 0
				for _, c := range r.Chunks {
					ch := OutChunk{Index: c.Key.Prop, Value: c.Value, Refs: c.Refs}
					live[g].props[c.Key] = ch
					if !isNew {
						dirty[g] = setChange(dirty[g], OutChange{c.Key.Sec, c.Key.Prop, ch})
					}
				}
				if !isNew && indexOf(order, g) < 0 {
					order = append(order, g)
				}
			}
		}
		if f%KeyEvery == 0 {
			ids := make([]int, 0, len(live))
			for g := range live {
				ids = append(ids, g)
			}
			slices.SortFunc(ids, func(a, b int) int { return cmp.Compare(live[a].seq, live[b].seq) })
			ghosts := make([]*OutGhost, len(ids))
			counts = make(map[int][]int, len(ids))
			for i, g := range ids {
				bl, err := baseline(g, live[g], tpl)
				if err != nil {
					return nil, nil, info, err
				}
				ghosts[i] = bl
				counts[g] = bl.Counts()
			}
			pl, nb, err := EncodeKeyframe(ghosts)
			if err != nil {
				return nil, nil, info, err
			}
			frames = append(frames, OutFrame{1, pl, nb})
			continue
		}
		d := &OutDelta{DT: DTBits, Deleted: deleted}
		for _, g := range order {
			if indexOf(news, g) >= 0 {
				bl, err := baseline(g, live[g], tpl)
				if err != nil {
					return nil, nil, info, err
				}
				d.Dirty = append(d.Dirty, OutDirty{ID: g, New: bl})
				bad, err := tplNewBad(bl, live[g], tpl) // = tplnew_bad over the snapshot taken here
				if err != nil {
					return nil, nil, info, err
				}
				if bad {
					info.TplNewBad = append(info.TplNewBad, [2]int{f, g})
				}
			} else if ch, ok := dirty[g]; ok {
				d.Dirty = append(d.Dirty, OutDirty{ID: g, Changes: ch}) // sorted(dirty[g].items())
			}
		}
		pl, nb, err := EncodeDelta(d, counts)
		if err != nil {
			return nil, nil, info, err
		}
		for _, g := range d.Deleted {
			delete(counts, g)
		}
		for _, x := range d.Dirty { // ghosts created in a delta need their section counts for later upd
			if x.New != nil {
				counts[x.ID] = x.New.Counts()
			}
		}
		frames = append(frames, OutFrame{2, pl, nb})
	}
	data := WriteFile(Prolog, frames)
	// .fbgk: u32 interval (150); per keyframe u32 delta-frame counter, f32 sim time 0.0, u32 byte offset
	k := binary.LittleEndian.AppendUint32(nil, KeyEvery-1)
	off := len(Prolog)
	for f, fr := range frames {
		if fr.Marker == 1 {
			k = binary.LittleEndian.AppendUint32(k, uint32(f-f/KeyEvery))
			k = binary.LittleEndian.AppendUint32(k, 0)
			if off > math.MaxUint32 {
				return nil, nil, info, errors.New("struct.error: 'I' format requires 0 <= number <= 4294967295")
			}
			k = binary.LittleEndian.AppendUint32(k, uint32(off))
			info.Keyframes++
		}
		off += 9 + len(fr.Payload)
	}
	info.Frames = n
	if info.TplNewBad == nil {
		info.TplNewBad = [][2]int{}
	}
	return data, k, info, nil
}
