package events

// Port of v3_wire_destruction (r142/r269) and the impact part (manager 0) of argon_destruction_offline.read_manager:
// the destruction stream that precedes the ghost block of a logged client packet, and the companion map config.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"slices"

	"bc2replay/builder/internal/capture"
)

// preambleLens is v3_wire_deletes.PREAMBLE (r140 observed preamble lengths; ghost_starts still uses them).
var preambleLens = [3]int{3, 33, 39}

// ImpactConfig is manager 0's map config (cfg[0]).
type ImpactConfig struct {
	MaxIndex       int64
	IDWidth        int
	VectorWidths   []int
	FractionBits   int
	MagnitudeWidth int
	RawVectorMode  int
}

// CompositeConfig is manager 1's (cfg[1]): id width and per entity (element count, value count), ids 0..N-1.
type CompositeConfig struct {
	IDWidth int
	Entries [][2]int64
}

// GroupConfig is manager 2's (cfg[2]): id width and per group its part count.
type GroupConfig struct {
	IDWidth int
	Entries []int64
}

// Config is config_bits(): {0: impact, 1: composite, 2: group}.
type Config struct {
	Impact    ImpactConfig
	Composite CompositeConfig
	Group     GroupConfig
}

// CompEvent is a composite event (entity id, element, value).
type CompEvent struct{ I, El, V int64 }

// GroupEvent is a group event (group, part index).
type GroupEvent struct{ G, J int64 }

// Rec is a companion record (marker 0 key / 1 delta, bit string).
type Rec struct {
	M byte
	S string
}

// readBits is records()' tobits(raw, k): the bit string of raw cut to k bits.
func readBits(raw []byte, k int) string {
	return string(slice(payloadBits(raw), 0, k))
}

// Records is v3_wire_destruction.records(path): a companion file -> (header bits, [(marker, bits)]) (r141 layout).
func Records(path string) (string, []Rec, error) {
	d, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	le := binary.LittleEndian
	if len(d) < 48 {
		return "", nil, fmt.Errorf("%s: struct.error: short companion header", path)
	}
	n, nb := int(le.Uint32(d[40:])), int(le.Uint32(d[44:]))
	hdr := readBits(slice(d, 48, 48+n), nb)
	var out []Rec
	p := 48 + n
	for p < len(d) && d[p] != 2 {
		if p+9 > len(d) {
			return "", nil, fmt.Errorf("%s: struct.error: short record header at %d", path, p)
		}
		sz, nb := int(le.Uint32(d[p+1:])), int(le.Uint32(d[p+5:]))
		out = append(out, Rec{d[p], readBits(slice(d, p+9, p+9+sz), nb)})
		p += 9 + sz
	}
	return hdr, out, nil
}

// ConfigBits is config_bits(hc, hg, hi): the map config from the three companion header bit strings.
func ConfigBits(hc, hg, hi string) (*Config, error) {
	cfg := &Config{}
	h := &bitReader{b: hc}
	N, err := h.r(32)
	if err != nil {
		return nil, err
	}
	for range N {
		var v [2]int64
		for k := range 2 { // (h.r(h.r(5)) + 1, h.r(h.r(5)) + 1), left to right
			w, err := h.r(5)
			if err != nil {
				return nil, err
			}
			x, err := h.r(int(w))
			if err != nil {
				return nil, err
			}
			v[k] = int64(x) + 1
		}
		cfg.Composite.Entries = append(cfg.Composite.Entries, v)
	}
	if h.p != len(h.b) {
		return nil, errors.New("AssertionError: composite header not fully read")
	}
	cfg.Composite.IDWidth = bitLength(int64(N) - 1)
	g := &bitReader{b: hg}
	ng, err := g.r(32)
	if err != nil {
		return nil, err
	}
	cfg.Group.Entries = []int64{}
	for range ng {
		x, err := g.r(32)
		if err != nil {
			return nil, err
		}
		cfg.Group.Entries = append(cfg.Group.Entries, int64(x))
	}
	cfg.Group.IDWidth = max(1, bitLength(int64(len(cfg.Group.Entries))-1))
	i := &bitReader{b: hi}
	var e error
	rd := func(k int) int64 {
		if e != nil {
			return 0
		}
		x, err := i.r(k)
		e = err
		return int64(x)
	}
	frac := rd(5)
	vw := []int{}
	for range rd(5) {
		vw = append(vw, int(rd(5)))
	}
	mag := rd(5)
	rd(31)
	M := rd(32)
	if e != nil {
		return nil, e
	}
	cfg.Impact = ImpactConfig{MaxIndex: M - 1, IDWidth: max(1, bitLength(M-1)), VectorWidths: vw, FractionBits: int(frac),
		MagnitudeWidth: int(mag)}
	return cfg, nil
}

