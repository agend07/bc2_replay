package wire

// Group D (r276d, wire_py_small): the small section readers, AC38 (plain Harness) and the soldier reader.

// rd8F0560 is 8F0560 ClTicketCounterSection_ReadUpdate (probable; bytes 8F0560..8F0607): p0 P[u16 + 50BEA0(width =
// create field +0x54, 0 when absent)]; p1 P[b]; dirty present -> tail (+ 8E7D20 apply).
func rd8F0560(r *Rd) {
	a := r.pres()
	if a {
		r.u(16)
		r.u(r.inp.SecFields[0x54] & m32)
	}
	b := r.pres()
	if b {
		r.b()
	}
	if a || b {
		r.tail()
	}
}

// rd8F22C0 is 8F22C0 Section143b17c_ReadUpdate (probable; bytes 8F22C0..8F238E): p0 P[r(0,16)]; p1 P[50C110 m(9) +
// f32]; p2 P[b]; dirty present -> tail (+ 8E8300 apply).
func rd8F22C0(r *Rd) {
	dirty := false
	if r.pres() {
		dirty = true
		r.rng(0, 16)
	}
	if r.pres() {
		dirty = true
		r.m(9)
		r.f32()
	}
	if r.pres() {
		dirty = true
		r.b()
	}
	if dirty {
		r.tail()
	}
}

// rd8F3F40 is 8F3F40 Section143b66c_ReadUpdate (probable; bytes 8F3F40..8F40EB): p0 P[b]; p1 P[b; 1 -> [arg2] vt
// slot 0 object read at 8F3FA8 (never in the corpus: unported)]; p2 P[u(bsr(2)+1) = u2]; p3 P[b + f32];
// p4 P[r(0,17)]; dirty present -> tail (+ 8EF1B0 apply).
func rd8F3F40(r *Rd) {
	dirty := false
	if r.pres() {
		r.b()
		dirty = true
	}
	if r.pres() {
		if r.b() {
			r.unported("8F3F40 p1 reference branch (8F3FA8) not in the corpus")
		}
		dirty = true
	}
	if r.pres() {
		r.u(2)
		dirty = true
	}
	if r.pres() {
		r.b()
		r.f32()
		dirty = true
	}
	if r.pres() {
		r.rng(0, 17)
		dirty = true
	}
	if dirty {
		r.tail()
	}
}

// rd8D3AE0 is 8D3AE0 Section14383cc_ReadUpdate (probable; events; bytes 8D3AE0..8D3B8B): p0 P[b; 1 -> 8D3A70 stub];
// p1 P[b; 1 -> f32 + u32, then a name lookup (877AB0 / 1026AD0 / 101FDA0) = 0 offline -> no 8D3A70 call].
func rd8D3AE0(r *Rd) {
	if r.pres() && r.b() {
		r.stub(0x8d3a70)
	}
	if r.pres() && r.b() {
		r.f32()
		r.u(32)
	}
}

// rd8C4760 is 8C4760 ClSection_143800c_ReadP0 (probable; bytes 8C4760..8C478B): p0 P[7FF060 stub, u([[cfg+8]+8])],
// width = cfg_child[8:12] little-endian (0 without a cfg child).
func rd8C4760(r *Rd) {
	if r.pres() {
		r.stub(0x7ff060)
		var w int64
		if c := r.inp.CfgChild; len(c) > 8 {
			for i := min(len(c), 12) - 1; i >= 8; i-- {
				w = w<<8 | int64(c[i])
			}
		}
		r.u(w)
	}
}

// rd7EDAB0 is 7EDAB0 Section1432098_ReadUpdate_Nop (confirmed; bytes `c2 08 00` ret 8): reads nothing.
func rd7EDAB0(*Rd) {}

// rd8F1B20 is 8F1B20 Section143ac38_ReadUpdate (probable; AC38, plain Harness; bytes 8F1B20..8F2048 + 78E3D0):
// p0 P[b(a) + b + 50FE00 originVec(a ? 10 : 5); x87 distance test on the zeroed section, no stream read];
// p1 P[b(c)]; p2 P[78E3D0 physP3 (base zeros)]; p3 P[b] -> tail (+ 8E80A0); without p3 the tail runs when p0 was
// present or c = 1. AC38_DROP_X87 lives in qunit.
func rd8F1B20(r *Rd) {
	bl := false
	if r.pres() {
		if r.b() {
			r.b()
			r.originVec(10)
		} else {
			r.b()
			r.originVec(5)
		}
		bl = true
	}
	if r.pres() && r.b() {
		bl = true
	}
	if r.pres() {
		r.physP3([3]float64{})
	}
	if r.pres() {
		r.b()
		r.tail()
	} else if bl {
		r.tail()
	}
}

// ---------------------------------------------------------------------------------------------------- soldier

func soldierP0(r *Rd) {
	r.u(1)
	r.u(1)
	r.u(1)
	r.u(3)
	r.rng(0, 13)
	if r.b() {
		r.rng(0, 2)
	}
	r.b()
}

