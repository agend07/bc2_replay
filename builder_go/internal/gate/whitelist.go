package gate

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/pyc"
)

// Version is fbgd_gate.VERSION.
const Version = 6

// slot is a whitelist slot (signature, section, property); its whitelist JSON key is json.dumps([sig, si, pi]).
type slot struct {
	sig    string
	si, pi int
}

// formKey encodes an ordered flag tuple as one byte per flag (flags are u1); string order == Python tuple order.
func formKey(flags []int) string {
	b := make([]byte, len(flags))
	for i, f := range flags {
		b[i] = byte(f)
	}
	return string(b)
}

func formOf(key string) []int {
	out := make([]int, len(key))
	for i := range key {
		out[i] = int(key[i])
	}
	return out
}

func shapeKey(shape []int) string {
	var sb strings.Builder
	for i, n := range shape {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.Itoa(n))
	}
	return sb.String()
}

// slotSets are the allowed widths / indices / forms / stale forms of one slot.
type slotSets struct {
	widths, indices map[int]bool
	forms, stale    map[string]bool
}

// Whitelist is fbgd_gate's `allowed` (widths, indices, forms, shapes, stale) plus its header.
type Whitelist struct {
	Header *pyc.Object // nil in corpus mode
	slots  map[slot]*slotSets
	shapes map[string]map[string][]int // signature -> shape key -> shape
}

func newWhitelist() *Whitelist {
	return &Whitelist{slots: map[slot]*slotSets{}, shapes: map[string]map[string][]int{}}
}

func (w *Whitelist) slotSets(s slot) *slotSets {
	ss := w.slots[s]
	if ss == nil {
		ss = &slotSets{widths: map[int]bool{}, indices: map[int]bool{}, forms: map[string]bool{}, stale: map[string]bool{}}
		w.slots[s] = ss
	}
	return ss
}

func (w *Whitelist) addShape(sig string, shape []int) {
	m := w.shapes[sig]
	if m == nil {
		m = map[string][]int{}
		w.shapes[sig] = m
	}
	m[shapeKey(shape)] = shape
}

// sortedShapes is sorted(allowed['shapes'].get(sig, set())).
func (w *Whitelist) sortedShapes(sig string) [][]int {
	var out [][]int
	for _, s := range w.shapes[sig] {
		out = append(out, s)
	}
	slices.SortFunc(out, slices.Compare)
	return out
}

func sortedInts(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func sortedForms(m map[string]bool) []any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = formOf(k)
	}
	return out
}

// parseSlotKey decodes json.dumps([sig, si, pi]).
func parseSlotKey(k string) (slot, error) {
	v, err := pyc.Loads([]byte(k))
	l, ok := v.([]any)
	if err != nil || !ok || len(l) != 3 {
		return slot{}, fmt.Errorf("bad whitelist slot key %q", k)
	}
	sig, ok1 := l[0].(string)
	si, ok2 := l[1].(int64)
	pi, ok3 := l[2].(int64)
	if !ok1 || !ok2 || !ok3 {
		return slot{}, fmt.Errorf("bad whitelist slot key %q", k)
	}
	return slot{sig, int(si), int(pi)}, nil
}

func intList(v any) ([]int, bool) {
	l, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]int, len(l))
	for i, x := range l {
		n, ok := x.(int64)
		if !ok {
			return nil, false
		}
		out[i] = int(n)
	}
	return out, true
}

