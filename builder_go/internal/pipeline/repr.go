package pipeline

import (
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/golden"
	"bc2replay/builder/internal/pyc"
)

// tuple is a Python tuple for repr ((a,) for one item).
type tuple []any

// pySet is a Python set of ints for repr ({1, 2} / set()); items are printed in the given order.
type pySet []int

// counterRepr is a collections.Counter's repr: Counter({k: n, ...}) in most_common() order (count descending,
// insertion order among equal counts); Counter() when empty.
type counterRepr struct{ c *fbgd.Counter }

// strRepr is Python repr(str): fbgd.StrRepr for ASCII; non-ASCII text keeps printable characters (unicode.IsPrint
// stands in for str.isprintable) and escapes the others \xhh / \uhhhh / \Uhhhhhhhh.
func strRepr(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return fbgd.StrRepr(s)
	}
	q := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		q = '"'
	}
	var b strings.Builder
	b.WriteRune(q)
	for _, c := range s {
		switch {
		case c == '\\':
			b.WriteString(`\\`)
		case c == q:
			b.WriteRune('\\')
			b.WriteRune(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, c)
		case c < 0x80 || unicode.IsPrint(c):
			b.WriteRune(c)
		case c < 0x100:
			fmt.Fprintf(&b, `\x%02x`, c)
		case c < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, c)
		default:
			fmt.Fprintf(&b, `\U%08x`, c)
		}
	}
	b.WriteRune(q)
	return b.String()
}

// repr is Python repr() of the values the cl04c prints carry.
func repr(v any) string {
	var b strings.Builder
	writeRepr(&b, v)
	return b.String()
}

func writeRepr(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("None")
	case bool:
		if x {
			b.WriteString("True")
		} else {
			b.WriteString("False")
		}
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case uint32:
		b.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint64:
		b.WriteString(strconv.FormatUint(x, 10))
	case *big.Int:
		b.WriteString(x.String())
	case bits.Bits:
		b.WriteString(x.Big().String())
	case float64:
		b.WriteString(pyc.Repr(x))
	case string:
		b.WriteString(strRepr(x))
	case []any:
		b.WriteByte('[')
		for i, it := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeRepr(b, it)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, it := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeRepr(b, it)
		}
		b.WriteByte(']')
	case tuple:
		b.WriteByte('(')
		for i, it := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeRepr(b, it)
		}
		if len(x) == 1 {
			b.WriteByte(',')
		}
		b.WriteByte(')')
	case pySet:
		if len(x) == 0 {
			b.WriteString("set()")
			return
		}
		b.WriteByte('{')
		for i, it := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.Itoa(it))
		}
		b.WriteByte('}')
	case *pyc.Object:
		b.WriteByte('{')
		for i, k := range x.Keys() {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strRepr(k))
			b.WriteString(": ")
			val, _ := x.Get(k)
			writeRepr(b, val)
		}
		b.WriteByte('}')
	case golden.Pairs:
		b.WriteByte('{')
		for i, p := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeRepr(b, p.K)
			b.WriteString(": ")
			writeRepr(b, p.V)
		}
		b.WriteByte('}')
	case *fbgd.Counter:
		writeRepr(b, counterDict(x))
	case counterRepr:
		if len(x.c.Keys()) == 0 {
			b.WriteString("Counter()")
			return
		}
		ks := slices.Clone(x.c.Keys())
		slices.SortStableFunc(ks, func(p, q string) int { return x.c.Get(q) - x.c.Get(p) })
		o := pyc.NewObject()
		for _, k := range ks {
			o.Set(k, x.c.Get(k))
		}
		b.WriteString("Counter(")
		writeRepr(b, o)
		b.WriteByte(')')
	case *idMap:
		b.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "%d: %d", k, x.m[k])
		}
		b.WriteByte('}')
	default:
		fmt.Fprintf(b, "<%T>", v)
	}
}

// counterDict is dict(c) as an ordered object.
func counterDict(c *fbgd.Counter) *pyc.Object {
	o := pyc.NewObject()
	for _, k := range c.Keys() {
		o.Set(k, c.Get(k))
	}
	return o
}

// printLine is print(*args): str() of each argument (repr except for str), joined by spaces.
func printLine(args ...any) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		if s, ok := a.(string); ok {
			b.WriteString(s)
		} else {
			writeRepr(&b, a)
		}
	}
	return b.String()
}
