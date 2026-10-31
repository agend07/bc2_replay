package wirecheck

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// Counts is one row of the exam (wire_py_check check_runs): equal = pos/end/flags/err/refs/ovs identical (a recorded
// failure compares err only); differ (incl. a reader error other than unported); unported = ErrUnported;
// diag = equal but calls / q50c650 differ (diagnostics).
type Counts struct {
	Runs, Equal, Differ, Unported, Diag int
	First                               []Diff // first differing runs
	DiagFirst                           []Diff // first diagnostic-only runs
}

// Diff is one differing (or diagnostic) run.
type Diff struct {
	Log   string `json:"log"`
	I     int64  `json:"i"`
	Site  string `json:"site"`
	Sidx  int64  `json:"sidx"`
	Entry string `json:"entry"`
	Go    string `json:"go"`
	Rec   string `json:"rec"`
}

// Report is the result of one exam over the corpus.
type Report struct {
	Logs     []string
	ByReader map[uint32]*Counts
	BySite   map[string]*Counts
	Total    Counts
	Runs     int
	Load     time.Duration // gz + JSON + input construction (parallel over logs)
	Exam     time.Duration // RunReader + comparison
	Readers  time.Duration // a second pass of RunReader alone over every run
}

// Load reads the given logs in parallel (one goroutine per log) and returns their runs (and sites, when asked) in
// log order.
func Load(dir string, names []string, sites bool) ([][]*Run, [][]*Site, error) {
	out := make([][]*Run, len(names))
	ss := make([][]*Site, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], ss[i], errs[i] = LoadLog(dir, n, sites)
		}()
	}
	wg.Wait()
	return out, ss, errors.Join(errs...)
}

var dumpOpt = pyc.DumpOptions{SortKeys: true}

func js(v any) string {
	s, err := pyc.Dumps(v, dumpOpt)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return s
}

// Tree is the JSON-equivalent value of a run output's compared fields (wire_py_core.out_core).
func Tree(o wire.Out) map[string]any {
	pos := make([]any, len(o.Pos))
	for i, p := range o.Pos {
		pos[i] = p
	}
	var err, refs, ovs any
	if o.Err != "" {
		err = o.Err
	}
	if o.Refs != nil {
		l := make([]any, len(o.Refs))
		for i, r := range o.Refs {
			l[i] = map[string]any{"start": r.Start, "end": r.End, "target": r.Target}
		}
		refs = l
	}
	if o.Ovs != nil {
		l := make([]any, len(o.Ovs))
		for i, v := range o.Ovs {
			l[i] = map[string]any{"start": v.Start, "end": v.End, "xyz": []any{v.XYZ[0], v.XYZ[1], v.XYZ[2]}}
		}
		ovs = l
	}
	return map[string]any{"pos": pos, "end": o.End, "flags": o.Flags, "err": err, "refs": refs, "ovs": ovs}
}

var coreKeys = []string{"pos", "end", "flags", "err", "refs", "ovs"}

func recTree(rec *pyc.Object) map[string]any {
	m := map[string]any{}
	for _, k := range coreKeys {
		m[k] = get(rec, k)
	}
	return m
}

func diagOf(o wire.Out) string {
	calls := make([]any, len(o.Calls))
	for i, c := range o.Calls {
		calls[i] = fmt.Sprintf("%x", c)
	}
	q := make([]any, len(o.Q))
	for i, c := range o.Q {
		q[i] = c
	}
	return js([]any{calls, q})
}

func recDiag(rec *pyc.Object) string { return js([]any{get(rec, "calls"), get(rec, "q50c650")}) }

