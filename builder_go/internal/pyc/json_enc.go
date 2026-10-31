package pyc

import (
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Valuer lets a type supply the Python value json.dumps would see (e.g. a dataclass converted to a dict).
type Valuer interface{ PyValue() any }

// DumpOptions mirrors json.dumps keyword arguments. Indent 0 means indent=None (Python's indent=0 is not supported).
// Empty ItemSep/KeySep take Python's defaults: (', ', ': ') without indent, (',', ': ') with it.
type DumpOptions struct {
	EnsureASCII bool
	Indent      int
	SortKeys    bool
	ItemSep     string
	KeySep      string
	DisallowNaN bool // allow_nan=False: NaN/inf -> error
}

// Dumps is json.dumps(v, ...). Supported values: nil, bool, Go ints, *big.Int, float64, float32 (widened), string,
// []any, []string, []int, []int64, []float64, *Object, Valuer. A plain map is accepted only with SortKeys.
func Dumps(v any, o DumpOptions) (string, error) {
	e := encoder{o: o}
	if e.o.ItemSep == "" {
		e.o.ItemSep = ", "
		if o.Indent > 0 {
			e.o.ItemSep = ","
		}
	}
	if e.o.KeySep == "" {
		e.o.KeySep = ": "
	}
	if err := e.value(v, 0); err != nil {
		return "", err
	}
	return e.b.String(), nil
}

type encoder struct {
	o DumpOptions
	b strings.Builder
}

func (e *encoder) newline(level int) {
	if e.o.Indent > 0 {
		e.b.WriteByte('\n')
		e.b.WriteString(strings.Repeat(" ", e.o.Indent*level))
	}
}

func (e *encoder) value(v any, level int) error {
	switch x := v.(type) {
	case nil:
		e.b.WriteString("null")
	case bool:
		if x {
			e.b.WriteString("true")
		} else {
			e.b.WriteString("false")
		}
	case int:
		e.b.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		e.b.WriteString(strconv.FormatInt(x, 10))
	case int32:
		e.b.WriteString(strconv.FormatInt(int64(x), 10))
	case uint32:
		e.b.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint64:
		e.b.WriteString(strconv.FormatUint(x, 10))
	case *big.Int:
		e.b.WriteString(x.String())
	case float64:
		return e.float(x)
	case float32:
		return e.float(float64(x))
	case string:
		e.str(x)
	case *Object:
		return e.object(x.keys, func(k string) any { return x.vals[k] }, level)
	case map[string]any:
		if !e.o.SortKeys {
			return fmt.Errorf("pyjson: unordered map needs SortKeys")
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		return e.object(keys, func(k string) any { return x[k] }, level)
	case []any:
		return e.array(len(x), func(i int) any { return x[i] }, level)
	case []string:
		return e.array(len(x), func(i int) any { return x[i] }, level)
	case []int:
		return e.array(len(x), func(i int) any { return x[i] }, level)
	case []int64:
		return e.array(len(x), func(i int) any { return x[i] }, level)
	case []float64:
		return e.array(len(x), func(i int) any { return x[i] }, level)
	case Valuer:
		return e.value(x.PyValue(), level)
	default:
		return fmt.Errorf("pyjson: Object of type %T is not JSON serializable", v)
	}
	return nil
}

func (e *encoder) float(x float64) error {
	switch {
	case math.IsNaN(x):
		if e.o.DisallowNaN {
			return fmt.Errorf("pyjson: Out of range float values are not JSON compliant: nan")
		}
		e.b.WriteString("NaN")
	case math.IsInf(x, 1), math.IsInf(x, -1):
		if e.o.DisallowNaN {
			return fmt.Errorf("pyjson: Out of range float values are not JSON compliant: %v", x)
		}
		if x > 0 {
			e.b.WriteString("Infinity")
		} else {
			e.b.WriteString("-Infinity")
		}
	default:
		e.b.WriteString(Repr(x))
	}
	return nil
}

func (e *encoder) array(n int, at func(int) any, level int) error {
	if n == 0 {
		e.b.WriteString("[]")
		return nil
	}
	e.b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			e.b.WriteString(e.o.ItemSep)
		}
		e.newline(level + 1)
		if err := e.value(at(i), level+1); err != nil {
			return err
		}
	}
	e.newline(level)
	e.b.WriteByte(']')
	return nil
}