// readImpact is read_manager(0, fld, cfg) with fld = a plain read (values discarded): the impact manager body
// (BFF800 -> BFBAA0). The config is always complete here (config_bits), so the LookupError path cannot occur.
func readImpact(b *bitReader, c *ImpactConfig) error {
	var e error
	rd := func(k int) uint64 {
		if e != nil {
			return 0
		}
		x, err := b.r(k)
		e = err
		return x
	}
	width := func(v int) (int, error) {
		if v < 0 || v > 32 {
			return 0, errors.New("ValueError: invalid manager 0 width")
		}
		return v, nil
	}
	dense := rd(1) == 1
	limit, hasLimit := c.MaxIndex+1, dense
	for entry := int64(0); e == nil && (!hasLimit || entry < limit); entry++ {
		active := rd(1) == 1
		if e != nil {
			break
		}
		if !active && !dense {
			break
		}
		if !active {
			continue
		}
		hasLimit = true // BFF865 bounds both modes
		if !dense {
			w, err := width(c.IDWidth)
			if err != nil {
				return err
			}
			rd(w)
		}
		for e == nil && rd(1) == 1 {
			n := 32
			if c.RawVectorMode == 0 {
				table := c.VectorWidths
				sel := int(rd(bitLength(int64(len(table)))))
				if e != nil {
					break
				}
				if sel > len(table) {
					return errors.New("ValueError: invalid impact vector selector")
				}
				if sel > 0 {
					f, err := width(c.FractionBits)
					if err != nil {
						return err
					}
					n = table[sel-1] + f
				}
			}
			for range 3 {
				rd(n)
			}
			mag, err := width(c.MagnitudeWidth)
			if err != nil {
				return err
			}
			if mag < 24 {
				rd(mag)
			} else if rd(1) == 0 {
				rd(23)
			}
		}
	}
	return e
}

// compBody is comp_body (C0BAB0/C09A70): [(id, element, value)].
func compBody(b *bitReader, c *CompositeConfig) ([]CompEvent, error) {
	out := []CompEvent{}
	cnt, err := b.r(5)
	if err != nil {
		return nil, err
	}
	for range cnt + 1 {
		iu, err := b.r(c.IDWidth)
		if err != nil {
			return nil, err
		}
		i := int64(iu)
		if i >= int64(len(c.Entries)) {
			return nil, fmt.Errorf("KeyError: %d", i)
		}
		a, v := c.Entries[i][0], c.Entries[i][1]
		iw, vw := bitLength(a-1), bitLength(v-1)
		dense, err := b.r(1)
		if err != nil {
			return nil, err
		}
		if dense == 1 {
			for el := range a {
				pr, err := b.r(1)
				if err != nil {
					return nil, err
				}
				if pr == 1 {
					x, err := b.r(vw)
					if err != nil {
						return nil, err
					}
					out = append(out, CompEvent{i, el, int64(x)})
				}
			}
			continue
		}
		for {
			pr, err := b.r(1)
			if err != nil {
				return nil, err
			}
			if pr == 0 {
				break
			}
			el, err := b.r(iw)
			if err != nil {
				return nil, err
			}
			x, err := b.r(vw)
			if err != nil {
				return nil, err
			}
			out = append(out, CompEvent{i, int64(el), int64(x)})
		}
	}
	return out, nil
}

