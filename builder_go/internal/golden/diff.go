package golden

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"

	"bc2replay/builder/internal/pyc"
)

// Difference is the first place two dumps disagree.
type Difference struct {
	Line int    // 1-based line number
	Path string // JSON path inside the line, e.g. $.rows[3].value
	A, B string // canonical snippets (or a short description)
}

func (d *Difference) String() string {
	return fmt.Sprintf("line %d %s: A=%s B=%s", d.Line, d.Path, d.A, d.B)
}

// Options tune Diff. By default two NaN $f64 values with different payloads count as equal.
type Options struct {
	StrictNaN bool
}

// Diff compares two gzipped dumps on their decompressed lines and returns the first difference, or nil.
// Byte-equal lines are skipped; otherwise both lines are decoded and walked to the first differing path.
func Diff(a, b io.Reader, o Options) (*Difference, error) {
	ra, err := NewReader(a)
	if err != nil {
		return nil, fmt.Errorf("A: %w", err)
	}
	rb, err := NewReader(b)
	if err != nil {
		return nil, fmt.Errorf("B: %w", err)
	}
	for {
		la, ea := ra.Next()
		lb, eb := rb.Next()
		switch {
		case ea == io.EOF && eb == io.EOF:
			return nil, nil
		case ea != nil && ea != io.EOF:
			return nil, fmt.Errorf("A: %w", ea)
		case eb != nil && eb != io.EOF:
			return nil, fmt.Errorf("B: %w", eb)
		case ea == io.EOF:
			return &Difference{Line: rb.LineNo(), Path: "$", A: "<end of dump>", B: snip(string(lb))}, nil
		case eb == io.EOF:
			return &Difference{Line: ra.LineNo(), Path: "$", A: snip(string(la)), B: "<end of dump>"}, nil
		}
		if bytes.Equal(la, lb) {
			continue
		}
		ta, err := pyc.Loads(la)
		if err != nil {
			return nil, fmt.Errorf("A line %d: %w", ra.LineNo(), err)
		}
		tb, err := pyc.Loads(lb)
		if err != nil {
			return nil, fmt.Errorf("B line %d: %w", rb.LineNo(), err)
		}
		if d := walk(ta, tb, "$", o); d != nil {
			d.Line = ra.LineNo()
			return d, nil
		}
	}
}

func snip(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

func canon(t any) string {
	s, err := dump(t)
	if err != nil {
		return fmt.Sprintf("<%v>", err)
	}
	return snip(s)
}

// leaf reports a tagged scalar ({"$int"|"$f64"|"$bytes": "..."}), compared as a whole.
func leaf(o *pyc.Object) (string, string, bool) {
	if o.Len() != 1 {
		return "", "", false
	}
	k := o.Keys()[0]
	v, _ := o.Get(k)
	s, ok := v.(string)
	return k, s, ok && (k == "$int" || k == "$f64" || k == "$bytes")
}

func isNaNHex(s string) bool {
	u, err := strconv.ParseUint(s, 16, 64)
	return err == nil && u&0x7ff0000000000000 == 0x7ff0000000000000 && u&0xfffffffffffff != 0
}

func walk(a, b any, path string, o Options) *Difference {
	switch x := a.(type) {
	case []any:
		y, ok := b.([]any)
		if !ok {
			break
		}
		for i := 0; i < min(len(x), len(y)); i++ {
			if d := walk(x[i], y[i], path+"["+strconv.Itoa(i)+"]", o); d != nil {
				return d
			}
		}
		if len(x) != len(y) {
			return &Difference{Path: path, A: fmt.Sprintf("len %d", len(x)), B: fmt.Sprintf("len %d", len(y))}
		}
		return nil
	case *pyc.Object:
		y, ok := b.(*pyc.Object)
		if !ok {
			break
		}
		if ka, sa, ok := leaf(x); ok {
			if kb, sb, ok := leaf(y); ok && ka == kb && (sa == sb || ka == "$f64" && !o.StrictNaN && isNaNHex(sa) && isNaNHex(sb)) {
				return nil
			}
			break
		}
		kx, ky := x.Keys(), y.Keys()
		for i := 0; i < min(len(kx), len(ky)); i++ {
			if kx[i] != ky[i] {
				return &Difference{Path: path, A: "keys " + strings.Join(kx, ","), B: "keys " + strings.Join(ky, ",")}
			}
			va, _ := x.Get(kx[i])
			vb, _ := y.Get(ky[i])
			if d := walk(va, vb, path+"."+kx[i], o); d != nil {
				return d
			}
		}
		if len(kx) != len(ky) {
			return &Difference{Path: path, A: "keys " + strings.Join(kx, ","), B: "keys " + strings.Join(ky, ",")}
		}
		return nil
	}
	if ca, cb := canon(a), canon(b); ca != cb {
		return &Difference{Path: path, A: ca, B: cb}
	}
	return nil
}
