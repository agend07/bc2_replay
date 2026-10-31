package gate

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"bc2replay/builder/internal/fbgd"
)

// This file ports v3_src_creates.py (r192/r207 builder self-check crebase_vs_src) and the origin-zero part of
// v3_create_rebase.rebase_create it uses to read create positions.

var srcTypes = []int{3, 6, 31}

const marginFrac, marginMin = 0.10, 10.0

var rebaseTable = []int{1, 3, 5, 7, 9, 11, 13}

// vtype is v3_src_creates.vtype: the variable-width create type (3, 6, 31 with flag bit 6) or -1.
func vtype(c *fbgd.Chunk) int {
	if c.Bits < 19 {
		return -1
	}
	k := int(c.Top(6))
	if fbgd.VariableCreateTypes[k] && c.Top(7)&1 == 1 {
		return k
	}
	return -1
}

// pos is one positions() result: the 19-bit header and the origin-zero xyz of every table-coded vec3.
type pos struct {
	header string
	xyz    [][3]float64
}

func (p *pos) key() string {
	var sb strings.Builder
	sb.WriteString(p.header)
	for _, v := range p.xyz {
		for _, x := range v {
			sb.WriteByte('|')
			sb.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
		}
	}
	return sb.String()
}

// pyInt2 is int(s, 2) (ValueError on an empty string).
func pyInt2(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 2, 64)
	return v, err == nil
}

// slice is Python's s[a:b] for 0 <= a, b.
func slice(s string, a, b int) string {
	a, b = min(a, len(s)), min(b, len(s))
	if a >= b {
		return ""
	}
	return s[a:b]
}

// readVec is v3_create_rebase._read_vec at origin zero: (end, xyz, idx); ok false = ValueError.
func readVec(bits string, i, C int) (int, [3]float64, int, bool) {
	var xyz [3]float64
	idx, ok := pyInt2(slice(bits, i, i+3))
	if !ok {
		return 0, xyz, 0, false
	}
	i += 3
	if idx == 0 {
		return i + 96, xyz, 0, true
	}
	w := rebaseTable[idx-1] + C
	for a := range 3 {
		v, ok := pyInt2(slice(bits, i, i+w))
		if !ok {
			return 0, xyz, 0, false
		}
		i += w
		if v>>(w-1) != 0 {
			v -= 1 << w
		}
		xyz[a] = float64(v) / float64(int64(1)<<C)
	}
	return i, xyz, int(idx), true
}

// positions is v3_src_creates.positions on a create bit string: nil, ok=false = None (layout does not parse);
// nil, ok=true = () (no table-coded vec3).
func positions(cb string) (*pos, bool) {
	if len(cb) < 19 || cb[6] != '1' {
		return nil, true
	}
	kind, _ := pyInt2(cb[:6])
	p := &pos{header: cb[:19]}
	add := func(xyz [3]float64, idx int) {
		if idx != 0 {
			p.xyz = append(p.xyz, xyz)
		}
	}
	i := 19
	switch kind {
	case 3: // vehicle: vec3 C=5, rest copied
		_, xyz, idx, ok := readVec(cb, i, 5)
		if !ok {
			return nil, false
		}
		add(xyz, idx)
	case 31: // spawner: quaternion N=8 (w-sign + 3 x (present [+8])), vec3 C=10, end
		j := i + 1
		for range 3 {
			if j >= len(cb) {
				return nil, false
			}
			if cb[j] == '1' {
				j += 9
			} else {
				j++
			}
		}
		end, xyz, idx, ok := readVec(cb, j, 10)
		if !ok || end != len(cb) {
			return nil, false
		}
		add(xyz, idx)
	case 6: // projectile
		if i >= len(cb) {
			return nil, false
		}
		p1 := cb[i]
		i++
		if p1 == '1' {
			end, xyz, idx, ok := readVec(cb, i, 5)
			if !ok {
				return nil, false
			}
			add(xyz, idx)
			i = end + 4
		}
		i += 48
		if i >= len(cb) {
			return nil, false
		}
		p2 := cb[i]
		i++
		if p2 == '1' {
			end, xyz, idx, ok := readVec(cb, i, 5)
			if !ok {
				return nil, false
			}
			add(xyz, idx)
			i = end
		}
		i++
		if i != len(cb) {
			return nil, false
		}
	default:
		return nil, true
	}
	if len(p.xyz) == 0 {
		return nil, true
	}
	return p, true
}

// SrcIndex is v3_src_creates.src_index: the (bits, value) of every variable-class SRC create and their positions.
type SrcIndex struct {
	exact map[string]bool
	pos   map[string]*pos
}

func exactKey(bits int, cb string) string { return strconv.Itoa(bits) + ":" + cb }

func (x *SrcIndex) add(bits int, cb string) {
	x.exact[exactKey(bits, cb)] = true
	if q, ok := positions(cb); ok && q != nil {
		x.pos[q.key()] = q
	}
}

