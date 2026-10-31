// Package fbgd reads (read.go, read_chunk.go) and, separately, writes .fbgd ghost demo streams.
//
// The read side is the port of fbgd_read.py (frames, keyframe, delta, read_chunk) plus the two fbgd_diff/fbgd_lint helpers
// every consumer needs (Soldier, Signature). Python exceptions that reach output (fbgd_gate's parse-or-input detail) are
// returned as *PyError carrying the Python class name and str(e).
package fbgd

import (
	"encoding/binary"
	"fmt"
)

// PyError is a Python exception raised by the reader: Class is the Python class name ("EOFError", "ValueError",
// "KeyError", "IndexError", "error" for struct.error), Msg is str(e).
type PyError struct {
	Class string
	Msg   string
}

func (e *PyError) Error() string { return e.Msg }

// Repr is repr(e) (KeyError keeps the repr of its key as Msg, so both are KeyError(<Msg>)).
func (e *PyError) Repr() string {
	if e.Msg == "" {
		return e.Class + "()"
	}
	if e.Class == "KeyError" {
		return e.Class + "(" + e.Msg + ")"
	}
	return e.Class + "(" + StrRepr(e.Msg) + ")"
}

// StrRepr is repr(str) for ASCII text: single quotes unless the text holds a ' and no ".
func StrRepr(s string) string {
	q := byte('\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			q = '"'
		}
		if s[i] == '"' {
			q = '\''
			break
		}
	}
	out := []byte{q}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			out = append(out, '\\', '\\')
		case c == q:
			out = append(out, '\\', c)
		case c == '\n':
			out = append(out, '\\', 'n')
		case c == '\r':
			out = append(out, '\\', 'r')
		case c == '\t':
			out = append(out, '\\', 't')
		case c < 0x20 || c == 0x7f:
			out = append(out, fmt.Sprintf("\\x%02x", c)...)
		default:
			out = append(out, c)
		}
	}
	return string(append(out, q))
}

// KeyError is Python's KeyError(key) for an int key.
func KeyError(key int) *PyError { return &PyError{"KeyError", fmt.Sprint(key)} }

// Reader is fbgd_read.Bits: MSB-first reads limited to n bits of buf.
type Reader struct {
	buf []byte
	n   int
	p   int
}

// NewReader is Bits(data, nbits).
func NewReader(data []byte, nbits int) *Reader { return &Reader{buf: data, n: nbits} }

// Pos is Bits.p (bits consumed).
func (r *Reader) Pos() int { return r.p }

func (r *Reader) check(w int) error {
	if r.p+w > r.n {
		return &PyError{"EOFError", fmt.Sprintf("read %d at %d past %d", w, r.p, r.n)}
	}
	if r.p+w > len(r.buf)*8 { // only without the scan's size check: Python shifts by a negative count
		return &PyError{"ValueError", "negative shift count"}
	}
	return nil
}

// Get is Bits.get(w) for w <= 64 (get(0) = 0 without a bounds check).
func (r *Reader) Get(w int) (uint64, error) {
	if w == 0 {
		return 0, nil
	}
	if err := r.check(w); err != nil {
		return 0, err
	}
	var v uint64
	p, n := r.p, w
	for n > 0 {
		off := p & 7
		take := min(8-off, n)
		v = v<<take | uint64(r.buf[p>>3])>>(8-off-take)&(1<<take-1)
		p += take
		n -= take
	}
	r.p = p
	return v, nil
}

// Skip is Bits.get(w) for any width when the value is not needed (the bits stay addressable through buf).
func (r *Reader) Skip(w int) error {
	if w == 0 {
		return nil
	}
	if err := r.check(w); err != nil {
		return err
	}
	r.p += w
	return nil
}

func (r *Reader) get(w int, err *error) int {
	if *err != nil {
		return 0
	}
	v, e := r.Get(w)
	*err = e
	return int(v)
}

// Frame is one fbgd_read.frames() record: (marker, offset, size, bitpos, payload).
type Frame struct {
	Marker  int
	Off     int
	Size    int
	BitPos  int
	Payload []byte
}

// Frames is fbgd_read.frames(data) -> (version, frames). Payload slices alias data and may be shorter than Size when
// the file is truncated (as in Python).
func Frames(data []byte) ([]byte, []Frame, error) {
	i := indexZero(data, 0)
	if i < 0 {
		return nil, nil, &PyError{"ValueError", "subsection not found"}
	}
	p := i + 1
	j := indexZero(data, p)
	if j < 0 {
		return nil, nil, &PyError{"ValueError", "subsection not found"}
	}
	q := j + 1
	version := data[p : q-1]
	var out []Frame
	for p < len(data) {
		if len(out) == 0 {
			p = q
		}
		if p >= len(data) {
			return nil, nil, &PyError{"IndexError", "index out of range"}
		}
		marker := int(data[p])
		if marker == 0 {
			break
		}
		if p+9 > len(data) {
			return nil, nil, &PyError{"error", fmt.Sprintf(
				"unpack_from requires a buffer of at least %d bytes for unpacking 8 bytes at offset %d (actual buffer size is %d)",
				p+9, p+1, len(data))}
		}
		size := int(binary.LittleEndian.Uint32(data[p+1:]))
		bitpos := int(binary.LittleEndian.Uint32(data[p+5:]))
		end := min(p+9+size, len(data))
		out = append(out, Frame{marker, p, size, bitpos, data[p+9 : end]})
		p = p + 9 + size
		q = p
	}
	return version, out, nil
}

func indexZero(b []byte, from int) int {
	for i := from; i < len(b); i++ {
		if b[i] == 0 {
			return i
		}
	}
	return -1
}

// PrologEnd is the offset after the company and version C-strings (fbgd_gate.scan prolog_end); -1 when absent.
func PrologEnd(data []byte) int {
	i := indexZero(data, 0)
	if i < 0 {
		return -1
	}
	j := indexZero(data, i+1)
	if j < 0 {
		return -1
	}
	return j + 1
}
