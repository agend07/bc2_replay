// Package bits holds chunk values the Python builder keeps as (bits, value) pairs of unbounded ints and as '0'/'1'
// strings. A Bits is a width plus the value packed in 64-bit words; its String() is the Python bit string.
//
// Python idioms covered (rows_to_fbgd, fbgd_write, v3_assemble, wire_py_core, v3_wire_companions):
//
//	int(body, 2) / int(body or '0', 2), empty -> (0, 0)   Parse
//	bin(v | 1 << bits)[3:], format(v, '0%db' % bits)      FromBig/FromUint + String
//	format(x, f'0{w}b') with any w (wider value, w=0)     FormatBin, FormatBinBig
//	bin(int.from_bytes(b'\x01' + p, 'big'))[3:]           FromBytes
//	''.join(f'{v:08b}' for v in p)                         FromBytes
//	''.join(f'{b:08b}' for b in bytes.fromhex(h))[:n]      FromHex
//	tobytes(bits) / W.bytes() (zero-pad right to bytes)    Bytes, Hex
//	v >> k, v << k (13 -> 17 bits), v & ((1 << m) - 1)    Shr, Shl, Low
//	body[a:b] with negative indices (c[-21:-5], [:8])     Slice
//	body + body2, W.put(val, w)                            Concat, Append, Writer
//	v.bit_length(), (bits, value) tuple equality           BitLen, Equal, Key
//	MSB-first bit reading                                  Reader
package bits

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"math/bits"
	"strings"
)

// Bits is an n-bit unsigned value (0 <= value < 2**n). w holds the value least significant word first,
// len(w) == (n+63)/64, bits above n are zero. The zero Bits is the empty body (0 bits, value 0).
type Bits struct {
	n int
	w []uint64
}

func nwords(n int) int { return (n + 63) / 64 }

// clip zeroes the bits above n in the top word.
func (b *Bits) clip() {
	if r := b.n % 64; r != 0 {
		b.w[len(b.w)-1] &= 1<<r - 1
	}
}

// FromUint is (n, v); v must fit in n bits (fbgd_write's assert 0 <= val < 1 << w).
func FromUint(v uint64, n int) (Bits, error) {
	if n < 0 || n < 64 && v>>n != 0 {
		return Bits{}, fmt.Errorf("bits: value %d does not fit %d bits", v, n)
	}
	b := Bits{n: n, w: make([]uint64, nwords(n))}
	if n > 0 {
		b.w[0] = v
	}
	return b, nil
}

// FromBig is (n, v) for an unbounded non-negative int that fits n bits.
func FromBig(v *big.Int, n int) (Bits, error) {
	if v.Sign() < 0 || v.BitLen() > n {
		return Bits{}, fmt.Errorf("bits: value of %d bits does not fit %d bits", v.BitLen(), n)
	}
	b := Bits{n: n, w: make([]uint64, nwords(n))}
	for i, x := range v.Bits() {
		b.w[i] = uint64(x) // big.Word is 64-bit on amd64/arm64
	}
	return b, nil
}

// Parse is int(body, 2) with width len(body); body may only hold '0' and '1'. Empty -> zero width, value 0.
func Parse(body string) (Bits, error) {
	n := len(body)
	b := Bits{n: n, w: make([]uint64, nwords(n))}
	for i := 0; i < n; i++ {
		switch body[i] {
		case '1':
			k := n - 1 - i
			b.w[k/64] |= 1 << (k % 64)
		case '0':
		default:
			return Bits{}, fmt.Errorf("bits: invalid binary digit %q", body[i])
		}
	}
	return b, nil
}

// FromBytes is the bit string of p, 8 bits per byte, MSB first.
func FromBytes(p []byte) Bits {
	n := 8 * len(p)
	b := Bits{n: n, w: make([]uint64, nwords(n))}
	for i, c := range p {
		k := n - 8 - 8*i // value bit index of the byte's LSB
		b.w[k/64] |= uint64(c) << (k % 64)
	}
	return b
}

// FromHex is the first n bits of bytes.fromhex(h), MSB first; h must hold at least n bits.
func FromHex(n int, h string) (Bits, error) {
	p, err := hex.DecodeString(h)
	if err != nil {
		return Bits{}, err
	}
	if 8*len(p) < n {
		return Bits{}, fmt.Errorf("bits: %d hex bytes hold fewer than %d bits", len(p), n)
	}
	return FromBytes(p).Slice(0, n), nil
}

// Len is the width in bits (Python len(body), the chunk's 'bits').
func (b Bits) Len() int { return b.n }

// BitLen is value.bit_length().
func (b Bits) BitLen() int {
	for i := len(b.w) - 1; i >= 0; i-- {
		if b.w[i] != 0 {
			return 64*i + bits.Len64(b.w[i])
		}
	}
	return 0
}

// IsZero reports value == 0.
func (b Bits) IsZero() bool { return b.BitLen() == 0 }

// Uint64 returns the value when it fits 64 bits.
func (b Bits) Uint64() (uint64, bool) {
	if b.BitLen() > 64 {
		return 0, false
	}
	if len(b.w) == 0 {
		return 0, true
	}
	return b.w[0], true
}

