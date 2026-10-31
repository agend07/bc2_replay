package wire

// Readers is the static dispatch table VA -> Go port (wire_py_core.READERS after load_groups). Every Known() VA is
// present; a VA outside it is ErrUnported. One file per r276 group: readers_vec.go (A), readers_ref.go (B),
// readers_big.go (C), readers_small.go (D); the two core proof readers are below.
var Readers = map[uint32]func(*Rd){
	// core (r275)
	0x7e8900: rd7E8900,
	0xc152d0: rdC152D0,
	// group A vec (r276a)
	0x8ebce0: rd8EBCE0,
	0x8f0900: rd8F0900,
	0x8f4890: rd8F4890,
	0x7ee800: rd7EE800,
	// group B ref (r276b)
	0x8f5450: rd8F5450,
	0x8f9fa0: rd8F9FA0,
	0x823180: rd823180,
	0x8279b0: rd8279B0,
	0x8b8d50: rd8B8D50,
	// group C big (r276c)
	0x825e50: rd825E50,
	0x7f7820: rd7F7820,
	0x8c0620: rd8C0620,
	// group D small (r276d)
	0x8f0560: rd8F0560,
	0x8f22c0: rd8F22C0,
	0x8f3f40: rd8F3F40,
	0x8d3ae0: rd8D3AE0,
	0x8c4760: rd8C4760,
	0x7edab0: rd7EDAB0,
	0x8f1b20: rd8F1B20,
	0x8c07e0: rd8C07E0,
}

// rd7E8900 is 7E8900 Section1430314_ReadUpdate (reverser confirmed; vt 0x143010c / 0x1430524 multi sections; bytes
// 7E8900..7E893C): 50BDE0 property bit; set -> 50BE90 1 bit (7E61D0 applies it, no stream argument).
func rd7E8900(r *Rd) {
	if r.pres() {
		r.b()
	}
}

// rdC152D0 is C152D0 (vt 0x1431b7c, player registry multi section): loops over the section's vector [this+4]..[this+8];
// offline the section is zeroed ([+4] == [+8]) -> returns at C152FA without a stream read. OFFLINE (oracle) behaviour;
// REGWIRE (v3_reg0) decodes these sections for the build.
func rdC152D0(r *Rd) {
	sf := r.inp.SecFields
	if sf[4]&m32 != sf[8]&m32 {
		r.unported("C152D0 with a non-empty section vector (sec_fields +4/+8)")
	}
}