func soldierP1(r *Rd) {
	if r.u(31) == 0 { // 50C0B0 f31 + fcompi with 0.0: only raw 0 (+0.0) takes the branch (soldier_py r201)
		if r.rng(0, 6) == 4 { // ghost ref @8C093A + u4, then 89F6B0 / 747980 / 41B880 (call order unverified)
			r.unported("8C07E0 p1 sel 4 (ghost reference @8C093A) not in the corpus")
		}
		r.raw3()
	}
	if r.b() { // 8C09CA
		r.stub(0x7ff060)
		r.cfg()
	}
}

func soldierP2(r *Rd) { // 8B3700: 7FF060, u4 (50BEA0), B02F30 mode 2, 7FF060, 8B1680
	r.stub(0x7ff060)
	r.u(4)
	if r.b() {
		r.originVec(7)
	}
	r.b()
	for range 3 {
		r.sparse(5)
	}
	r.stub(0x7ff060)
	r.stub(0x8b1680)
}

func soldierP3(r *Rd) { // 8B39D0: 7FF060, B02F30 mode 3, C118C0
	r.stub(0x7ff060)
	if r.b() {
		r.u(8*int64(r.u(1)) + 8)
	}
	if r.b() {
		r.rng(0, 2)
		r.rng(0, 2)
	}
	if r.rng(0, 10) == 3 {
		r.f32()
		r.f32()
	}
	r.b()
	r.stub(0xc118c0)
}

func soldierP4(r *Rd) {
	for range r.rng(0, 15) {
		r.u(12)
		r.u(16)
	}
}

func soldierP5(r *Rd) {
	r.u(8)
	r.b()
	r.u(3)
}

func soldierP6(r *Rd) {
	r.s(12) // 7F0400
	r.s(12)
	r.b()
	v := r.u(3) // 8026D0(0)
	r.b()
	if v == 1 || v == 2 { // 802731
		r.u(5)
		r.stub(0x7ff060)
	}
	if r.b() {
		r.m(8)
	}
	if r.b() { // 8C0AE2 -> 6D68B0(0)
		if r.stub(0x6d68b0) { // SOLDIER_CREATE_STUB_6D68B0 'stub': 0 bits
			return
		}
		if r.b() && r.b() {
			if r.b() {
				r.u(16)
			}
			if r.b() {
				r.sparse(5)
			}
			if r.b() {
				r.m(24)
			}
			if r.b() {
				r.u(3)
			}
		}
	}
}

func soldierP7(r *Rd) {
	r.m(32)
	r.s(32)
	r.b()
}

func soldierP8(r *Rd) {
	if r.b() {
		r.originVec(8)
		r.b()
		r.f31()
	}
}

func soldierP9(r *Rd) {
	anyp := false
	for range 5 {
		if r.b() {
			r.u(16)
			anyp = true
		}
	}
	r.u(8)
	if anyp && r.b() {
		r.stub(0x7ff060) // 8C0D13: list bound = cfg child +4
		lm := r.inp.ListMax
		for range r.rng(1, lm) {
			r.rng(0, lm-1)
			r.stub(0x55ea10)
		}
	}
}

func soldierP10(r *Rd) {
	for range 13 {
		r.b()
	}
}

func soldierP11(r *Rd) {
	r.b()
	r.rng(0, 4)
	if r.b() { // 8C0E32
		r.stub(0x7ff060)
		r.cfg()
	}
}

func soldierP13(r *Rd) {
	if r.b() {
		r.f31()
	}
}

// soldierP14 is 8C0EE8: b [+ ghost reference via [arg2] vt 0x1417488 -> C1E200; then 652720 + 6477C0 stubs whatever
// the id (unresolved: C1BBC0 first, inside gref)].
func soldierP14(r *Rd) {
	if r.b() {
		r.gref()
		r.stub(0x652720)
		r.stub(0x6477c0)
	}
}

func soldierBit(r *Rd) { r.b() }

func soldierP15(r *Rd) { r.f31() }

// soldierProps are the 17 property bodies p0..p16 of 8C07E0 (soldier_py r201 grammar).
var soldierProps = [...]func(*Rd){soldierP0, soldierP1, soldierP2, soldierP3, soldierP4, soldierP5, soldierP6,
	soldierP7, soldierP8, soldierP9, soldierP10, soldierP11, soldierBit, soldierP13, soldierP14, soldierP15, soldierBit}

// rd8C07E0 is 8C07E0 ClSoldierSection_ReadUpdate: 898900 stub at entry, 17 x P[+ body], one read per native 506660
// call so reads past the packet limit match Unicorn; dirty present -> tail + 8C0390 apply (stubs).
func rd8C07E0(r *Rd) {
	r.stub(0x898900) // 8C07F7
	dirty := false
	for _, body := range soldierProps {
		if r.pres() {
			body(r)
			dirty = true
		}
	}
	if dirty { // 8C1015
		r.tail()
		r.stub(0x8c0390)
	}
}
