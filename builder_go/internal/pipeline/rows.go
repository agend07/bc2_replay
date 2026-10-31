package pipeline

import (
	"math"

	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/golden"
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/soldier"
	"bc2replay/builder/internal/wire"
)

// tkind is the Python type of a row's 't': int, float, or loose (the DELNULL pass made the row; Go does not track its
// type: rendered as an int when integral, compared integral-float == int by the maintainer check).
type tkind uint8

const (
	tInt tkind = iota
	tFloat
	tLoose
)

// idMap is an insertion-ordered {int: int} dict (cl04c's cur / _cur id snapshots).
type idMap struct {
	keys []int
	m    map[int]int
}

func newIDMap() *idMap { return &idMap{m: map[int]int{}} }

func (c *idMap) set(k, v int) {
	if _, ok := c.m[k]; !ok {
		c.keys = append(c.keys, k)
	}
	c.m[k] = v
}

func (c *idMap) get(k int) (int, bool) { v, ok := c.m[k]; return v, ok }

func (c *idMap) clone() *idMap {
	n := &idMap{keys: append([]int(nil), c.keys...), m: make(map[int]int, len(c.m))}
	for k, v := range c.m {
		n.m[k] = v
	}
	return n
}

// PyValue is the dict ({"$dict": int pairs}; {} when empty).
func (c *idMap) PyValue() any {
	if len(c.keys) == 0 {
		return pyc.NewObject()
	}
	ps := make(golden.Pairs, len(c.keys))
	for i, k := range c.keys {
		ps[i] = golden.Pair{K: k, V: c.m[k]}
	}
	return ps
}

// meta is what a cl04c row dict carries beyond fbgd.Row: the transient keys (_initial/_vt, _cur, _srefs/_wt, syn)
// and the dict forms the checkpoint renderer needs (t type, chunk 'refs' key presence, fbgd_read chunk forms).
type meta struct {
	tf tkind

	initial *wire.SliceResult // _initial (with _vt) until create_refs pops it
	vt      uint32
	cur     *idMap // _cur
	srefs   []soldier.PropRefs
	wt      float64 // _wt (int)
	syn     bool
}

var zeroMeta = &meta{}

// rowset is cl04c's rows list plus the side data of each row dict.
type rowset struct {
	rows []*fbgd.Row
	m    map[*fbgd.Row]*meta
}

func (s *rowset) mt(r *fbgd.Row) *meta {
	if x := s.m[r]; x != nil {
		return x
	}
	return zeroMeta
}

func (s *rowset) mw(r *fbgd.Row) *meta {
	x := s.m[r]
	if x == nil {
		x = &meta{}
		s.m[r] = x
	}
	return x
}

func (s *rowset) add(r *fbgd.Row, tf tkind) {
	s.rows = append(s.rows, r)
	if tf != tInt {
		s.mw(r).tf = tf
	}
}

func timeValue(t float64, tf tkind) any {
	switch tf {
	case tInt:
		return int64(t)
	case tLoose:
		if t == math.Trunc(t) && math.Abs(t) < 1<<53 {
			return int64(t)
		}
	}
	return t
}

func refsValue(refs []fbgd.Ref) []any {
	out := make([]any, len(refs))
	for i, r := range refs {
		out[i] = []any{r.Target, r.Flag}
	}
	return out
}

// chunkValue is a row chunk dict: {value, bits[, refs]} ('refs' present when the Go chunk has a non-nil table);
// SRC rows: the fbgd_read form {index, bits, value, start, refs}.
func chunkValue(c *fbgd.RowChunk) *pyc.Object {
	o := pyc.NewObject()
	o.Set("value", c.Value)
	o.Set("bits", c.Value.Len())
	if c.Refs != nil {
		o.Set("refs", refsValue(c.Refs))
	}
	return o
}

func createValue(c *fbgd.OutChunk) *pyc.Object {
	o := pyc.NewObject()
	o.Set("index", c.Index)
	o.Set("bits", c.Value.Len())
	o.Set("value", c.Value)
	o.Set("refs", refsValue(c.Refs))
	return o
}

func chunksValue(r *fbgd.Row) any {
	if len(r.Chunks) == 0 {
		return pyc.NewObject()
	}
	ps := make(golden.Pairs, len(r.Chunks))
	for i := range r.Chunks {
		c := &r.Chunks[i]
		var k any = c.Key.Prop
		if c.Key.Sec != 0 || c.Tuple {
			k = []any{c.Key.Sec, c.Key.Prop}
		}
		ps[i] = golden.Pair{K: k, V: chunkValue(c)}
	}
	return ps
}

func srefsValue(rs []soldier.PropRefs) any {
	if len(rs) == 0 {
		return pyc.NewObject()
	}
	ps := make(golden.Pairs, len(rs))
	for i, pr := range rs {
		l := make([]any, len(pr.Refs))
		for j, x := range pr.Refs {
			l[j] = []any{x.Off, x.Bits}
		}
		ps[i] = golden.Pair{K: pr.Prop, V: l}
	}
	return ps
}

// rowValue is the row dict in its Python key order at this moment.
func (s *rowset) rowValue(r *fbgd.Row) *pyc.Object {
	m := s.mt(r)
	o := pyc.NewObject()
	o.Set("k", string(r.K))
	o.Set("t", timeValue(r.T, m.tf))
	o.Set("id", r.ID)
	switch r.K {
	case 'c':
		o.Set("type", r.Type)
		o.Set("create", createValue(&r.Create))
	case 'u':
		if m.initial != nil {
			o.Set("_initial", m.initial)
			o.Set("_vt", m.vt)
		}
		o.Set("chunks", chunksValue(r))
		if m.srefs != nil {
			o.Set("_srefs", srefsValue(m.srefs))
			o.Set("_wt", int64(m.wt))
		}
	}
	if m.cur != nil {
		o.Set("_cur", m.cur)
	}
	if m.syn {
		o.Set("syn", true)
	}
	return o
}

func (s *rowset) loose(r *fbgd.Row) bool { return s.mt(r).tf == tLoose }

// rowsValue renders rows (a loop record's appended rows / a head's row list).
func (s *rowset) rowsValue(rows []*fbgd.Row) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = s.rowValue(r)
	}
	return out
}
