package pipeline

// Maintainer check (builder dev cl04c; private dumps, never shipped): Run on a golden dump's m-log with that dump's
// recipe, every P2-E checkpoint compared in production order (golden.Diff on the first differing record, the
// comparison stops there), then the 9 files' sha256 against outputs.json and the cl04c lines of trial.txt.
//
// Projection: rows the DELNULL pass made carry a 't' whose Python type Go does not
// track; in records holding such rows an integral $f64 't' equals the same int. Everything else is compared exactly.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/golden"
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/sections"
)

// DevRun is one compared run (a dump).
type DevRun struct {
	Name     string
	Ckpts    int // checkpoint files compared equal
	CkptsPy  int // checkpoint files in the dump
	Records  int // records compared
	Loose    int // records equal only under the loose-t projection
	LooseAt  []string
	FirstBad string // first differing checkpoint ("" = all equal)
	Files    int    // output files sha-equal (of 9)
	FilesPy  int
	FileDiff string
	Lines    int // cl04c lines of trial.txt equal
	LinesPy  int
	LineDiff string
	Err      error
	Took     time.Duration // the checked run (with checkpoints)
	Plain    time.Duration // a plain run (production mode), when timed
}

// OK is every comparison equal.
func (d *DevRun) OK() bool {
	return d.Err == nil && d.FirstBad == "" && d.Ckpts == d.CkptsPy && d.Files == d.FilesPy && d.FileDiff == "" &&
		d.Lines == d.LinesPy && d.LineDiff == ""
}

// DevOptions configure DevCheck.
type DevOptions struct {
	Datapack string // data pack dir ("" = BC2_DATAPACK_DIR, else <golden root>/../datapack_oracle_r277)
	Demos    string // archive demos dir for 9-file SRC sets not in the map pack ("" = BC2_DEMOS_DIR)
	Work     string // scratch dir for the outputs ("" = os.TempDir)
	Time     bool   // also time a plain (checkpoint-free) run
	Workers  int
}

// datapack is the data pack dir for dump dir.
func (o DevOptions) datapack(dir string) string {
	if o.Datapack != "" {
		return o.Datapack
	}
	return sections.DatapackDir(filepath.Dir(dir))
}

type summaryJSON struct {
	Capture, Src, Stem string
	Env                [][2]string
}

func readSummary(dir string) (*summaryJSON, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		return nil, err
	}
	v, err := pyc.Loads(raw)
	if err != nil {
		return nil, err
	}
	o, _ := v.(*pyc.Object)
	if o == nil {
		return nil, errors.New("summary.json: not an object")
	}
	s := &summaryJSON{}
	str := func(k string) string { x, _ := o.Get(k); y, _ := x.(string); return y }
	s.Capture, s.Src, s.Stem = str("capture"), str("src"), str("stem")
	if e, _ := o.Get("env"); e != nil {
		eo, _ := e.(*pyc.Object)
		for _, k := range eo.Keys() {
			x, _ := eo.Get(k)
			y, _ := x.(string)
			s.Env = append(s.Env, [2]string{k, y})
		}
	}
	return s, nil
}

// ckCompare compares checkpoint records against the dump files as they are produced.
type ckCompare struct {
	dir     string
	readers map[string]*golden.Reader
	files   map[string]*os.File
	seen    []string
	n       int
	loose   int
	looseAt []string
	bad     string
}

func (c *ckCompare) close() {
	for _, f := range c.files {
		f.Close()
	}
}

// normT replaces an integral {"$f64"} value of every 't' key with the int.
func normT(t any) any {
	switch x := t.(type) {
	case []any:
		for i := range x {
			x[i] = normT(x[i])
		}
	case *pyc.Object:
		for _, k := range x.Keys() {
			v, _ := x.Get(k)
			if k == "t" {
				if vo, ok := v.(*pyc.Object); ok && vo.Len() == 1 && vo.Keys()[0] == "$f64" {
					if u, err := golden.Untag(vo); err == nil {
						if f, ok := u.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
							x.Set(k, int64(f))
							continue
						}
					}
				}
			}
			x.Set(k, normT(v))
		}
	}
	return t
}

func canonLine(t any) (string, error) {
	return pyc.Dumps(t, pyc.DumpOptions{EnsureASCII: true, ItemSep: ",", KeySep: ":", DisallowNaN: true})
}

// diffPath is golden.Diff of one record pair (first differing JSON path).
func diffPath(stage string, a, b []byte) string {
	gz := func(l []byte) io.Reader {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Write([]byte(`{"golden":1}` + "\n"))
		w.Write(l)
		w.Write([]byte("\n"))
		w.Close()
		return &buf
	}
	d, err := golden.Diff(gz(a), gz(b), golden.Options{})
	if err != nil {
		return err.Error()
	}
	if d == nil {
		return "(equal after decoding)"
	}
	return fmt.Sprintf("%s %s: go=%s py=%s", stage, d.Path, d.A, d.B)
}

