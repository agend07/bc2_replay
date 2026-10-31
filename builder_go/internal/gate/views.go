package gate

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/pyc"
)

// unseen is fbgd_gate.UNSEEN: the inheritable encoding-debt rules.
var unseen = map[string]bool{"unseen-widths": true, "unseen-indices": true, "unseen-forms": true, "unseen-shape": true}

func getStr(o *pyc.Object, k string) string {
	v, _ := o.Get(k)
	s, _ := v.(string)
	return s
}

// getInt is o.get(k) as an int; ok is false when the key is absent or not an int.
func getInt(o *pyc.Object, k string) (int, bool) {
	v, ok := o.Get(k)
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	}
	return 0, false
}

// errorForm is fbgd_gate.error_form as a canonical string ("" = None: structural, never inherited).
func errorForm(e *pyc.Object) string {
	rule := getStr(e, "rule")
	if !unseen[rule] {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(rule)
	sb.WriteByte('|')
	if v, ok := e.Get("signature"); ok && v != nil {
		sb.WriteString("s" + fmt.Sprint(v))
	}
	for _, k := range []string{"section", "property", "width", "index"} {
		sb.WriteByte('|')
		if n, ok := getInt(e, k); ok {
			sb.WriteString(strconv.Itoa(n))
		}
	}
	sb.WriteByte('|')
	if v, ok := e.Get("refs"); ok {
		for _, r := range v.(refsValue) {
			sb.WriteString(strconv.Itoa(r.Flag) + ",")
		}
	}
	sb.WriteByte('|')
	if v, ok := e.Get("shape"); ok {
		sb.WriteString(shapeKey(v.([]int)))
	}
	return sb.String()
}

// errorFormJSON builds the same canonical string from a --save-baseline-summary form list
// [rule, signature, section, property, width, index, [flags], [shape]].
func errorFormJSON(v any) (string, error) {
	l, ok := v.([]any)
	if !ok || len(l) != 8 {
		return "", fmt.Errorf("bad baseline summary form %v", v)
	}
	rule, ok := l[0].(string)
	if !ok {
		return "", fmt.Errorf("bad baseline summary form %v", v)
	}
	var sb strings.Builder
	sb.WriteString(rule)
	sb.WriteByte('|')
	if s, ok := l[1].(string); ok {
		sb.WriteString("s" + s)
	} else if l[1] != nil {
		return "", fmt.Errorf("bad baseline summary form %v", v)
	}
	for _, x := range l[2:6] {
		sb.WriteByte('|')
		if n, ok := x.(int64); ok {
			sb.WriteString(strconv.FormatInt(n, 10))
		} else if x != nil {
			return "", fmt.Errorf("bad baseline summary form %v", v)
		}
	}
	sb.WriteByte('|')
	flags, ok := intList(l[6])
	if !ok {
		return "", fmt.Errorf("bad baseline summary form %v", v)
	}
	for _, f := range flags {
		sb.WriteString(strconv.Itoa(f) + ",")
	}
	sb.WriteByte('|')
	shape, ok := intList(l[7])
	if !ok {
		return "", fmt.Errorf("bad baseline summary form %v", v)
	}
	sb.WriteString(shapeKey(shape))
	return sb.String(), nil
}

// formPairs is fbgd_gate's {(error_form, in_load_phase)}: forms = every form, load = the forms seen in the load phase.
type formPairs struct{ forms, load map[string]bool }

// baselineForms is fbgd_gate.baseline_forms(errors, first_soldier) (bfs < 0 = None).
func baselineForms(errs []*pyc.Object, bfs int) formPairs {
	p := formPairs{map[string]bool{}, map[string]bool{}}
	for _, e := range errs {
		f := errorForm(e)
		if f == "" {
			continue
		}
		p.forms[f] = true
		if bfs >= 0 {
			fr, ok := getInt(e, "frame")
			if !ok {
				fr = bfs
			}
			if fr < bfs {
				p.load[f] = true
			}
		}
	}
	return p
}

// partitionForms is fbgd_gate.partition_forms -> (new, inherited).
func partitionForms(errs []*pyc.Object, p formPairs, fs, bfs int) ([]*pyc.Object, []*pyc.Object) {
	phased := fs >= 0 && bfs >= 0
	var newE, inh []*pyc.Object
	for _, e := range errs {
		f := errorForm(e)
		load := false
		if phased {
			fr, ok := getInt(e, "frame")
			if !ok {
				fr = fs
			}
			load = fr < fs
		}
		set := p.forms
		if load {
			set = p.load
		}
		if f != "" && set[f] {
			inh = append(inh, e)
		} else if load && f != "" && p.forms[f] {
			newE = append(newE, with(e, "phase", "load"))
		} else {
			newE = append(newE, e)
		}
	}
	return newE, inh
}

// chunkValue is the early tuple (index, bits, value, refs) as json sees it.
func chunkValue(c *fbgd.Chunk) any {
	if c == nil {
		return nil
	}
	var v any = 0
	if c.Bits <= 62 {
		v = int64(c.Top(c.Bits))
	} else {
		v = c.Value()
	}
	return []any{c.Index, c.Bits, v, refsValue(c.Refs)}
}

func chunkEqual(a, b *fbgd.Chunk) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Index != b.Index || a.Bits != b.Bits || len(a.Refs) != len(b.Refs) || !slices.Equal(a.Refs, b.Refs) {
		return false
	}
	return a.ValueEqual(b)
}

