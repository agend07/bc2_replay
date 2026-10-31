package events

// Port of v3_wire_companions (r143/r154/r211/r269): template / template_from_pack, build, wire_events, and the
// v3_cl04c _fo closure (FrameOf). encode / write are fbgd.EncodeCompanion; selfcheck / resets are maintainer tools,
// not ported.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/pyc"
)

const (
	// Key is v3_wire_companions.KEY (= fbgd.CompanionKey): a key record before delta L when L % 450 == 0.
	Key = fbgd.CompanionKey
	// BurstGap is BURST_GAP (r211): idle deltas that end the level-start burst.
	BurstGap = 10
)

// Exts is v3_wire_companions.EXTS.
var Exts = fbgd.CompanionExts

// ElKey is a composite state key (entity id, element).
type ElKey struct{ I, El int64 }

// CompState is a composite state dict {(id, element): value} with Python insertion order.
type CompState struct {
	Keys []ElKey
	M    map[ElKey]int64
}

// Set is d[k] = v (an existing key keeps its place).
func (c *CompState) Set(k ElKey, v int64) {
	if c.M == nil {
		c.M = map[ElKey]int64{}
	}
	if _, ok := c.M[k]; !ok {
		c.Keys = append(c.Keys, k)
	}
	c.M[k] = v
}

// GroupSet is a set of destroyed part indices of one group.
type GroupSet map[int64]struct{}

// Template is template() / template_from_pack(): the map's companion headers, config, level-start state and
// impact key from a same-map companion set.
type Template struct {
	Cfg     *Config
	Prolog  []byte
	Hdr     map[string]string
	RecExts []string // the exts of Recs in Python order (fbcdd, fbgdd[, fbidd])
	Recs    map[string][]Rec
	DT      int64
	Comp0   *CompState
	Grp0    []GroupSet
	Burst   int
	ImpKey  string
}

// ckeyDec is ckey_dec: a composite key record -> state.
func ckeyDec(s string, E [][2]int64) (*CompState, error) {
	b, st := &bitReader{b: s}, &CompState{M: map[ElKey]int64{}}
	for i, av := range E {
		vw := bitLength(av[1] - 1)
		for el := range av[0] {
			pr, err := b.r(1)
			if err != nil {
				return nil, err
			}
			if pr == 1 {
				v, err := b.r(vw)
				if err != nil {
					return nil, err
				}
				st.Set(ElKey{int64(i), el}, int64(v))
			}
		}
	}
	if b.p != len(s) {
		return nil, errors.New("AssertionError: composite key not fully read")
	}
	return st, nil
}

// gkeyDec is gkey_dec: a group key record -> one set per group.
func gkeyDec(s string, G []int64) ([]GroupSet, error) {
	b := &bitReader{b: s}
	var e error
	rd := func(k int) uint64 {
		if e != nil {
			return 0
		}
		x, err := b.r(k)
		e = err
		return x
	}
	st := make([]GroupSet, 0, len(G))
	for _, size := range G {
		n := rd(int(rd(5)) + 1)
		nb := rd(int(rd(6)) + 1)
		var words []uint64
		for k := uint64(0); e == nil && k < (nb+31)/32; k++ {
			words = append(words, rd(32))
		}
		if e != nil {
			return nil, e
		}
		set := GroupSet{}
		for j := range nb {
			if words[j/32]>>(j%32)&1 == 1 {
				set[int64(j)] = struct{}{}
			}
		}
		if int64(nb) != size || n != uint64(len(set)) {
			return nil, errors.New("AssertionError: group key size / count")
		}
		st = append(st, set)
	}
	if b.p != len(s) {
		return nil, errors.New("AssertionError: group key not fully read")
	}
	return st, nil
}

// ckeyEnc is ckey_enc: per entity, per element a present bit and the value.
func ckeyEnc(st map[ElKey]int64, E [][2]int64) string {
	var sb strings.Builder
	for i, av := range E {
		vw := bitLength(av[1] - 1)
		for el := range av[0] {
			x, ok := st[ElKey{int64(i), el}]
			if !ok {
				sb.WriteByte('0')
				continue
			}
			sb.WriteByte('1')
			if vw > 0 {
				fmtBin(&sb, uint64(x), vw)
			}
		}
	}
	return sb.String()
}

