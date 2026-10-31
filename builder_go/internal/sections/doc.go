// Package sections ports the multi-section / create side of the cl04c build: the physics-vehicle section codecs
// (v3_vehsec, v3_phys_p2), the origin-zero create re-encode (v3_create_rebase), v3_audit888's chunks_of / rebase,
// v3_multi_slice.next_entries, cl04c's patched create_chunk (cc: camera create, CREBASE, CUT, AA80B0) and the
// WIREMULTI loop that drives them. run_section and the camera create init are wire.RunSection / wire.CameraInit.
//
// Bit strings are Go strings of '0'/'1' like the Python code: Python's slice clamping and int(s, 2) on a short
// slice carry over through slice / pyInt2; a chunk becomes bits.Bits only at the row boundary.
//
// Python exceptions are *PyError values ("Type: message", the golden wire_sites 'exc' form). Inside the codecs they
// travel as panics and are recovered at the exported entry points (like a Python raise unwinding to its caller).
package sections

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// PyError is a Python exception: Type (ValueError, IndexError, ...) and the str() of the exception.
type PyError struct{ Type, Msg string }

func (e *PyError) Error() string { return e.Type + ": " + e.Msg }

// IsValueError reports whether err is a Python ValueError (the only exception vehsec's caller catches).
func IsValueError(err error) bool {
	var pe *PyError
	return errors.As(err, &pe) && pe.Type == "ValueError"
}

func raise(typ, format string, a ...any) { panic(&PyError{typ, fmt.Sprintf(format, a...)}) }

// catch turns a *PyError panic into *err (other panics propagate).
func catch(err *error) {
	if r := recover(); r != nil {
		pe, ok := r.(*PyError)
		if !ok {
			panic(r)
		}
		*err = pe
	}
}

// slice is s[a:b] for a, b >= 0 with Python clamping.
func slice(s string, a, b int) string {
	a, b = min(max(a, 0), len(s)), min(max(b, 0), len(s))
	if b <= a {
		return ""
	}
	return s[a:b]
}

// at is s[i] (IndexError past the end).
func at(s string, i int) byte {
	if i < 0 || i >= len(s) {
		raise("IndexError", "string index out of range")
	}
	return s[i]
}

// pyInt2 is int(s, 2) for a '0'/'1' string; only values of <= 64 bits are needed (wider reads are skips: 0).
func pyInt2(s string) uint64 {
	if s == "" {
		raise("ValueError", "invalid literal for int() with base 2: ''")
	}
	if len(s) > 64 {
		return 0
	}
	v, _ := strconv.ParseUint(s, 2, 64)
	return v
}

// pyIndex is seq[i] for a list index (IndexError when out of range).
func pyIndex(table []int64, i int) int64 {
	if i < 0 || i >= len(table) {
		raise("IndexError", "list index out of range")
	}
	return table[i]
}

// bitLength is int.bit_length().
func bitLength(n int) int {
	k := 0
	for ; n > 0; n >>= 1 {
		k++
	}
	return k
}

// formatBin is format(v, '0%db' % w) for 0 <= v < 2**w.
func formatBin(v uint64, w int) string {
	s := strconv.FormatUint(v, 2)
	for len(s) < w {
		s = "0" + s
	}
	return s
}

// f32 is v3_phys_p2.F32 (struct pack/unpack '<f'); a finite overflow raises OverflowError.
func f32(x float64) float64 {
	y, err := pyc.F32(x)
	if err != nil {
		raise("OverflowError", "float too large to pack with f format")
	}
	return y
}

// round is int(round(x)) (half-even); inf -> OverflowError, nan -> ValueError; ok false = |result| >= 2**63 (no
// table width fits such a value).
func round(x float64) (int64, bool) {
	switch {
	case math.IsNaN(x):
		raise("ValueError", "cannot convert float NaN to integer")
	case math.IsInf(x, 0):
		raise("OverflowError", "cannot convert float infinity to integer")
	}
	k, err := pyc.RoundInt64(x)
	return k, err == nil
}

// OriginOf is v3_create_rebase.origin_of: the record's packet origin (3 x f32 bit patterns) as floats.
func OriginOf(g *wire.Ghost) [3]float64 {
	return [3]float64{pyc.UnpackF32(g.OriginBits[0]), pyc.UnpackF32(g.OriginBits[1]), pyc.UnpackF32(g.OriginBits[2])}
}