func (c *ckCompare) record(stage string, rec any, loose bool) {
	if c.bad != "" {
		return
	}
	r := c.readers[stage]
	if r == nil {
		if !slices.Contains(c.seen, stage) {
			c.seen = append(c.seen, stage)
			f, err := os.Open(filepath.Join(c.dir, stage+".jsonl.gz"))
			if err != nil {
				c.bad = fmt.Sprintf("%s: Go writes it, the dump has none", stage)
				return
			}
			c.files[stage] = f
			if r, err = golden.NewReader(f); err != nil {
				c.bad = fmt.Sprintf("%s: %v", stage, err)
				return
			}
			if _, err := r.Next(); err != nil { // header
				c.bad = fmt.Sprintf("%s: %v", stage, err)
				return
			}
			c.readers[stage] = r
		}
	}
	gl, err := golden.Line(rec)
	if err != nil {
		c.bad = fmt.Sprintf("%s: encode: %v", stage, err)
		return
	}
	pl, err := r.Next()
	if err == io.EOF {
		c.bad = fmt.Sprintf("%s line %d: the dump ends, Go has %.200s", stage, r.LineNo()+1, gl)
		return
	} else if err != nil {
		c.bad = fmt.Sprintf("%s: %v", stage, err)
		return
	}
	c.n++
	if string(pl) == gl {
		return
	}
	if loose {
		if t, err := pyc.Loads(pl); err == nil {
			if l, err := canonLine(normT(t)); err == nil && l == gl {
				c.loose++
				if !slices.Contains(c.looseAt, stage) {
					c.looseAt = append(c.looseAt, stage)
				}
				return
			}
		}
	}
	c.bad = fmt.Sprintf("line %d %s", r.LineNo(), diffPath(stage, []byte(gl), slices.Clone(pl)))
}

// finish checks every dump checkpoint file was produced and read to its end.
func (c *ckCompare) finish() (equal, total int) {
	names, _ := filepath.Glob(filepath.Join(c.dir, "ckpt_*.jsonl.gz"))
	for _, p := range names {
		total++
		st := strings.TrimSuffix(filepath.Base(p), ".jsonl.gz")
		r := c.readers[st]
		switch {
		case r == nil:
			if c.bad == "" {
				c.bad = st + ": in the dump, Go writes none"
			}
		default:
			if _, err := r.Next(); err != io.EOF {
				if c.bad == "" {
					c.bad = fmt.Sprintf("%s: the dump has more records (line %d)", st, r.LineNo())
				}
				continue
			}
			equal++
		}
	}
	if c.bad != "" && equal == total {
		equal--
	}
	return equal, total
}

func fileSha(p string) (string, int64, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", 0, err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), int64(len(b)), nil
}

// cl04cPrefixes are the cl04c lines v3_trial copies into its summary (after two spaces).
var cl04cPrefixes = []string{"wire multi", "wire deletes", "wire companions", "wire registry", "tplnew_bad", "crebase_vs_src"}

// compareLines checks the trial.txt lines that came from cl04c against Go's stdout (timings excluded).
func compareLines(trial string, stdout string) (eq, total int, diff string) {
	raw, err := os.ReadFile(trial)
	if err != nil {
		return 0, 0, ""
	}
	out := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	last := ""
	if len(out) > 0 {
		last = out[len(out)-1]
	}
	k := 0
	for _, l := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if _, tail, ok := strings.Cut(l, "build: ok in "); ok && strings.HasPrefix(l, "build: ok in ") {
			_, t, _ := strings.Cut(tail, "; ")
			total++
			if t == last {
				eq++
			} else if diff == "" {
				diff = fmt.Sprintf("last line:\n    go %s\n    py %s", last, t)
			}
			continue
		}
		body, ok := strings.CutPrefix(l, "  ")
		if !ok || !slices.ContainsFunc(cl04cPrefixes, func(p string) bool { return strings.HasPrefix(body, p) }) {
			continue
		}
		total++
		found := false
		for ; k < len(out); k++ {
			if out[k] == body {
				found = true
				k++
				break
			}
		}
		if found {
			eq++
		} else if diff == "" {
			diff = fmt.Sprintf("trial.txt line not in Go stdout (in order): %.300s", body)
			k = 0
		}
	}
	return eq, total, diff
}

