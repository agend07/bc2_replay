package wire

// Group C (r276c, wire_py_big): 825E50 player, 7F7820, 8C0620 multi.

const (
	stubStrAssign = 0x4245b0 // EASTL string assign (player fixture stub)
	stubFree      = 0x41b8b0 // operator delete (stub)
)

// u64of is 50BF80 BitStream_ReadBits64 (bytes 50BF80..50BFC9): u(min(n, 32)) [+ u(n - 32) when n > 32].
func (r *Rd) u64of(n int64) {
	r.u(min(n, 32))
	if n > 32 {
		r.u(n - 32)
	}
}

// bitArray4EA690 is 4EA690 BitArray_Read (bytes 4EA690..4EA6DA, resize 4EA5F0..4EA68B): k = u(6); n = u(k + 1); the
// resize frees the old buffer when n != 0 (41B8B0 stub, section zeroed offline); ceil(n / 32) x u(32).
func (r *Rd) bitArray4EA690() {
	k := int64(r.u(6))
	if k+1 > 32 {
		r.unported("4EA690 bit-count width > 32 (506660 n > 32 not modelled)")
	}
	n := r.u(k + 1)
	if n != 0 {
		r.stub(stubFree)
	}
	for range (int64(n) + 31) >> 5 {
		r.u(32)
	}
}

// rd825E50 is 825E50 ClPlayerSection_ReadUpdate (probable; vt 0x1431d14; bytes 825E50..826504). 15 properties:
//
//	p0 r(0,16)                           p8  b; if 1: b b b
//	p1 string 514390 (+ 4245B0 assign)   p9  u(9), 50BF80(32)
//	p2 r(0,24) r(0,3) u(16)              p10 u(16)
//	p3 5B9EE0: 50BF80(64), len = u(7), len x u(8)
//	p4 b b    p5 b    p6 b               p11 4EA690 bit array, 6DC110 stub, u(25)
//	p7 u(8); arg2 vt+0xC (541BC0 stub -> 0); == 0 -> 7FF060
//	p12 b; if 1: ghost reference (541B80 -> C1E200); u(32)
//	p13 string, u(8) count, count x (string, strncpy [0x140561C] stub, 5EBCA0 stub)
//	p14 r(0,8) count, count x (r(0,8), r(0,0x800), vector push -> 754420 stub offline)
//
// p0-p10 and p12 set the dirty flag -> tail + 81F150 (stubs).
func rd825E50(r *Rd) {
	dirty := false
	if r.pres() {
		r.rng(0, 16)
		dirty = true
	}
	if r.pres() {
		r.string514390()
		r.stub(stubStrAssign)
		dirty = true
	}
	if r.pres() {
		r.rng(0, 24)
		r.rng(0, 3)
		r.u(16)
		dirty = true
	}
	if r.pres() { // 5B9EE0
		r.u64of(64)
		for range r.u(7) {
			r.u(8)
		}
		dirty = true
	}
	if r.pres() {
		r.b()
		r.b()
		dirty = true
	}
	for range 2 { // p5, p6
		if r.pres() {
			r.b()
			dirty = true
		}
	}
	if r.pres() {
		v := r.u(8)
		r.stub(0x541bc0) // arg2 vt+0xC (returns 0)
		if v == 0 {
			r.stub(0x7ff060)
		}
		dirty = true
	}
	if r.pres() {
		if r.b() {
			r.b()
			r.b()
			r.b()
		}
		dirty = true
	}
	if r.pres() {
		r.u(9)
		r.u64of(32)
		dirty = true
	}
	if r.pres() {
		r.u(16)
		dirty = true
	}
	if r.pres() {
		r.bitArray4EA690()
		r.stub(0x6dc110)
		r.u(25)
	}
	if r.pres() {
		if r.b() {
			r.gref()
		}
		r.u(32)
		dirty = true
	}
	if r.pres() {
		r.string514390()
		r.stub(stubStrAssign)
		for range r.u(8) {
			r.string514390()
			r.stub(IAT140561C[1])
			r.stub(0x5ebca0)
		}
	}
	if r.pres() {
		for range r.rng(0, 8) {
			r.rng(0, 8)
			r.rng(0, 0x800)
			r.stub(0x754420)
		}
	}
	if dirty {
		r.tail()
		r.stub(0x81f150)
	}
}

// widths7F7820 are the widths of 7F7820's p0..p9 (s8 u16 u16 s32 s8 u8 s8 f32 u10 s32; s = 50BEA0, one read).
var widths7F7820 = [...]int64{8, 16, 16, 32, 8, 8, 8, 32, 10, 32}

// rd7F7820 is 7F7820 ClSection_1431940_ReadUpdate (confirmed; bytes 7F7820..7F795D). 11 properties, no stubs:
// p0..p9 = widths7F7820 (p8 u10 clamped to 999 at 7F78F3, no stream effect), p10 = 11 x s15.
func rd7F7820(r *Rd) {
	for _, n := range widths7F7820 {
		if r.pres() {
			r.u(n)
		}
	}
	if r.pres() {
		for range 11 {
			r.u(15)
		}
	}
}

// item885830 is 885830 Section1436cbcState_ReadItem: b; if 1: r(0,100) (7 bits).
func (r *Rd) item885830() {
	if r.b() {
		r.rng(0, 100)
	}
}

// rd8C0620 is 8C0620 ClSection_1436cbc_ReadUpdate (confirmed; multi section vt 0x1436cbc; bytes 8C0620..8C069D) ->
// body 8858D0 Section1436cbcState_Read (bytes 8858D0..885A68), 10 properties: p0-p3 one 885830 item each; p4 u(2)
// count x item; p5 b [+ 7FF060 + cfg]; p6 b; p7 r(0,16); p8 f32; p9 b, r(0,4), b [+ 7FF060 + cfg].
// 'changed' (any p0-p4, p5 with b = 1, p6-p9) -> tail (+ 8BDD00 apply).
func rd8C0620(r *Rd) {
	ch := false
	for range 4 {
		if r.pres() {
			r.item885830()
			ch = true
		}
	}
	if r.pres() {
		for range r.u(2) {
			r.item885830()
		}
		ch = true
	}
	if r.pres() && r.b() {
		r.stub(0x7ff060)
		r.cfg()
		ch = true
	}
	if r.pres() {
		r.b()
		ch = true
	}
	if r.pres() {
		r.rng(0, 16)
		ch = true
	}
	if r.pres() {
		r.f32()
		ch = true
	}
	if r.pres() {
		r.b()
		r.rng(0, 4)
		if r.b() {
			r.stub(0x7ff060)
			r.cfg()
		}
		ch = true
	}
	if ch {
		r.tail()
	}
}
