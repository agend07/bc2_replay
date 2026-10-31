package wire

// Group B (r276b, wire_py_ref): the ghost-reference readers 8F5450 / 8F9FA0 / 823180 / 8279B0 / 8B8D50.
// Notation: P = 50BDE0 property bit, REF = refReader vt0 = C1E200 (ref), OV(C) = 50FE00 origin vector.
// Native callees Unicorn runs without a stream argument have no Go body (they read no bits).
// Not exercised by the corpus -> ErrUnported: an unresolved nonzero reference; 8279B0 p5 flag 1 + id 0;
// 8F9FA0 p0 count > 255.

// ref is REF + the reader's own pointer adjust: -> target id; an unresolved nonzero id is unported.
func (r *Rd) ref() uint32 {
	t, obj := r.gref()
	if obj == refUnresolved {
		r.unported("unresolved nonzero ghost reference (not in the corpus)")
	}
	return t
}

// rd8F5450 is 8F5450 Section143b2b4_ReadUpdate (confirmed; bytes 8F5450..8F5545): p0 P[r(0,16)]; p1 P[b[REF ->
// 652720, 6477C0 stubs]]; any property -> tail (+ 8EF620 apply, native).
func rd8F5450(r *Rd) {
	dirty := false
	if r.pres() {
		r.rng(0, 16)
		dirty = true
	}
	if r.pres() {
		dirty = true
		if r.b() {
			r.ref()
			r.stub(0x652720)
			r.stub(0x6477c0)
		}
	}
	if dirty {
		r.tail()
	}
}

// rd8F9FA0 is 8F9FA0 SpawnerSection143b3bc_ReadUpdate (confirmed; bytes 8F9FA0..8FA0FA): p0 P[n = u(16); n x b[REF ->
// 89F6B0 (WireHarness hook) -> vector push 8F36C0]]; p1 P[r(0,16)]; any -> tail (+ 8F8B40); then 8E1510 + free
// (41B8B0 x2 when the vector is non-empty).
func rd8F9FA0(r *Rd) {
	dirty, pushed := false, false
	if r.pres() {
		dirty = true
		n := r.u(16)
		if n > 255 {
			r.unported("p0 count %d > 255 (not in the corpus)", n)
		}
		for range n {
			if r.b() {
				r.ref()
				r.stub(0x89f6b0)
				pushed = true
			}
		}
	}
	if r.pres() {
		r.rng(0, 16)
		dirty = true
	}
	if dirty {
		r.tail()
	}
	if pushed {
		r.stub(0x41b8b0)
		r.stub(0x41b8b0)
	}
}

// rd823180 is 823180 ClCameraContextSection_ReadNetState (confirmed; bytes 823180..823355): p0 P[u6] p1 P[u3]
// p2 P[REF] p3 P[REF u4] p4 P[REF] p5 P[REF] (direct references, id 0 = null). Any -> tail (+ 8188B0).
func rd823180(r *Rd) {
	dirty := false
	for p := range 6 {
		if !r.pres() {
			continue
		}
		dirty = true
		switch p {
		case 0:
			r.u(6)
		case 1:
			r.u(3)
		case 3:
			r.ref()
			r.u(4)
		default:
			r.ref()
		}
	}
	if dirty {
		r.tail()
	}
}

// damage7F8360 is the 7F8360 ClPlayerView_OnDamageReceived stream part (bytes 7F8360..7F83F2): OV(5); 50C590(6) =
// m(6) + u(1) + m(5); OV(5); 50C120(4) = m(4); 50C120(6) = m(6). The rest needs [this+0x8C] (zero offline).
func (r *Rd) damage7F8360() {
	r.originVec(5)
	r.m(6)
	r.u(1)
	r.m(5)
	r.originVec(5)
	r.m(4)
	r.m(6)
}

