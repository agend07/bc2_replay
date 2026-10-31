package soldier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/golden"
)

// Maintainer exam: Convert over Go's own capture decode of a golden dump's m-log against the dump's soldier_rows
// stage (the frozen Python convert's soldier_rows.pkl, private: never shipped). Callers skip when it is absent.

// Check is one dump's result: In v1 rows, Out converted rows, Lines records compared equal, Diff the first
// difference ("" = equal), Convert the wall time of Convert alone, Total Go's TOTAL line tail and TotalPy the
// Python convert's (from the dump's trial.txt; "" when it has none).
type Check struct {
	Name           string
	In, Out, Lines int
	Diff           string
	Load, Convert  time.Duration
	Stats          *Stats
	Total, TotalPy string
}

// Repr is the TOTAL line tail: repr(dict(total)) and the row count.
func (s *Stats) Repr(n int) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range s.keys {
		if i > 0 {
			b.WriteString(", ")
		}
		q := "'"
		if strings.Contains(k, "'") && !strings.Contains(k, `"`) {
			q = `"`
		}
		fmt.Fprintf(&b, "%s%s%s: %d", q, k, q, s.m[k])
	}
	fmt.Fprintf(&b, "} %d", n)
	return b.String()
}

// pyTotal is the tail of the "soldier rows: converted in N s -> TOTAL ..." line of a dump's trial.txt.
func pyTotal(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "trial.txt"))
	if err != nil {
		return ""
	}
	for l := range strings.Lines(string(b)) {
		if _, t, ok := strings.Cut(l, "soldier rows: converted in "); ok {
			if _, t, ok = strings.Cut(t, " -> TOTAL "); ok {
				return strings.TrimRight(t, "\r\n")
			}
		}
	}
	return ""
}

// CheckDump loads g's m-log (capture.Load), converts it and compares every row with <g.Dir>/soldier_rows.jsonl.gz.
func CheckDump(g capture.GoldenDump, workers int) (Check, error) {
	c := Check{Name: g.Name}
	t0 := time.Now()
	raw, err := os.ReadFile(g.MLog)
	if err != nil {
		return c, err
	}
	d, err := capture.Load(raw)
	if err != nil {
		return c, err
	}
	c.In = len(capture.ToV1Rows(d))
	t1 := time.Now()
	rows, st, err := Convert(d, workers)
	c.Load, c.Convert = t1.Sub(t0), time.Since(t1)
	if err != nil {
		return c, err
	}
	c.Out, c.Stats, c.Total, c.TotalPy = len(rows), st, st.Repr(len(rows)), pyTotal(g.Dir)
	if c.TotalPy != "" && c.TotalPy != c.Total {
		c.Diff = fmt.Sprintf("TOTAL\n      go: %s\n      py: %s", c.Total, c.TotalPy)
		return c, nil
	}
	f, err := os.Open(filepath.Join(g.Dir, "soldier_rows.jsonl.gz"))
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
		return c, err
	}
	var hdr struct {
		Golden int
		Stage  string
	}
	if json.Unmarshal(h, &hdr) != nil || hdr.Golden != 1 || hdr.Stage != "soldier_rows" {
		return c, fmt.Errorf("%s: not a golden v1 soldier_rows dump", f.Name())
	}
	for i := range rows {
		l, err := golden.Line(&rows[i])
		if err != nil {
			return c, err
		}
		want, err := r.Next()
		if err == io.EOF {
			c.Diff = fmt.Sprintf("record %d: python dump ends, go has %.300s", i, l)
			return c, nil
		} else if err != nil {
			return c, err
		}
		if !bytes.Equal(want, []byte(l)) {
			c.Diff = fmt.Sprintf("record %d:\n      go: %.400s\n      py: %.400s", i, l, want)
			return c, nil
		}
		c.Lines++
	}
	if extra, err := r.Next(); err == nil {
		c.Diff = fmt.Sprintf("record %d: go ends, python has %.300s", c.Lines, extra)
	} else if err != io.EOF {
		return c, err
	}
	return c, nil
}
