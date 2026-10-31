package pyc

import (
	"errors"
	"math"
	"math/big"
)

// Python exception equivalents.
var (
	ErrZeroDivision = errors.New("ZeroDivisionError")
	ErrOverflow     = errors.New("OverflowError")
	ErrValue        = errors.New("ValueError")
)

// FloorDiv is Python a // b on int64. b == 0 panics like Go division; MinInt64 // -1 wraps to MinInt64
// (Python gives 2**63, which int64 cannot hold).
func FloorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// Mod is Python a % b on int64: the result has the sign of b.
func Mod(a, b int64) int64 {
	m := a % b
	if m != 0 && ((m < 0) != (b < 0)) {
		m += b
	}
	return m
}

// FloatDivMod ports CPython _float_div_mod (Objects/floatobject.c): fmod-based, with its sign and zero rules.
func FloatDivMod(vx, wx float64) (floordiv, mod float64, err error) {
	if wx == 0 {
		return 0, 0, ErrZeroDivision
	}
	mod = math.Mod(vx, wx)
	div := (vx - mod) / wx
	if mod != 0 {
		if (wx < 0) != (mod < 0) {
			mod += wx
			div -= 1.0
		}
	} else {
		mod = math.Copysign(0, wx)
	}
	if div != 0 {
		floordiv = math.Floor(div)
		if div-floordiv > 0.5 {
			floordiv += 1.0
		}
	} else {
		floordiv = math.Copysign(0, vx/wx)
	}
	return floordiv, mod, nil
}

// FloatFloorDiv is Python x // y on floats.
func FloatFloorDiv(x, y float64) (float64, error) {
	q, _, err := FloatDivMod(x, y)
	return q, err
}

// RoundInt is Python round(x) for a float: half-even to an int; inf -> OverflowError, nan -> ValueError.
func RoundInt(x float64) (*big.Int, error) {
	if math.IsInf(x, 0) {
		return nil, ErrOverflow
	}
	if math.IsNaN(x) {
		return nil, ErrValue
	}
	r := math.RoundToEven(x)
	z, _ := new(big.Float).SetFloat64(r).Int(nil)
	return z, nil
}

// RoundInt64 is RoundInt for results that fit int64 (the builder's case); out of range -> ErrOverflow.
func RoundInt64(x float64) (int64, error) {
	z, err := RoundInt(x)
	if err != nil {
		return 0, err
	}
	if !z.IsInt64() {
		return 0, ErrOverflow
	}
	return z.Int64(), nil
}

// CPython float.__round__ short-circuits (NDIGITS_MAX/MIN in Objects/floatobject.c).
const (
	ndigitsMax = 323  // (DBL_MANT_DIG - DBL_MIN_EXP) * 0.30103
	ndigitsMin = -308 // -(DBL_MAX_EXP + 1) * 0.30103
)

// RoundFloat is Python round(x, ndigits) for a float: the exact binary value rounded half-even to ndigits decimals,
// then correctly rounded back to a double (CPython double_round via _Py_dg_dtoa mode 3 + strtod). The sign of a
// zero result follows x. A result too large to represent -> OverflowError.
func RoundFloat(x float64, ndigits int) (float64, error) {
	if math.IsInf(x, 0) || math.IsNaN(x) || x == 0 || ndigits > ndigitsMax {
		return x, nil
	}
	if ndigits < ndigitsMin {
		return 0.0 * x, nil
	}
	r := new(big.Rat).SetFloat64(x)
	neg := r.Sign() < 0
	r.Abs(r)
	p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(ndigits))), nil)
	if ndigits >= 0 {
		r.Mul(r, new(big.Rat).SetInt(p))
	} else {
		r.Quo(r, new(big.Rat).SetInt(p))
	}
	q := roundHalfEvenRat(r)
	var out big.Rat
	if ndigits >= 0 {
		out.SetFrac(q, p)
	} else {
		out.SetInt(q.Mul(q, p))
	}
	f, _ := out.Float64()
	if math.IsInf(f, 0) {
		return 0, ErrOverflow
	}
	if neg {
		f = -f
	}
	return f, nil
}

// roundHalfEvenRat rounds a non-negative rational to the nearest integer, ties to even.
func roundHalfEvenRat(r *big.Rat) *big.Int {
	q, m := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	c := new(big.Int).Lsh(m, 1).Cmp(r.Denom())
	if c > 0 || (c == 0 && q.Bit(0) == 1) {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// RoundIntN is Python round(v, ndigits) for an int: identity for ndigits >= 0, else half-even to a multiple of
// 10**-ndigits (long_round).
func RoundIntN(v *big.Int, ndigits int) *big.Int {
	if ndigits >= 0 {
		return new(big.Int).Set(v)
	}
	p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-ndigits)), nil)
	q, m := new(big.Int).DivMod(v, p, new(big.Int)) // Euclidean = floor for p > 0
	c := new(big.Int).Lsh(m, 1).Cmp(p)
	if c > 0 || (c == 0 && q.Bit(0) == 1) {
		q.Add(q, big.NewInt(1))
	}
	return q.Mul(q, p)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