// groupBody is group_body (5D67A0/5D0F00): [(group, index)].
func groupBody(b *bitReader, c *GroupConfig) ([]GroupEvent, error) {
	out := []GroupEvent{}
	w := c.IDWidth
	cnt, err := b.r(w)
	if err != nil {
		return nil, err
	}
	for range cnt + 1 {
		g, err := b.r(w)
		if err != nil {
			return nil, err
		}
		if g >= uint64(len(c.Entries)) {
			return nil, fmt.Errorf("KeyError: %d", g)
		}
		iw := bitLength(c.Entries[g] - 1)
		for {
			pr, err := b.r(1)
			if err != nil {
				return nil, err
			}
			if pr == 0 {
				break
			}
			j, err := b.r(iw)
			if err != nil {
				return nil, err
			}
			out = append(out, GroupEvent{int64(g), int64(j)})
		}
	}
	return out, nil
}

// ghostStarts is ghost_starts(d): packet -> ghost block start q (Python: (q, payload bits)) when exactly one q
// closes the block (deletes' chain search, preamble lengths PREAMBLE).
func ghostStarts(d *capture.Decoded, width int) (map[uint32]int, error) {
	ent, _ := entryCursors(d)
	out := map[uint32]int{}
	var buf []byte
	for _, s := range d.PacketSeqs {
		es, ok := ent[s]
		if !ok {
			continue
		}
		buf = appendBits(buf, d.Packets[s].Payload)
		E, n, b := slices.Min(es), len(es), buf
		bit := func(i int) (byte, error) {
			if i >= len(b) {
				return 0, fmt.Errorf("IndexError: pseq %d: bit %d of %d", s, i, len(b))
			}
			return b[i], nil
		}
		qs := -1
		nq := 0
		for q := 0; q < E-32-width; q++ {
			bq, err := bit(q)
			if err != nil {
				return nil, err
			}
			if bq != '1' {
				continue
			}
			cu, err := intOf(b, q+32, q+32+width)
			if err != nil {
				return nil, err
			}
			c, x, k := int(cu), q+32+width, 0
			for x < E {
				bx, err := bit(x)
				if err != nil {
					return nil, err
				}
				if bx != '1' || k > c {
					break
				}
				k++
				x += 1 + width
			}
			if x < E {
				bx, err := bit(x)
				if err != nil {
					return nil, err
				}
				if bx == '0' && c-k == n && slices.Contains(preambleLens[:], E-x-1) {
					qs = q // each q is visited once: the set holds distinct qs
					nq++
				}
			}
		}
		if nq == 1 {
			out[s] = qs
		}
	}
	return out, nil
}

// WireRow is one packet's destruction stream: dict(t, start, end, impact_bits, comp, grp).
type WireRow struct {
	Pseq       uint32
	T          uint64
	Start, End int
	ImpactBits int
	Comp       []CompEvent
	Grp        []GroupEvent
}

// noStream is r294: packet header bit 4 clear = the packet carries no destruction stream (the ghost block follows
// the header directly; no events). Seen only after a map rotation on one connection (cap_20261031_002547_52296
// round 2, MP_007: 1,062 of 11,462 packets, all in the round's first 36 s, then every packet carries the stream
// again); every other corpus packet has the bit set and a stream (the frozen Python asserts the float31 match).
func noStream(payloadBits []byte) bool { return len(payloadBits) > 4 && payloadBits[4] == '0' }

