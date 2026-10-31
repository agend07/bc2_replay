package sections

import (
	"fmt"

	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/golden"
	"bc2replay/builder/internal/pyc"
)

// Port of v3_vehsec (r160..r190): per-vt Python codecs for the sections of the physics vehicle class 0x1436cbc, any
// layout. A layout is walkable when its section count is in Layouts and every section vt has a codec here (PY) or
// is one of the native-runner vts (wire.RunSection). Property counts come from the demo template of the layout: a vt
// reader has a fixed set of specific properties and, when the section object asks for it, two base properties
// first (p0 1[+2], p1 1); count = spec or spec + 2 decides. Codec notes (bytes): see the Python module docstring.

// Section vts.
const (
	VTGhost  = 0x1436cbc
	VTPhys   = 0x1430b84
	VTS2     = 0x142f1f4
	VTNop3   = 0x143010c
	VTNop4   = 0x1430524
	VTWheel  = 0x1434404
	VTP01    = 0x1430a10
	VTS13    = 0x1431010
	VTS28    = 0x1430d24
	VTTurret = 0x143459c
	VTTWheel = 0x142ea38 // r184: wheel subclass of the /68 /76 tracked vehicles (same reader 8B6A00 as VTWheel)
	VTS314   = 0x1430314 // r160 bytes 7E8900: p0 = 1 bit, no base pair (/15 only)
)

var (
	// PY are the vts with a Python codec; Native the ones the caller runs natively.
	PY     = map[uint32]bool{VTPhys: true, VTS2: true, VTWheel: true, VTTWheel: true, VTP01: true, VTS13: true, VTS28: true, VTTurret: true, VTS314: true}
	Native = map[uint32]bool{VTGhost: true, VTNop3: true, VTNop4: true}
	spec   = map[uint32]int{VTPhys: 2, VTS2: 1, VTWheel: 1, VTTWheel: 1, VTP01: 0, VTS13: 1, VTTurret: 1, VTS314: 1} // S28: 1 or 2
	// WheelCfg is WHEEL_CFG: (section count, section index) -> wheel blob cfg (default 0).
	WheelCfg = map[[2]int]int{{29, 5}: 1, {29, 6}: 1, {40, 6}: 1, {40, 7}: 1, {54, 6}: 1, {54, 7}: 1, {35, 12}: 1, {35, 13}: 1}
	// Layouts are the verified section counts (LAYOUTS).
	// r295: + /5, /61, /80 (White Pass MP_007 cap 52296 round 2: jet-ski-like /5 v634, tracked /61 v309 and /80 v334 with 14/16 VTTWheel sections).
	Layouts = map[int]bool{29: true, 37: true, 10: true, 15: true, 40: true, 14: true, 4: true, 7: true, 8: true, 35: true, 54: true, 68: true, 76: true, 13: true, 45: true, 47: true, 5: true, 61: true, 80: true}
	// physShape is PHYS_SHAPE: layout -> s1.p2 shape (absent = StdShape).
	physShape = map[int]PhysShape{14: {false, 0}, 4: {false, 0}, 7: {false, 0}, 8: {false, 0}, 13: {false, 0}, 68: {true, 4}, 76: {true, 4},
		ShapeB7: {true, 2}}
)

// ShapeB7 (r300f) is the layout key of the second 7-section vehicle shape [ghost phys 0x142f1f4 0x143010c 0x1431010
// 0x1431010 0x1430524] (Isla Inocentes /7 v377, Oasis /7 v419): its physics p2 carries the two sparse vectors and a
// 2-bit inner blob (offline oracle: 599/600 + 594/600 wire updates end exactly on the next entry; the verified /7 shape
// [ghost phys 0x1431010 x3 0x1430d24 0x143010c] has neither). Only physShape is keyed by it; Layouts / WheelCfg use 7.
const ShapeB7 = 1007

// LayoutKey is the physShape / WheelCfg key of a section list: its length, or ShapeB7 for the second /7 shape.
func LayoutKey(vts []uint32) int {
	if len(vts) == 7 && vts[2] == VTS2 {
		return ShapeB7
	}
	return len(vts)
}

// Walkable is walkable(vts): first section the ghost section, a verified layout, every vt PY or Native.
func Walkable(vts []uint32) bool {
	if len(vts) == 0 || vts[0] != VTGhost || !Layouts[len(vts)] {
		return false
	}
	for _, v := range vts {
		if !PY[v] && !Native[v] {
			return false
		}
	}
	return true
}

// PropBody is one present property of a section: index and body bits.
type PropBody struct {
	P    int
	Body string
}

// SectionResult is section()'s (chunks {pi: body}, end, info); P2 nil = info {} (no physics p2 decoded).
type SectionResult struct {
	Chunks []PropBody
	End    int
	P2     *P2
}

// PyValue is the 3-tuple (int-keyed chunk dict as $dict).
func (s *SectionResult) PyValue() any {
	var ch any = pyc.NewObject() // an empty dict encodes as {} (no non-str key)
	if len(s.Chunks) > 0 {
		ps := make(golden.Pairs, len(s.Chunks))
		for i, c := range s.Chunks {
			ps[i] = golden.Pair{K: c.P, V: c.Body}
		}
		ch = ps
	}
	info := pyc.NewObject()
	if s.P2 != nil {
		info.Set("p2", s.P2)
	}
	return []any{ch, s.End, info}
}

