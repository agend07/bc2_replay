package wire

// Group A (r276a, wire_py_vec): vector readers 8EBCE0 / 8F0900 / 8F4890 / 7EE800.
// Offline contract (every corpus run: sec_fields {} -> zeroed section): the section-state guards these readers test
// read zero (8EBCE0 [[this+4]+0x1c] == [+0x2c]; 8F0900 [this+0x20] == [this+0x30], [[this-0x6c]+0xa8] == 0; 8F4890
// the [this-0x28..-0xc] vector sizes = 0, 7EDA60 lookup = 0). Nonzero sec_fields -> ErrUnported.

func zeroSection(r *Rd) {
	for _, v := range r.inp.SecFields {
		if v&m32 != 0 {
			r.unported("nonzero sec_fields (section-state guards not ported)")
		}
	}
}

// rd8EBCE0 is 8EBCE0 Section143afd0_ReadUpdate (reverser probable; projectile, vt 0x143afd0). 4 properties:
// p0 quat 50CBB0(N=8) + 50FE00 originVec(C=5) + 50C440 ReadScaledVec3(16) = 3 x s(16); p1 b + u(8);
// p2 78E3D0 (base = p0's vector, zero when p0 absent); p3 b. Any present -> tail (+ 8E5A30 apply).
func rd8EBCE0(r *Rd) {
	zeroSection(r)
	dirty := false
	var base [3]float64
	if r.pres() {
		r.quat(8)
		base = r.originVec(5)
		for range 3 {
			r.s(16)
		}
		dirty = true
	}
	if r.pres() {
		r.b()
		r.u(8)
		dirty = true
	}
	if r.pres() {
		r.physP3(base)
		dirty = true
	}
	if r.pres() {
		r.b()
		dirty = true
	}
	if dirty {
		r.tail()
	}
}

// rd8F0900 is 8F0900 Section1439b4c_ReadUpdate (reverser probable; quads, vt 0x14399c8). 3 properties:
// p0 50FE00 originVec(C=7) + 50CBD0 quatN(12) low path; p1 78E3D0 (base = p0's vector); p2 b ([[this-0x6c]+0xa8] == 0
// offline: no effect). p0 present -> tail (+ 8E7E70 apply).
func rd8F0900(r *Rd) {
	zeroSection(r)
	var base [3]float64
	p0 := r.pres()
	if p0 {
		base = r.originVec(7)
		r.quatN(12)
	}
	if r.pres() {
		r.physP3(base)
	}
	if r.pres() {
		r.b()
	}
	if p0 {
		r.tail()
	}
}

// rd8F4890 is 8F4890 Section143aa80_ReadUpdate (C4 / gadget sections vt 0x143aa80 / 0x143a794 / 0x143ade4). 5 props:
// p0 b hi, b; hi -> originVec(10) + quat(25), else originVec(5) + quat(8).
// p1 b attached; set -> C1E200 ghost reference (C4_REF_DROP: nonzero id fails in gref), b [+ u(16) asset id],
// 50C3E0 raw3, quat(25). p2 b. p3 b; set -> 7FF060 + cfg + 7FF060 (7EDA60 -> 0 offline).
// p4 b + b; second set -> 7FF060 + cfg. p0..p2 or p4 present -> tail (+ 8EF310); p3 alone does not set the flag.
func rd8F4890(r *Rd) {
	zeroSection(r)
	flag := false
	if r.pres() {
		hi := r.b()
		r.b()
		if hi {
			r.originVec(10)
			r.quat(25)
		} else {
			r.originVec(5)
			r.quat(8)
		}
		flag = true
	}
	if r.pres() {
		if r.b() {
			if tgt, _ := r.gref(); tgt == 0 {
				r.unported("8F4890 id-0 ghost reference (not in the oracle corpus)")
			}
			if r.b() {
				r.u(16)
			}
			r.raw3()
			r.quat(25)
		}
		flag = true
	}
	if r.pres() {
		r.b()
		flag = true
	}
	if r.pres() && r.b() {
		r.stub(0x7ff060)
		r.cfg()
		r.stub(0x7ff060)
	}
	if r.pres() {
		r.b()
		if r.b() {
			r.stub(0x7ff060)
			r.cfg()
		}
		flag = true
	}
	if flag {
		r.tail()
	}
}

// rd7EE800 is 7EE800 ClCameraContextSection_ReadNetInit (reverser confirmed; camera create init, vt 0x1431c0c +8).
// No property bits: quat(25) + originVec(5) + b secondary flag; set -> quat(25) + 50C7F0 ovec(5, base = primary).
func rd7EE800(r *Rd) {
	r.quat(25)
	base := r.originVec(5)
	if r.b() {
		r.quat(25)
		r.ovec(5, base)
	}
}