// cdeltaEnc is cdelta_enc(dt, ev, E, idw): ids and elements ascending.
func cdeltaEnc(dt int64, ev map[ElKey]int64, E [][2]int64, idw int) (string, error) {
	keys := make([]ElKey, 0, len(ev))
	for k := range ev {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b ElKey) int {
		if a.I != b.I {
			return cmpInt(a.I, b.I)
		}
		return cmpInt(a.El, b.El)
	})
	var ids []int64 // by: ids in order, each with its (element, value) list
	by := map[int64][]ElKey{}
	for _, k := range keys {
		if _, ok := by[k.I]; !ok {
			ids = append(ids, k.I)
		}
		by[k.I] = append(by[k.I], k)
	}
	var sb strings.Builder
	fmtBin(&sb, uint64(dt), 31)
	fmtBin(&sb, uint64(len(ids)), 32)
	f := func(x uint64, w int) {
		if w > 0 {
			fmtBin(&sb, x, w)
		}
	}
	for _, i := range ids {
		if i < 0 || i >= int64(len(E)) {
			return "", fmt.Errorf("KeyError: %d", i)
		}
		iw, vw := bitLength(E[i][0]-1), bitLength(E[i][1]-1)
		els := by[i]
		f(uint64(i), idw)
		f(uint64(len(els)-1), iw)
		for _, k := range els {
			f(uint64(k.El), iw)
			f(uint64(ev[k]), vw)
		}
	}
	return sb.String(), nil
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// gkeyEnc is gkey_enc: per group u5 w-1, popcount[w], then the bitset (u6 w2-1, size[w2], ceil(size/32) x u32).
func gkeyEnc(st []GroupSet, G []int64) (string, error) {
	var sb strings.Builder
	for g, size := range G {
		n := int64(len(st[g]))
		w, w2 := max(1, bitLength(n)), max(1, bitLength(size))
		words := make([]uint64, (size+31)/32)
		for j := range st[g] {
			if j < 0 || j/32 >= int64(len(words)) {
				return "", fmt.Errorf("IndexError: group %d part %d of %d", g, j, size)
			}
			words[j/32] |= 1 << (j % 32)
		}
		fmtBin(&sb, uint64(w-1), 5)
		fmtBin(&sb, uint64(n), w)
		fmtBin(&sb, uint64(w2-1), 6)
		fmtBin(&sb, uint64(size), w2)
		for _, x := range words {
			fmtBin(&sb, x, 32)
		}
	}
	return sb.String(), nil
}

// gdeltaEnc is gdelta_enc(dt, ev, G).
func gdeltaEnc(dt int64, ev map[int64]GroupSet, G []int64) string {
	var sb strings.Builder
	fmtBin(&sb, uint64(dt), 31)
	for g := range int64(len(G)) {
		s := ev[g]
		if len(s) == 0 {
			sb.WriteByte('0')
			continue
		}
		ix := make([]int64, 0, len(s))
		for j := range s {
			ix = append(ix, j)
		}
		slices.Sort(ix)
		sb.WriteByte('1')
		fmtBin(&sb, uint64(len(ix)), 32)
		for _, j := range ix {
			fmtBin(&sb, uint64(j), 32)
		}
	}
	return sb.String()
}

// TemplateFiles is template(base): the template from a same-map 9-file companion set (base = path without ext).
func TemplateFiles(base string) (*Template, error) {
	hdr, recs := map[string]string{}, map[string][]Rec{}
	for _, ext := range Exts {
		h, r, err := Records(base + "." + ext)
		if err != nil {
			return nil, err
		}
		hdr[ext], recs[ext] = h, r
	}
	raw, err := os.ReadFile(base + ".fbcdd")
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, r := range recs["fbidd"] {
		if r.M == 0 {
			keys = append(keys, r.S)
		}
	}
	if len(keys) < 2 {
		return nil, errors.New("IndexError: fbidd has fewer than 2 keys")
	}
	return newTemplate(slice(raw, 0, 40), hdr, recs, Exts[:], keys[1])
}

// HexBits is a map pack [bits, hex] pair.
type HexBits struct {
	N   int
	Hex string
}

