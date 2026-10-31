package gate

import (
	"encoding/binary"
	"os"
	"slices"

	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/pyc"
)

// exts is fbgd_gate.EXTS: the companion files a demo must have.
var exts = []string{"fbgk", "fbgdd", "fbgdk", "fbcdd", "fbcdk", "fbidd", "fbidk", "txt"}

// companionKey is v3_wire_companions.KEY: a key record every 450 deltas.
const companionKey = 450

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// companions is fbgd_gate.companions(path, s): missing files, the .fbgk key index, the destruction companion replay.
func (g *gate) companions(path string, s *scanResult) []*pyc.Object {
	var errs []*pyc.Object
	for _, ext := range exts {
		if !isFile(withSuffix(path, "."+ext)) {
			errs = append(errs, obj("rule", "missing-companion", "extension", ext))
		}
	}
	if p := withSuffix(path, ".fbgk"); isFile(p) {
		data, err := os.ReadFile(p)
		if err == nil {
			if len(data) < 4 || (len(data)-4)%12 != 0 {
				errs = append(errs, obj("rule", "invalid-key-index-size"))
			} else {
				offsets := make([]int, 0, (len(data)-4)/12)
				for i := 4; i < len(data); i += 12 {
					offsets = append(offsets, int(binary.LittleEndian.Uint32(data[i+8:])))
				}
				if !slices.Equal(offsets, s.keys) {
					errs = append(errs, obj("rule", "invalid-key-index-offsets", "expected", nonNil(s.keys), "actual", offsets))
				}
			}
		}
	}
	if base, ok := companionSet(path); ok {
		e, _ := g.companionReplay(base)
		errs = append(errs, e...)
	}
	return errs
}

func nonNil(x []int) []int {
	if x == nil {
		return []int{}
	}
	return x
}

// companionSet is fbgd_gate.companion_set: the base stem when .fbcdd, .fbgdd and .fbidd all exist.
func companionSet(path string) (string, bool) {
	base := withSuffix(path, "")
	for _, e := range []string{"fbcdd", "fbgdd", "fbidd"} {
		if !isFile(withSuffix(base, "."+e)) {
			return "", false
		}
	}
	return base, true
}

type replayResult struct{ errors, warnings []*pyc.Object }

// companionReplay is fbgd_gate.companion_replay(base) (memoized per run like its lru_cache).
func (g *gate) companionReplay(base string) ([]*pyc.Object, []*pyc.Object) {
	if r, ok := g.replays[base]; ok {
		return r.errors, r.warnings
	}
	var r replayResult
	findings, err := resets(base)
	if err != nil {
		detail := err.Repr()
		if len(detail) > 160 {
			detail = detail[:160]
		}
		r.errors = []*pyc.Object{obj("rule", "companion-parse", "detail", detail)}
	} else {
		for _, f := range findings {
			rule, _ := f.Get("rule")
			delta, _ := f.Get("delta")
			if rule == "companion-reset" && delta == any(0) {
				r.warnings = append(r.warnings, with(f, "rule", "companion-reset-key0"))
			} else {
				r.errors = append(r.errors, f)
			}
		}
	}
	g.replays[base] = r
	return r.errors, r.warnings
}

// bitStr is v3_wire_destruction.Bits over a '0'/'1' record body kept as bytes + bit length.
type bitStr struct {
	b []byte
	n int
	p int
}

var errEOF = &fbgd.PyError{Class: "EOFError"}
var errAssert = &fbgd.PyError{Class: "AssertionError"}

func (s *bitStr) r(k int) (uint64, *fbgd.PyError) {
	if s.p+k > s.n {
		return 0, errEOF
	}
	var v uint64
	for i := 0; i < k; i++ {
		q := s.p + i
		v = v<<1 | uint64(s.b[q>>3]>>(7-q&7)&1)
	}
	s.p += k
	return v, nil
}

type record struct {
	marker int
	bits   bitStr
}