// DevCheck runs the check on one dump dir.
func DevCheck(ctx context.Context, dir, mlog string, o DevOptions) []*DevRun {
	s, err := readSummary(dir)
	if err != nil {
		return []*DevRun{{Name: filepath.Base(dir), Err: err}}
	}
	o.Datapack = o.datapack(dir)
	if o.Demos == "" {
		o.Demos = os.Getenv("BC2_DEMOS_DIR")
	}
	return []*DevRun{devOne(ctx, dir, filepath.Base(dir), mlog, s, s.Env, strings.TrimPrefix(s.Stem, "cl04_"), o)}
}

func devOne(ctx context.Context, dir, name, mlog string, s *summaryJSON, env [][2]string, suffix string, o DevOptions) *DevRun {
	d := &DevRun{Name: name}
	work, err := os.MkdirTemp(o.Work, "cl04c-")
	if err != nil {
		d.Err = err
		return d
	}
	defer os.RemoveAll(work)
	rec := DefaultRecipe()
	rec.SRC = s.Src
	rec.DELNULL = ""  // r284: the golden dumps come from the frozen r277 oracle, where DELNULL was off by default
	rec.JOINKEY = "6" // r289: ... and JOINKEY was 6 (default now 'auto')
	for _, kv := range env {
		if err := rec.Set(kv[0], kv[1]); err != nil {
			d.Err = err
			return d
		}
	}
	in := Inputs{Log: mlog, Datapack: o.Datapack, OutDir: filepath.Join(work, "builds"), Suffix: suffix, Recipe: rec,
		Workers: o.Workers}
	if e, _ := lookupPack(o.Datapack, "", "", s.Src); e == nil { // 9-file SRC (not in the map pack), as the dumper
		if o.Demos == "" {
			d.Err = fmt.Errorf("SRC %s is not in the map pack: set BC2_DEMOS_DIR (archive demos)", s.Src)
			return d
		}
		in.DemosDir = o.Demos
	}
	if o.Time {
		pin := in
		pin.OutDir = filepath.Join(work, "plain")
		t := time.Now()
		if _, err := Run(ctx, pin); err != nil {
			d.Err = fmt.Errorf("plain run: %w", err)
			return d
		}
		d.Plain = time.Since(t)
	}
	cc := &ckCompare{dir: dir, readers: map[string]*golden.Reader{}, files: map[string]*os.File{}}
	defer cc.close()
	in.Checkpoint = cc.record
	t := time.Now()
	res, err := Run(ctx, in)
	d.Took = time.Since(t)
	d.Ckpts, d.CkptsPy = cc.finish()
	d.Records, d.FirstBad, d.Loose, d.LooseAt = cc.n, cc.bad, cc.loose, cc.looseAt
	if err != nil {
		d.Err = err
		return d
	}
	outs, err := readOutputs(filepath.Join(dir, "outputs.json"))
	if err != nil {
		d.Err = err
		return d
	}
	d.FilesPy = len(outs)
	for _, ext := range Exts {
		w, ok := outs[ext]
		if !ok {
			continue
		}
		sum, n, err := fileSha(filepath.Join(in.OutDir, "cl04_"+suffix+"."+ext))
		if err == nil && sum == w.sha && n == w.size {
			d.Files++
		} else if d.FileDiff == "" {
			d.FileDiff = fmt.Sprintf("%s: go %s (%d B) py %s (%d B) %v", ext, sum, n, w.sha, w.size, err)
		}
	}
	d.Lines, d.LinesPy, d.LineDiff = compareLines(filepath.Join(dir, "trial.txt"), res.Stdout)
	return d
}

type shaSize struct {
	sha  string
	size int64
}

func readOutputs(p string) (map[string]shaSize, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	v, err := pyc.Loads(raw)
	if err != nil {
		return nil, err
	}
	o, _ := v.(*pyc.Object)
	if o == nil {
		return nil, errors.New("outputs.json: not an object")
	}
	out := map[string]shaSize{}
	for _, k := range o.Keys() {
		x, _ := o.Get(k)
		e, _ := x.(*pyc.Object)
		if e == nil {
			continue
		}
		sv, _ := e.Get("sha256")
		zv, _ := e.Get("size")
		s, _ := sv.(string)
		z, _ := zv.(int64)
		out[k] = shaSize{s, z}
	}
	return out, nil
}

// DevDumps resolves dump dirs: a dir with summary.json is one dump, else every dump under it (names filter).
func DevDumps(arg string, names []string) ([]capture.GoldenDump, error) {
	if _, err := os.Stat(filepath.Join(arg, "summary.json")); err == nil {
		ds, err := capture.GoldenDumps(filepath.Dir(arg), []string{filepath.Base(arg)})
		if err != nil {
			return nil, err
		}
		if len(ds) == 0 {
			return nil, fmt.Errorf("%s: its capture m-log is missing", arg)
		}
		return ds, nil
	}
	return capture.GoldenDumps(arg, names)
}
