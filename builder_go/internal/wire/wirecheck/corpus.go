// Package wirecheck is the maintainer exam of package wire against the r274 Unicorn oracle corpus
// (<corpus>/<log>.jsonl.gz, private; never shipped): the Go port of wire_py_check.py run mode.
package wirecheck

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"

	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// Logs are the corpus names in wire_py_check.LOGS order.
var Logs = []string{"t05", "t09", "cap173136", "cap181502", "cap182734", "t06", "t07", "t08", "t01", "t03", "t04"}

// CorpusEnv overrides the corpus directory.
const CorpusEnv = "BC2_WIRE_CORPUS"

// DefaultCorpus is $BC2_WIRE_CORPUS, else the first <ancestor>/bc2-client-recorder/work/private/wire_oracle (the
// private sibling repo) or <ancestor>/work/private/wire_oracle above dir; "" when none exists.
func DefaultCorpus(dir string) string {
	if v := os.Getenv(CorpusEnv); v != "" {
		return v
	}
	d, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		for _, c := range []string{filepath.Join(d, "bc2-client-recorder", "work", "private", "wire_oracle"),
			filepath.Join(d, "work", "private", "wire_oracle")} {
			if st, err := os.Stat(c); err == nil && st.IsDir() {
				return c
			}
		}
		p := filepath.Dir(d)
		if p == d {
			return ""
		}
		d = p
	}
}

// Available lists the corpus logs present under dir (in Logs order, or the requested order).
func Available(dir string, want []string) []string {
	if len(want) == 0 {
		want = Logs
	}
	var out []string
	for _, n := range want {
		if st, err := os.Stat(filepath.Join(dir, n+".jsonl.gz")); err == nil && !st.IsDir() {
			out = append(out, n)
		}
	}
	return out
}

// Run is one recorded harness run: its input (as wire_py_core.inp_from_record builds it) and the recorded output.
type Run struct {
	Log     string
	I       int64
	Site    string
	Sidx    int64
	Attempt int64
	Entry   uint32
	Inp     *wire.Inp
	Out     *pyc.Object // recorded wire_oracle._out
}

// Site is one recorded site record (wire_oracle: a former Unicorn call site and its verbatim result).
type Site struct {
	Kind   string
	Idx    int64
	Runs   []int64
	Args   *pyc.Object
	Result any
	Exc    any
}

// LoadLog reads one corpus log: buf records (hex payloads), run records and, when sites is set, site records;
// meta/end records are skipped.
func LoadLog(dir, name string, sites bool) ([]*Run, []*Site, error) {
	var ss []*Site
	runs, err := loadLog(dir, name, func(o *pyc.Object) error {
		if !sites {
			return nil
		}
		s := &Site{Kind: str(o, "site"), Args: nil, Result: get(o, "result"), Exc: get(o, "exc")}
		s.Args, _ = get(o, "args").(*pyc.Object)
		var err error
		if s.Idx, err = toInt(get(o, "idx")); err != nil {
			return err
		}
		if s.Runs, err = intList(get(o, "runs")); err != nil {
			return err
		}
		ss = append(ss, s)
		return nil
	}, sites)
	return runs, ss, err
}

func loadLog(dir, name string, site func(*pyc.Object) error, parseSites bool) ([]*Run, error) {
	f, err := os.Open(filepath.Join(dir, name+".jsonl.gz"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 1<<20), 1<<28)
	bufs := map[string][]byte{}
	type pending struct {
		run *Run
		key string
	}
	var runs []pending
	for ln := 1; sc.Scan(); ln++ {
		line := sc.Bytes()
		switch {
		case bytes.HasPrefix(line, []byte(`{"type":"buf"`)):
			var b struct{ Key, Hex string }
			if err := json.Unmarshal(line, &b); err != nil {
				return nil, fmt.Errorf("%s:%d: %v", name, ln, err)
			}
			p, err := hex.DecodeString(b.Hex)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %v", name, ln, err)
			}
			bufs[b.Key] = p
			continue
		case !parseSites && bytes.HasPrefix(line, []byte(`{"type":"site"`)):
			continue
		}
		v, err := pyc.Loads(line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", name, ln, err)
		}
		o, ok := v.(*pyc.Object)
		if !ok {
			return nil, fmt.Errorf("%s:%d: not an object", name, ln)
		}
		switch str(o, "type") {
		case "buf":
			p, err := hex.DecodeString(str(o, "hex"))
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %v", name, ln, err)
			}
			bufs[str(o, "key")] = p
		case "run":
			r, err := runOf(name, o)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %v", name, ln, err)
			}
			runs = append(runs, pending{r, str(o, "buf")})
		case "site":
			if err := site(o); err != nil {
				return nil, fmt.Errorf("%s:%d: %v", name, ln, err)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	out := make([]*Run, len(runs))
	for i, p := range runs {
		b, ok := bufs[p.key]
		if !ok {
			return nil, fmt.Errorf("%s: run %d: buffer %s missing", name, p.run.I, p.key)
		}
		p.run.Inp.Buf = b
		out[i] = p.run
	}
	return out, nil
}

// ---------------------------------------------------------------------------------------------------- record -> input

func get(o *pyc.Object, k string) any {
	v, _ := o.Get(k)
	return v
}

func str(o *pyc.Object, k string) string {
	s, _ := get(o, k).(string)
	return s
}