// records is v3_wire_destruction.records(path): header bits and the (marker, bits) records.
func records(path string) (bitStr, []record, *fbgd.PyError) {
	d, err := os.ReadFile(path)
	if err != nil {
		return bitStr{}, nil, &fbgd.PyError{Class: "OSError", Msg: err.Error()}
	}
	short := &fbgd.PyError{Class: "error", Msg: "unpack requires a buffer of 8 bytes"}
	if len(d) < 48 {
		return bitStr{}, nil, short
	}
	tobits := func(raw []byte, k int) bitStr { return bitStr{b: raw, n: min(k, 8*len(raw))} }
	n := int(binary.LittleEndian.Uint32(d[40:]))
	nb := int(binary.LittleEndian.Uint32(d[44:]))
	hdr := tobits(d[48:min(48+n, len(d))], nb)
	p := 48 + n
	var out []record
	for p < len(d) && d[p] != 2 {
		if p+9 > len(d) {
			return bitStr{}, nil, short
		}
		sz := int(binary.LittleEndian.Uint32(d[p+1:]))
		nb := int(binary.LittleEndian.Uint32(d[p+5:]))
		out = append(out, record{int(d[p]), tobits(d[p+9:min(p+9+sz, len(d))], nb)})
		p += 9 + sz
	}
	return hdr, out, nil
}

func bitLength(x int) int {
	if x < 0 {
		x = -x
	}
	n := 0
	for x > 0 {
		n++
		x >>= 1
	}
	return n
}

type compCfg struct {
	idWidth int
	entries map[int][2]int // id -> (elements, values)
}

// configBits is v3_wire_destruction.config_bits (only the composite and group parts the replay uses).
func configBits(hc, hg, hi bitStr) (compCfg, []int, *fbgd.PyError) {
	var c compCfg
	h := hc
	nn, e := h.r(32)
	if e != nil {
		return c, nil, e
	}
	N := int(nn)
	c.entries = map[int][2]int{}
	for i := range N {
		var ab [2]int
		for j := range 2 {
			w, e := h.r(5)
			if e != nil {
				return c, nil, e
			}
			v, e := h.r(int(w))
			if e != nil {
				return c, nil, e
			}
			ab[j] = int(v) + 1
		}
		c.entries[i] = ab
	}
	if h.p != h.n {
		return c, nil, errAssert
	}
	c.idWidth = bitLength(N - 1)
	g := hg
	cnt, e := g.r(32)
	if e != nil {
		return c, nil, e
	}
	var G []int
	for range cnt {
		v, e := g.r(32)
		if e != nil {
			return c, nil, e
		}
		G = append(G, int(v))
	}
	// .fbidd header: u5 frac, u5 n, n x u5, u5 magnitude, f31, u32 N (read for its exceptions only)
	i := hi
	if _, e := i.r(5); e != nil {
		return c, nil, e
	}
	nv, e := i.r(5)
	if e != nil {
		return c, nil, e
	}
	for range nv {
		if _, e := i.r(5); e != nil {
			return c, nil, e
		}
	}
	for _, w := range []int{5, 31, 32} {
		if _, e := i.r(w); e != nil {
			return c, nil, e
		}
	}
	return c, G, nil
}

