package fbgd

// Port of v3_refmap: ghost-reference (13-bit id) remap for client-only builds. Create refs sit at fixed bit offsets
// per create signature (prefix8, bits); an unmappable target -> id 0 (the native reader returns null for 0 and for
// unknown ids; the entry is omitted from the table). Ref table flag meaning unverified.

import (
	"errors"
	"fmt"
	"strings"

	"bc2replay/builder/internal/bits"
)

// RefWidth is v3_refmap.W (ghost id bits).
const RefWidth = 13

type createSig struct {
	prefix string
	n      int
}

// createRefPos is v3_refmap.CREATE_REF_POS: (first 8 bits, bits) -> ref bit offsets.
var createRefPos = map[createSig][]int{
	{"00011100", 20}: {7}, {"10101000", 20}: {7}, {"00100010", 184}: {20}, {"00100110", 184}: {20},
}

// PositionsFromTable is v3_refmap.positions_from_table: for a server chunk, the left-to-right search of each table id
// (13-bit) in the body. A ref not found is ValueError.
func PositionsFromTable(c *OutChunk) ([]int, error) {
	s := c.Value.String()
	pos := make([]int, 0, len(c.Refs))
	at := 0
	for _, r := range c.Refs {
		i := -1
		if at <= len(s) {
			if j := strings.Index(s[at:], bits.FormatBin(uint64(r.Target), RefWidth)); j >= 0 {
				i = at + j
			}
		}
		if i < 0 {
			return nil, fmt.Errorf("ValueError: ref %d not found in chunk", r.Target)
		}
		pos = append(pos, i)
		at = i + RefWidth
	}
	return pos, nil
}

// CreatePositions is v3_refmap.create_positions: the ref offsets of a create chunk's signature (none if unknown).
func CreatePositions(c *OutChunk) []int {
	return createRefPos[createSig{c.Value.Slice(0, 8).String(), c.Value.Len()}]
}

// Remap is v3_refmap.remap: rewrite the 13-bit ids at positions through mapfn(old) -> new (0 = null) and rebuild the
// table (one (new, flag) per non-null result, flag from the old table entry at the same index, else 0). Index and
// width are kept.
func Remap(c OutChunk, positions []int, mapfn func(int) int) (OutChunk, error) {
	b := []byte(c.Value.String())
	refs := []Ref{}
	for k, p := range positions {
		if p < 0 { // Python would slice from the end; never produced by the builder
			return c, errors.New("ValueError: negative ref position")
		}
		if p+RefWidth > len(b) {
			return c, errors.New("ValueError: ref position outside chunk")
		}
		o := 0
		for _, x := range b[p : p+RefWidth] {
			o = o<<1 | int(x-'0')
		}
		n := mapfn(o)
		if n < 0 || n >= 1<<RefWidth { // Python would widen the body here
			return c, fmt.Errorf("ValueError: mapped id %d does not fit %d bits", n, RefWidth)
		}
		copy(b[p:], bits.FormatBin(uint64(n), RefWidth))
		if n != 0 {
			f := 0
			if k < len(c.Refs) {
				f = c.Refs[k].Flag
			}
			refs = append(refs, Ref{n, f})
		}
	}
	v, err := bits.Parse(string(b))
	if err != nil {
		return c, err
	}
	return OutChunk{Index: c.Index, Value: v, Refs: refs}, nil
}

// RemapServerChunk is v3_refmap.remap_server_chunk: remap at the table's positions; a chunk without refs is returned
// unchanged.
func RemapServerChunk(c OutChunk, mapfn func(int) int) (OutChunk, error) {
	if len(c.Refs) == 0 {
		return c, nil
	}
	pos, err := PositionsFromTable(&c)
	if err != nil {
		return c, err
	}
	return Remap(c, pos, mapfn)
}