func toInt(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case *big.Int:
		if x.IsInt64() {
			return x.Int64(), nil
		}
	}
	return 0, fmt.Errorf("not an int: %v (%T)", v, v)
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int64:
		return float64(x), nil
	}
	return 0, fmt.Errorf("not a float: %v (%T)", v, v)
}

func hexKey(k string) (uint32, error) {
	v, err := strconv.ParseUint(k, 16, 32)
	return uint32(v), err
}

func intList(v any) ([]int64, error) {
	l, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("not a list: %T", v)
	}
	out := make([]int64, len(l))
	for i, x := range l {
		n, err := toInt(x)
		if err != nil {
			return nil, err
		}
		out[i] = n
	}
	return out, nil
}

// runOf is wire_py_core.inp_from_record (+ the run's bookkeeping fields).
func runOf(log string, o *pyc.Object) (*Run, error) {
	r := &Run{Log: log, Site: str(o, "site")}
	var err error
	ints := map[string]*int64{"i": &r.I, "sidx": &r.Sidx, "attempt": &r.Attempt}
	for k, p := range ints {
		if *p, err = toInt(get(o, k)); err != nil {
			return nil, fmt.Errorf("%s: %v", k, err)
		}
	}
	if r.Entry, err = hexKey(str(o, "entry")); err != nil {
		return nil, err
	}
	r.Out, _ = get(o, "out").(*pyc.Object)
	if r.Out == nil {
		return nil, fmt.Errorf("run without out")
	}
	inp := wire.NewInp()
	r.Inp = inp
	inp.Plain = str(o, "cls") != "WireHarness"
	inp.Entry = r.Entry
	if inp.NBits, err = toInt(get(o, "nbits")); err != nil {
		return nil, err
	}
	if st := get(o, "state"); st != nil {
		l, err := intList(st)
		if err != nil || len(l) != 4 {
			return nil, fmt.Errorf("state %v: %v", st, err)
		}
		inp.State = &wire.State{Cursor: l[0], Cache: l[1] & 0xffffffff, DwordOffset: l[2], Flags: l[3]}
	}
	a, _ := get(o, "attrs").(*pyc.Object)
	if a == nil {
		return nil, fmt.Errorf("run without attrs")
	}
	for _, k := range a.Keys() {
		if err := setAttr(inp, k, get(a, k)); err != nil {
			return nil, fmt.Errorf("attr %s: %v", k, err)
		}
	}
	if inp.Plain {
		inp.ResolveRefs = false // no C1E21E hook in the plain Harness
	}
	return r, nil
}

func setAttr(inp *wire.Inp, k string, v any) error {
	var err error
	boolOf := func(p *bool) error {
		b, ok := v.(bool)
		if !ok {
			return fmt.Errorf("not a bool: %T", v)
		}
		*p = b
		return nil
	}
	intOf := func(p *int64) error { *p, err = toInt(v); return err }
	switch k {
	case "generic":
		return boolOf(&inp.Generic)
	case "resolve_refs":
		return boolOf(&inp.ResolveRefs)
	case "ref_real":
		return boolOf(&inp.RefReal)
	case "player_fixture":
		return boolOf(&inp.PlayerFixture)
	case "qn":
		return intOf(&inp.QN)
	case "list_max":
		return intOf(&inp.ListMax)
	case "ref_bits":
		return intOf(&inp.RefBits)
	case "ghost_bits":
		return intOf(&inp.GhostBits)
	case "sec_vt":
		if v == nil {
			return nil
		}
		var n int64
		if err := intOf(&n); err != nil {
			return err
		}
		inp.SecVT = uint32(n)
	case "origin":
		l, ok := v.([]any)
		if !ok || len(l) != 3 {
			return fmt.Errorf("origin %v", v)
		}
		for i, x := range l {
			if inp.Origin[i], err = toFloat(x); err != nil {
				return err
			}
		}
	case "qt":
		if v == nil {
			inp.QT = nil
			return nil
		}
		inp.QT, err = intList(v)
		return err
	case "cfg_child":
		if v == nil {
			inp.CfgChild = nil
			return nil
		}
		s, _ := v.(string)
		inp.CfgChild, err = hex.DecodeString(s)
		if inp.CfgChild == nil {
			inp.CfgChild = []byte{}
		}
		return err
	case "sec_fields":
		m, ok := v.(*pyc.Object)
		if !ok {
			return fmt.Errorf("sec_fields %T", v)
		}
		inp.SecFields = map[uint32]int64{}
		for _, kk := range m.Keys() {
			a, err := hexKey(kk)
			if err != nil {
				return err
			}
			if inp.SecFields[a], err = toInt(get(m, kk)); err != nil {
				return err
			}
		}
	case "stubs":
		m, ok := v.(*pyc.Object)
		if !ok {
			return fmt.Errorf("stubs %T", v)
		}
		inp.Stubs = wire.StubSet{}
		for _, kk := range m.Keys() {
			a, err := hexKey(kk)
			if err != nil {
				return err
			}
			inp.Stubs[a] = struct{}{}
		}
	}
	// entry (same value as the run's), heap / skips / alloc_fixture (no reader reads them), watch / bookmarks
	// (diagnostics, off on the default path): ignored like wire_py_core.inp_from_record.
	return nil
}
