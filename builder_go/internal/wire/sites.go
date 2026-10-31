package wire

import (
	"encoding/binary"
	"fmt"
	"math"
	mbits "math/bits"
	"strconv"
	"strings"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/pyc"
)

// Site wrappers (wire_py_core layer 4): the former Unicorn call sites over a decoded m-log (recorder_v3.decode).

// Packet is one decoded P record (payload + bit limit).
type Packet struct {
	Payload []byte
	Limit   int64
	bits    *bits.Bits // payload_bits, cached
}

// Bits is payload_bits(payload) (cached).
func (p *Packet) Bits() bits.Bits {
	if p.bits == nil {
		b := bits.FromBytes(p.Payload)
		p.bits = &b
	}
	return *p.bits
}

// Config is one decoded C record.
type Config struct {
	ChildRaw []byte
	Tables   [2][]int64
}

// Ghost is one decoded G record (ghosts: one section; multi: Sections set, SectionVT 0).
type Ghost struct {
	T           uint64
	Pseq        int64
	ID          int64
	Phase       int64
	SectionVT   uint32
	EntryCursor int64
	Cursor      int64
	Cache       uint32
	DwordOffset int64
	OriginBits  [3]uint32
	CreateCur   int64
	Config      int
	Sections    []uint32
}

// Log is the decoded m-log the wrappers read (recorder_v3.decode: packets by seq, ghosts, configs, multi) plus the
// create_fields memo (wire_py_core / v3_wire_audit d['_create_ctx']) as explicit state: built once, on the first
// CreateFields call, from Ghosts in order, keyed by record identity, never rebuilt (same as the Python memo).
type Log struct {
	Packets   map[int64]*Packet
	Ghosts    []*Ghost
	Configs   []*Config
	Multi     []*Ghost
	createCtx map[*Ghost]map[uint32]int64

	// OnCreateFields, when set, observes every CreateFields call (maintainer checkpoints): built reports that this
	// call built the memo; res is the returned fields.
	OnCreateFields func(g *Ghost, built bool, res map[uint32]int64)
}

// CreateCtx is the create_fields memo entry of g (ok false = not memoized or the memo is not built yet).
func (d *Log) CreateCtx(g *Ghost) (map[uint32]int64, bool) {
	f, ok := d.createCtx[g]
	return f, ok
}

func (d *Log) packet(g *Ghost) (*Packet, error) {
	if p := d.Packets[g.Pseq]; p != nil {
		return p, nil
	}
	return nil, &ReaderError{fmt.Sprintf("KeyError: packet %d", g.Pseq)}
}

func (d *Log) config(g *Ghost) (*Config, error) {
	if g.Config >= 0 && g.Config < len(d.Configs) {
		return d.Configs[g.Config], nil
	}
	return nil, &ReaderError{fmt.Sprintf("IndexError: config %d", g.Config)}
}

// bitInt is int(s, 2) of a bit string slice (ValueError when empty).
func bitInt(b bits.Bits) (int64, error) {
	if b.Len() == 0 {
		return 0, &ReaderError{"ValueError: invalid literal for int() with base 2: ''"}
	}
	v, _ := b.Uint64()
	return int64(v), nil
}

// CreateFields is wire_py_core.create_fields (= v3_wire_audit.create_fields): the 82/40 ticket u16 -> +0x50 and u5
// -> +0x54 of the latest create of the record's ghost id at that point of the log; {} for other sections or a g that
// is not one of d.Ghosts.
func (d *Log) CreateFields(g *Ghost) (map[uint32]int64, error) {
	if d.OnCreateFields != nil {
		built := d.createCtx == nil
		res, err := d.createFields(g)
		if err == nil {
			d.OnCreateFields(g, built, res)
		}
		return res, err
	}
	return d.createFields(g)
}

