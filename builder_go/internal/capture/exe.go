package capture

import (
	"encoding/binary"
	"fmt"
)

// Exe builds (Header.ExeSHA256). Everything downstream of extract (templates, Reader map, recipes, layouts, the
// datapack) keys on retail vtable values, so a Steam capture is normalised to retail once, when extract splits the
// .bc2raw: m-log.bin / raw-log.bin then hold retail values and no later stage needs to know the build.
const (
	ExeRetail = "retail"
	ExeSteam  = "steam"
)

// ExeBuild names the build of a header hash ("" = unknown: older DLLs, tests).
func ExeBuild(sha string) string {
	switch sha {
	case ExeRetailSHA256:
		return ExeRetail
	case ExeSteamSHA256:
		return ExeSteam
	}
	return ""
}

// Steam BFBC2Game.exe image: base 0x400000, SizeOfImage 0x1970000. A recorded vtable field value inside it must be a
// table key; 0 and values outside the image (never a vtable) pass unchanged.
const (
	steamImageLo = 0x400000
	steamImageHi = 0x400000 + 0x1970000
)

// vtField is one record field holding a raw exe vtable address: body offset, or the M section list (off < 0).
type vtField struct {
	typ  int
	off  int
	name string
}

// steamVTFields are the record fields that carry exe vtable addresses (r302): G ghost_vt / section_vt, B ghost_vt,
// S dispatcher_vt, C child_raw +0 / +16 (body +8 / +24), M sections[].
var steamVTFields = []vtField{
	{TG, 20, "ghost_vt"}, {TG, 24, "section_vt"}, {TB, 8, "ghost_vt"}, {TS, 12, "dispatcher_vt"},
	{TC, 8, "child_raw+0"}, {TC, 24, "child_raw+16"}, {TM, -1, "sections"},
}

// NormalizeSteam rewrites, in place, every vtable field of a Steam capture's frames to its retail value and returns
// how many u32 changed. A non-zero value inside the Steam image that is not in the table is an error naming the record
// type, its index, the field and the value. Fields a short body does not hold are left to Decode's size errors.
func NormalizeSteam(frames []Frame) (int, error) {
	n := 0
	fix := func(i int, f Frame, name string, off int) error {
		b := f.Body()
		if off+4 > len(b) {
			return nil
		}
		v := binary.LittleEndian.Uint32(b[off:])
		if v < steamImageLo || v >= steamImageHi {
			return nil
		}
		r, ok := steamToRetail[v]
		if !ok {
			return &PyError{"ValueError", fmt.Sprintf("steam capture: record %d (%s) %s 0x%x is not in the Steam vtable table",
				i, typeName(f.Type), name, v)}
		}
		binary.LittleEndian.PutUint32(b[off:], r)
		n++
		return nil
	}
	for i, f := range frames {
		for _, fl := range steamVTFields {
			if fl.typ != f.Type {
				continue
			}
			if fl.off >= 0 {
				if err := fix(i, f, fl.name, fl.off); err != nil {
					return n, err
				}
				continue
			}
			b := f.Body()
			if len(b) < 4 {
				continue
			}
			cnt := uint64(binary.LittleEndian.Uint32(b))
			for k := uint64(0); k < cnt && 4+4*k+4 <= uint64(len(b)); k++ {
				if err := fix(i, f, fmt.Sprintf("sections[%d]", k), 4+4*int(k)); err != nil {
					return n, err
				}
			}
		}
	}
	return n, nil
}
