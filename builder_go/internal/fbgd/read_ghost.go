package fbgd

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Ref is one chunk reference (u16 target, u1 flag).
type Ref struct{ Target, Flag int }

// Chunk is fbgd_read.read_chunk: index, bits and refs; the value stays in the payload (buf at bit Start, Bits wide) and
// is materialized on demand (Top, Value, BitString).
type Chunk struct {
	Index int
	Bits  int
	Start int
	Refs  []Ref
	buf   []byte
}

// Top is value >> (bits - k): the k most significant value bits, 0 <= k <= min(64, Bits).
func (c *Chunk) Top(k int) uint64 {
	var v uint64
	p, n := c.Start, k
	for n > 0 {
		off := p & 7
		take := min(8-off, n)
		v = v<<take | uint64(c.buf[p>>3])>>(8-off-take)&(1<<take-1)
		p += take
		n -= take
	}
	return v
}

// BitString is format(value, '0%db' % bits) (bin(value | 1 << bits)[3:]).
func (c *Chunk) BitString() string {
	var sb strings.Builder
	sb.Grow(c.Bits)
	for k := c.Start; k < c.Start+c.Bits; k++ {
		sb.WriteByte('0' + c.buf[k>>3]>>(7-k&7)&1)
	}
	return sb.String()
}

// Value is the chunk value as an unbounded int.
func (c *Chunk) Value() *big.Int {
	v := new(big.Int)
	if c.Bits == 0 {
		return v
	}
	v.SetString(c.BitString(), 2)
	return v
}

// ValueEqual is (bits, value) equality.
func (c *Chunk) ValueEqual(o *Chunk) bool {
	if c.Bits != o.Bits {
		return false
	}
	for k := 0; k < c.Bits; {
		n := min(64, c.Bits-k)
		a := Chunk{Start: c.Start + k, buf: c.buf}
		b := Chunk{Start: o.Start + k, buf: o.buf}
		if a.Top(n) != b.Top(n) {
			return false
		}
		k += n
	}
	return true
}

// ReadChunk is fbgd_read.read_chunk(b).
func ReadChunk(r *Reader) (Chunk, error) {
	var err error
	c := Chunk{buf: r.buf}
	c.Index = r.get(6, &err)
	c.Bits = r.get(16, &err)
	if err != nil {
		return c, err
	}
	c.Start = r.p
	if err = r.Skip(c.Bits); err != nil {
		return c, err
	}
	n := r.get(6, &err)
	if n > 0 && err == nil {
		c.Refs = make([]Ref, n)
		for i := range c.Refs {
			c.Refs[i].Target = r.get(16, &err)
			c.Refs[i].Flag = r.get(1, &err)
		}
	}
	return c, err
}

// Section is one baseline section: header (count << 16 | bit offset), the count chunks and the mask (bits at MaskStart).
type Section struct {
	Header    uint32
	Count     int
	MaskStart int
	Chunks    []Chunk
}

// Ghost is fbgd_read.read_baseline: id, create chunk, sections.
type Ghost struct {
	ID       int
	Create   Chunk
	Sections []Section
}

// Counts is [s['count'] for s in g['sections']].
func (g *Ghost) Counts() []int {
	out := make([]int, len(g.Sections))
	for i, s := range g.Sections {
		out[i] = s.Count
	}
	return out
}

// Shape is tuple(s['count'] for s in g['sections']) (same as Counts).
func (g *Ghost) Shape() []int { return g.Counts() }

// ReadBaseline is fbgd_read.read_baseline(b, gid): gid < 0 reads the id from the stream.
func ReadBaseline(r *Reader, gid int) (*Ghost, error) {
	var err error
	if gid < 0 {
		gid = r.get(16, &err)
	}
	nsec := r.get(16, &err)
	if err != nil {
		return nil, err
	}
	g := &Ghost{ID: gid}
	if g.Create, err = ReadChunk(r); err != nil {
		return nil, err
	}
	g.Sections = make([]Section, 0, min(nsec, 64))
	for range nsec {
		h := r.get(32, &err)
		if err != nil {
			return nil, err
		}
		s := Section{Header: uint32(h), Count: h >> 16, MaskStart: r.p}
		if err = r.Skip(s.Count); err != nil {
			return nil, err
		}
		s.Chunks = make([]Chunk, s.Count)
		for i := range s.Chunks {
			if s.Chunks[i], err = ReadChunk(r); err != nil {
				return nil, err
			}
		}
		g.Sections = append(g.Sections, s)
	}
	return g, nil
}