// Check replays every run through wire.RunReader and compares it with the recorded Unicorn output,
// exactly as wire_py_check.check_runs. va != 0 restricts to one reader. ndiff = differing runs kept per row.
func Check(runs [][]*Run, va uint32, ndiff int) *Report {
	rep := &Report{ByReader: map[uint32]*Counts{}, BySite: map[string]*Counts{}}
	t0 := time.Now()
	for _, log := range runs {
		for _, r := range log {
			if va != 0 && r.Entry != va {
				continue
			}
			rep.Runs++
			rows := []*Counts{rep.row(r.Entry), rep.site(r.Site), &rep.Total}
			for _, c := range rows {
				c.Runs++
			}
			bump := func(kind string, d *Diff) {
				for _, c := range rows {
					switch kind {
					case "equal":
						c.Equal++
					case "differ":
						c.Differ++
					case "unported":
						c.Unported++
					}
					if kind == "diag" {
						c.Diag++
					}
					switch {
					case d == nil:
					case kind == "diag" && len(c.DiagFirst) < ndiff:
						c.DiagFirst = append(c.DiagFirst, *d)
					case kind == "differ" && len(c.First) < ndiff:
						c.First = append(c.First, *d)
					}
				}
			}
			mk := func(goS, recS string) *Diff {
				return &Diff{Log: r.Log, I: r.I, Site: r.Site, Sidx: r.Sidx, Entry: fmt.Sprintf("%x", r.Entry), Go: goS, Rec: recS}
			}
			if !wire.IsPorted(r.Entry) {
				bump("unported", nil)
				continue
			}
			o, err := wire.RunReader(r.Inp)
			var un *wire.ErrUnported
			switch {
			case errors.As(err, &un):
				bump("unported", nil)
				continue
			case err != nil:
				bump("differ", mk("exception: "+err.Error(), js(recTree(r.Out))))
				continue
			}
			var ok bool
			var goS, recS string
			if recErr := get(r.Out, "err"); recErr != nil {
				goS, recS = js(Tree(o)["err"]), js(recErr)
			} else {
				goS, recS = js(Tree(o)), js(recTree(r.Out))
			}
			ok = goS == recS
			if !ok {
				bump("differ", mk(js(Tree(o)), js(recTree(r.Out)))) // full outputs, whatever was compared
				continue
			}
			bump("equal", nil)
			if gd, rd := diagOf(o), recDiag(r.Out); gd != rd {
				bump("diag", mk(gd, rd))
			}
		}
	}
	rep.Exam = time.Since(t0)
	t0 = time.Now()
	for _, log := range runs {
		for _, r := range log {
			if va == 0 || r.Entry == va {
				_, _ = wire.RunReader(r.Inp)
			}
		}
	}
	rep.Readers = time.Since(t0)
	return rep
}

func (rep *Report) row(va uint32) *Counts {
	c := rep.ByReader[va]
	if c == nil {
		c = &Counts{}
		rep.ByReader[va] = c
	}
	return c
}

func (rep *Report) site(s string) *Counts {
	c := rep.BySite[s]
	if c == nil {
		c = &Counts{}
		rep.BySite[s] = c
	}
	return c
}

// OK reports whether no run differs.
func (rep *Report) OK() bool { return rep.Total.Differ == 0 }

// Print writes the per-reader and per-site-kind tables (wire_py_check layout) and the diffs.
func (rep *Report) Print(w io.Writer, diffs bool) {
	fmt.Fprintf(w, "logs %s\n", strings.Join(rep.Logs, ","))
	line := func(name string, c *Counts) {
		fmt.Fprintf(w, "%-14s %7d %7d %7d %8d %5d\n", name, c.Runs, c.Equal, c.Differ, c.Unported, c.Diag)
		if diffs {
			for _, d := range slices.Concat(c.First, c.DiagFirst) {
				fmt.Fprintf(w, "    %s i=%d site=%s sidx=%d entry=%s\n      go:  %.400s\n      rec: %.400s\n",
					d.Log, d.I, d.Site, d.Sidx, d.Entry, d.Go, d.Rec)
			}
		}
	}
	fmt.Fprintf(w, "%-14s %7s %7s %7s %8s %5s\n", "reader", "runs", "equal", "differ", "unported", "diag")
	vas := make([]uint32, 0, len(rep.ByReader))
	for va := range rep.ByReader {
		vas = append(vas, va)
	}
	sort.Slice(vas, func(i, j int) bool {
		a, b := rep.ByReader[vas[i]], rep.ByReader[vas[j]]
		return a.Runs > b.Runs || a.Runs == b.Runs && vas[i] < vas[j]
	})
	for _, va := range vas {
		line(fmt.Sprintf("%x", va), rep.ByReader[va])
	}
	fmt.Fprintf(w, "%-14s %7s %7s %7s %8s %5s\n", "site kind", "runs", "equal", "differ", "unported", "diag")
	sites := make([]string, 0, len(rep.BySite))
	for s := range rep.BySite {
		sites = append(sites, s)
	}
	slices.Sort(sites)
	for _, s := range sites {
		line(s, rep.BySite[s])
	}
	line("total", &Counts{Runs: rep.Total.Runs, Equal: rep.Total.Equal, Differ: rep.Total.Differ,
		Unported: rep.Total.Unported, Diag: rep.Total.Diag})
	fmt.Fprintf(w, "seconds load %.2f exam %.2f readers-only %.2f\n", rep.Load.Seconds(), rep.Exam.Seconds(),
		rep.Readers.Seconds())
}
