package events

import (
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"bc2replay/builder/internal/capture"
)

// Synthetic vectors (values from the frozen Python modules; no capture data), plus one logged packet (r294).

func TestFrameOf(t *testing.T) {
	const t0 = 8411843320483
	fo, err := NewFrameOf(t0, "6", "2000")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]int64{{0, -1}, {1, -1}, {1999999, -1}, {2000000, 5}, {6666666, 18}, {6666667, 2005},
		{7000000, 2006}, {50333283, 2135}, {151000016, 2436}, {10000012345, 31788}} {
		got, err := fo.Of(uint64(t0 + c[0]))
		if err != nil || int64(got) != c[1] {
			t.Errorf("_fo(t0+%d) = %d, %v; want %d", c[0], got, err, c[1])
		}
	}
}

func TestEncoders(t *testing.T) {
	E := [][2]int64{{3, 4}, {1, 2}, {2, 1}}
	st := map[ElKey]int64{{0, 1}: 3, {1, 0}: 1, {2, 1}: 0}
	if got := ckeyEnc(st, E); got != "011101101" {
		t.Errorf("ckey_enc %s", got)
	}
	cd, err := cdeltaEnc(5, map[ElKey]int64{{1, 0}: 1, {0, 2}: 2, {0, 0}: 3}, E, 2)
	if want := "000000000000000000000000000010100000000000000000000000000000010000100111010011"; err != nil || cd != want {
		t.Errorf("cdelta_enc %s %v", cd, err)
	}
	G := []int64{5, 40}
	gk, err := gkeyEnc([]GroupSet{{1: {}, 4: {}}, {0: {}, 33: {}, 39: {}}}, G)
	want := "00001100000101010000000000000000000000000001001000001110001011010000000000000000000000000000000000100000000000000000000000010000010"
	if err != nil || gk != want {
		t.Errorf("gkey_enc %s %v", gk, err)
	}
	if st2, err := gkeyDec(gk, G); err != nil || len(st2[0]) != 2 || len(st2[1]) != 3 {
		t.Errorf("gkey_dec %v %v", st2, err)
	}
	if got := gdeltaEnc(7, map[int64]GroupSet{1: {3: {}, 2: {}}}, G); got != "000000000000000000000000000011101000000000000000000000000000000100000000000000000000000000000001000000000000000000000000000000011" {
		t.Errorf("gdelta_enc %s", got)
	}
}

func TestParseRows(t *testing.T) {
	r := &Registry{}
	b := "0" + "1" + "1" + "0" + "1" + "100000001101100000000000000" + "0000000" + "0000000"
	rows, end, why := r.ParseRows(b, 0, RegProps)
	want := []RegRow{{1, 0, 1, "0", []int{}}, {1, 1, 27, "100000001101100000000000000", []int{1}}}
	if why != "" || end != 45 || !slices.EqualFunc(rows, want, func(a, b RegRow) bool {
		return a.SI == b.SI && a.PJ == b.PJ && a.L == b.L && a.Body == b.Body && slices.Equal(a.Pos, b.Pos)
	}) {
		t.Errorf("parse_rows = %v %d %q", rows, end, why)
	}
	if _, _, why := r.ParseRows("1", 0, RegProps); why != "s0 present or past end" {
		t.Errorf("parse_rows reason %q", why)
	}
}

// wireNoStreamPacket is cap_20261031_002547_52296 round 2 pseq 164 (MP_007, 240 bytes, 2 s after the map rotation):
// header bit 4 clear, no destruction stream, the ghost block at bit 24 (5 ghost + 4 multi entries).
const wireNoStreamPacket = "1204e2c0feeeef0048000674570000000003bc1b4c3a9b20efffe937ff2f60029d15c000000000ef053b0ea6f83a0035d8fffbd801efc4e0952eb940614c982a5e000301c44065bf1322b0b757a5872e61a5c8000c0e91002c437230585cf10302cbbfd70900b1f40000b1f48d4001c71dcd65316671614814000000004f10d5eb45b92be890bd8203c5d0115490d8004f6bab6305c32e0ec502800000000d200200000073255993d7b32aa19fe43a407fbe01c013670126c49c3af013ff89b326c4000000000000000003a12b757ac38f86988015587bffc00e04d94089b01320209b226c04d78000000000001d8000"

func TestWireNoStreamPacket(t *testing.T) {
	payload, err := hex.DecodeString(wireNoStreamPacket)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(p []byte) *capture.Decoded {
		d := &capture.Decoded{Packets: map[uint32]*capture.Packet{164: {T: 14456400492059, Seq: 164, Payload: p}}, PacketSeqs: []uint32{164}}
		for _, c := range []uint32{413, 555, 697, 995, 1262} {
			d.Ghosts = append(d.Ghosts, &capture.Ghost{Pseq: 164, EntryCursor: c})
		}
		for _, c := range []uint32{73, 243, 1307, 1616} {
			d.Multi = append(d.Multi, &capture.Ghost{Pseq: 164, EntryCursor: c})
		}
		return d
	}
	gs, err := ghostStarts(mk(payload), DeleteWidth)
	if err != nil || gs[164] != 24 {
		t.Fatalf("ghost block start %v, %v (want 24)", gs, err)
	}
	rows, err := Wire(mk(payload), &Config{})
	if err != nil || len(rows) != 0 {
		t.Errorf("no-stream packet: rows %v, err %v (want none, nil)", rows, err)
	}
	// the same packet with bit 4 set claims a stream it does not have: the pre-r294 error
	flipped := slices.Clone(payload)
	flipped[0] |= 0x08
	if _, err := Wire(mk(flipped), &Config{}); err == nil || !strings.Contains(err.Error(), "destruction stream of pseq 164: float31 found at []") {
		t.Errorf("bit 4 set: err %v", err)
	}
}
