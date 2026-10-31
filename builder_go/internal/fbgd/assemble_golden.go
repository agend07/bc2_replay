package fbgd

// Maintainer exam for the write side: Assemble fed from a golden assemble_args dump (private, never shipped) must
// reproduce assemble_out (fbgd/fbgk length + sha256, info), and EncodeCompanion fed from the wire_companions dump must
// reproduce the six companion files of outputs.json.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/golden"
	"bc2replay/builder/internal/pyc"
)

// AssembleArgs is one decoded assemble_args dump.
type AssembleArgs struct {
	T0      float64
	KeySnap bool
	Tpl     Templates
	Rows    []*Row
}

// readDump calls f with every untagged record of a gz JSONL dump (header skipped).
func readDump(path string, f func(rec *pyc.Object) error) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	r, err := golden.NewReader(fh)
	if err != nil {
		return err
	}
	for {
		l, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if r.LineNo() == 1 {
			continue
		}
		t, err := pyc.Loads(l)
		if err != nil {
			return err
		}
		u, err := golden.Untag(t)
		if err != nil {
			return err
		}
		o, ok := u.(*pyc.Object)
		if !ok {
			return fmt.Errorf("%s line %d: not an object", path, r.LineNo())
		}
		if err := f(o); err != nil {
			return fmt.Errorf("%s line %d: %w", filepath.Base(path), r.LineNo(), err)
		}
	}
}

func field(o *pyc.Object, k string) (any, error) {
	v, ok := o.Get(k)
	if !ok {
		return nil, fmt.Errorf("missing %q", k)
	}
	return v, nil
}

func asInt(v any) (int64, error) {
	if x, ok := v.(int64); ok {
		return x, nil
	}
	return 0, fmt.Errorf("want int, got %T", v)
}

func intField(o *pyc.Object, k string) (int64, error) {
	v, err := field(o, k)
	if err != nil {
		return 0, err
	}
	return asInt(v)
}

// asTime is a row t / t0: an int below 2**53 or a float.
func asTime(v any) (float64, error) {
	switch x := v.(type) {
	case int64:
		if x <= -1<<53 || x >= 1<<53 {
			return 0, fmt.Errorf("time %d not exact in a float64", x)
		}
		return float64(x), nil
	case float64:
		return x, nil
	}
	return 0, fmt.Errorf("time: %T", v)
}

func asBits(v any, n int64) (bits.Bits, error) {
	switch x := v.(type) {
	case int64:
		if x < 0 {
			return bits.Bits{}, fmt.Errorf("negative value %d", x)
		}
		return bits.FromUint(uint64(x), int(n))
	case *big.Int:
		return bits.FromBig(x, int(n))
	}
	return bits.Bits{}, fmt.Errorf("value: %T", v)
}

func asRefs(v any) ([]Ref, error) {
	l, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("refs: %T", v)
	}
	out := make([]Ref, len(l))
	for i, it := range l {
		switch x := it.(type) {
		case int64:
			out[i] = Ref{int(x), 0}
		case []any:
			if len(x) < 1 {
				return nil, errors.New("empty ref")
			}
			a, err := asInt(x[0])
			if err != nil {
				return nil, err
			}
			var b int64
			if len(x) > 1 {
				if b, err = asInt(x[1]); err != nil {
					return nil, err
				}
			}
			out[i] = Ref{int(a), int(b)}
		default:
			return nil, fmt.Errorf("ref: %T", it)
		}
	}
	return out, nil
}

// chunkOf reads dict(value, bits[, refs][, index]); index defaults to idx.
func chunkOf(v any, idx int) (OutChunk, error) {
	o, ok := v.(*pyc.Object)
	if !ok {
		return OutChunk{}, fmt.Errorf("chunk: %T", v)
	}
	n, err := intField(o, "bits")
	if err != nil {
		return OutChunk{}, err
	}
	val, err := field(o, "value")
	if err != nil {
		return OutChunk{}, err
	}
	c := OutChunk{Index: idx}
	if c.Value, err = asBits(val, n); err != nil {
		return c, err
	}
	if x, ok := o.Get("index"); ok {
		i, err := asInt(x)
		if err != nil {
			return c, err
		}
		c.Index = int(i)
	}
	if x, ok := o.Get("refs"); ok {
		if c.Refs, err = asRefs(x); err != nil {
			return c, err
		}
	}
	return c, nil
}