// SrcIndexBytes is v3_src_creates.src_index_bytes(data).
func SrcIndexBytes(data []byte) (*SrcIndex, error) {
	x := &SrcIndex{map[string]bool{}, map[string]*pos{}}
	err := walk(data, func(_ int, _ string, g *fbgd.Ghost, _ bool) {
		if vtype(&g.Create) >= 0 {
			x.add(g.Create.Bits, g.Create.BitString())
		}
	})
	if err != nil {
		return nil, err
	}
	return x, nil
}

// PackCreate is one map-pack 'creates' row [bits, hexvalue].
type PackCreate struct {
	Bits int
	Hex  string
}

// SrcIndexCreates is mappack.creates(entry): the same index from the map pack's create rows.
func SrcIndexCreates(rows []PackCreate) (*SrcIndex, error) {
	x := &SrcIndex{map[string]bool{}, map[string]*pos{}}
	for _, r := range rows {
		v, ok := new(big.Int).SetString(r.Hex, 16)
		if !ok || v.Sign() < 0 {
			return nil, fmt.Errorf("map pack create %q is not hex", r.Hex)
		}
		// bits_of = bin(v | 1 << bits)[3:]
		t := new(big.Int).Or(v, new(big.Int).Lsh(big.NewInt(1), uint(r.Bits))).Text(2)[1:]
		b := v.Text(2)
		if v.Sign() == 0 {
			b = ""
		}
		x.exact[exactKey(r.Bits, strings.Repeat("0", max(0, r.Bits-len(b)))+b)] = true
		if q, ok := positions(t); ok && q != nil {
			x.pos[q.key()] = q
		}
	}
	return x, nil
}

// walk is v3_src_creates.walk: f(frame, kind, ghost, mid) for every keyframe ghost and delta NEW.
func walk(data []byte, f func(frame int, kind string, g *fbgd.Ghost, mid bool)) error {
	_, raw, err := fbgd.Frames(data)
	if err != nil {
		return err
	}
	st := fbgd.State{}
	for i, fr := range raw {
		r := fbgd.NewReader(fr.Payload, fr.BitPos)
		if fr.Marker == 1 {
			gl, err := fbgd.Keyframe(r)
			if err != nil {
				return err
			}
			for _, g := range gl {
				_, live := st[g.ID]
				f(i, "key", g, i > 0 && !live)
			}
			st = fbgd.State{}
			for _, g := range gl {
				st[g.ID] = g.Counts()
			}
			continue
		}
		d, err := fbgd.Delta(r, st)
		if err != nil {
			return err
		}
		for _, x := range d.Dirty {
			if x.New {
				f(i, "new", x.Ghost, true)
			}
		}
	}
	return nil
}

type srcRow struct {
	frame, id int
	sig       string
	bits      int
	phase     string
}

// SrcResult is v3_src_creates.check's result.
type SrcResult struct {
	matched, unmatched     map[string]map[int]int
	posonly, novec         map[int]int
	rows                   []srcRow
	FirstSoldier           int // -1 = None
	Paired                 bool
	Plausible, Implausible int
	// Edge (r295) counts the unmatched type-31 creates that lie outside the SRC box by at most edgeBand on every axis
	// (a base the empty-server SRC round never spawned: MP_007 lake-level cluster 12-13 m below the box, origin applied);
	// EdgeMax is their largest excess. Un-rebased rows are >= 60 m off (r207), so they stay implausible.
	Edge    int
	EdgeMax float64
}

// edgeBand is the tolerance (m) outside the SRC box that still counts as plausible (r295; r207: un-rebased >= 60 m).
const edgeBand = 30.0

// CheckSrcCreates is v3_src_creates.check(data, src, paired).
func CheckSrcCreates(data []byte, src *SrcIndex, paired bool) (*SrcResult, error) {
	res := &SrcResult{
		matched:   map[string]map[int]int{"load": {}, "game": {}},
		unmatched: map[string]map[int]int{"load": {}, "game": {}},
		posonly:   map[int]int{}, novec: map[int]int{}, FirstSoldier: -1, Paired: paired,
	}
	type pend struct {
		frame int
		g     *fbgd.Ghost
	}
	var pending []pend
	err := walk(data, func(i int, _ string, g *fbgd.Ghost, mid bool) {
		if res.FirstSoldier < 0 && mid && fbgd.Soldier(g) {
			res.FirstSoldier = i
		}
		if mid && vtype(&g.Create) >= 0 {
			pending = append(pending, pend{i, g})
		}
	})
	if err != nil {
		return nil, err
	}
	var box *[2][3]float64
	if !paired {
		box = bbox(src)
	}
	fs := res.FirstSoldier
	for _, p := range pending {
		c := &p.g.Create
		t := vtype(c)
		ph := "game"
		if fs >= 0 && p.frame < fs {
			ph = "load"
		}
		cb := c.BitString()
		q, ok := positions(cb)
		if ok && q == nil {
			res.novec[t]++
			continue
		}
		if src.exact[exactKey(c.Bits, cb)] {
			res.matched[ph][t]++
			continue
		}
		res.unmatched[ph][t]++
		if q != nil && src.pos[q.key()] != nil && ph == "game" {
			res.posonly[t]++
		}
		if !paired && t == 31 {
			switch ex, ok := excess(q, box); {
			case ok && ex == 0:
				res.Plausible++
			case ok && ex <= edgeBand:
				res.Edge++
				res.EdgeMax = max(res.EdgeMax, ex)
			default:
				res.Implausible++
			}
		}
		res.rows = append(res.rows, srcRow{p.frame, p.g.ID, fbgd.Signature(p.g), c.Bits, ph})
	}
	return res, nil
}