// regression is fbgd_gate.regression(s, baseline): load-phase chunks that differ between build and baseline.
func regression(s, b *scanResult) []*pyc.Object {
	limit := -1
	for _, x := range []int{s.firstSoldier, b.firstSoldier, s.frames, b.frames} {
		if x >= 0 && (limit < 0 || x < limit) {
			limit = x
		}
	}
	keys := map[earlyKey]bool{}
	for k := range b.early {
		keys[k] = true
	}
	for k := range s.early {
		keys[k] = true
	}
	var sorted []earlyKey
	for k := range keys {
		if k.frame < limit {
			sorted = append(sorted, k)
		}
	}
	slices.SortFunc(sorted, func(x, y earlyKey) int {
		return cmp.Or(cmp.Compare(x.frame, y.frame), cmp.Compare(x.ghost, y.ghost), cmp.Compare(x.kind, y.kind),
			cmp.Compare(x.si, y.si), cmp.Compare(x.pi, y.pi))
	})
	var out []*pyc.Object
	for _, k := range sorted {
		a, c := b.early[k], s.early[k]
		if chunkEqual(a, c) {
			continue
		}
		out = append(out, obj("rule", "load-chunk-change", "frame", k.frame, "ghost", k.ghost, "kind", k.kind,
			"section", k.si, "property", k.pi, "before", chunkValue(a), "after", chunkValue(c)))
	}
	return out
}

// counter is an insertion-ordered collections.Counter of strings.
type counter struct {
	keys []string
	n    map[string]int
}

func (c *counter) add(k string) {
	if c.n == nil {
		c.n = map[string]int{}
	}
	if _, ok := c.n[k]; !ok {
		c.keys = append(c.keys, k)
	}
	c.n[k]++
}

func (c *counter) object() *pyc.Object {
	o := pyc.NewObject()
	for _, k := range c.keys {
		o.Set(k, c.n[k])
	}
	return o
}