func typeKey(v any) (TypeKey, error) {
	switch v.(type) {
	case int64, string:
		return v, nil
	}
	return nil, fmt.Errorf("type key: %T", v)
}

// TemplateOf converts a decoded template dict {'sections': [{header, count, mask, defaults}]} (the data pack's
// templates_pack.json entries as pyc.Loads returns them).
func TemplateOf(v any) (*Template, error) { return templateOf(v) }

func templateOf(v any) (*Template, error) {
	o, ok := v.(*pyc.Object)
	if !ok {
		return nil, fmt.Errorf("template: %T", v)
	}
	ss, err := field(o, "sections")
	if err != nil {
		return nil, err
	}
	l, _ := ss.([]any)
	t := &Template{Sections: make([]TplSection, len(l))}
	for i, it := range l {
		so, ok := it.(*pyc.Object)
		if !ok {
			return nil, fmt.Errorf("section: %T", it)
		}
		h, err := intField(so, "header")
		if err != nil {
			return nil, err
		}
		if h < 0 || h > 0xffffffff {
			return nil, fmt.Errorf("AssertionError: header %d", h)
		}
		cnt, err := intField(so, "count")
		if err != nil {
			return nil, err
		}
		m, err := field(so, "mask")
		if err != nil {
			return nil, err
		}
		s := TplSection{Header: uint32(h)}
		if s.Mask, err = asBits(m, cnt); err != nil {
			return nil, err
		}
		dv, err := field(so, "defaults")
		if err != nil {
			return nil, err
		}
		dl, _ := dv.([]any)
		s.Defaults = make([]OutChunk, len(dl))
		for j, d := range dl {
			if s.Defaults[j], err = chunkOf(d, j); err != nil {
				return nil, err
			}
		}
		t.Sections[i] = s
	}
	return t, nil
}

func chunkKey(k any) (ChunkKey, bool, error) {
	switch x := k.(type) {
	case int64:
		return ChunkKey{0, int(x)}, false, nil
	case []any:
		if len(x) == 2 {
			a, e1 := asInt(x[0])
			b, e2 := asInt(x[1])
			if e1 == nil && e2 == nil {
				return ChunkKey{int(a), int(b)}, true, nil
			}
		}
	}
	return ChunkKey{}, false, fmt.Errorf("chunk key: %v", k)
}

// RowOf converts one golden row (create, update or delete dict) to a Row.
func RowOf(o *pyc.Object) (*Row, error) {
	kv, err := field(o, "k")
	if err != nil {
		return nil, err
	}
	k, _ := kv.(string)
	tv, err := field(o, "t")
	if err != nil {
		return nil, err
	}
	r := &Row{}
	if r.T, err = asTime(tv); err != nil {
		return nil, err
	}
	id, err := intField(o, "id")
	if err != nil {
		return nil, err
	}
	r.ID = int(id)
	switch k {
	case "c":
		r.K = 'c'
		tp, err := field(o, "type")
		if err != nil {
			return nil, err
		}
		if r.Type, err = typeKey(tp); err != nil {
			return nil, err
		}
		cv, err := field(o, "create")
		if err != nil {
			return nil, err
		}
		if r.Create, err = chunkOf(cv, 0); err != nil {
			return nil, err
		}
	case "u":
		r.K = 'u'
		cv, err := field(o, "chunks")
		if err != nil {
			return nil, err
		}
		var items golden.Pairs
		switch x := cv.(type) {
		case golden.Pairs:
			items = x
		case *pyc.Object:
			if x.Len() != 0 {
				return nil, errors.New("chunks with str keys")
			}
		default:
			return nil, fmt.Errorf("chunks: %T", cv)
		}
		r.Chunks = make([]RowChunk, len(items))
		for i, p := range items {
			key, tuple, err := chunkKey(p.K)
			if err != nil {
				return nil, err
			}
			c, err := chunkOf(p.V, key.Prop)
			if err != nil {
				return nil, err
			}
			r.Chunks[i] = RowChunk{key, tuple, c.Value, c.Refs}
		}
	case "d":
		r.K = 'd'
	default:
		return nil, fmt.Errorf("row kind %q", k)
	}
	return r, nil
}

