package capture

import (
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// WireLog is the decode as the wire site wrappers read it (wire.Log: packets by seq, ghosts, configs, multi).
func WireLog(d *Decoded) *wire.Log {
	l := &wire.Log{Packets: make(map[int64]*wire.Packet, len(d.Packets))}
	for seq, p := range d.Packets {
		l.Packets[int64(seq)] = &wire.Packet{Payload: p.Payload, Limit: int64(p.Limit)}
	}
	for _, c := range d.Configs {
		l.Configs = append(l.Configs, &wire.Config{ChildRaw: c.ChildRaw, Tables: [2][]int64{i64s(c.Tables[0]), i64s(c.Tables[1])}})
	}
	conv := func(gs []*Ghost) []*wire.Ghost {
		out := make([]*wire.Ghost, len(gs))
		for i, g := range gs {
			out[i] = &wire.Ghost{T: g.T, Pseq: int64(g.Pseq), ID: int64(g.ID), Phase: int64(g.Phase), SectionVT: g.SectionVT,
				EntryCursor: int64(g.EntryCursor), Cursor: int64(g.Cursor), Cache: g.Cache, DwordOffset: int64(g.DwordOffset),
				OriginBits: g.OriginBits, CreateCur: int64(g.CreateCur), Config: g.Config, Sections: g.Sections}
		}
		return out
	}
	l.Ghosts, l.Multi = conv(d.Ghosts), conv(d.Multi)
	return l
}

func i64s(v []uint32) []int64 {
	out := make([]int64, len(v))
	for i, x := range v {
		out[i] = int64(x)
	}
	return out
}

func u32List(v []uint32) []any {
	out := make([]any, len(v))
	for i, x := range v {
		out[i] = x
	}
	return out
}

func pairList(v [][2]uint32) []any {
	out := make([]any, len(v))
	for i, x := range v {
		out[i] = []any{x[0], x[1]}
	}
	return out
}

// PyValue is the packet dict(t, seq, buffer, limit, payload).
func (p *Packet) PyValue() any {
	o := pyc.NewObject()
	o.Set("t", p.T)
	o.Set("seq", p.Seq)
	o.Set("buffer", p.Buffer)
	o.Set("limit", p.Limit)
	o.Set("payload", p.Payload)
	return o
}

// PyValue is the raw dict(t, qseq, buffer, limit, first, payload).
func (q *Raw) PyValue() any {
	o := pyc.NewObject()
	o.Set("t", q.T)
	o.Set("qseq", q.Qseq)
	o.Set("buffer", q.Buffer)
	o.Set("limit", q.Limit)
	o.Set("first", q.First)
	o.Set("payload", q.Payload)
	return o
}

// PyValue is dict(child_raw, tables).
func (c *Config) PyValue() any {
	o := pyc.NewObject()
	o.Set("child_raw", c.ChildRaw)
	o.Set("tables", []any{u32List(c.Tables[0]), u32List(c.Tables[1])})
	return o
}

// PyValue is the bookmarks dict(pre, post, result, sec_counter, prop_counter, sections, props).
func (b *Bookmarks) PyValue() any {
	o := pyc.NewObject()
	o.Set("pre", b.Pre)
	o.Set("post", b.Post)
	o.Set("result", b.Result)
	o.Set("sec_counter", b.SecCounter)
	o.Set("prop_counter", b.PropCounter)
	o.Set("sections", pairList(b.Sections))
	o.Set("props", pairList(b.Props))
	return o
}

// ghostValue is the ghost dict; hinted adds v3_types.load's end_hint/span.
func ghostValue(g *Ghost, hinted bool) any {
	o := pyc.NewObject()
	o.Set("t", g.T)
	o.Set("pseq", g.Pseq)
	o.Set("id", int64(g.ID))
	o.Set("phase", int64(g.Phase))
	o.Set("ghost_vt", g.GhostVT)
	o.Set("section_vt", g.SectionVT)
	o.Set("entry_cursor", g.EntryCursor)
	o.Set("cursor", g.Cursor)
	o.Set("cache", g.Cache)
	o.Set("dword_offset", g.DwordOffset)
	o.Set("origin_bits", u32List(g.OriginBits[:]))
	o.Set("create_cur", g.CreateCur)
	o.Set("config", g.Config)
	if g.Sections != nil {
		o.Set("sections", u32List(g.Sections))
	}
	if g.Bookmarks != nil {
		o.Set("bookmarks", g.Bookmarks.PyValue())
	}
	if hinted {
		if g.HasEndHint {
			o.Set("end_hint", g.EndHint)
		} else {
			o.Set("end_hint", nil)
		}
		o.Set("span", nil)
	}
	return o
}

// PyValue is the ghost dict as decode leaves it (no end_hint/span).
func (g *Ghost) PyValue() any { return ghostValue(g, false) }

// PyValue is the session dict.
func (s *Session) PyValue() any {
	o := pyc.NewObject()
	o.Set("ver", s.Ver)
	o.Set("flags", s.Flags)
	o.Set("conn", s.Conn)
	o.Set("dispatcher_vt", s.DispatcherVT)
	o.Set("ghost_bits", s.GhostBits)
	o.Set("qseq", s.Qseq)
	o.Set("pseq", s.Pseq)
	o.Set("level_ptr", s.LevelPtr)
	o.Set("filetime", s.Filetime)
	o.Set("it", s.IT)
	o.Set("name", s.Name)
	return o
}

// PyValue is the sync dict.
func (t *Sync) PyValue() any {
	o := pyc.NewObject()
	o.Set("it", t.IT)
	o.Set("filetime", t.Filetime)
	o.Set("qseq", t.Qseq)
	o.Set("pseq", t.Pseq)
	o.Set("prod", t.Prod)
	o.Set("drops", t.Drops)
	o.Set("contention", t.Contention)
	o.Set("skip", t.Skip)
	return o
}

// PyValue is the end dict.
func (e *End) PyValue() any {
	o := pyc.NewObject()
	o.Set("reason", e.Reason)
	o.Set("it", e.IT)
	o.Set("qseq", e.Qseq)
	o.Set("pseq", e.Pseq)
	return o
}

// PyValue is the full v1 row dict (marker t id phase cursor cache dword_offset flags limit payload child_raw count0
// tables origin_bits).
func (r *V1Row) PyValue() any {
	o := pyc.NewObject()
	o.Set("marker", 1)
	o.Set("t", r.G.T)
	o.Set("id", int64(r.G.ID))
	o.Set("phase", 0)
	o.Set("cursor", r.G.Cursor)
	o.Set("cache", r.G.Cache)
	o.Set("dword_offset", r.G.DwordOffset)
	o.Set("flags", 0)
	o.Set("limit", r.P.Limit)
	o.Set("payload", r.P.Payload)
	o.Set("child_raw", r.C.ChildRaw)
	o.Set("count0", len(r.C.Tables[0]))
	o.Set("tables", []any{u32List(r.C.Tables[0]), u32List(r.C.Tables[1])})
	o.Set("origin_bits", u32List(r.G.OriginBits[:]))
	return o
}

// ------------------------------------------------------------------------------------------------- golden stages

// Golden stage records (the Python golden dumper's stage format): emit receives each record in dump order.

// GoldenParse emits the 'parse' stage: {call, i, typ, body}.
func GoldenParse(call int, recs []Record, emit func(any) error) error {
	for i, r := range recs {
		o := pyc.NewObject()
		o.Set("call", call)
		o.Set("i", i)
		o.Set("typ", r.Type)
		o.Set("body", r.Body)
		if err := emit(o); err != nil {
			return err
		}
	}
	return nil
}

// GoldenDecode emits the 'decode' stage (the v3_types.load dict): {call, part, key|i, v} per dict/list item in the
// decode dict's key order, {call, part, v} for the scalars.
func GoldenDecode(call int, d *Decoded, emit func(any) error) error {
	rec := func(part, ik string, k, v any) error {
		o := pyc.NewObject()
		o.Set("call", call)
		o.Set("part", part)
		if ik != "" {
			o.Set(ik, k)
		}
		o.Set("v", v)
		return emit(o)
	}
	for _, s := range d.PacketSeqs {
		if err := rec("packets", "key", s, d.Packets[s].PyValue()); err != nil {
			return err
		}
	}
	ghosts := func(part string, gs []*Ghost, hinted bool) error {
		for i, g := range gs {
			if err := rec(part, "i", i, ghostValue(g, hinted)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := ghosts("ghosts", d.Ghosts, d.Hinted); err != nil {
		return err
	}
	for i, c := range d.Configs {
		if err := rec("configs", "i", i, c.PyValue()); err != nil {
			return err
		}
	}
	if err := ghosts("multi", d.Multi, false); err != nil {
		return err
	}
	if err := rec("orphans", "", nil, d.Orphans); err != nil {
		return err
	}
	if err := rec("borphans", "", nil, d.BOrphans); err != nil {
		return err
	}
	for i, s := range d.Sessions {
		if err := rec("sessions", "i", i, s.PyValue()); err != nil {
			return err
		}
	}
	for i, s := range d.Syncs {
		if err := rec("syncs", "i", i, s.PyValue()); err != nil {
			return err
		}
	}
	for i, e := range d.Ends {
		if err := rec("ends", "i", i, e.PyValue()); err != nil {
			return err
		}
	}
	for _, s := range d.RawSeqs {
		if err := rec("raws", "key", s, d.Raws[s].PyValue()); err != nil {
			return err
		}
	}
	return nil
}

// GoldenV1Rows emits the 'to_v1_rows' stage: each row without payload/child_raw/tables (the decode's objects) plus
// pseq and config.
func GoldenV1Rows(rows []V1Row, emit func(any) error) error {
	for i := range rows {
		r := &rows[i]
		o := r.PyValue().(*pyc.Object)
		o.Delete("payload")
		o.Delete("child_raw")
		o.Delete("tables")
		o.Set("pseq", r.G.Pseq)
		o.Set("config", r.G.Config)
		if err := emit(o); err != nil {
			return err
		}
	}
	return nil
}