// repr is repr(dict(counter)): {'a': 1, 'b': 2}.
func (c *counter) repr() string {
	parts := make([]string, len(c.keys))
	for i, k := range c.keys {
		parts[i] = pyStr(k) + ": " + strconv.Itoa(c.n[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// ghostView is fbgd_gate.ghost_view(errors, allowed).
func ghostView(errs []*pyc.Object, w *Whitelist) []*pyc.Object {
	prefixWidths := map[string]map[int]bool{}
	for sig := range w.shapes {
		prefix, width, _ := strings.Cut(sig, "/")
		if !strings.HasPrefix(width, "*") {
			n, _ := strconv.Atoi(width)
			if prefixWidths[prefix] == nil {
				prefixWidths[prefix] = map[int]bool{}
			}
			prefixWidths[prefix][n] = true
		}
	}
	type row struct {
		ghost       int
		sig         string
		frame       int
		errors      int
		rules       counter
		slots       map[[2]int]bool
		shape       []int
		createWidth any
	}
	type rk struct {
		ghost int
		sig   string
	}
	rows := map[rk]*row{}
	var order []*row
	for _, e := range errs {
		rule := getStr(e, "rule")
		if !unseen[rule] {
			continue
		}
		g, _ := getInt(e, "ghost")
		sig := getStr(e, "signature")
		fr, _ := getInt(e, "frame")
		r := rows[rk{g, sig}]
		if r == nil {
			cw, ok := e.Get("create_width")
			if !ok {
				cw = nil
			}
			r = &row{ghost: g, sig: sig, frame: fr, slots: map[[2]int]bool{}, createWidth: cw}
			rows[rk{g, sig}] = r
			order = append(order, r)
		}
		r.errors++
		r.rules.add(rule)
		r.frame = min(r.frame, fr)
		if rule == "unseen-shape" {
			v, _ := e.Get("shape")
			r.shape = v.([]int)
		} else if v, ok := e.Get("section"); ok && v != nil {
			si, _ := getInt(e, "section")
			pi, _ := getInt(e, "property")
			r.slots[[2]int{si, pi}] = true
		}
	}
	var out []*pyc.Object
	sort := make([]*row, len(order))
	copy(sort, order)
	slices.SortStableFunc(sort, func(a, b *row) int {
		return cmp.Or(cmp.Compare(b.errors, a.errors), cmp.Compare(a.frame, b.frame), cmp.Compare(a.ghost, b.ghost))
	})
	for _, r := range sort {
		prefix, width, _ := strings.Cut(r.sig, "/")
		var same []int
		if r.shape != nil {
			for sig, shapes := range w.shapes {
				p2, w2, _ := strings.Cut(sig, "/")
				if p2 != prefix || fbgd.VariableWidth(sig) {
					continue
				}
				for _, sh := range shapes {
					if len(sh) == len(r.shape) {
						n, _ := strconv.Atoi(w2)
						same = append(same, n)
						break
					}
				}
			}
			slices.Sort(same)
		}
		corpus := same
		if len(same) == 0 {
			corpus = sortedInts(prefixWidths[prefix])
		}
		var cw any = r.createWidth
		if !strings.HasPrefix(width, "*") {
			cw, _ = strconv.Atoi(width)
		}
		var shape any
		if r.shape != nil {
			shape = r.shape
		}
		out = append(out, obj("ghost", r.ghost, "signature", r.sig, "shape", shape, "frame", r.frame,
			"errors", r.errors, "rules", r.rules.object(), "slots", len(r.slots), "create_width", cw,
			"corpus_widths", nonNil(corpus), "same_sections", len(same) > 0, "known_create", len(w.shapes[r.sig]) > 0))
	}
	return out
}

// warningView is fbgd_gate.warning_view(warnings).
func warningView(ws []*pyc.Object) []*pyc.Object {
	type row struct{ ghost, frame, warnings, buildOnly, baselineOnly, changed int }
	rows := map[int]*row{}
	var order []*row
	for _, w := range ws {
		if getStr(w, "rule") != "load-chunk-change" {
			continue
		}
		g, _ := getInt(w, "ghost")
		fr, _ := getInt(w, "frame")
		r := rows[g]
		if r == nil {
			r = &row{ghost: g, frame: fr}
			rows[g] = r
			order = append(order, r)
		}
		r.warnings++
		r.frame = min(r.frame, fr)
		before, _ := w.Get("before")
		after, _ := w.Get("after")
		switch {
		case before == nil:
			r.buildOnly++
		case after == nil:
			r.baselineOnly++
		default:
			r.changed++
		}
	}
	slices.SortStableFunc(order, func(a, b *row) int {
		return cmp.Or(cmp.Compare(b.warnings, a.warnings), cmp.Compare(a.frame, b.frame))
	})
	out := make([]*pyc.Object, len(order))
	for i, r := range order {
		out[i] = obj("ghost", r.ghost, "frame", r.frame, "warnings", r.warnings, "build_only", r.buildOnly,
			"baseline_only", r.baselineOnly, "changed", r.changed)
	}
	return out
}