// LoadWhitelist is fbgd_gate.load_whitelist for the r270 JSON form (.json / .json.zlib). A pickle whitelist is refused.
func LoadWhitelist(path string) (*Whitelist, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > 0 && data[0] == 0x78 {
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		data, err = io.ReadAll(zr)
		if err != nil {
			return nil, err
		}
	}
	if len(data) == 0 || data[0] != '{' {
		return nil, fmt.Errorf("whitelist %s is not the JSON form (pickle whitelists are not supported by the Go gate)", path)
	}
	v, err := pyc.Loads(data)
	if err != nil {
		return nil, err
	}
	top, ok := v.(*pyc.Object)
	if !ok {
		return nil, fmt.Errorf("whitelist %s: not a JSON object", path)
	}
	hv, _ := top.Get("header")
	av, _ := top.Get("allowed")
	h, ok1 := hv.(*pyc.Object)
	a, ok2 := av.(*pyc.Object)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("KeyError: whitelist %s lacks header/allowed", path)
	}
	gv, _ := h.Get("gate_version")
	lv, _ := h.Get("lint_version")
	if gv != any(int64(Version)) || lv != any(int64(fbgd.LintVersion)) {
		return nil, fmt.Errorf("whitelist %s has gate/lint VERSION %s/%s, this gate is %d/%d: regenerate with --save-whitelist",
			path, pyReprAny(gv), pyReprAny(lv), Version, fbgd.LintVersion)
	}
	w := newWhitelist()
	w.Header = h
	bad := func(cat, k string) error { return fmt.Errorf("whitelist %s: bad %s entry %q", path, cat, k) }
	for _, cat := range a.Keys() {
		cv, _ := a.Get(cat)
		d, ok := cv.(*pyc.Object)
		if !ok {
			return nil, bad(cat, "")
		}
		for _, k := range d.Keys() {
			vv, _ := d.Get(k)
			vals, ok := vv.([]any)
			if !ok {
				return nil, bad(cat, k)
			}
			if cat == "shapes" {
				if strings.HasPrefix(k, "[") {
					return nil, bad(cat, k)
				}
				if w.shapes[k] == nil {
					w.shapes[k] = map[string][]int{}
				}
				for _, x := range vals {
					s, ok := intList(x)
					if !ok {
						return nil, bad(cat, k)
					}
					w.addShape(k, s)
				}
				continue
			}
			s, err := parseSlotKey(k)
			if err != nil {
				return nil, err
			}
			ss := w.slotSets(s)
			for _, x := range vals {
				switch cat {
				case "widths", "indices":
					n, ok := x.(int64)
					if !ok {
						return nil, bad(cat, k)
					}
					if cat == "widths" {
						ss.widths[int(n)] = true
					} else {
						ss.indices[int(n)] = true
					}
				case "forms", "stale":
					f, ok := intList(x)
					if !ok {
						return nil, bad(cat, k)
					}
					for _, flag := range f {
						if flag != 0 && flag != 1 {
							return nil, bad(cat, k)
						}
					}
					if cat == "forms" {
						ss.forms[formKey(f)] = true
					} else {
						ss.stale[formKey(f)] = true
					}
				default:
					return nil, bad(cat, k)
				}
			}
		}
	}
	return w, nil
}

// CorpusSHA is fbgd_gate.corpus_sha(names): sha256 of the sorted stems joined by newlines.
func CorpusSHA(names []string) string {
	stems := make([]string, len(names))
	for i, n := range names {
		stems[i] = pathStem(n)
	}
	slices.Sort(stems)
	h := sha256.Sum256([]byte(strings.Join(stems, "\n")))
	return hex.EncodeToString(h[:])
}

func pathStem(p string) string {
	base := filepath.Base(strings.ReplaceAll(p, "\\", "/"))
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		return base[:i]
	}
	return base
}

// corpusWorks is the 'works' part of fbgd_lint.LABELS (every other label is dropped by the gate).
var corpusWorks = map[string]bool{"bis405t_u_971": true, "bisE_951": true, "bisG_951": true, "bisK_951": true}

// corpusOther is every non-'works' fbgd_lint.LABELS stem: such a stem is never corpus even with a 'works' name pattern.
var corpusOther = func() map[string]bool {
	m := map[string]bool{"bis405_965": true, "bis405p_966": true, "bis405s_967": true, "bis405t_u_970": true, "cl04_975": true,
		"bis405t_d1": true, "bis405t_d2": true, "cl04_976": true, "bisB_951": true, "bisB_950": true,
		"hybc_20261017_125023_950": true, "hybc2_20261017_125023_950": true}
	for _, n := range []int{961, 962, 963, 964, 972, 973, 974} {
		m[fmt.Sprintf("cl04_%d", n)] = true
	}
	return m
}()

// Corpus is the 'works' rows of fbgd_lint.corpus(root): root/demos then root/old_demos, each *.fbgd sorted like WindowsPath
// (case-insensitive), files < 64 bytes skipped.
func Corpus(root string) ([]string, error) {
	var out []string
	for _, dir := range []string{"demos", "old_demos"} {
		ents, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var names []string
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".fbgd") {
				names = append(names, e.Name())
			}
		}
		slices.SortStableFunc(names, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
		for _, n := range names {
			p := filepath.Join(root, dir, n)
			st, err := os.Stat(p)
			if err != nil {
				return nil, err
			}
			if st.Size() < 64 {
				continue
			}
			stem := pathStem(n)
			works := corpusWorks[stem] || (!corpusOther[stem] &&
				(strings.HasPrefix(stem, "patched_") || slices.Contains([]string{"MP_004", "MP_006CQ", "MP_5", "MP_6"}, stem)))
			if works {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// collector gathers fbgd_gate.whitelist(scans) while corpus demos are scanned (no per-file exemplars kept).
type collector struct{ w *Whitelist }

func (c *collector) observe(s slot, bits, index int, form string) {
	ss := c.w.slotSets(s)
	ss.widths[bits] = true
	ss.indices[index] = true
	ss.forms[form] = true
}

func (c *collector) shape(sig string, shape []int) { c.w.addShape(sig, shape) }

func (c *collector) staleForm(s slot, form string) { c.w.slotSets(s).stale[form] = true }
