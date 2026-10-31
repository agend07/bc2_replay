package fbgd

// Port of v3_wire_companions.encode / write: the destruction companion files (.fbcdd/.fbgdd/.fbidd) and their indices
// (.fbcdk/.fbgdk/.fbidk) from records of bit strings (r141 container). build / wire_events stay with the companions
// stage.

import (
	"encoding/binary"

	"bc2replay/builder/internal/bits"
)

// CompanionKey is the companion key interval (a key record before delta L when L % 450 == 0).
const CompanionKey = 450

// CompanionExts are v3_wire_companions.EXTS; the index file of ext is ext[:-2] + "dk".
var CompanionExts = [3]string{"fbcdd", "fbgdd", "fbidd"}

// CompRec is one companion record (marker 0 key / 1 delta, payload bits).
type CompRec struct {
	Marker byte
	Bits   bits.Bits
}

// EncodeCompanion is v3_wire_companions.encode(prolog, hdr, recs) -> (companion bytes, index bytes): prolog, header
// (u32 size, u32 bits, payload), records (u8 marker, u32 size, u32 bits, payload), end 02 + 8 zero bytes; index u32 450
// + per key (u32 delta count so far, f32 0.0, u32 byte offset of the key marker).
func EncodeCompanion(prolog []byte, hdr bits.Bits, recs []CompRec) ([]byte, []byte) {
	le := binary.LittleEndian
	out := append([]byte{}, prolog...)
	hb := hdr.Bytes()
	out = le.AppendUint32(out, uint32(len(hb)))
	out = le.AppendUint32(out, uint32(hdr.Len()))
	out = append(out, hb...)
	idx := le.AppendUint32(nil, CompanionKey)
	L := 0
	for _, r := range recs {
		if r.Marker == 0 {
			idx = le.AppendUint32(idx, uint32(L))
			idx = le.AppendUint32(idx, 0)
			idx = le.AppendUint32(idx, uint32(len(out)))
		} else {
			L++
		}
		pb := r.Bits.Bytes()
		out = append(out, r.Marker)
		out = le.AppendUint32(out, uint32(len(pb)))
		out = le.AppendUint32(out, uint32(r.Bits.Len()))
		out = append(out, pb...)
	}
	out = append(out, 2, 0, 0, 0, 0, 0, 0, 0, 0)
	return out, idx
}

// CompanionIndexExt is the index file extension of a companion ext (fbcdd -> fbcdk).
func CompanionIndexExt(ext string) string { return ext[:len(ext)-2] + "dk" }