// rd8279B0 is 8279B0 ClComponentSection_ReadNetState (confirmed, r167; vt 0x1431df8, player fixture; bytes
// 8279B0..827F71): p0 P[REF]; p1 P[r(0,0x11) -> 7FF060 + 6CA490]; p2 P[b[u32]]; p3 P[b[u32 u4 u16]]; p4 P[b[u32]];
// p5 P[b[REF -> fake ghost vt+0x18 (stub ZERO+0x100)]]; p6 P[m(8)]; p7/p8 P[7F8360]; p9 P[OV(5)];
// p10 P[b[7FF060 + 6CA490]]; p11 P[r(0,0x14), string, string]; p12 P[REF; nonzero -> u(4)].
// Dirty = p0, p2-p4 when b, p11; dirty or p12 -> tail + 2 x 4245B0 (81F310). Two 4245B0 at entry (816AD0 prologue).
func rd8279B0(r *Rd) {
	r.stub(0x4245b0)
	r.stub(0x4245b0)
	dirty, p12 := false, false
	if r.pres() {
		r.ref()
		dirty = true
	}
	if r.pres() {
		r.rng(0, 0x11)
		r.stub(0x7ff060)
	}
	if r.pres() && r.b() {
		r.u(32)
		dirty = true
	}
	if r.pres() && r.b() {
		r.u(32)
		r.u(4)
		r.u(16)
		dirty = true
	}
	if r.pres() && r.b() {
		r.u(32)
		dirty = true
	}
	if r.pres() && r.b() {
		if r.ref() == 0 {
			r.unported("p5: flag 1 + ghost id 0 (native AV at 827CBB)")
		}
		r.stub(Zero + 0x100)
	}
	if r.pres() {
		r.m(8)
	}
	if r.pres() {
		r.damage7F8360()
	}
	if r.pres() {
		r.damage7F8360()
	}
	if r.pres() {
		r.originVec(5)
	}
	if r.pres() && r.b() {
		r.stub(0x7ff060)
	}
	if r.pres() {
		r.rng(0, 0x14)
		r.string514390()
		r.stub(0x4245b0)
		r.string514390()
		r.stub(0x4245b0)
		dirty = true
	}
	if r.pres() {
		p12 = true
		if r.ref() != 0 {
			r.u(4)
		}
	}
	if dirty || p12 {
		r.tail()
		r.stub(0x4245b0)
		r.stub(0x4245b0)
	}
}

// rd8B8D50 is 8B8D50 DeathObjectSection1436010_ReadUpdate (confirmed; bytes 8B8D50..8B9250):
// p0 P[quatN(12); b; OV(b ? 5 : 10); 610BF0 stub]
// p1 P[b; 1: f31, OV(10) -> pos, quatN(12), f32, 50C7F0 ovec(10, base = pos) (not origin-relative), quatN(12)]
// p2 P[b[r(0,0x800) -> 5DEB30 stub]]
// p3 P[n = u8; n x (r(0,0x800) [nonzero -> 5DEB30], u8, 50BEA0(8), 50BEA0(8), u16, b) = 53 bits each]
// p4 P[n = u8; n x b]  p5 P[u6 u6 b b[REF -> 652720 stub]]
func rd8B8D50(r *Rd) {
	if r.pres() {
		r.quatN(12)
		if r.b() {
			r.originVec(5)
		} else {
			r.originVec(10)
		}
		r.stub(0x610bf0)
	}
	if r.pres() && r.b() {
		r.f31()
		pos := r.originVec(10)
		r.quatN(12)
		r.f32()
		r.ovec(10, pos)
		r.quatN(12)
	}
	if r.pres() && r.b() {
		r.rng(0, 0x800)
		r.stub(0x5deb30)
	}
	if r.pres() {
		for range r.u(8) {
			if r.rng(0, 0x800) != 0 {
				r.stub(0x5deb30)
			}
			r.u(8)
			r.u(8)
			r.u(8)
			r.u(16)
			r.b()
		}
	}
	if r.pres() {
		for range r.u(8) {
			r.b()
		}
	}
	if r.pres() {
		r.u(6)
		r.u(6)
		r.b()
		if r.b() {
			r.ref()
			r.stub(0x652720)
		}
	}
}
