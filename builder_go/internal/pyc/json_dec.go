package pyc

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Loads is json.loads(s) with Python's types kept apart: JSON objects -> *Object (file order; a duplicate key keeps
// its first position and takes the last value, like dict(pairs)), arrays -> []any, ints -> int64 or *big.Int when
// out of range, numbers with a fraction or exponent -> float64 (1e400 -> inf), NaN/Infinity/-Infinity accepted,
// strings -> string, true/false -> bool, null -> nil. Lone surrogate escapes become U+FFFD (Go strings cannot
// hold them; Python keeps them).
func Loads(s []byte) (any, error) {
	if !utf8.Valid(s) {
		return nil, fmt.Errorf("pyjson: input is not valid UTF-8")
	}
	d := decoder{s: s}
	d.ws()
	v, err := d.value()
	if err != nil {
		return nil, err
	}
	d.ws()
	if d.i != len(d.s) {
		return nil, d.errf("Extra data")
	}
	return v, nil
}

type decoder struct {
	s []byte
	i int
}

func (d *decoder) errf(msg string) error { return fmt.Errorf("pyjson: %s: char %d", msg, d.i) }

func (d *decoder) ws() {
	for d.i < len(d.s) {
		switch d.s[d.i] {
		case ' ', '\t', '\n', '\r':
			d.i++
		default:
			return
		}
	}
}

func (d *decoder) lit(w string) bool {
	if len(d.s)-d.i >= len(w) && string(d.s[d.i:d.i+len(w)]) == w {
		d.i += len(w)
		return true
	}
	return false
}

func (d *decoder) value() (any, error) {
	if d.i >= len(d.s) {
		return nil, d.errf("Expecting value")
	}
	switch c := d.s[d.i]; {
	case c == '"':
		d.i++
		return d.str()
	case c == '{':
		d.i++
		return d.object()
	case c == '[':
		d.i++
		return d.array()
	case d.lit("null"):
		return nil, nil
	case d.lit("true"):
		return true, nil
	case d.lit("false"):
		return false, nil
	}
	if v, ok := d.number(); ok {
		return v, nil
	}
	switch {
	case d.lit("NaN"):
		return math.NaN(), nil
	case d.lit("Infinity"):
		return math.Inf(1), nil
	case d.lit("-Infinity"):
		return math.Inf(-1), nil
	}
	return nil, d.errf("Expecting value")
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// number matches Python's NUMBER_RE: (-?(?:0|[1-9]\d*))(\.\d+)?([eE][-+]?\d+)?
func (d *decoder) number() (any, bool) {
	s, i := d.s, d.i
	if i < len(s) && s[i] == '-' {
		i++
	}
	switch {
	case i < len(s) && s[i] == '0':
		i++
	case i < len(s) && isDigit(s[i]):
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	default:
		return nil, false
	}
	isFloat := false
	if i+1 < len(s) && s[i] == '.' && isDigit(s[i+1]) {
		isFloat = true
		for i++; i < len(s) && isDigit(s[i]); i++ {
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j < len(s) && isDigit(s[j]) {
			isFloat = true
			for i = j; i < len(s) && isDigit(s[i]); i++ {
			}
		}
	}
	lit := string(s[d.i:i])
	d.i = i
	if isFloat {
		f, _ := strconv.ParseFloat(lit, 64) // correctly rounded like float(); overflow -> +-inf
		return f, true
	}
	if n, err := strconv.ParseInt(lit, 10, 64); err == nil {
		return n, true
	}
	z, _ := new(big.Int).SetString(lit, 10)
	return z, true
}

func hex4(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var r rune
	for _, c := range b[:4] {
		switch {
		case '0' <= c && c <= '9':
			r = r<<4 | rune(c-'0')
		case 'a' <= c && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case 'A' <= c && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
}

// str scans after the opening quote (strict=True: raw control characters are errors).
func (d *decoder) str() (string, error) {
	var out []byte
	for {
		if d.i >= len(d.s) {
			return "", d.errf("Unterminated string")
		}
		c := d.s[d.i]
		switch {
		case c == '"':
			d.i++
			return string(out), nil
		case c < 0x20:
			return "", d.errf("Invalid control character")
		case c != '\\':
			out = append(out, c)
			d.i++
			continue
		}
		d.i++
		if d.i >= len(d.s) {
			return "", d.errf("Unterminated string")
		}
		e := d.s[d.i]
		d.i++
		switch e {
		case '"', '\\', '/':
			out = append(out, e)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			r, ok := hex4(d.s[d.i:])
			if !ok {
				return "", d.errf("Invalid \\uXXXX escape")
			}
			d.i += 4
			if 0xd800 <= r && r <= 0xdbff && len(d.s)-d.i >= 6 && d.s[d.i] == '\\' && d.s[d.i+1] == 'u' {
				if r2, ok := hex4(d.s[d.i+2:]); ok && 0xdc00 <= r2 && r2 <= 0xdfff {
					r = utf16.DecodeRune(r, r2)
					d.i += 6
				}
			}
			out = utf8.AppendRune(out, r) // a lone surrogate encodes as U+FFFD
		default:
			d.i--
			return "", d.errf("Invalid \\escape")
		}
	}
}

func (d *decoder) object() (any, error) {
	o := NewObject()
	d.ws()
	if d.i < len(d.s) && d.s[d.i] == '}' {
		d.i++
		return o, nil
	}
	for {
		if d.i >= len(d.s) || d.s[d.i] != '"' {
			return nil, d.errf("Expecting property name enclosed in double quotes")
		}
		d.i++
		k, err := d.str()
		if err != nil {
			return nil, err
		}
		d.ws()
		if d.i >= len(d.s) || d.s[d.i] != ':' {
			return nil, d.errf("Expecting ':' delimiter")
		}
		d.i++
		d.ws()
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		o.Set(k, v)
		d.ws()
		if d.i < len(d.s) && d.s[d.i] == '}' {
			d.i++
			return o, nil
		}
		if d.i >= len(d.s) || d.s[d.i] != ',' {
			return nil, d.errf("Expecting ',' delimiter")
		}
		d.i++
		d.ws()
	}
}

func (d *decoder) array() (any, error) {
	a := []any{}
	d.ws()
	if d.i < len(d.s) && d.s[d.i] == ']' {
		d.i++
		return a, nil
	}
	for {
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		d.ws()
		if d.i < len(d.s) && d.s[d.i] == ']' {
			d.i++
			return a, nil
		}
		if d.i >= len(d.s) || d.s[d.i] != ',' {
			return nil, d.errf("Expecting ',' delimiter")
		}
		d.i++
		d.ws()
	}
}