type fixup struct {
	a, b int
	nb   string
}

func wheel(w *rd, cfg int) {
	if cfg != 0 {
		w.opt(3)
	}
	if w.u(1) != 0 {
		w.opt(8)
		w.opt(5)
		if w.u(1) != 0 {
			n := int(w.u(1))
			w.u(n*8 + 8)
		}
	}
	w.opt(5)
}

// s28P0 is 8026D0 + 7F03D0; the optional vec3 (C=7) is origin-relative -> its origin-zero form appended to out.
func s28P0(w *rd, origin [3]float64, out *[]fixup) {
	v := w.u(3)
	w.u(1)
	two := v == 1 || v == 2
	if two {
		w.u(5)
	}
	w.opt(8)
	if two && w.u(1) != 0 {
		a := w.i
		nb, end, _ := rebaseVec(w.b, a, 7, origin, true)
		w.i = end
		if out != nil {
			*out = append(*out, fixup{a, end, nb})
		}
		p := &rd{b: w.b, i: w.i, parseFirst: true}
		p.quat(14)
		w.i = p.i
	}
	w.u(1)
}

func s28P1(w *rd, table []int64) {
	if w.u(1) != 0 && w.u(1) != 0 {
		w.opt(16)
		if w.u(1) != 0 {
			p := &rd{b: w.b, i: w.i, parseFirst: true}
			p.sparse(table, 5)
			w.i = p.i
		}
		w.opt(24)
		w.opt(3)
	}
}

// Section is v3_vehsec.section(bits, cur, vt, count, origin, table, cfg, rebase, tpl, layout): parse one present
// section (cur = right after its presence bit). Physics p2 is converted to the demo form when rebase (the s28 vec3
// likewise). tpl is the template section (nil = None): 0x1431010's specific property is path B (10 bits) when the
// last template default is 10 bits wide, else path A. layout 0 = None. Errors are Python exceptions (*PyError); the
// WIREMULTI caller catches only ValueError.
func Section(bits string, cur int, vt uint32, count int, origin [3]float64, table []int64, cfg int, rebase bool,
	tpl *fbgd.TplSection, layout int) (res *SectionResult, err error) {
	defer catch(&err)
	if !PY[vt] {
		raise("ValueError", "vt %x needs the native runner", vt)
	}
	if vt == VTTWheel {
		vt = VTWheel
	}
	var sp int
	if vt == VTS28 {
		sp = 1
		if count == 2 || count == 4 {
			sp = 2
		}
	} else {
		sp = spec[vt]
	}
	if (vt == VTWheel || vt == VTS13) && (count == 0 || count == 2) {
		sp = 0 // r184 8B27C0 / r172 7EBB30: base-only forms
	}
	if count != sp && count != sp+2 {
		raise("ValueError", "vt %x count %d", vt, count)
	}
	w := &rd{b: bits, i: cur}
	res = &SectionResult{}
	pi := 0
	var fix []fixup
	prop := func(fn func()) {
		s := w.i
		if w.u(1) != 0 {
			fn()
			body := slice(bits, s+1, w.i)
			for _, f := range fix { // same width: table index kept
				body = slice(body, 0, f.a-s-1) + f.nb + slice(body, f.b-s-1, len(body))
			}
			res.Chunks = append(res.Chunks, PropBody{pi, body})
		}
		fix = fix[:0]
		pi++
	}
	if count == sp+2 {
		prop(func() { w.u(2) })
		prop(func() {})
	}
	switch vt {
	case VTPhys:
		if w.u(1) != 0 {
			sh, ok := physShape[layout]
			if !ok {
				sh = StdShape
			}
			var body string
			var r *P2
			if rebase {
				body, r = toDemoForm(bits, w.i, origin, table, sh)
			} else {
				r = parseP2(bits, w.i, origin, table, sh)
				body = slice(bits, w.i, r.End)
			}
			res.Chunks = append(res.Chunks, PropBody{2, body})
			res.P2 = r
			w.i = r.End
		}
		pi = 3
		prop(func() { w.i = parseP3(bits, w.i, table) })
	case VTS2:
		prop(func() { w.opt(16) })
	case VTWheel:
		if sp != 0 {
			prop(func() { wheel(w, cfg) })
		}
	case VTS13:
		switch {
		case sp == 0:
		case tpl != nil && len(tpl.Defaults) > 0 && tpl.Defaults[len(tpl.Defaults)-1].Value.Len() == 10:
			prop(func() { w.u(10) }) // path B
		default:
			prop(func() { w.opt(10); w.opt(10) }) // path A
		}
	case VTS28:
		prop(func() {
			if rebase {
				s28P0(w, origin, &fix)
			} else {
				s28P0(w, origin, nil)
			}
		})
		if sp == 2 {
			prop(func() { s28P1(w, table) })
		}
	case VTTurret:
		prop(func() { w.opt(6) })
	case VTS314:
		prop(func() { w.u(1) })
	}
	res.End = w.i
	return res, nil
}

// String is a short form for diagnostics.
func (s *SectionResult) String() string { return fmt.Sprintf("%d chunks end %d", len(s.Chunks), s.End) }