func (e *encoder) object(keys []string, at func(string) any, level int) error {
	if len(keys) == 0 {
		e.b.WriteString("{}")
		return nil
	}
	if e.o.SortKeys {
		keys = slices.Clone(keys)
		slices.Sort(keys) // UTF-8 byte order == code point order == Python str order
	}
	e.b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			e.b.WriteString(e.o.ItemSep)
		}
		e.newline(level + 1)
		e.str(k)
		e.b.WriteString(e.o.KeySep)
		if err := e.value(at(k), level+1); err != nil {
			return err
		}
	}
	e.newline(level)
	e.b.WriteByte('}')
	return nil
}

const hexdig = "0123456789abcdef"

func (e *encoder) u4(c rune) {
	e.b.WriteString(`\u`)
	e.b.WriteByte(hexdig[c>>12&15])
	e.b.WriteByte(hexdig[c>>8&15])
	e.b.WriteByte(hexdig[c>>4&15])
	e.b.WriteByte(hexdig[c&15])
}

// str escapes like CPython's c-encoder: '"', '\\' and < 0x20 always; with EnsureASCII also everything outside
// ' '..'~' as \uXXXX (lowercase, surrogate pairs above the BMP). Invalid UTF-8 bytes are taken as U+FFFD.
func (e *encoder) str(s string) {
	e.b.WriteByte('"')
	for i := 0; i < len(s); {
		c, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch c {
		case '"':
			e.b.WriteString(`\"`)
		case '\\':
			e.b.WriteString(`\\`)
		case '\n':
			e.b.WriteString(`\n`)
		case '\r':
			e.b.WriteString(`\r`)
		case '\t':
			e.b.WriteString(`\t`)
		case '\b':
			e.b.WriteString(`\b`)
		case '\f':
			e.b.WriteString(`\f`)
		default:
			switch {
			case c < 0x20:
				e.u4(c)
			case c < 0x7f || !e.o.EnsureASCII:
				e.b.WriteRune(c)
			case c > 0xffff:
				c -= 0x10000
				e.u4(0xd800 | (c >> 10 & 0x3ff))
				e.u4(0xdc00 | (c & 0x3ff))
			default:
				e.u4(c)
			}
		}
	}
	e.b.WriteByte('"')
}

// Repr is repr(x) for a float: the shortest round-trip digits, fixed notation for 1e-4 <= |x| < 1e16 (with '.0'
// on whole numbers), else d[.ddd]e[+-]XX with at least two exponent digits; 'inf', '-inf', 'nan'.
func Repr(x float64) string {
	switch {
	case math.IsNaN(x):
		return "nan"
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	}
	s := strconv.FormatFloat(x, 'e', -1, 64) // [-]d[.ddd]e[+-]XX
	sign := ""
	if s[0] == '-' {
		sign, s = "-", s[1:]
	}
	ei := strings.IndexByte(s, 'e')
	exp, _ := strconv.Atoi(s[ei+1:])
	digits := strings.Replace(s[:ei], ".", "", 1)
	decpt := exp + 1
	if decpt <= -4 || decpt > 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		es := "+"
		if exp < 0 {
			es, exp = "-", -exp
		}
		return fmt.Sprintf("%s%se%s%02d", sign, m, es, exp)
	}
	switch {
	case decpt <= 0:
		return sign + "0." + strings.Repeat("0", -decpt) + digits
	case decpt >= len(digits):
		return sign + digits + strings.Repeat("0", decpt-len(digits)) + ".0"
	default:
		return sign + digits[:decpt] + "." + digits[decpt:]
	}
}
