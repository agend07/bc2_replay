package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"bc2replay/builder/internal/golden"
)

// Maintainer exam: Go's parse / decode (v3_types.load) / to_v1_rows against the golden stage dumps of the frozen
// Python builder (private: <root>/<name>/{parse,decode,to_v1_rows}.jsonl.gz + summary.json naming the capture dir
// whose m-log.bin was dumped). Never shipped data; the exams skip when it is absent.

// GoldenEnv overrides the golden dump root.
const GoldenEnv = "BC2_GO_GOLDEN"

// DefaultGolden is $BC2_GO_GOLDEN, else the first <ancestor>/bc2-client-recorder/work/private/go_golden or
// <ancestor>/work/private/go_golden above dir; "" when none exists.
func DefaultGolden(dir string) string {
	if v := os.Getenv(GoldenEnv); v != "" {
		return v
	}
	d, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		for _, c := range []string{filepath.Join(d, "bc2-client-recorder", "work", "private", "go_golden"),
			filepath.Join(d, "work", "private", "go_golden")} {
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

// GoldenDump is one dump dir with the m-log it was made from.
type GoldenDump struct {
	Name, Dir, MLog string
}

// GoldenDumps lists the complete dumps under root (summary.json written last by the dumper; its "capture" names
// <root>/../<capture>/m-log.bin), in name order, restricted to want when given.
func GoldenDumps(root string, want []string) ([]GoldenDump, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []GoldenDump
	for _, e := range ents {
		if !e.IsDir() || len(want) > 0 && !slices.Contains(want, e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
		if err != nil {
			continue
		}
		var s struct{ Capture string }
		if json.Unmarshal(b, &s) != nil || s.Capture == "" {
			continue
		}
		m := filepath.Join(filepath.Dir(root), s.Capture, "m-log.bin")
		if _, err := os.Stat(m); err != nil {
			continue
		}
		out = append(out, GoldenDump{e.Name(), dir, m})
	}
	return out, nil
}

// StageCheck is one stage's comparison: Lines records compared, Diff the first difference ("" = equal).
type StageCheck struct {
	Stage string
	Lines int
	Diff  string
}

// Timing is the Go side's wall time per step (Decode includes v3_types.load's end_hint pass).
type Timing struct{ Read, Parse, Decode, V1Rows time.Duration }

// Total is the sum of the steps.
func (t Timing) Total() time.Duration { return t.Read + t.Parse + t.Decode + t.V1Rows }

// TimeLoad times read + ParseLog + Decode/Hint + ToV1Rows of one m-log.
func TimeLoad(mlog string) (Timing, error) {
	tr := time.Now()
	raw, err := os.ReadFile(mlog)
	if err != nil {
		return Timing{}, err
	}
	t0 := time.Now()
	recs, err := ParseLog(raw)
	if err != nil {
		return Timing{}, err
	}
	t1 := time.Now()
	d, err := Decode(recs)
	if err != nil {
		return Timing{}, err
	}
	d.Hint()
	t2 := time.Now()
	_ = ToV1Rows(d)
	return Timing{t0.Sub(tr), t1.Sub(t0), t2.Sub(t1), time.Since(t2)}, nil
}

// CheckDump decodes the dump's m-log and compares the three stages line by line with the dump files (the header
// line's meta is not compared). Stages whose file is absent are skipped.
func CheckDump(g GoldenDump) ([]StageCheck, Timing, *Decoded, error) {
	var tm Timing
	tr := time.Now()
	raw, err := os.ReadFile(g.MLog)
	if err != nil {
		return nil, tm, nil, err
	}
	t0 := time.Now()
	recs, err := ParseLog(raw)
	if err != nil {
		return nil, tm, nil, err
	}
	t1 := time.Now()
	d, err := Decode(recs)
	if err != nil {
		return nil, tm, nil, err
	}
	d.Hint()
	t2 := time.Now()
	rows := ToV1Rows(d)
	tm = Timing{t0.Sub(tr), t1.Sub(t0), t2.Sub(t1), time.Since(t2)}
	var out []StageCheck
	for _, st := range []struct {
		name string
		gen  func(func(any) error) error
	}{
		{"parse", func(e func(any) error) error { return GoldenParse(0, recs, e) }},
		{"decode", func(e func(any) error) error { return GoldenDecode(0, d, e) }},
		{"to_v1_rows", func(e func(any) error) error { return GoldenV1Rows(rows, e) }},
	} {
		c, err := compareStage(filepath.Join(g.Dir, st.name+".jsonl.gz"), st.name, st.gen)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, tm, nil, err
		}
		out = append(out, c)
	}
	return out, tm, d, nil
}

var errStop = errors.New("stop")

func compareStage(path, stage string, gen func(func(any) error) error) (StageCheck, error) {
	c := StageCheck{Stage: stage}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	r, err := golden.NewReader(f)
	if err != nil {
		return c, err
	}
	h, err := r.Next()
	if err != nil {
		return c, fmt.Errorf("%s: header: %v", path, err)
	}
	var hdr struct {
		Golden int
		Stage  string
	}
	if json.Unmarshal(h, &hdr) != nil || hdr.Golden != 1 || hdr.Stage != stage {
		return c, fmt.Errorf("%s: not a golden v1 %s dump", path, stage)
	}
	err = gen(func(rec any) error {
		l, err := golden.Line(rec)
		if err != nil {
			return err
		}
		want, err := r.Next()
		if err == io.EOF {
			c.Diff = fmt.Sprintf("record %d: python dump ends, go has %.300s", c.Lines, l)
			return errStop
		} else if err != nil {
			return err
		}
		if !bytes.Equal(want, []byte(l)) {
			c.Diff = fmt.Sprintf("record %d:\n      go: %.400s\n      py: %.400s", c.Lines, l, want)
			return errStop
		}
		c.Lines++
		return nil
	})
	if err != nil && err != errStop {
		return c, err
	}
	if c.Diff == "" {
		if extra, err := r.Next(); err == nil {
			c.Diff = fmt.Sprintf("record %d: go ends, python has %.300s", c.Lines, extra)
		} else if err != io.EOF {
			return c, err
		}
	}
	return c, nil
}

// CompareStage compares the records gen emits (in order, as canonical golden lines) with the dump file at path,
// whose header must name stage; the first difference is in the returned StageCheck.Diff ("" = equal).
func CompareStage(path, stage string, gen func(emit func(any) error) error) (StageCheck, error) {
	return compareStage(path, stage, gen)
}
