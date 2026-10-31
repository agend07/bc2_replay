package pyc

import "math"

// PackF32 is struct.pack('<f', x) as the uint32 bit pattern (PyFloat_Pack4). A finite x that rounds to infinity
// -> ErrOverflow. NaNs keep the sign and the top 23 payload bits; a signalling NaN stays signalling (CPython 3.14).
func PackF32(x float64) (uint32, error) {
	if math.IsNaN(x) {
		u := math.Float64bits(x)
		r := uint32(u>>32)&0x80000000 | 0x7f800000 | uint32(u>>29)&0x7fffff
		if u&(1<<51) != 0 {
			r |= 1 << 22
		} else if r&0x7fffff == 0 {
			r |= 1 << 22 // CPython: the cast quietens a payload that would truncate to infinity
		}
		return r, nil
	}
	y := float32(x)
	if math.IsInf(float64(y), 0) && !math.IsInf(x, 0) {
		return 0, ErrOverflow
	}
	return math.Float32bits(y), nil
}

// UnpackF32 is struct.unpack('<f', ...) from the uint32 bit pattern (PyFloat_Unpack4); NaN payloads are kept
// bit for bit, signalling ones included.
func UnpackF32(u uint32) float64 {
	if u&0x7f800000 == 0x7f800000 && u&0x7fffff != 0 {
		return math.Float64frombits(uint64(u&0x80000000)<<32 | 0x7ff0000000000000 | uint64(u&0x7fffff)<<29)
	}
	return float64(math.Float32frombits(u))
}

// F32 is struct.unpack('<f', struct.pack('<f', x))[0]: the float32 truncation point used by the soldier code.
func F32(x float64) (float64, error) {
	u, err := PackF32(x)
	if err != nil {
		return 0, err
	}
	return UnpackF32(u), nil
}
