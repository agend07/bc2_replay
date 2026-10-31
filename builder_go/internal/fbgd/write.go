package fbgd

// Write side: port of fbgd_write.py (the exact inverse of fbgd_read: parsed frames -> bytes). encode_keyframe /
// encode_delta take the write-side structures below (OutGhost, OutDelta); write_file assembles the container.
// The round-trip CLI (rebuild / __main__) is not ported: the gate checks the bit round trip with the read side.

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"slices"

	"bc2replay/builder/internal/bits"
)

// BitWriter is fbgd_write.W: an MSB-first bit stream, zero-padded on the right to whole bytes by Bytes.
type BitWriter struct {
	buf  []byte
	cur  byte
	fill int // bits used in cur
	n    int // total bits
}

// Len is the number of bits written (W.n).
func (w *BitWriter) Len() int { return w.n }

// Bytes is W.bytes(): the stream zero-padded to whole bytes.
func (w *BitWriter) Bytes() []byte {
	out := make([]byte, len(w.buf), len(w.buf)+1)
	copy(out, w.buf)
	if w.fill > 0 {
		out = append(out, w.cur)
	}
	return out
}

func (w *BitWriter) put(v uint64, width int) {
	for width > 0 {
		free := 8 - w.fill
		take := min(free, width)
		x := v >> (width - take) & (1<<take - 1)
		w.cur |= byte(x << (free - take))
		w.fill += take
		w.n += take
		width -= take
		if w.fill == 8 {
			w.buf = append(w.buf, w.cur)
			w.cur, w.fill = 0, 0
		}
	}
}

// PutUint is W.put(val, w) for 0 <= w <= 64; a value that does not fit is Python's AssertionError.
func (w *BitWriter) PutUint(v uint64, width int) error {
	if width < 0 || width > 64 || width < 64 && v>>width != 0 {
		return fmt.Errorf("AssertionError: (%d, %d)", v, width)
	}
	w.put(v, width)
	return nil
}

// PutInt is PutUint for an int value (negative -> AssertionError).
func (w *BitWriter) PutInt(v, width int) error {
	if v < 0 {
		return fmt.Errorf("AssertionError: (%d, %d)", v, width)
	}
	return w.PutUint(uint64(v), width)
}

// PutBits is W.put(value, bits) for a chunk value (its width is b.Len(); a Bits value always fits).
func (w *BitWriter) PutBits(b bits.Bits) {
	n := b.Len()
	if n == 0 {
		return
	}
	if n <= 64 {
		v, _ := b.Uint64()
		w.put(v, n)
		return
	}
	p := b.Bytes()
	full := n / 8
	if w.fill == 0 {
		w.buf = append(w.buf, p[:full]...)
		w.n += 8 * full
	} else {
		for _, c := range p[:full] {
			w.put(uint64(c), 8)
		}
	}
	if r := n % 8; r != 0 {
		w.put(uint64(p[full]>>(8-r)), r)
	}
}

// OutChunk is a chunk to write (put_chunk): u6 index, u16 bits, value[bits], u6 ref count, refs (u16 target, u1 flag).
type OutChunk struct {
	Index int
	Value bits.Bits // width = the chunk's 'bits'
	Refs  []Ref
}

// OutSection is a baseline section: u32 header, mask[count], count chunks (count = Mask.Len()).
type OutSection struct {
	Header uint32
	Mask   bits.Bits
	Chunks []OutChunk
}

// OutGhost is a baseline (keyframe ghost or delta 'new').
type OutGhost struct {
	ID       int
	Create   OutChunk
	Sections []OutSection
}

// Counts is [s['count'] for s in g['sections']].
func (g *OutGhost) Counts() []int {
	out := make([]int, len(g.Sections))
	for i, s := range g.Sections {
		out[i] = s.Mask.Len()
	}
	return out
}

// OutChange is one (section, property, chunk) of a delta 'upd' entry.
type OutChange struct {
	Sec, Prop int
	Chunk     OutChunk
}

// OutDirty is one delta dirty entry: (gid, 'new', baseline) when New != nil, else (gid, 'upd', changes).
type OutDirty struct {
	ID      int
	New     *OutGhost
	Changes []OutChange
}

// OutDelta is a delta frame: f31 dt, f31 sim, deleted ids, dirty entries.
type OutDelta struct {
	DT, Sim uint64
	Deleted []int
	Dirty   []OutDirty
}

// OutFrame is one container record (marker 1 key / 2 delta, payload, bit length).
type OutFrame struct {
	Marker  byte
	Payload []byte
	NBits   int
}

// PutChunk is fbgd_write.put_chunk.
func PutChunk(w *BitWriter, c *OutChunk) error {
	if err := w.PutInt(c.Index, 6); err != nil {
		return err
	}
	if err := w.PutInt(c.Value.Len(), 16); err != nil {
		return err
	}
	w.PutBits(c.Value)
	if err := w.PutInt(len(c.Refs), 6); err != nil {
		return err
	}
	for _, r := range c.Refs {
		if err := w.PutInt(r.Target, 16); err != nil {
			return err
		}
		if err := w.PutInt(r.Flag, 1); err != nil {
			return err
		}
	}
	return nil
}