// Keyframe is fbgd_read.keyframe(b).
func Keyframe(r *Reader) ([]*Ghost, error) {
	var err error
	if r.get(1, &err) == 0 {
		return nil, err
	}
	n := r.get(16, &err) + 1
	if err != nil {
		return nil, err
	}
	out := make([]*Ghost, 0, n)
	for range n {
		g, err := ReadBaseline(r, -1)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

// State is the delta parser's {ghostId: [propertyCount per section]}.
type State map[int][]int

// Change is one updated property of an 'upd' dirty entry: (section, property, chunk).
type Change struct {
	Section, Property int
	Chunk             Chunk
}

// Dirty is one dirty entry: (gid, 'new', ghost) or (gid, 'upd', changes).
type Dirty struct {
	ID      int
	New     bool
	Ghost   *Ghost
	Changes []Change
}

// DeltaFrame is fbgd_read.delta's result. EmptySection marks an 'upd' section flag set with no property flag set: the
// only stream form fbgd_write.encode_delta does not reproduce (it writes the section flag as 0).
type DeltaFrame struct {
	DT, Sim      uint32
	Deleted      []int
	Dirty        []Dirty
	EmptySection bool
}

// Delta is fbgd_read.delta(b, state); state is updated as in Python (news added while parsing, deletes popped at the end).
func Delta(r *Reader, state State) (*DeltaFrame, error) {
	var err error
	d := &DeltaFrame{}
	d.DT = uint32(r.get(31, &err))
	d.Sim = uint32(r.get(31, &err))
	if r.get(1, &err) == 1 {
		n := r.get(16, &err) + 1
		if err != nil {
			return nil, err
		}
		d.Deleted = make([]int, n)
		for i := range d.Deleted {
			d.Deleted[i] = r.get(16, &err)
		}
	}
	if err != nil {
		return nil, err
	}
	if r.get(1, &err) == 1 {
		n := r.get(16, &err) + 1
		if err != nil {
			return nil, err
		}
		for range n {
			gid := r.get(16, &err)
			isNew := r.get(1, &err)
			if err != nil {
				return nil, err
			}
			if isNew == 1 {
				g, err := ReadBaseline(r, gid)
				if err != nil {
					return nil, err
				}
				state[gid] = g.Counts()
				d.Dirty = append(d.Dirty, Dirty{ID: gid, New: true, Ghost: g})
				continue
			}
			counts, ok := state[gid]
			if !ok {
				return nil, KeyError(gid)
			}
			var changes []Change
			for si, count := range counts {
				if r.get(1, &err) == 0 {
					if err != nil {
						return nil, err
					}
					continue
				}
				got := false
				for pi := range count {
					if r.get(1, &err) == 1 {
						c, err := ReadChunk(r)
						if err != nil {
							return nil, err
						}
						changes = append(changes, Change{si, pi, c})
						got = true
					}
					if err != nil {
						return nil, err
					}
				}
				if !got {
					d.EmptySection = true
				}
			}
			d.Dirty = append(d.Dirty, Dirty{ID: gid, Changes: changes})
		}
	}
	if err != nil {
		return nil, err
	}
	for _, gid := range d.Deleted {
		delete(state, gid)
	}
	return d, nil
}

// Soldier is fbgd_diff.soldier(g): an 8+ bit create with prefix 10.
func Soldier(g *Ghost) bool { return g.Create.Bits >= 8 && g.Create.Top(8) == 10 }

// VariableCreateTypes / NoVariantTypes are fbgd_lint.VARIABLE_CREATE_TYPES / NO_VARIANT_TYPES.
var (
	VariableCreateTypes = map[int]bool{3: true, 6: true, 31: true}
	NoVariantTypes      = map[int]bool{31: true}
)

// LintVersion is fbgd_lint.VERSION.
const LintVersion = 6

// VariableWidth is fbgd_lint.variable_width(sig): a '/*N' signature.
func VariableWidth(sig string) bool {
	_, w, _ := strings.Cut(sig, "/")
	return strings.HasPrefix(w, "*")
}

// Signature is fbgd_lint.signature(g).
func Signature(g *Ghost) string {
	c := &g.Create
	var prefix string
	if c.Bits == 0 {
		prefix = "0"
	} else {
		k := min(8, c.Bits)
		prefix = strconv.FormatUint(c.Top(k), 2)
		prefix = strings.Repeat("0", k-len(prefix)) + prefix
	}
	if c.Bits >= 19 && VariableCreateTypes[int(c.Top(6))] && c.Top(7)&1 == 1 {
		if NoVariantTypes[int(c.Top(6))] {
			return fmt.Sprintf("%s/*%d", prefix, len(g.Sections))
		}
		return fmt.Sprintf("%s/*%dv%d", prefix, len(g.Sections), c.Top(19)&0xfff)
	}
	return fmt.Sprintf("%s/%d", prefix, c.Bits)
}