func (d *Log) createFields(g *Ghost) (map[uint32]int64, error) {
	if d.createCtx == nil {
		ctx, live := map[*Ghost]map[uint32]int64{}, map[int64]map[uint32]int64{}
		for _, x := range d.Ghosts {
			if x.Phase == 1 {
				live[x.ID] = nil
				if x.SectionVT == TicketVT {
					p, err := d.packet(x)
					if err != nil {
						return nil, err
					}
					c := p.Bits().Slice(int(x.CreateCur), int(x.Cursor))
					a, err := bitInt(c.Slice(-21, -5))
					if err != nil {
						return nil, err
					}
					b, err := bitInt(c.Slice(-5, c.Len()))
					if err != nil {
						return nil, err
					}
					live[x.ID] = map[uint32]int64{0x50: a, 0x54: b}
				}
			}
			if x.SectionVT == TicketVT {
				ctx[x] = live[x.ID]
			}
		}
		d.createCtx = ctx
	}
	if f := d.createCtx[g]; len(f) > 0 {
		return f, nil
	}
	return map[uint32]int64{}, nil
}

// InferIDWidths is wire_py_core.infer_id_widths (= v3_wire_audit): ghost-id width per config from the create
// bookmarks (id + isNew bit between entry_cursor and create_cur).
func InferIDWidths(d *Log) (map[int]int64, error) {
	widths := map[int]int64{}
	for _, g := range d.Ghosts {
		if g.Phase != 1 {
			continue
		}
		n := g.CreateCur - g.EntryCursor - 1
		p, err := d.packet(g)
		if err != nil {
			return nil, err
		}
		header := p.Bits().Slice(int(g.EntryCursor), int(g.CreateCur)).String()
		if n < 1 || n > 16 || header != bits.FormatBin(uint64(g.ID), int(n))+"1" {
			return nil, &ReaderError{fmt.Sprintf("ValueError: invalid ID bookmark/header: ghost %d t %d", g.ID, g.T)}
		}
		if w, ok := widths[g.Config]; ok && w != n {
			return nil, &ReaderError{"ValueError: ID width changed within config; need per-record width"}
		}
		widths[g.Config] = n
	}
	return widths, nil
}

func originOf(ob [3]uint32) [3]float64 {
	return [3]float64{pyc.UnpackF32(ob[0]), pyc.UnpackF32(ob[1]), pyc.UnpackF32(ob[2])}
}

func le32(b []byte, off int) (int64, error) {
	if len(b) < off+4 {
		return 0, &ReaderError{"struct.error: unpack_from requires a buffer of at least " + strconv.Itoa(off+4) + " bytes"}
	}
	return int64(binary.LittleEndian.Uint32(b[off:])), nil
}

// wireInp is wire_py_core.wire_inp (= v3_wire_audit.fixture as a run input) for record g read as section vt with
// sec_fields fields.
func wireInp(d *Log, g *Ghost, widths map[int]int64, vt uint32, fields map[uint32]int64) (*Inp, error) {
	gb, ok := widths[g.Config]
	if !ok {
		return nil, &ReaderError{"ValueError: no create bookmark establishing ID width for config"}
	}
	c, err := d.config(g)
	if err != nil {
		return nil, err
	}
	inp := NewInp()
	inp.PlayerFixture = vt == PlayerVTs[0] || vt == PlayerVTs[1]
	inp.Stubs = WireStubs(inp.PlayerFixture)
	inp.GhostBits = gb
	if inp.ListMax, err = le32(c.ChildRaw, 4); err != nil {
		return nil, err
	}
	if inp.RefBits, err = le32(c.ChildRaw, 12); err != nil {
		return nil, err
	}
	inp.CfgChild = c.ChildRaw
	inp.QN, inp.QT = int64(len(c.Tables[0])), c.Tables[0]
	inp.Origin = originOf(g.OriginBits)
	inp.SecVT = vt
	inp.SecFields = fields
	return inp, nil
}

// ---------------------------------------------------------------------------------------------------- slice_record

// Chunk is one present property of a sliced record.
type Chunk struct {
	Property   int
	Value      bits.Bits // body (Len = 'bits', value = int(body or '0', 2))
	BodyStart  int64
	References []Ref
}

// SliceResult is the slice_record dict (v3_wire_audit.slice_record).
type SliceResult struct {
	G              *Ghost
	End            uint32
	Properties     int
	Flags          uint32
	Error          string // "" = None
	PlayerFixture  bool
	ResolvedRefs   bool
	CreateFields   map[uint32]int64
	ReferenceReads []Ref
	OriginVectors  []Ovec
	Chunks         []Chunk
}