// PutBaseline is fbgd_write.put_baseline.
func PutBaseline(w *BitWriter, g *OutGhost, withID bool) error {
	if withID {
		if err := w.PutInt(g.ID, 16); err != nil {
			return err
		}
	}
	if err := w.PutInt(len(g.Sections), 16); err != nil {
		return err
	}
	if err := PutChunk(w, &g.Create); err != nil {
		return err
	}
	for i := range g.Sections {
		s := &g.Sections[i]
		w.put(uint64(s.Header), 32)
		w.PutBits(s.Mask)
		for j := range s.Chunks {
			if err := PutChunk(w, &s.Chunks[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

// EncodeKeyframe is fbgd_write.encode_keyframe: (payload, bit length).
func EncodeKeyframe(ghosts []*OutGhost) ([]byte, int, error) {
	var w BitWriter
	if len(ghosts) == 0 {
		w.put(0, 1)
		return w.Bytes(), w.Len(), nil
	}
	w.put(1, 1)
	if err := w.PutInt(len(ghosts)-1, 16); err != nil {
		return nil, 0, err
	}
	for _, g := range ghosts {
		if err := PutBaseline(&w, g, true); err != nil {
			return nil, 0, err
		}
	}
	return w.Bytes(), w.Len(), nil
}

// EncodeDelta is fbgd_write.encode_delta. counts ({gid: [propertyCount per section]}) is updated as in Python: a
// 'new' sets its counts while writing, the deleted ids are popped at the end. An 'upd' of a gid without counts is
// Python's KeyError. Changes outside counts (section or property index) are not written.
func EncodeDelta(d *OutDelta, counts map[int][]int) ([]byte, int, error) {
	var w BitWriter
	if err := w.PutUint(d.DT, 31); err != nil {
		return nil, 0, err
	}
	if err := w.PutUint(d.Sim, 31); err != nil {
		return nil, 0, err
	}
	if len(d.Deleted) == 0 {
		w.put(0, 1)
	} else {
		w.put(1, 1)
		if err := w.PutInt(len(d.Deleted)-1, 16); err != nil {
			return nil, 0, err
		}
		for _, g := range d.Deleted {
			if err := w.PutInt(g, 16); err != nil {
				return nil, 0, err
			}
		}
	}
	if len(d.Dirty) == 0 {
		w.put(0, 1)
	} else {
		w.put(1, 1)
		if err := w.PutInt(len(d.Dirty)-1, 16); err != nil {
			return nil, 0, err
		}
		for i := range d.Dirty {
			x := &d.Dirty[i]
			if err := w.PutInt(x.ID, 16); err != nil {
				return nil, 0, err
			}
			if x.New != nil {
				w.put(1, 1)
				if err := PutBaseline(&w, x.New, false); err != nil {
					return nil, 0, err
				}
				counts[x.ID] = x.New.Counts()
				continue
			}
			w.put(0, 1)
			cs, ok := counts[x.ID]
			if !ok {
				return nil, 0, KeyError(x.ID)
			}
			ch := sortedChanges(x.Changes)
			j := 0
			for si, count := range cs {
				for j < len(ch) && ch[j].Sec < si {
					j++
				}
				if j == len(ch) || ch[j].Sec != si {
					w.put(0, 1)
					continue
				}
				w.put(1, 1)
				for pi := range count {
					for j < len(ch) && ch[j].Sec == si && ch[j].Prop < pi {
						j++
					}
					if j == len(ch) || ch[j].Sec != si || ch[j].Prop != pi {
						w.put(0, 1)
						continue
					}
					w.put(1, 1)
					if err := PutChunk(&w, &ch[j].Chunk); err != nil {
						return nil, 0, err
					}
				}
			}
		}
	}
	for _, g := range d.Deleted {
		delete(counts, g)
	}
	return w.Bytes(), w.Len(), nil
}

func changeCmp(a, b OutChange) int {
	return cmp.Or(cmp.Compare(a.Sec, b.Sec), cmp.Compare(a.Prop, b.Prop))
}

// sortedChanges is the 'upd' body as Python's by[si][pi] dict sees it: ordered by (section, property), a repeated
// (section, property) keeping its last chunk. Already sorted unique input (assemble's) is returned as is.
func sortedChanges(ch []OutChange) []OutChange {
	ok := true
	for i := 1; i < len(ch) && ok; i++ {
		ok = changeCmp(ch[i-1], ch[i]) < 0
	}
	if ok {
		return ch
	}
	s := slices.Clone(ch)
	slices.SortStableFunc(s, changeCmp)
	out := s[:0]
	for _, c := range s {
		if n := len(out); n > 0 && changeCmp(out[n-1], c) == 0 {
			out[n-1] = c
		} else {
			out = append(out, c)
		}
	}
	return out
}

// WriteFile is fbgd_write.write_file: prolog, then per frame u8 marker, u32 payload size, u32 bit length, payload;
// then a 0 byte.
func WriteFile(prolog []byte, frames []OutFrame) []byte {
	size := len(prolog) + 1
	for _, f := range frames {
		size += 9 + len(f.Payload)
	}
	out := make([]byte, 0, size)
	out = append(out, prolog...)
	for _, f := range frames {
		out = append(out, f.Marker)
		out = binary.LittleEndian.AppendUint32(out, uint32(len(f.Payload)))
		out = binary.LittleEndian.AppendUint32(out, uint32(f.NBits))
		out = append(out, f.Payload...)
	}
	return append(out, 0)
}
