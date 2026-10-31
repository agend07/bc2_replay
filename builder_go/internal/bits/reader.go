package bits

import (
	"errors"
	"fmt"
)

// ErrShort is returned when a read runs past the end of the bit buffer.
var ErrShort = errors.New("bits: read past end")

// Reader reads MSB-first bit fields from a byte buffer limited to nbits.
type Reader struct {
	p     []byte
	nbits int
	pos   int
}

// NewReader reads the first nbits of p (nbits <= 8*len(p)).
func NewReader(p []byte, nbits int) *Reader {
	if nbits > 8*len(p) {
		nbits = 8 * len(p)
	}
	return &Reader{p: p, nbits: nbits}
}

// Remaining is the number of unread bits.
func (r *Reader) Remaining() int { return r.nbits - r.pos }

// Read returns the next n bits (0 <= n <= 64) as an unsigned value.
func (r *Reader) Read(n int) (uint64, error) {
	if n < 0 || n > 64 {
		return 0, fmt.Errorf("bits: Read(%d) outside 0..64", n)
	}
	if n > r.Remaining() {
		return 0, ErrShort
	}
	var v uint64
	for n > 0 {
		off := r.pos & 7
		take := min(8-off, n)
		c := uint64(r.p[r.pos>>3]) >> (8 - off - take) & (1<<take - 1)
		v = v<<take | c
		r.pos += take
		n -= take
	}
	return v, nil
}

// ReadBits returns the next n bits as a Bits (any width).
func (r *Reader) ReadBits(n int) (Bits, error) {
	if n < 0 || n > r.Remaining() {
		return Bits{}, ErrShort
	}
	off := r.pos & 7
	b := FromBytes(r.p[r.pos>>3:(r.pos+n+7)>>3]).Slice(off, off+n)
	r.pos += n
	return b, nil
}

// Writer accumulates MSB-first fields (fbgd_write.W).
type Writer struct{ parts []Bits }

// Put appends val as a w-bit field; val must fit (fbgd_write asserts 0 <= val < 1 << w).
func (w *Writer) Put(val uint64, n int) error {
	b, err := FromUint(val, n)
	if err != nil {
		return err
	}
	w.parts = append(w.parts, b)
	return nil
}

// PutBits appends a whole bit string.
func (w *Writer) PutBits(b Bits) { w.parts = append(w.parts, b) }

// Bits returns everything written so far.
func (w *Writer) Bits() Bits { return Concat(w.parts...) }

// Len is the number of bits written.
func (w *Writer) Len() int {
	n := 0
	for _, p := range w.parts {
		n += p.n
	}
	return n
}

// Bytes is W.bytes(): the bits zero-padded on the right to whole bytes.
func (w *Writer) Bytes() []byte { return w.Bits().Bytes() }