// resets is v3_wire_companions.resets(base): replay the .fbgdd group deltas against its keys.
func resets(base string) ([]*pyc.Object, *fbgd.PyError) {
	var hdr [3]bitStr
	var recs [3][]record
	for i, e := range []string{"fbcdd", "fbgdd", "fbidd"} {
		h, r, err := records(base + "." + e)
		if err != nil {
			return nil, err
		}
		hdr[i], recs[i] = h, r
	}
	cc, G, err := configBits(hdr[0], hdr[1], hdr[2])
	if err != nil {
		return nil, err
	}
	// keys = records(fbgdd) again, companion() = records(fbcdd), records(fbgdd): same bytes, same result
	var keys []bitStr
	for _, r := range recs[1] {
		if r.marker == 0 {
			keys = append(keys, r.bits)
		}
	}
	// composite deltas (parsed for their exceptions; the replay uses the group deltas only)
	for _, r := range recs[0] {
		if r.marker != 1 {
			continue
		}
		b := r.bits
		if _, e := b.r(31); e != nil {
			return nil, e
		}
		n, e := b.r(32)
		if e != nil {
			return nil, e
		}
		for range n {
			id, e := b.r(cc.idWidth)
			if e != nil {
				return nil, e
			}
			av, ok := cc.entries[int(id)]
			if !ok {
				return nil, fbgd.KeyError(int(id))
			}
			iw, vw := bitLength(av[0]-1), bitLength(av[1]-1)
			k, e := b.r(iw)
			if e != nil {
				return nil, e
			}
			for range k + 1 {
				if _, e := b.r(iw); e != nil {
					return nil, e
				}
				if _, e := b.r(vw); e != nil {
					return nil, e
				}
			}
		}
		if b.p != b.n {
			return nil, errAssert
		}
	}
	type gev struct{ g, j int }
	var gc [][]gev
	for _, r := range recs[1] {
		if r.marker != 1 {
			continue
		}
		b := r.bits
		if _, e := b.r(31); e != nil {
			return nil, e
		}
		var ev []gev
		for g := range G {
			f, e := b.r(1)
			if e != nil {
				return nil, e
			}
			if f == 0 {
				continue
			}
			n, e := b.r(32)
			if e != nil {
				return nil, e
			}
			for range n {
				j, e := b.r(32)
				if e != nil {
					return nil, e
				}
				ev = append(ev, gev{g, int(j)})
			}
		}
		if b.p != b.n {
			return nil, errAssert
		}
		gc = append(gc, ev)
	}
	var grp []map[int]bool
	var out []*pyc.Object
	k := 0
	for L := range gc {
		if L%companionKey == 0 {
			if k >= len(keys) {
				return nil, &fbgd.PyError{Class: "IndexError", Msg: "list index out of range"}
			}
			st, e := gkeyDec(keys[k], G)
			if e != nil {
				return nil, e
			}
			k++
			if grp != nil {
				var diff []int
				for g := range G {
					if !sameSet(st[g], grp[g]) {
						diff = append(diff, g)
					}
				}
				if len(diff) > 0 {
					out = append(out, obj("rule", "companion-key-mismatch", "key", k-1, "delta", L,
						"groups", nonNil(diff[:min(8, len(diff))])))
				}
			}
			grp = st
		}
		for _, x := range gc[L] {
			if grp[x.g][x.j] {
				out = append(out, obj("rule", "companion-reset", "delta", L, "group", x.g, "part", x.j))
			} else {
				grp[x.g][x.j] = true
			}
		}
	}
	return out, nil
}

// gkeyDec is v3_wire_companions.gkey_dec(s, G).
func gkeyDec(s bitStr, G []int) ([]map[int]bool, *fbgd.PyError) {
	b := s
	st := make([]map[int]bool, 0, len(G))
	for _, size := range G {
		w, e := b.r(5)
		if e != nil {
			return nil, e
		}
		n, e := b.r(int(w) + 1)
		if e != nil {
			return nil, e
		}
		w2, e := b.r(6)
		if e != nil {
			return nil, e
		}
		nb, e := b.r(int(w2) + 1)
		if e != nil {
			return nil, e
		}
		nw := (nb + 31) / 32
		if nb > 1<<40 || int(nw)*32 > b.n-b.p { // Python reads words until EOFError
			return nil, errEOF
		}
		words := make([]uint64, nw)
		for i := range words {
			if words[i], e = b.r(32); e != nil {
				return nil, e
			}
		}
		set := map[int]bool{}
		for j := range int(nb) {
			if words[j/32]>>(j%32)&1 == 1 {
				set[j] = true
			}
		}
		st = append(st, set)
		if int(nb) != size || int(n) != len(set) {
			return nil, errAssert
		}
	}
	if b.p != b.n {
		return nil, errAssert
	}
	return st, nil
}

func sameSet(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// withSuffix is pathlib's with_suffix on a demo path (suffix "" strips it).
func withSuffix(p, suffix string) string {
	i := len(p) - 1
	for i >= 0 && p[i] != '.' && p[i] != '/' && p[i] != '\\' {
		i--
	}
	if i > 0 && p[i] == '.' && p[i-1] != '/' && p[i-1] != '\\' {
		return p[:i] + suffix
	}
	return p + suffix
}