// bbox is v3_src_creates.bbox(pos): per-axis (lo, hi) of every SRC xyz widened by max(frac * extent, min); nil when empty.
func bbox(src *SrcIndex) *[2][3]float64 {
	var box [2][3]float64
	n := 0
	for _, q := range src.pos {
		for _, p := range q.xyz {
			for a := range 3 {
				if n == 0 || p[a] < box[0][a] {
					box[0][a] = p[a]
				}
				if n == 0 || p[a] > box[1][a] {
					box[1][a] = p[a]
				}
			}
			n++
		}
	}
	if n == 0 {
		return nil
	}
	for a := range 3 {
		m := max(float64(marginFrac*float64(box[1][a]-box[0][a])), marginMin)
		box[0][a] -= m
		box[1][a] += m
	}
	return &box
}

// excess is the largest per-axis distance of q's vec3s outside box (0 = plausible(q, box) in v3_src_creates);
// ok false when the layout did not parse or there is no box.
func excess(q *pos, box *[2][3]float64) (float64, bool) {
	if q == nil || box == nil {
		return 0, false
	}
	ex := 0.0
	for _, p := range q.xyz {
		for a := range 3 {
			ex = max(ex, box[0][a]-p[a], p[a]-box[1][a])
		}
	}
	return ex, true
}

func counterRepr(m map[int]int) string {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d: %d", k, m[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// Line is v3_src_creates.line(res, first): the one-line crebase_vs_src summary.
func (r *SrcResult) Line(first int) string {
	per := func(ph string) string {
		parts := make([]string, len(srcTypes))
		for i, t := range srcTypes {
			parts[i] = fmt.Sprintf("t%d %d/%d", t, r.matched[ph][t], r.unmatched[ph][t])
		}
		return strings.Join(parts, " ")
	}
	sum := func(m map[int]int) int {
		n := 0
		for _, v := range m {
			n += v
		}
		return n
	}
	rows := slices.Clone(r.rows)
	slices.SortStableFunc(rows, func(a, b srcRow) int {
		ka, kb := !strings.HasPrefix(a.sig, "011111"), !strings.HasPrefix(b.sig, "011111")
		if ka != kb {
			if !ka {
				return -1
			}
			return 1
		}
		return a.frame - b.frame
	})
	if first < 0 {
		first = max(0, len(rows)+first)
	}
	rows = rows[:min(first, len(rows))]
	parts := make([]string, len(rows))
	for i, x := range rows {
		parts[i] = fmt.Sprintf("(%d, %d, %s, %d, %s)", x.frame, x.id, fbgd.StrRepr(x.sig), x.bits, fbgd.StrRepr(x.phase))
	}
	up := ""
	if !r.Paired {
		up = fmt.Sprintf(" unpaired t31 plausible %d implausible %d", r.Plausible, r.Implausible)
		if r.Edge > 0 {
			up += fmt.Sprintf(" edge %d (max %.1f m outside the SRC box, band %.0f m)", r.Edge, r.EdgeMax, edgeBand)
		}
	}
	fs := "None"
	if r.FirstSoldier >= 0 {
		fs = strconv.Itoa(r.FirstSoldier)
	}
	return fmt.Sprintf("crebase_vs_src matched %d / unmatched %d (game %s; load %s; matched/unmatched) posonly %s novec %s "+
		"first_soldier %s%s first unmatched (frame, id, sig, width, phase) [%s]",
		sum(r.matched["game"]), sum(r.unmatched["game"]), per("game"), per("load"),
		counterRepr(r.posonly), counterRepr(r.novec), fs, up, strings.Join(parts, ", "))
}

// ResolveSrc is v3_src_creates.resolve(src): a .fbgd path as is, a path stem + '.fbgd', a bare stem in BC2_DEMOS_DIR.
func ResolveSrc(src string) string {
	if filepath.Ext(src) == ".fbgd" {
		return src
	}
	if strings.ContainsAny(src, `/\`) {
		return src + ".fbgd"
	}
	demos := os.Getenv("BC2_DEMOS_DIR") // r278: else <archive>/demos (BC2_ARCHIVE_DIR or the builder-relative default)
	if demos == "" {
		demos = filepath.Join(DefaultArchive(), "demos")
	}
	return filepath.Join(demos, src+".fbgd")
}

// Rows is res['rows'] in check order, each as Python prints the tuple: (frame, id, 'sig', width, 'phase').
func (r *SrcResult) Rows() []string {
	out := make([]string, len(r.rows))
	for i, x := range r.rows {
		out[i] = fmt.Sprintf("(%d, %d, %s, %d, %s)", x.frame, x.id, fbgd.StrRepr(x.sig), x.bits, fbgd.StrRepr(x.phase))
	}
	return out
}
