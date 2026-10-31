// Package wire is the Go port of wire_py_core and its four reader groups (wire_py_vec / _ref / _big / _small): the
// client's network section readers re-expressed on an exact model of the BitStream reader 506660, reproducing the
// offline Unicorn harness (WireHarness / plain AC38 Harness) run for run.
//
// Layers (same as wire_py_core):
//  1. Frozen exe table (table.go): RetImm, Reader, Thunks, the harness stub sets and error strings.
//  2. Rd (rd.go): one emulated run = BitStream state (cursor / 32-bit cache / next dword / flags) + the primitives,
//     with the Unicorn side channels (pos at 50BDE0, refs at C1E200, ovs at 50FE00, 50C650 hits, stub calls).
//  3. RunReader (rd.go): the Harness.run contract -> Out{pos, end, flags, err, calls, refs, ovs, q50c650}.
//     Readers live in readers_*.go, one file per r276 group, registered in the static Readers table.
//  4. Site wrappers (sites.go): slice_record / run_section / camera init / AC38 slice_row over a decoded log.
//
// The three old-Unicorn quirks (wire_py_core.POLICY, r274 §3-5) are fixed at their Unicorn-equal defaults:
// SOLDIER_CREATE_STUB_6D68B0=stub (6D68B0 stubbed in the soldier reader 8C07E0), AC38_DROP_X87=drop (plain AC38
// Harness + non-pole 50C650 code -> ERR_EXCEPTION), C4_REF_DROP=drop (8F4890 + nonzero ghost reference ->
// ERR_BUDGET). The opt-in alternatives were removed for the release.
//
// Failures: a reader reproduces a Unicorn failure with UcFail (Out.Err); a path the oracle never exercised is
// ErrUnported{VA}; both travel as panics inside a run and are recovered by RunReader only.
//
// The oracle exam (wirecheck subpackage, `builder dev wire-check`) replays the private r274 corpus.
package wire
