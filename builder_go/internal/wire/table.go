package wire

import "fmt"

// Frozen exe table (r273 §2, = wire_py_core layer 1; read from the SHA-checked exe, compared to it by the Python
// test_wire_py.test_frozen_table_matches_exe). Everything the default path reads from the exe besides code.

const m32 = 0xffffffff

// RetImm is the `ret N` immediate of every stub VA (soldier_update_uc.ret_imm). Only membership matters to the readers;
// the values are kept so the stub sets stay traceable to the harness.
var RetImm = map[uint32]uint32{
	0x41b880: 0, 0x41b8b0: 0, 0x4245b0: 8, 0x541bc0: 0, 0x55ea10: 8, 0x5deb30: 4, 0x5ebca0: 16, 0x610bf0: 8,
	0x6477c0: 4, 0x652720: 0, 0x6d68b0: 8, 0x6dc110: 0, 0x747980: 4, 0x754420: 8, 0x7ff060: 0, 0x81f150: 12,
	0x898900: 0, 0x89ee00: 0, 0x89f6b0: 0, 0x8b1680: 8, 0x8c0390: 12, 0xc118c0: 0, 0xc1bbc0: 8,
}

// Reader maps a section vtable to its [vt+4] update reader (v3_wire_audit / v3_audit888 / v3_types).
var Reader = map[uint32]uint32{
	0x1431c0c: 0x823180, 0x143b2b4: 0x8f5450, 0x1431df8: 0x8279b0, 0x1431d14: 0x825e50, 0x143aa80: 0x8f4890,
	0x143b17c: 0x8f22c0, 0x143956c: 0x8f0560, 0x143b66c: 0x8f3f40, 0x1431940: 0x7f7820, 0x14383cc: 0x8d3ae0,
	0x143800c: 0x8c4760, 0x143b3bc: 0x8f9fa0, 0x14377f8: 0x8c07e0, 0x143afd0: 0x8ebce0, 0x1436010: 0x8b8d50,
	0x143a794: 0x8f4890, 0x143ade4: 0x8f4890, 0x143ac38: 0x8f1b20, 0x1436cbc: 0x8c0620, 0x143010c: 0x7e8900,
	0x1430524: 0x7e8900, 0x14399c8: 0x8f0900, 0x1432098: 0x7edab0, 0x1431b7c: 0xc152d0,
}

// Thunks are the 3 MSVCR80 x87 import thunks WireHarness verifies before shimming (v3_wire_audit:62-69).
var Thunks = map[uint32]string{0xc521b0: "ff2528554001", 0xc521b6: "ff252c554001", 0xc521bc: "ff2530554001"}

// IAT140561C is the IAT dword 0x140561C (strncpy) and its target, stubbed by the player fixture.
var IAT140561C = [2]uint32{0x140561c, 0x10e6e80}

// Well-known VAs and section vtables.
const (
	CameraInitVA = 0x7ee800 // ClCameraContextSection_ReadNetInit (reverser confirmed): create init, vt+8, not in Reader
	Zero         = 0x6bf0000
	Heap0        = 0x6a00000
	CameraVT     = 0x1431c0c
	TicketVT     = 0x143956c
	AC38VT       = 0x143ac38
	SoldierVT    = 0x14377f8
	GhostBits    = 13 // soldier_py.GHOST_BITS (r210 default ghost-id width)
)

// PlayerVTs are the sections whose records run with the player fixture.
var PlayerVTs = [2]uint32{0x1431d14, 0x1431df8}

// Harness stub sets (frozen copies of soldier_update_uc / v3_wire_audit / v3_multi_slice).
var (
	// Stubs = soldier_update_uc.STUBS (generic users keep 6D68B0).
	Stubs = []uint32{0x898900, 0x8C0390, 0x41B880, 0x41B8B0, 0x7FF060, 0x652720, 0x6477C0, 0x89F6B0, 0x8B1680,
		0x89EE00, 0xC118C0, 0x747980, 0x6D68B0, 0x55EA10}
	RefStubs    = []uint32{0xC1BBC0}                                   // ghost-id map lookup
	WireExtra   = []uint32{0x610bf0, 0x8d3a70, 0x5deb30, Zero + 0x100} // WireHarness extras
	PlayerExtra = []uint32{0x4245b0, 0x541bc0, 0x6dc110, 0x5ebca0, 0x754420, 0x81f150, IAT140561C[1]}
)

// Unicorn error strings as the harness reports them.
const (
	ErrBudget    = "instruction budget exhausted"
	ErrException = "Unhandled CPU exception (UC_ERR_EXCEPTION)"
)

// StubSet is the set of VAs a harness answers with a stub (wire_py_core stubs dict; the ret immediates are unused).
type StubSet map[uint32]struct{}

func stubSet(lists ...[]uint32) StubSet {
	s := StubSet{}
	for _, l := range lists {
		for _, va := range l {
			s[va] = struct{}{}
		}
	}
	return s
}

// WireStubs = wire_py_core.wire_stubs(player).
func WireStubs(player bool) StubSet {
	if player {
		return stubSet(Stubs, RefStubs, WireExtra, PlayerExtra)
	}
	return stubSet(Stubs, RefStubs, WireExtra)
}

// PlainStubs = wire_py_core.plain_stubs() (the AC38 Harness).
func PlainStubs() StubSet { return stubSet(Stubs, RefStubs) }

// Known lists every reader VA with a port: the Reader values plus CameraInitVA (wire_py_core.KNOWN).
func Known() []uint32 {
	seen := map[uint32]bool{CameraInitVA: true}
	out := []uint32{CameraInitVA}
	for _, va := range Reader {
		if !seen[va] {
			seen[va] = true
			out = append(out, va)
		}
	}
	return out
}

// SectionReader is [vt+4] from the frozen Reader table (no exe read).
func SectionReader(vt uint32) (uint32, error) {
	if va, ok := Reader[vt]; ok {
		return va, nil
	}
	return 0, &ErrUnported{Reason: fmt.Sprintf("section vt %#x not in the frozen READER table (r273 §2)", vt)}
}
