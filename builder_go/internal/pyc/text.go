package pyc

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// DecodeUTF8Replace is b.decode('utf-8', 'replace'): each maximal subpart of an ill-formed sequence becomes one
// U+FFFD (Unicode Table 3-7, as CPython does). Go's utf8.DecodeRune would emit one U+FFFD per byte instead.
func DecodeUTF8Replace(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			sb.WriteByte(c)
			i++
			continue
		}
		n, lo, hi := 0, byte(0x80), byte(0xbf) // sequence length and the allowed range of the second byte
		switch {
		case c >= 0xc2 && c <= 0xdf:
			n = 2
		case c == 0xe0:
			n, lo = 3, 0xa0
		case c == 0xed:
			n, hi = 3, 0x9f
		case c >= 0xe1 && c <= 0xef:
			n = 3
		case c == 0xf0:
			n, lo = 4, 0x90
		case c == 0xf4:
			n, hi = 4, 0x8f
		case c >= 0xf1 && c <= 0xf3:
			n = 4
		}
		if n == 0 { // 80..C1, F5..FF: never a lead byte
			sb.WriteRune(utf8.RuneError)
			i++
			continue
		}
		j := i + 1
		for k := 1; k < n && j < len(b); k++ {
			x := b[j]
			if k == 1 && (x < lo || x > hi) || k > 1 && (x < 0x80 || x > 0xbf) {
				break
			}
			j++
		}
		if j-i == n {
			sb.Write(b[i:j])
		} else {
			sb.WriteRune(utf8.RuneError)
		}
		i = j
	}
	return sb.String()
}

// NFKDASCII is unicodedata.normalize('NFKD', s).encode('ascii', 'ignore').decode('ascii'), from a per-code-point
// table generated from the frozen CPython (see nfkd_table.go for the Unicode version).
func NFKDASCII(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r < 0x80 {
			sb.WriteByte(byte(r))
			continue
		}
		i := sort.Search(len(nfkdCP), func(i int) bool { return nfkdCP[i] >= uint32(r) })
		if i < len(nfkdCP) && nfkdCP[i] == uint32(r) {
			sb.WriteString(nfkdASCII[nfkdOff[i]:nfkdOff[i+1]])
		}
	}
	return sb.String()
}