// Big returns the value as a new big.Int.
func (b Bits) Big() *big.Int {
	ws := make([]big.Word, len(b.w))
	for i, x := range b.w {
		ws[i] = big.Word(x)
	}
	return new(big.Int).SetBits(ws)
}

// bitAt is the value bit k (0 = least significant).
func (b Bits) bitAt(k int) bool { return b.w[k/64]>>(k%64)&1 != 0 }

// String is the Python bit string: format(v, '0%db' % n); the empty string for n == 0.
func (b Bits) String() string {
	var sb strings.Builder
	sb.Grow(b.n)
	for k := b.n - 1; k >= 0; k-- {
		if b.bitAt(k) {
			sb.WriteByte('1')
		} else {
			sb.WriteByte('0')
		}
	}
	return sb.String()
}

// Bytes is the bit string zero-padded on the right to whole bytes (v3_wire_companions.tobytes, fbgd_write W.bytes).
func (b Bits) Bytes() []byte {
	pad := -b.n & 7
	s := b.Shl(pad)
	p := make([]byte, s.n/8)
	for i := range p {
		k := s.n - 8 - 8*i
		p[i] = byte(s.w[k/64] >> (k % 64))
	}
	return p
}

// Hex is hex(Bytes()), lowercase.
func (b Bits) Hex() string { return hex.EncodeToString(b.Bytes()) }

// extract returns value bits [lo, lo+cnt) as a cnt-bit value.
func (b Bits) extract(lo, cnt int) Bits {
	r := Bits{n: cnt, w: make([]uint64, nwords(cnt))}
	ws, sh := lo/64, uint(lo%64)
	for i := range r.w {
		j := ws + i
		var x uint64
		if j < len(b.w) {
			x = b.w[j] >> sh
		}
		if sh != 0 && j+1 < len(b.w) {
			x |= b.w[j+1] << (64 - sh)
		}
		r.w[i] = x
	}
	r.clip()
	return r
}

// Shr is v >> k keeping the top n-k bits (body[:-k]); k >= n gives the empty body.
func (b Bits) Shr(k int) Bits {
	if k >= b.n {
		return Bits{}
	}
	return b.extract(k, b.n-k)
}

// Shl is v << k with width n+k (v3_assemble: 13-bit value << 4 -> 17 bits).
func (b Bits) Shl(k int) Bits {
	r := Bits{n: b.n + k, w: make([]uint64, nwords(b.n+k))}
	orShifted(r.w, b.w, k)
	return r
}

// Low is v & ((1 << m) - 1) as a min(m, n)-bit value (body[-m:]).
func (b Bits) Low(m int) Bits { return b.extract(0, min(max(m, 0), b.n)) }

// Slice is body[lo:hi] with Python slice rules (negative indices count from the end, out of range clamps).
func (b Bits) Slice(lo, hi int) Bits {
	norm := func(i int) int {
		if i < 0 {
			i += b.n
		}
		return min(max(i, 0), b.n)
	}
	lo, hi = norm(lo), norm(hi)
	if hi <= lo {
		return Bits{}
	}
	return b.extract(b.n-hi, hi-lo)
}

// orShifted does dst |= src << k (dst must be wide enough; overflow is dropped).
func orShifted(dst, src []uint64, k int) {
	ws, sh := k/64, uint(k%64)
	for i, x := range src {
		if j := i + ws; j < len(dst) {
			dst[j] |= x << sh
		}
		if j := i + ws + 1; sh != 0 && j < len(dst) {
			dst[j] |= x >> (64 - sh)
		}
	}
}

// Concat is a + b + ... on bit strings.
func Concat(parts ...Bits) Bits {
	n := 0
	for _, p := range parts {
		n += p.n
	}
	r := Bits{n: n, w: make([]uint64, nwords(n))}
	for _, p := range parts {
		n -= p.n
		orShifted(r.w, p.w, n)
	}
	return r
}

// Append is b + o.
func (b Bits) Append(o Bits) Bits { return Concat(b, o) }

// And is v & o.v; the width is the larger of the two.
func (b Bits) And(o Bits) Bits {
	r := Bits{n: max(b.n, o.n), w: make([]uint64, nwords(max(b.n, o.n)))}
	for i := range r.w {
		if i < len(b.w) && i < len(o.w) {
			r.w[i] = b.w[i] & o.w[i]
		}
	}
	return r
}

// Equal is (bits, value) tuple equality.
func (b Bits) Equal(o Bits) bool {
	if b.n != o.n {
		return false
	}
	for i := range b.w {
		if b.w[i] != o.w[i] {
			return false
		}
	}
	return true
}

// Key is a canonical map key for (bits, value).
func (b Bits) Key() string { return fmt.Sprintf("%d:%s", b.n, b.Hex()) }

// FormatBin is format(v, '0%db' % w): zero-padded to w digits, never truncated, and '0' for v == 0 even at w == 0.
func FormatBin(v uint64, w int) string {
	s := fmt.Sprintf("%b", v)
	if len(s) < w {
		s = strings.Repeat("0", w-len(s)) + s
	}
	return s
}

// FormatBinBig is FormatBin for an unbounded non-negative int.
func FormatBinBig(v *big.Int, w int) string {
	s := v.Text(2)
	if len(s) < w {
		s = strings.Repeat("0", w-len(s)) + s
	}
	return s
}