// UnmarshalJSON reads [n, "hex"].
func (h *HexBits) UnmarshalJSON(b []byte) error {
	var a []json.RawMessage
	if err := json.Unmarshal(b, &a); err != nil || len(a) != 2 {
		return fmt.Errorf("map pack: want [bits, hex], got %.40s", b)
	}
	if err := json.Unmarshal(a[0], &h.N); err != nil {
		return err
	}
	return json.Unmarshal(a[1], &h.Hex)
}

// unhex is unhex(x): the first N bits of bytes.fromhex(hex).
func (h HexBits) unhex() (string, error) {
	p, err := hex.DecodeString(h.Hex)
	if err != nil {
		return "", err
	}
	return readBits(p, h.N), nil
}

// PackEntry is the part of a mappack.json set entry the companions read (txt and creates are left to others).
type PackEntry struct {
	Prolog string             `json:"prolog"`
	Hdr    map[string]HexBits `json:"hdr"`
	Recs   map[string]struct {
		Key0   HexBits   `json:"key0"`
		Deltas []HexBits `json:"deltas"`
	} `json:"recs"`
	ImpKey HexBits `json:"imp_key"`
}

// LoadPackEntry reads the set stem of a mappack.json (format 1); nil when the stem is not in the pack.
func LoadPackEntry(path, stem string) (*PackEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p struct {
		Format int                        `json:"format"`
		Sets   map[string]json.RawMessage `json:"sets"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if p.Format != 1 {
		return nil, fmt.Errorf("%s: map pack format %d, want 1", path, p.Format)
	}
	raw, ok := p.Sets[stem]
	if !ok {
		return nil, nil
	}
	e := &PackEntry{}
	return e, json.Unmarshal(raw, e)
}

// TemplateFromPack is template_from_pack(entry) (r269): equal to template(SRC) except Recs (truncated).
func TemplateFromPack(e *PackEntry) (*Template, error) {
	recs := map[string][]Rec{}
	for _, ext := range Exts[:2] {
		r, ok := e.Recs[ext]
		if !ok {
			return nil, fmt.Errorf("KeyError: %q", ext)
		}
		k0, err := r.Key0.unhex()
		if err != nil {
			return nil, err
		}
		rs := []Rec{{0, k0}}
		for _, x := range r.Deltas {
			s, err := x.unhex()
			if err != nil {
				return nil, err
			}
			rs = append(rs, Rec{1, s})
		}
		recs[ext] = rs
	}
	hdr := map[string]string{}
	for _, ext := range Exts {
		h, ok := e.Hdr[ext]
		if !ok {
			return nil, fmt.Errorf("KeyError: %q", ext)
		}
		s, err := h.unhex()
		if err != nil {
			return nil, err
		}
		hdr[ext] = s
	}
	prolog, err := hex.DecodeString(e.Prolog)
	if err != nil {
		return nil, err
	}
	imp, err := e.ImpKey.unhex()
	if err != nil {
		return nil, err
	}
	return newTemplate(prolog, hdr, recs, Exts[:2], imp)
}

// newTemplate is _template(prolog, hdr, recs, imp_key): config, dt, key0 state plus the level-start burst (key0 +
// the active deltas up to the first run of > BURST_GAP idle deltas, composite toggles (1, 2) ignored).
func newTemplate(prolog []byte, hdr map[string]string, recs map[string][]Rec, exts []string, impKey string) (*Template, error) {
	cfg, err := ConfigBits(hdr["fbcdd"], hdr["fbgdd"], hdr["fbidd"])
	if err != nil {
		return nil, err
	}
	t := &Template{Cfg: cfg, Prolog: prolog, Hdr: hdr, RecExts: exts, Recs: recs, ImpKey: impKey}
	first := func(ext string, m byte) (string, error) {
		for _, r := range recs[ext] {
			if r.M == m {
				return r.S, nil
			}
		}
		return "", fmt.Errorf("%s: no record with marker %d", ext, m)
	}
	d0, err := first("fbcdd", 1)
	if err != nil {
		return nil, err
	}
	dt, err := (&bitReader{b: d0}).r(31)
	if err != nil {
		return nil, err
	}
	t.DT = int64(dt)
	E := cfg.Composite.Entries
	k0, err := first("fbcdd", 0)
	if err != nil {
		return nil, err
	}
	if t.Comp0, err = ckeyDec(k0, E); err != nil {
		return nil, err
	}
	g0, err := first("fbgdd", 0)
	if err != nil {
		return nil, err
	}
	if t.Grp0, err = gkeyDec(g0, cfg.Group.Entries); err != nil {
		return nil, err
	}
	cc, gc, err := companionRecs(recs["fbcdd"], recs["fbgdd"], cfg)
	if err != nil {
		return nil, err
	}
	for L := range min(len(cc), Key) {
		if L-t.Burst > BurstGap {
			break
		}
		if L >= len(gc) {
			return nil, fmt.Errorf("IndexError: group delta %d of %d", L, len(gc))
		}
		active := len(gc[L]) > 0 || slices.ContainsFunc(cc[L], func(e CompEvent) bool { return E[e.I] != [2]int64{1, 2} })
		if active {
			t.Burst = L + 1
		}
	}
	for L := range t.Burst {
		for _, e := range cc[L] {
			t.Comp0.Set(ElKey{e.I, e.El}, e.V)
		}
		for _, e := range gc[L] {
			t.Grp0[e.G][e.J] = struct{}{}
		}
	}
	return t, nil
}

// Events is one destination of wire_events: (composite events, group events).
type Events struct {
	Comp []CompEvent
	Grp  []GroupEvent
}

// Deltas is wire_events' deltas dict {L: (comp, grp)} with Python insertion order.
type Deltas struct {
	Keys []int
	M    map[int]*Events
}

// FrameOf is the v3_cl04c _fo closure: a wire time -> delta index L (-1 = key0), through the same time transforms
// as the rows (JOINKEY fold into t0, SHIFTF level-load shift); a key bin goes to the next delta.
type FrameOf struct {
	t0, jk     float64
	sh         int64
	join, shft bool
}

// NewFrameOf builds _fo for t0 and the JOINKEY / SHIFTF recipe values ("" = off, else an int, like E() + int()).
func NewFrameOf(t0 float64, joinKey, shiftF string) (*FrameOf, error) {
	dt := fbgd.DTTicks // a variable: Python rounds DT_TICKS first, then multiplies
	f := &FrameOf{t0: t0}
	if joinKey != "" {
		n, err := strconv.Atoi(strings.TrimSpace(joinKey))
		if err != nil {
			return nil, fmt.Errorf("JOINKEY: %w", err)
		}
		f.join, f.jk = true, t0+float64(float64(n)*dt)
	}
	if shiftF != "" {
		n, err := strconv.Atoi(strings.TrimSpace(shiftF))
		if err != nil {
			return nil, fmt.Errorf("SHIFTF: %w", err)
		}
		f.shft, f.sh = true, int64(float64(n)*dt)
	}
	return f, nil
}

// Of is _fo(t).
func (f *FrameOf) Of(t uint64) (int, error) {
	dt := fbgd.DTTicks
	x := float64(t) // < 2**53: exact, so int / float comparisons and sums match Python's
	if f.join && f.t0 < x && x < f.jk {
		x = f.t0
	}
	if f.shft && x > f.t0+float64(20*dt) {
		x += float64(f.sh)
	}
	q, err := pyc.FloatFloorDiv(x-f.t0, dt)
	if err != nil {
		return 0, err
	}
	fr := int(q)
	if fr <= 0 {
		return -1, nil
	}
	r := fr - fr/fbgd.KeyEvery
	if fr%fbgd.KeyEvery != 0 {
		r--
	}
	return r, nil
}

// WireEvents is wire_events(d, cfg, frame_of): the destruction stream events per delta index, packets in pseq order.
// It also returns Wire's rows (the destruction.wire call it makes).
func WireEvents(d *capture.Decoded, cfg *Config, frameOf func(uint64) (int, error)) (Events, *Deltas, []WireRow, error) {
	key0 := Events{Comp: []CompEvent{}, Grp: []GroupEvent{}}
	deltas := &Deltas{M: map[int]*Events{}}
	rows, err := Wire(d, cfg)
	if err != nil {
		return key0, nil, nil, err
	}
	for _, r := range rows {
		L, err := frameOf(r.T)
		if err != nil {
			return key0, nil, nil, err
		}
		dst := &key0
		if L >= 0 {
			if dst = deltas.M[L]; dst == nil {
				dst = &Events{Comp: []CompEvent{}, Grp: []GroupEvent{}}
				deltas.M[L] = dst
				deltas.Keys = append(deltas.Keys, L)
			}
		}
		dst.Comp = append(dst.Comp, r.Comp...)
		dst.Grp = append(dst.Grp, r.Grp...)
	}
	return key0, deltas, rows, nil
}

// Build is build(t, key0, deltas, n): the three companions' records over n delta frames ({ext: [(marker, bits)]}):
// key0 events applied first, a key every Key deltas, composite re-sets kept (r154), group parts already set skipped.
func Build(t *Template, key0 Events, deltas *Deltas, n int) (map[string][]Rec, error) {
	E, G, idw := t.Cfg.Composite.Entries, t.Cfg.Group.Entries, t.Cfg.Composite.IDWidth
	comp := make(map[ElKey]int64, len(t.Comp0.M))
	for k, v := range t.Comp0.M {
		comp[k] = v
	}
	grp := make([]GroupSet, len(t.Grp0))
	for g, s := range t.Grp0 {
		grp[g] = make(GroupSet, len(s))
		for j := range s {
			grp[g][j] = struct{}{}
		}
	}
	for _, e := range key0.Comp {
		comp[ElKey{e.I, e.El}] = e.V
	}
	for _, e := range key0.Grp {
		if e.G < 0 || e.G >= int64(len(grp)) {
			return nil, fmt.Errorf("IndexError: group %d", e.G)
		}
		grp[e.G][e.J] = struct{}{}
	}
	rc, rg, ri := make([]Rec, 0, n+n/Key+1), make([]Rec, 0, n+n/Key+1), make([]Rec, 0, n+n/Key+1)
	var sb strings.Builder
	fmtBin(&sb, uint64(t.DT), 31)
	sb.WriteByte('0')
	idle := sb.String()
	for L := range n {
		if L%Key == 0 {
			ge, err := gkeyEnc(grp, G)
			if err != nil {
				return nil, err
			}
			rc = append(rc, Rec{0, ckeyEnc(comp, E)})
			rg = append(rg, Rec{0, ge})
			ri = append(ri, Rec{0, t.ImpKey})
		}
		ce, ge := map[ElKey]int64{}, map[int64]GroupSet{}
		if ev := deltas.M[L]; ev != nil {
			for _, e := range ev.Comp {
				ce[ElKey{e.I, e.El}] = e.V
			}
			for _, e := range ev.Grp {
				if e.G < 0 || e.G >= int64(len(grp)) {
					return nil, fmt.Errorf("IndexError: group %d", e.G)
				}
				if _, ok := grp[e.G][e.J]; !ok { // r154: a part already destroyed must not be set again
					if ge[e.G] == nil {
						ge[e.G] = GroupSet{}
					}
					ge[e.G][e.J] = struct{}{}
				}
			}
		}
		for k, v := range ce {
			comp[k] = v
		}
		for g, s := range ge {
			for j := range s {
				grp[g][j] = struct{}{}
			}
		}
		cd, err := cdeltaEnc(t.DT, ce, E, idw)
		if err != nil {
			return nil, err
		}
		rc = append(rc, Rec{1, cd})
		rg = append(rg, Rec{1, gdeltaEnc(t.DT, ge, G)})
		ri = append(ri, Rec{1, idle})
	}
	return map[string][]Rec{"fbcdd": rc, "fbgdd": rg, "fbidd": ri}, nil
}

// File is one written companion: the data file and its index (ext[:-2] + "dk").
type File struct {
	Ext       string
	Data, Idx []byte
}

// Encode is write(t, recs, out) without the file writes: each companion and its index, in Exts order.
func Encode(t *Template, recs map[string][]Rec) ([]File, error) {
	out := make([]File, 0, len(Exts))
	for _, ext := range Exts {
		h, err := bits.Parse(t.Hdr[ext])
		if err != nil {
			return nil, err
		}
		rs := make([]fbgd.CompRec, len(recs[ext]))
		for i, r := range recs[ext] {
			b, err := bits.Parse(r.S)
			if err != nil {
				return nil, err
			}
			rs[i] = fbgd.CompRec{Marker: r.M, Bits: b}
		}
		data, idx := fbgd.EncodeCompanion(t.Prolog, h, rs)
		out = append(out, File{ext, data, idx})
	}
	return out, nil
}