// SliceRecord is wire_py_core.slice_record_py (= v3_wire_audit.slice_record): the section reader over record g,
// resolve True then False when the first attempt fails or overflows; chunks = the present properties' bodies.
func SliceRecord(d *Log, g *Ghost, widths map[int]int64) (*SliceResult, error) {
	p, err := d.packet(g)
	if err != nil {
		return nil, err
	}
	var o Out
	var inp *Inp
	for _, resolve := range [2]bool{true, false} {
		fields, err := d.CreateFields(g)
		if err != nil {
			return nil, err
		}
		if inp, err = wireInp(d, g, widths, g.SectionVT, fields); err != nil {
			return nil, err
		}
		inp.ResolveRefs = resolve
		if inp.Entry, err = SectionReader(g.SectionVT); err != nil {
			return nil, err
		}
		inp.Buf, inp.NBits = p.Payload, p.Limit
		inp.State = &State{g.Cursor, int64(g.Cache), g.DwordOffset, 0}
		if o, err = RunReader(inp); err != nil {
			return nil, err
		}
		if o.Err == "" && o.Flags == 0 {
			break
		}
	}
	out := &SliceResult{G: g, End: o.End, Properties: len(o.Pos), Flags: o.Flags, Error: o.Err,
		PlayerFixture: inp.PlayerFixture, ResolvedRefs: inp.ResolveRefs, CreateFields: inp.SecFields,
		ReferenceReads: o.Refs, OriginVectors: o.Ovs, Chunks: []Chunk{}}
	end := int64(o.End)
	if o.Err != "" || o.Flags != 0 || !(g.Cursor <= end && end <= p.Limit) {
		return out, nil
	}
	pb := p.Bits()
	for pi, start := range o.Pos {
		stop := end
		if pi+1 < len(o.Pos) {
			stop = int64(o.Pos[pi+1])
		}
		s := int64(start)
		if !(g.Cursor <= s && s < stop && stop <= end) {
			out.Error, out.Chunks = "invalid property boundaries", []Chunk{}
			return out, nil
		}
		if s >= int64(pb.Len()) {
			return nil, &ReaderError{"IndexError: string index out of range"}
		}
		if pb.Slice(int(s), int(s)+1).String() == "1" {
			refs := []Ref{}
			for _, r := range o.Refs {
				if s < int64(r.Start) && int64(r.End) <= stop {
					refs = append(refs, r)
				}
			}
			out.Chunks = append(out.Chunks, Chunk{Property: pi, Value: pb.Slice(int(s)+1, int(stop)), BodyStart: s + 1,
				References: refs})
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------------------------------- run_section

// SectionResult is run_section's (positions, end, refs, origin_vectors, err).
type SectionResult struct {
	Positions []uint32
	End       uint32
	Refs      []Ref
	Ovs       []Ovec
	Err       string // "" = None
}

// RunSection is wire_py_core.run_section_py (= v3_audit888.run_section): section reader vt at cursor cur of the
// multi-section record g (state rebuilt from the payload at cur; sec_fields of g's own section, if any).
func RunSection(d *Log, g *Ghost, vt uint32, cur int64, widths map[int]int64) (*SectionResult, error) {
	p, err := d.packet(g)
	if err != nil {
		return nil, err
	}
	pb := p.Bits()
	var o Out
	for _, resolve := range [2]bool{true, false} {
		// sec_fields keyed by the original record (82/40 ticket width); {} for a multi record (section_vt 0).
		fields, err := d.CreateFields(g)
		if err != nil {
			return nil, err
		}
		inp, err := wireInp(d, g, widths, vt, fields)
		if err != nil {
			return nil, err
		}
		inp.ResolveRefs = resolve
		if inp.Entry, err = SectionReader(vt); err != nil {
			return nil, err
		}
		wd := cur / 32
		w := pb.Slice(int(wd*32), int(wd*32+32)).String()
		cache, _ := strconv.ParseUint(w+strings.Repeat("0", 32-len(w)), 2, 64)
		inp.Buf, inp.NBits = p.Payload, p.Limit
		inp.State = &State{cur, int64(uint32(cache << (cur % 32))), wd*4 + 4, 0}
		if o, err = RunReader(inp); err != nil {
			return nil, err
		}
		if o.Err == "" && o.Flags == 0 {
			break
		}
	}
	res := &SectionResult{Positions: o.Pos, End: o.End, Refs: o.Refs, Ovs: o.Ovs, Err: o.Err}
	if res.Positions == nil {
		res.Positions = []uint32{}
	}
	switch end := int64(o.End); {
	case res.Err != "":
	case o.Flags != 0:
		res.Err = fmt.Sprintf("flags %d", o.Flags)
	case !(cur <= end && end <= p.Limit):
		res.Err = "bad end"
	}
	return res, nil
}

// ---------------------------------------------------------------------------------------------------- camera init

// CameraResult is the camera create chunk (v3_camera_create.convert): index 32, origin-zero re-encoded body.
type CameraResult struct{ Value bits.Bits }

// EncodePosition is v3_camera_create.encode_position: origin-zero C=5 re-encode, raw f32 fallback.
func EncodePosition(xyz [3]float64, table []int64) (string, error) {
	for _, x := range xyz {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return "", &ReaderError{"ValueError: non-finite camera position"}
		}
	}
	idxBits := mbits.Len(uint(len(table)))
	var ks [3]int64
	exact := true
	for i, x := range xyz {
		// |round(x*32)| >= 2^63 cannot fit any table width (q + 5 <= 63): Python's exact test passes but every width
		// fails -> the same raw fallback.
		k, err := pyc.RoundInt64(float64(x * 32))
		ks[i] = k
		exact = exact && err == nil && float64(k)/32 == x
	}
	if exact {
		for i, q := range table {
			width := q + 5
			fits := true
			for _, k := range ks {
				fits = fits && width >= 1 && width <= 63 && -(int64(1)<<(width-1)) <= k && k < int64(1)<<(width-1)
			}
			if fits {
				s := bits.FormatBin(uint64(i+1), idxBits)
				for _, k := range ks {
					s += bits.FormatBin(uint64(k)&(uint64(1)<<width-1), int(width))
				}
				return s, nil
			}
		}
	}
	s := strings.Repeat("0", idxBits)
	for _, x := range xyz {
		u, err := pyc.PackF32(x)
		if err != nil {
			return "", &ReaderError{"OverflowError: float too large to pack with f format"}
		}
		s += bits.FormatBin(uint64(u), 32)
	}
	return s, nil
}

// CameraInit is wire_py_core.camera_init_py (= v3_camera_create.convert): native create init 7EE800 over the camera
// create body -> origin-zero demo create chunk. widths nil = InferIDWidths(d).
func CameraInit(d *Log, g *Ghost, widths map[int]int64) (*CameraResult, error) {
	if g.Phase != 1 || g.SectionVT != CameraVT {
		return nil, &ReaderError{"ValueError: requires a camera create record"}
	}
	var err error
	if widths == nil {
		if widths, err = InferIDWidths(d); err != nil {
			return nil, err
		}
	}
	fields, err := d.CreateFields(g)
	if err != nil {
		return nil, err
	}
	inp, err := wireInp(d, g, widths, g.SectionVT, fields)
	if err != nil {
		return nil, err
	}
	inp.Entry = CameraInitVA
	p, err := d.packet(g)
	if err != nil {
		return nil, err
	}
	bs := p.Bits().Slice(int(g.CreateCur), int(g.Cursor))
	if bs.Len() <= 6 {
		return nil, &ReaderError{"IndexError: string index out of range"}
	}
	header := 7
	if bs.Slice(6, 7).String() == "1" {
		header += 12
	}
	body := bs.Slice(header, bs.Len())
	if body.Len() == 0 {
		return nil, &ReaderError{"ValueError: invalid literal for int() with base 2: ''"}
	}
	inp.Buf, inp.NBits, inp.State = body.Bytes(), int64(body.Len()), nil
	o, err := RunReader(inp)
	if err != nil {
		return nil, err
	}
	n := int64(body.Len())
	if o.Err != "" || o.Flags != 0 || int64(o.End) != n || len(o.Ovs) != 1 {
		errS := "None"
		if o.Err != "" {
			errS = o.Err
		}
		return nil, &ReaderError{fmt.Sprintf("ValueError: unsupported camera init: %s, flags=%d, end=%d/%d", errS, o.Flags, o.End, n)}
	}
	v := o.Ovs[0]
	if int64(v.End) != n-1 || body.Slice(-1, body.Len()).String() != "0" {
		return nil, &ReaderError{"ValueError: secondary transform or unknown camera init layout"}
	}
	c, err := d.config(g)
	if err != nil {
		return nil, err
	}
	enc, err := EncodePosition(v.XYZ, c.Tables[0])
	if err != nil {
		return nil, err
	}
	encB, err := bits.Parse(enc)
	if err != nil {
		return nil, err
	}
	res := bits.Concat(bs.Slice(0, header), body.Slice(0, int(v.Start)), encB, body.Slice(int(v.End), body.Len()))
	return &CameraResult{Value: res}, nil
}

// ---------------------------------------------------------------------------------------------------- AC38 rows

// Row is v3_types.row_of: one AC38 update record with its config.
type Row struct {
	Payload     []byte
	Limit       int64
	Cursor      int64
	Cache       uint32
	DwordOffset int64
	Child       []byte
	Tables      [2][]int64
	OriginBits  [3]uint32
}

// RowOf is wire_py_core.row_of (= v3_types.row_of).
func RowOf(d *Log, g *Ghost) (*Row, error) {
	p, err := d.packet(g)
	if err != nil {
		return nil, err
	}
	c, err := d.config(g)
	if err != nil {
		return nil, err
	}
	return &Row{p.Payload, p.Limit, g.Cursor, g.Cache, g.DwordOffset, c.ChildRaw, c.Tables, g.OriginBits}, nil
}

// plainInp is wire_py_core.plain_inp (= soldier_update_demo_test.fixture() after v3_types.slice_row set the row).
func plainInp(row *Row, vt uint32) (*Inp, error) {
	entry, err := SectionReader(vt)
	if err != nil {
		return nil, err
	}
	inp := NewInp()
	inp.Plain, inp.Entry, inp.SecVT, inp.Stubs = true, entry, vt, PlainStubs()
	if inp.ListMax, err = le32(row.Child, 4); err != nil {
		return nil, err
	}
	if inp.RefBits, err = le32(row.Child, 12); err != nil {
		return nil, err
	}
	inp.QN, inp.QT = int64(len(row.Tables[0])), row.Tables[0]
	inp.Origin = originOf(row.OriginBits)
	inp.ResolveRefs = false
	inp.Buf, inp.NBits = row.Payload, row.Limit
	inp.State = &State{row.Cursor, int64(row.Cache), row.DwordOffset, 0}
	return inp, nil
}

// RowResult is v3_types.slice_row's dict(positions, end, flags, err, calls) (calls '0x..', first 6).
type RowResult struct {
	Positions []uint32
	End       uint32
	Flags     uint32
	Err       string
	Calls     []uint32
}

// SliceRow is wire_py_core.slice_row_py (= v3_types.slice_row on the plain AC38 fixture).
func SliceRow(row *Row, vt uint32) (*RowResult, error) {
	inp, err := plainInp(row, vt)
	if err != nil {
		return nil, err
	}
	o, err := RunReader(inp)
	if err != nil {
		return nil, err
	}
	pos := o.Pos
	if pos == nil {
		pos = []uint32{}
	}
	return &RowResult{pos, o.End, o.Flags, o.Err, o.Calls[:min(len(o.Calls), 6)]}, nil
}

// AC38Chunk is one present AC38 property.
type AC38Chunk struct {
	Index int
	Value bits.Bits
}

// AC38Row is one ac38_rows entry dict(t, id, chunks {i: dict(value, bits)}, end).
type AC38Row struct {
	T      uint64
	ID     int64
	Chunks []AC38Chunk
	End    uint32
}

// AC38Rows is wire_py_core.ac38_rows_py without the pickle cache (Go recomputes in memory): every AC38 update record
// that slices cleanly, with its present property bodies.
func AC38Rows(d *Log) ([]AC38Row, error) {
	var out []AC38Row
	for _, g := range d.Ghosts {
		if g.Phase != 0 || g.SectionVT != AC38VT {
			continue
		}
		row, err := RowOf(d, g)
		if err != nil {
			return nil, err
		}
		r, err := SliceRow(row, AC38VT)
		if err != nil {
			return nil, err
		}
		if r.Err != "" || r.Flags != 0 {
			continue
		}
		pb := d.Packets[g.Pseq].Bits()
		pos := append(append([]uint32{}, r.Positions...), r.End)
		var ch []AC38Chunk
		for i := 0; i+1 < len(pos); i++ {
			if int(pos[i]) >= pb.Len() {
				return nil, &ReaderError{"IndexError: string index out of range"}
			}
			if pb.Slice(int(pos[i]), int(pos[i])+1).String() == "1" {
				ch = append(ch, AC38Chunk{i, pb.Slice(int(pos[i])+1, int(pos[i+1]))})
			}
		}
		out = append(out, AC38Row{g.T, g.ID, ch, r.End})
	}
	return out, nil
}

// ---------------------------------------------------------------------------------------------------- Python values

func bitsValue(b bits.Bits) any {
	if v, ok := b.Uint64(); ok {
		return v
	}
	return b.Big()
}

func refsValue(rs []Ref) any {
	if rs == nil {
		return nil
	}
	l := make([]any, len(rs))
	for i, r := range rs {
		o := pyc.NewObject()
		o.Set("start", r.Start)
		o.Set("end", r.End)
		o.Set("target", r.Target)
		l[i] = o
	}
	return l
}

func ovsValue(vs []Ovec) any {
	if vs == nil {
		return nil
	}
	l := make([]any, len(vs))
	for i, v := range vs {
		o := pyc.NewObject()
		o.Set("start", v.Start)
		o.Set("end", v.End)
		o.Set("xyz", []any{v.XYZ[0], v.XYZ[1], v.XYZ[2]})
		l[i] = o
	}
	return l
}

func u32List(l []uint32) []any {
	out := make([]any, len(l))
	for i, v := range l {
		out[i] = v
	}
	return out
}

func strOrNone(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// PyValue is the slice_record dict in Python key order (pyc.Valuer).
func (s *SliceResult) PyValue() any {
	o := pyc.NewObject()
	g := s.G
	o.Set("id", g.ID)
	o.Set("phase", g.Phase)
	o.Set("t", g.T)
	o.Set("pseq", g.Pseq)
	o.Set("section_vt", fmt.Sprintf("%#x", g.SectionVT))
	o.Set("cursor", g.Cursor)
	o.Set("end", s.End)
	o.Set("properties", s.Properties)
	o.Set("flags", s.Flags)
	o.Set("error", strOrNone(s.Error))
	o.Set("player_fixture", s.PlayerFixture)
	o.Set("resolved_refs", s.ResolvedRefs)
	cf := pyc.NewObject()
	for _, k := range sortedKeys(s.CreateFields) {
		cf.Set(fmt.Sprintf("%#x", k), s.CreateFields[k])
	}
	o.Set("create_fields", cf)
	o.Set("reference_reads", refsValue(s.ReferenceReads))
	o.Set("origin_vectors", ovsValue(s.OriginVectors))
	ch := make([]any, len(s.Chunks))
	for i, c := range s.Chunks {
		co := pyc.NewObject()
		co.Set("property", c.Property)
		co.Set("bits", c.Value.Len())
		co.Set("value", bitsValue(c.Value))
		co.Set("body_start", c.BodyStart)
		co.Set("references", refsValue(c.References))
		ch[i] = co
	}
	o.Set("chunks", ch)
	return o
}

// sortedKeys orders create_fields keys as the Python dict ({0x50, 0x54} insertion order = ascending).
func sortedKeys(m map[uint32]int64) []uint32 {
	var ks []uint32
	for k := range m {
		ks = append(ks, k)
	}
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j] < ks[j-1]; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	return ks
}

// PyValue is run_section's 5-tuple as a list.
func (s *SectionResult) PyValue() any {
	return []any{u32List(s.Positions), s.End, refsValue(s.Refs), ovsValue(s.Ovs), strOrNone(s.Err)}
}

// PyValue is dict(index=32, bits, value, refs=[]).
func (c *CameraResult) PyValue() any {
	o := pyc.NewObject()
	o.Set("index", 32)
	o.Set("bits", c.Value.Len())
	o.Set("value", bitsValue(c.Value))
	o.Set("refs", []any{})
	return o
}

// PyValue is slice_row's dict(positions, end, flags, err, calls).
func (r *RowResult) PyValue() any {
	o := pyc.NewObject()
	o.Set("positions", u32List(r.Positions))
	o.Set("end", r.End)
	o.Set("flags", r.Flags)
	o.Set("err", strOrNone(r.Err))
	calls := make([]any, len(r.Calls))
	for i, c := range r.Calls {
		calls[i] = fmt.Sprintf("%#x", c)
	}
	o.Set("calls", calls)
	return o
}