// LoadAssembleArgs reads <dir>/assemble_args.jsonl.gz (one assemble call).
func LoadAssembleArgs(dir string) (*AssembleArgs, error) {
	a := &AssembleArgs{Tpl: Templates{}}
	nrows, ntpl := int64(-1), int64(-1)
	err := readDump(filepath.Join(dir, "assemble_args.jsonl.gz"), func(o *pyc.Object) error {
		if c, _ := intField(o, "call"); c != 0 {
			return fmt.Errorf("call %d: one assemble call expected", c)
		}
		if v, ok := o.Get("t0"); ok {
			var err error
			if a.T0, err = asTime(v); err != nil {
				return err
			}
			ks, _ := o.Get("keysnap")
			a.KeySnap, _ = ks.(bool)
			if kw, _ := o.Get("kw"); kw != nil {
				if ko, ok := kw.(*pyc.Object); !ok || ko.Len() != 0 {
					return errors.New("assemble keyword arguments are not ported")
				}
			}
			nrows, _ = intField(o, "n_rows")
			ntpl, _ = intField(o, "n_tpl")
			return nil
		}
		if v, ok := o.Get("tpl"); ok {
			kv, _ := v.([]any)
			if len(kv) != 2 {
				return errors.New("tpl record")
			}
			k, err := typeKey(kv[0])
			if err != nil {
				return err
			}
			t, err := templateOf(kv[1])
			if err != nil {
				return err
			}
			a.Tpl[k] = t
			return nil
		}
		rv, err := field(o, "row")
		if err != nil {
			return err
		}
		ro, _ := rv.(*pyc.Object)
		if ro == nil {
			return errors.New("row record")
		}
		r, err := RowOf(ro)
		if err != nil {
			return err
		}
		a.Rows = append(a.Rows, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if int64(len(a.Rows)) != nrows || int64(len(a.Tpl)) != ntpl {
		return nil, fmt.Errorf("assemble_args: %d rows / %d templates, header says %d / %d", len(a.Rows), len(a.Tpl), nrows, ntpl)
	}
	return a, nil
}

// SumLen is a file's {len, sha256}.
type SumLen struct {
	Len int
	Sha string
}

func sumOf(b []byte) SumLen {
	h := sha256.Sum256(b)
	return SumLen{len(b), hex.EncodeToString(h[:])}
}

// AssembleOut is one assemble_out record.
type AssembleOut struct {
	FBGD, FBGK SumLen
	InfoLine   string // canonical golden line of info
}

func sumField(o *pyc.Object, k string) (SumLen, error) {
	v, err := field(o, k)
	if err != nil {
		return SumLen{}, err
	}
	so, _ := v.(*pyc.Object)
	if so == nil {
		return SumLen{}, fmt.Errorf("%s: not an object", k)
	}
	n, err := intField(so, "len")
	if err != nil {
		return SumLen{}, err
	}
	s, _ := so.Get("sha256")
	str, _ := s.(string)
	return SumLen{int(n), str}, nil
}

// LoadAssembleOut reads <dir>/assemble_out.jsonl.gz.
func LoadAssembleOut(dir string) (*AssembleOut, error) {
	var out *AssembleOut
	err := readDump(filepath.Join(dir, "assemble_out.jsonl.gz"), func(o *pyc.Object) error {
		if out != nil {
			return errors.New("one assemble call expected")
		}
		out = &AssembleOut{}
		var err error
		if out.FBGD, err = sumField(o, "fbgd"); err != nil {
			return err
		}
		if out.FBGK, err = sumField(o, "fbgk"); err != nil {
			return err
		}
		info, err := field(o, "info")
		if err != nil {
			return err
		}
		out.InfoLine, err = golden.Line(info)
		return err
	})
	if err == nil && out == nil {
		err = errors.New("assemble_out: no record")
	}
	return out, err
}

// LoadCompanions reads the last template* and build results of <dir>/wire_companions.jsonl.gz:
// prolog, hdr {ext: bits}, recs {ext: [(marker, bits)]}.
func LoadCompanions(dir string) (prolog []byte, hdr map[string]bits.Bits, recs map[string][]CompRec, err error) {
	err = readDump(filepath.Join(dir, "wire_companions.jsonl.gz"), func(o *pyc.Object) error {
		fn, _ := o.Get("fn")
		res, _ := o.Get("result")
		ro, _ := res.(*pyc.Object)
		switch fn {
		case "template", "template_from_pack":
			if ro == nil {
				return errors.New("template result")
			}
			p, _ := ro.Get("prolog")
			prolog, _ = p.([]byte)
			h, _ := ro.Get("hdr")
			ho, _ := h.(*pyc.Object)
			if prolog == nil || ho == nil {
				return errors.New("template prolog/hdr")
			}
			hdr = map[string]bits.Bits{}
			for _, k := range ho.Keys() {
				v, _ := ho.Get(k)
				s, _ := v.(string)
				b, err := bits.Parse(s)
				if err != nil {
					return err
				}
				hdr[k] = b
			}
		case "build":
			if ro == nil {
				return errors.New("build result")
			}
			recs = map[string][]CompRec{}
			for _, k := range ro.Keys() {
				v, _ := ro.Get(k)
				l, _ := v.([]any)
				rs := make([]CompRec, len(l))
				for i, it := range l {
					ms, _ := it.([]any)
					if len(ms) != 2 {
						return errors.New("build record")
					}
					m, err := asInt(ms[0])
					if err != nil {
						return err
					}
					s, _ := ms[1].(string)
					b, err := bits.Parse(s)
					if err != nil {
						return err
					}
					rs[i] = CompRec{byte(m), b}
				}
				recs[k] = rs
			}
		}
		return nil
	})
	if err == nil && (hdr == nil || recs == nil) {
		err = errors.New("wire_companions: no template/build record")
	}
	return
}

// LoadOutputs reads <dir>/outputs.json: {ext: {sha256, size}}.
func LoadOutputs(dir string) (map[string]SumLen, error) {
	b, err := os.ReadFile(filepath.Join(dir, "outputs.json"))
	if err != nil {
		return nil, err
	}
	var m map[string]struct {
		Sha256 string
		Size   int
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	out := map[string]SumLen{}
	for k, v := range m {
		out[k] = SumLen{v.Size, v.Sha256}
	}
	return out, nil
}

// AssembleCheck is the result of CheckAssemble on one dump.
type AssembleCheck struct {
	FBGD, FBGK, Info   bool
	Companions         int // companion files equal to outputs.json (of 6); -1 = no wire_companions dump
	GotInfo, WantInfo  string
	GotFBGD, GotFBGK   SumLen
	Load, Run, CompRun time.Duration
	Rows               int
}

// OK reports fbgd, fbgk, info and every companion file equal.
func (c *AssembleCheck) OK() bool { return c.FBGD && c.FBGK && c.Info && c.Companions == 6 }

// CheckAssemble runs Assemble on <dir>'s assemble_args and compares with assemble_out; then encodes the companion
// files from the wire_companions dump and compares them with outputs.json.
func CheckAssemble(dir string) (*AssembleCheck, error) {
	c := &AssembleCheck{Companions: -1}
	t := time.Now()
	args, err := LoadAssembleArgs(dir)
	if err != nil {
		return nil, err
	}
	want, err := LoadAssembleOut(dir)
	if err != nil {
		return nil, err
	}
	c.Load, c.Rows = time.Since(t), len(args.Rows)
	t = time.Now()
	data, k, info, err := Assemble(args.Rows, args.Tpl, args.T0, args.KeySnap)
	c.Run = time.Since(t)
	if err != nil {
		return c, fmt.Errorf("assemble: %w", err)
	}
	c.GotFBGD, c.GotFBGK = sumOf(data), sumOf(k)
	c.FBGD, c.FBGK = c.GotFBGD == want.FBGD, c.GotFBGK == want.FBGK
	if c.GotInfo, err = golden.Line(info); err != nil {
		return c, err
	}
	c.WantInfo = want.InfoLine
	c.Info = c.GotInfo == c.WantInfo
	if _, err := os.Stat(filepath.Join(dir, "wire_companions.jsonl.gz")); err != nil {
		return c, nil
	}
	prolog, hdr, recs, err := LoadCompanions(dir)
	if err != nil {
		return c, err
	}
	outs, err := LoadOutputs(dir)
	if err != nil {
		return c, err
	}
	c.Companions = 0
	t = time.Now()
	for _, ext := range CompanionExts {
		h, ok := hdr[ext]
		if !ok {
			continue
		}
		d, idx := EncodeCompanion(prolog, h, recs[ext])
		if sumOf(d) == outs[ext] {
			c.Companions++
		}
		if sumOf(idx) == outs[CompanionIndexExt(ext)] {
			c.Companions++
		}
	}
	c.CompRun = time.Since(t)
	return c, nil
}
