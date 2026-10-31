// Package events ports the builder's wire event streams: ghost deletions (v3_wire_deletes), the registry join state
// and updates (v3_reg0, v3_multi_slice.next_entries), the destruction stream (v3_wire_destruction, the impact part
// of argon_destruction_offline.read_manager) and the destruction companions built from it (v3_wire_companions:
// template, wire_events, build; the v3_cl04c _fo time mapping). The v3_cl04c loops that use them (WIREDEL,
// REG0WIRE/REGWIRE, WIRECOMP) belong to the pipeline; RegistryUpdates is the REGWIRE loop itself.
//
// Bit strings are kept as Python keeps them: ASCII '0'/'1' (string or []byte), so that slicing, comparison and
// bits_sha1 follow the source literally.
package events

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"bc2replay/builder/internal/capture"
)

// payloadBits is wire_py_core.payload_bits: the payload as ASCII '0'/'1', MSB first.
func payloadBits(p []byte) []byte { return appendBits(nil, p) }

// appendBits is payloadBits into buf's storage (reused by the per-packet scans).
func appendBits(buf, p []byte) []byte {
	out := slices.Grow(buf[:0], 8*len(p))[:8*len(p)]
	for i, c := range p {
		for k := range 8 {
			out[8*i+k] = '0' + c>>(7-k)&1
		}
	}
	return out
}

// errEmptyInt is Python's int(”, 2) (a slice past the end of the bit string).
var errEmptyInt = errors.New("ValueError: invalid literal for int() with base 2: ''")

// intOf is int(bits[lo:hi], 2) with Python slice clamping; an empty slice is an error. hi - lo <= 64.
func intOf(b []byte, lo, hi int) (uint64, error) {
	hi = min(hi, len(b))
	if lo < 0 || lo >= hi {
		return 0, errEmptyInt
	}
	var v uint64
	for _, c := range b[lo:hi] {
		v = v<<1 | uint64(c-'0')
	}
	return v, nil
}

// slice is bits[lo:hi] with Python clamping (lo, hi >= 0).
func slice[S ~string | ~[]byte](b S, lo, hi int) S {
	hi = min(hi, len(b))
	lo = min(lo, hi)
	return b[lo:hi]
}

// bitLength is int.bit_length() (of the absolute value, like Python for negatives).
func bitLength(x int64) int {
	if x < 0 {
		x = -x
	}
	n := 0
	for ; x != 0; x >>= 1 {
		n++
	}
	return n
}

// fmtBin is format(x, '0%db' % w): zero-padded, never truncated.
func fmtBin(sb *strings.Builder, x uint64, w int) {
	s := strconv.FormatUint(x, 2)
	for i := len(s); i < w; i++ {
		sb.WriteByte('0')
	}
	sb.WriteString(s)
}

// bitReader is v3_wire_destruction.Bits: r(k) reads k bits, EOFError past the end.
type bitReader struct {
	b string
	p int
}

var errEOF = errors.New("EOFError")

func (r *bitReader) r(k int) (uint64, error) {
	if r.p+k > len(r.b) {
		return 0, errEOF
	}
	if k > 64 {
		return 0, fmt.Errorf("bit read of %d > 64 bits", k)
	}
	var v uint64
	for i := r.p; i < r.p+k; i++ {
		v = v<<1 | uint64(r.b[i]-'0')
	}
	r.p += k
	return v, nil
}

// entryCursors is the per-packet list of logged entry cursors (ghosts then multi, record order), keyed by pseq,
// with the pseqs in first-appearance order (the defaultdict(list) of v3_wire_deletes / v3_multi_slice).
func entryCursors(d *capture.Decoded) (map[uint32][]int, []uint32) {
	ent := map[uint32][]int{}
	var order []uint32
	for _, gs := range [2][]*capture.Ghost{d.Ghosts, d.Multi} {
		for _, g := range gs {
			if _, ok := ent[g.Pseq]; !ok {
				order = append(order, g.Pseq)
			}
			ent[g.Pseq] = append(ent[g.Pseq], int(g.EntryCursor))
		}
	}
	return ent, order
}