// Wire is v3_wire_destruction.wire(d, cfg): the destruction stream of every packet whose ghost block start is
// unambiguous, in pseq order, packets without one (noStream) skipped. A stream that does not close exactly at the
// ghost block is an error (Python asserts).
func Wire(d *capture.Decoded, cfg *Config) ([]WireRow, error) {
	gs, err := ghostStarts(d, DeleteWidth)
	if err != nil {
		return nil, err
	}
	seqs := make([]uint32, 0, len(gs))
	for s := range gs {
		seqs = append(seqs, s)
	}
	slices.Sort(seqs)
	out := make([]WireRow, 0, len(seqs))
	for _, s := range seqs {
		q, b := gs[s], payloadBits(d.Packets[s].Payload)
		if noStream(b) {
			continue
		}
		f := slice(b, q+1, q+32)
		var pos []int
		for i := 7; i < q-31; i++ {
			if bytes.Equal(slice(b, i, i+31), f) {
				pos = append(pos, i)
			}
		}
		if len(pos) != 1 || pos[0] < 2 || string(b[pos[0]-2:pos[0]]) != "01" {
			return nil, fmt.Errorf("AssertionError: destruction stream of pseq %d: float31 found at %v", s, pos)
		}
		r := &bitReader{b: string(b[:q]), p: pos[0] + 31}
		var pres [3]uint64
		for k := range pres {
			if pres[k], err = r.r(1); err != nil {
				return nil, fmt.Errorf("pseq %d: %w", s, err)
			}
		}
		row := WireRow{Pseq: s, T: d.Packets[s].T, Start: pos[0] - 2, End: q, Comp: []CompEvent{}, Grp: []GroupEvent{}}
		if pres[0] == 1 {
			p0 := r.p
			if err := readImpact(r, &cfg.Impact); err != nil {
				return nil, fmt.Errorf("pseq %d: impact: %w", s, err)
			}
			row.ImpactBits = r.p - p0
		}
		if pres[1] == 1 {
			if row.Comp, err = compBody(r, &cfg.Composite); err != nil {
				return nil, fmt.Errorf("pseq %d: composite: %w", s, err)
			}
		}
		if pres[2] == 1 {
			if row.Grp, err = groupBody(r, &cfg.Group); err != nil {
				return nil, fmt.Errorf("pseq %d: group: %w", s, err)
			}
		}
		if r.p != q {
			return nil, fmt.Errorf("AssertionError: (%d, %d, %d)", s, r.p, q)
		}
		out = append(out, row)
	}
	return out, nil
}

// companionRecs is companion_recs(crecs, grecs, cfg): per delta record (marker 1) the composite events (BF4EA0:
// f31, u32 n, n x (id, k-1, k x (index, value))) and the group events (5C7920: f31, per group bool, u32 n, n x u32).
func companionRecs(crecs, grecs []Rec, cfg *Config) ([][]CompEvent, [][]GroupEvent, error) {
	var cc [][]CompEvent
	var gc [][]GroupEvent
	E := cfg.Composite.Entries
	for _, rec := range crecs {
		if rec.M != 1 {
			continue
		}
		b := &bitReader{b: rec.S}
		ev := []CompEvent{}
		var e error
		rd := func(k int) uint64 {
			if e != nil {
				return 0
			}
			x, err := b.r(k)
			e = err
			return x
		}
		rd(31)
		for range rd(32) {
			if e != nil {
				break
			}
			i := int64(rd(cfg.Composite.IDWidth))
			if e != nil {
				break
			}
			if i >= int64(len(E)) {
				return nil, nil, fmt.Errorf("KeyError: %d", i)
			}
			iw, vw := bitLength(E[i][0]-1), bitLength(E[i][1]-1)
			for range rd(iw) + 1 {
				el := rd(iw)
				v := rd(vw)
				if e != nil {
					break
				}
				ev = append(ev, CompEvent{i, int64(el), int64(v)})
			}
		}
		if e != nil {
			return nil, nil, e
		}
		if b.p != len(rec.S) {
			return nil, nil, errors.New("AssertionError: composite delta not fully read")
		}
		cc = append(cc, ev)
	}
	for _, rec := range grecs {
		if rec.M != 1 {
			continue
		}
		b := &bitReader{b: rec.S}
		ev := []GroupEvent{}
		var e error
		rd := func(k int) uint64 {
			if e != nil {
				return 0
			}
			x, err := b.r(k)
			e = err
			return x
		}
		rd(31)
		for g := range int64(len(cfg.Group.Entries)) {
			if rd(1) == 1 {
				for range rd(32) {
					j := rd(32)
					if e != nil {
						break
					}
					ev = append(ev, GroupEvent{g, int64(j)})
				}
			}
			if e != nil {
				return nil, nil, e
			}
		}
		if b.p != len(rec.S) {
			return nil, nil, errors.New("AssertionError: group delta not fully read")
		}
		gc = append(gc, ev)
	}
	return cc, gc, nil
}
