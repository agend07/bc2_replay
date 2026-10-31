// Package gate is the Go port of fbgd_gate.py (frozen oracle r277): the deterministic offline pre-test gate of a built
// .fbgd demo, and of v3_src_creates.py (the builder's crebase_vs_src line, srccreates.go).
//
// Modes are the ones v3_trial uses: --whitelist FILE (JSON form) or the corpus scan under --root, and the baseline as
// --baseline DEMO, --baseline-summary FILE or --no-baseline, each optionally with --baseline-errors; --verbose, --top, --json.
// Not ported: --save-whitelist, --save-baseline-summary, --calibrate (maintainer data-pack paths), pickle whitelists and
// the pickle scan caches (Go rescans; --cache is accepted and ignored).
package gate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/pyc"
)

// Options mirrors fbgd_gate's command line (paths as given; output paths go through str(Path(...)) like Python).
type Options struct {
	Demo            string
	Baseline        string
	BaselineSummary string
	NoBaseline      bool
	BaselineErrors  bool
	Whitelist       string
	Root            string // corpus root when Whitelist is empty (default BC2_ARCHIVE_DIR or the archive)
	JSON            string
	Verbose         bool
	Top             int
}

// UsageError is argparse's ap.error: exit status 2.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

type gate struct {
	allowed *Whitelist
	replays map[string]replayResult
}

// DefaultArchive is fbgd_lint.ARCHIVE (r278 rule): BC2_ARCHIVE_DIR when set and non-empty, else
// <builder dir>/../runtime/builder/archive, the builder dir being the directory of builder.exe (Python: of the module).
func DefaultArchive() string {
	if v := os.Getenv("BC2_ARCHIVE_DIR"); v != "" {
		return v
	}
	dir := "."
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	}
	return filepath.Join(dir, "..", "runtime", "builder", "archive")
}

// pyErr converts a Go error into the Python exception fbgd_gate would see; ok is false for exceptions the gate's
// except clause does not catch (none of ours today).
func pyErr(err error, path string) *fbgd.PyError {
	var pe *fbgd.PyError
	if errors.As(err, &pe) {
		return pe
	}
	if errors.Is(err, fs.ErrNotExist) {
		return &fbgd.PyError{Class: "FileNotFoundError", Msg: "[Errno 2] No such file or directory: " + pyStr(PyPath(path))}
	}
	var perr *fs.PathError
	if errors.As(err, &perr) {
		return &fbgd.PyError{Class: "OSError", Msg: perr.Err.Error() + ": " + pyStr(PyPath(path))}
	}
	return &fbgd.PyError{Class: "ValueError", Msg: err.Error()}
}

func readFile(path string) ([]byte, *fbgd.PyError) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, pyErr(err, path)
	}
	return b, nil
}

// Run is fbgd_gate.main() after argument parsing: prints the CORPUS / ERROR / COMPANION / GHOST / SUMMARY lines to stdout,
// writes --json, returns the exit status (0 clean, 1 any ERROR). A *UsageError means argparse's exit 2.
func Run(o Options, stdout io.Writer) (int, error) {
	if o.Demo == "" {
		return 2, &UsageError{"DEMO is required unless --save-whitelist / --save-baseline-summary FILE (save-only run)"}
	}
	if o.BaselineSummary != "" && o.Baseline != "" {
		return 2, &UsageError{"--baseline-summary replaces --baseline"}
	}
	if o.NoBaseline && (o.Baseline != "" || o.BaselineSummary != "" || o.BaselineErrors) {
		return 2, &UsageError{"--no-baseline excludes --baseline / --baseline-summary / --baseline-errors"}
	}
	g := &gate{replays: map[string]replayResult{}}
	var header *pyc.Object
	var corpus []any
	if o.Whitelist != "" {
		w, err := LoadWhitelist(o.Whitelist)
		if err != nil {
			return 2, &UsageError{"--whitelist: " + err.Error()}
		}
		g.allowed, header = w, w.Header
		if v, ok := header.Get("corpus"); ok {
			l, _ := v.([]any)
			for _, n := range l {
				s, _ := n.(string)
				fmt.Fprintln(stdout, "CORPUS", filepath.Base(PyPath(s)))
				corpus = append(corpus, PyPath(s))
			}
		}
	} else {
		root := o.Root
		if root == "" {
			root = DefaultArchive()
		}
		good, err := Corpus(root)
		if err != nil {
			return 1, err
		}
		if len(good) == 0 {
			return 2, &UsageError{"no working corpus: refusing empty whitelist"}
		}
		c := &collector{newWhitelist()}
		var names []string
		for _, p := range good {
			fmt.Fprintln(stdout, "CORPUS", filepath.Base(p))
			data, err := os.ReadFile(p)
			if err != nil {
				return 1, err
			}
			if _, err := scan(data, scanOpts{collect: c}); err != nil {
				return 1, fmt.Errorf("corpus %s: %w", p, err)
			}
			names = append(names, filepath.Base(p))
			corpus = append(corpus, PyPath(p))
		}
		g.allowed = c.w
		header = obj("corpus_sha", CorpusSHA(names))
	}

	var errs, warnings, inherited []*pyc.Object
	var baselineOut any
	baseline := o.Baseline
	if baseline == "" && o.BaselineSummary == "" && !o.NoBaseline && strings.HasPrefix(pathStem(o.Demo), "cl04_") {
		baseline = filepath.Join(filepath.Dir(o.Demo), "cl04_977.fbgd")
	}
	regress := baseline != "" && o.BaselineSummary == ""
	// Simplification: the baseline is scanned once, up front (Python scans it twice, after the build); its load phase
	// bounds the build's early chunks. Its errors are reported in Python's order below.
	var b *scanResult
	var bErr *fbgd.PyError
	earlyLimit := -1
	if regress {
		var ballowed *Whitelist
		if o.BaselineErrors {
			ballowed = g.allowed
		}
		if bdata, e := readFile(baseline); e != nil {
			bErr = e
		} else if res, err := scan(bdata, scanOpts{allowed: ballowed, early: true, earlyLimit: -1}); err != nil {
			bErr = pyErr(err, baseline)
		} else {
			b = res
			earlyLimit = b.frames
			if b.firstSoldier >= 0 {
				earlyLimit = min(earlyLimit, b.firstSoldier)
			}
		}
	}
	pe := func() *fbgd.PyError {
		data, e := readFile(o.Demo)
		if e != nil {
			return e
		}
		s, err := scan(data, scanOpts{allowed: g.allowed, early: regress, earlyLimit: earlyLimit})
		if err != nil {
			return pyErr(err, o.Demo)
		}
		errs = append(s.errors, g.companions(o.Demo, s)...)
		if o.BaselineSummary != "" {
			baselineOut = PyPath(o.BaselineSummary)
			bfs, pairs, e := loadBaselineSummary(o.BaselineSummary, header)
			if e != nil {
				return e
			}
			warnings = []*pyc.Object{obj("rule", "baseline-summary", "note", "baseline summary (no load-chunk warnings)")}
			if o.BaselineErrors {
				errs, inherited = partitionForms(errs, pairs, s.firstSoldier, bfs)
			}
		} else if baseline != "" {
			baselineOut = PyPath(baseline)
			if bErr != nil {
				return bErr
			}
			warnings = regression(s, b)
			if o.BaselineErrors {
				var berrs []*pyc.Object
				for _, e := range b.errors { // r284: dangling-view-reference is judged on the build only
					if r, _ := e.Get("rule"); r != ViewRule {
						berrs = append(berrs, e)
					}
				}
				berrs = append(berrs, g.companions(baseline, b)...)
				for _, e := range berrs {
					if errorForm(e) == "" {
						return &fbgd.PyError{Class: "ValueError", Msg: "baseline has structural errors; cannot inherit its debt"}
					}
				}
				errs, inherited = partitionForms(errs, baselineForms(berrs, b.firstSoldier), s.firstSoldier, b.firstSoldier)
			}
		} else {
			if o.BaselineErrors {
				return &fbgd.PyError{Class: "ValueError", Msg: "--baseline-errors requires a baseline"}
			}
			warnings = []*pyc.Object{obj("rule", "no-baseline", "note", "supply --baseline for regression audit")}
		}
		if base, ok := companionSet(o.Demo); ok {
			_, w := g.companionReplay(base)
			warnings = append(warnings, w...)
		}
		return nil
	}()
	if pe != nil {
		errs = append(errs, obj("rule", "parse-or-input", "detail", pe.Msg))
	}
	ghosts, warnGhosts := ghostView(errs, g.allowed), warningView(warnings)
	result := obj("demo", PyPath(o.Demo), "baseline", baselineOut, "corpus", nonNilAny(corpus),
		"errors", objs(errs), "inherited_errors", objs(inherited), "warnings", objs(warnings),
		"ghosts", objs(ghosts), "warning_ghosts", objs(warnGhosts), "calibration", []any{},
		"limitations", []string{"Observed encodings only; values, selectors and apply semantics are not validated.",
			"Stale references allowed only for previously deleted IDs in working slot/table forms (r112 #6).",
			"Unused byte-padding is preserved/ignored; significant bits round-trip exactly.",
			"Working labels inherited from r111, not newly live-confirmed."})
	line := func(parts ...string) { fmt.Fprintln(stdout, strings.Join(parts, " ")) }
	dumps := func(v any) string {
		s, err := pyc.Dumps(v, pyc.DumpOptions{EnsureASCII: true})
		if err != nil {
			panic(err)
		}
		return s
	}
	for _, lv := range []struct {
		level string
		rows  []*pyc.Object
	}{{"ERROR", errs}, {"INHERITED_ERROR", inherited}, {"WARNING", warnings}} {
		for _, row := range lv.rows {
			rule := getStr(row, "rule")
			if o.Verbose || (lv.level == "ERROR" && !unseen[rule] && !strings.HasPrefix(rule, "companion-")) {
				line(lv.level, dumps(row))
			}
		}
	}
	var comp, compKey0 []*pyc.Object
	for _, e := range errs {
		if strings.HasPrefix(getStr(e, "rule"), "companion-") {
			comp = append(comp, e)
		}
	}
	for _, w := range warnings {
		if getStr(w, "rule") == "companion-reset-key0" {
			compKey0 = append(compKey0, w)
		}
	}
	if len(comp) > 0 || len(compKey0) > 0 {
		var rules counter
		deltas, groups := map[int]bool{}, map[int]bool{}
		for _, e := range comp {
			rules.add(getStr(e, "rule"))
			if d, ok := getInt(e, "delta"); ok {
				deltas[d] = true
			}
		}
		for _, w := range compKey0 {
			gr, _ := getInt(w, "group")
			groups[gr] = true
		}
		var first any
		if len(comp) > 0 {
			first = comp[0]
		}
		ds := sortedInts(deltas)
		line("COMPANION", dumps(obj("errors", rules.object(), "first", first, "deltas", ds[:min(8, len(ds))],
			"key0_resets", len(compKey0), "key0_groups", sortedInts(groups))))
	}
	for _, lv := range []struct {
		level string
		rows  []*pyc.Object
	}{{"GHOST", ghosts}, {"WARN-GHOST", warnGhosts}} {
		for i, row := range lv.rows {
			if i >= o.Top {
				break
			}
			line(lv.level, dumps(row))
		}
		if len(lv.rows) > o.Top {
			line(fmt.Sprintf("%s ... %d more rows in --json", lv.level, len(lv.rows)-o.Top))
		}
	}
	nUnseen := 0
	for _, r := range ghosts {
		n, _ := getInt(r, "errors")
		nUnseen += n
	}
	var wr counter
	for _, w := range warnings {
		wr.add(getStr(w, "rule"))
	}
	line(fmt.Sprintf("SUMMARY: %d ERROR (%d unseen-* on %d ghosts, %d structural), %d INHERITED_ERROR, %d WARNING; warning rules %s",
		len(errs), nUnseen, len(ghosts), len(errs)-nUnseen, len(inherited), len(warnings), wr.repr()))
	if o.JSON != "" {
		s, err := pyc.Dumps(result, pyc.DumpOptions{EnsureASCII: true, Indent: 2})
		if err != nil {
			return 1, err
		}
		if err := WriteText(o.JSON, s); err != nil {
			return 1, err
		}
	}
	if len(errs) > 0 {
		return 1, nil
	}
	return 0, nil
}

// WriteText is Path.write_text(s, encoding='utf-8') after mkdir(parents=True): '\n' becomes os.linesep.
func WriteText(path, s string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	return os.WriteFile(path, []byte(s), 0o644)
}

// WriteLog writes a gate.txt the way v3_trial.run logs a child: stdout, then '\n--- stderr ---\n' + stderr when non-empty.
func WriteLog(path, stdout, stderr string) error {
	if stderr != "" {
		stdout += "\n--- stderr ---\n" + stderr
	}
	return WriteText(path, stdout)
}

func objs(l []*pyc.Object) []any {
	out := make([]any, len(l))
	for i, x := range l {
		out[i] = x
	}
	return out
}

func nonNilAny(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}

// loadBaselineSummary is fbgd_gate.load_baseline_summary -> (first_soldier or -1, form pairs).
func loadBaselineSummary(path string, header *pyc.Object) (int, formPairs, *fbgd.PyError) {
	var p formPairs
	data, e := readFile(path)
	if e != nil {
		return 0, p, e
	}
	v, err := pyc.Loads(data)
	if err != nil {
		return 0, p, &fbgd.PyError{Class: "JSONDecodeError", Msg: err.Error()}
	}
	o, ok := v.(*pyc.Object)
	if !ok {
		return 0, p, &fbgd.PyError{Class: "ValueError", Msg: "baseline summary is not a JSON object"}
	}
	ver, _ := o.Get("version")
	lv, _ := o.Get("lint_version")
	if ver != any(int64(Version)) || lv != any(int64(fbgd.LintVersion)) {
		return 0, p, &fbgd.PyError{Class: "ValueError", Msg: fmt.Sprintf(
			"baseline summary %s has gate/lint VERSION %s/%s, this gate is %d/%d: regenerate with --save-baseline-summary",
			PyPath(path), pyFmt(ver), pyFmt(lv), Version, fbgd.LintVersion)}
	}
	var sha string
	if header != nil {
		hv, _ := header.Get("corpus_sha")
		sha, _ = hv.(string)
	}
	ov, _ := o.Get("corpus_sha")
	osha, _ := ov.(string)
	if osha != "" && sha != "" && osha != sha {
		return 0, p, &fbgd.PyError{Class: "ValueError", Msg: fmt.Sprintf(
			"baseline summary %s was made with another whitelist corpus (%s != %s): regenerate with --save-baseline-summary",
			PyPath(path), osha[:min(12, len(osha))], sha[:min(12, len(sha))])}
	}
	fv, ok := o.Get("first_soldier")
	if !ok {
		return 0, p, &fbgd.PyError{Class: "KeyError", Msg: "'first_soldier'"}
	}
	bfs := -1
	if n, ok := fv.(int64); ok {
		bfs = int(n)
	}
	formsV, ok := o.Get("forms")
	if !ok {
		return 0, p, &fbgd.PyError{Class: "KeyError", Msg: "'forms'"}
	}
	p = formPairs{map[string]bool{}, map[string]bool{}}
	l, _ := formsV.([]any)
	for _, x := range l {
		pair, ok := x.([]any)
		if !ok || len(pair) != 2 {
			return 0, p, &fbgd.PyError{Class: "ValueError", Msg: "bad baseline summary forms row"}
		}
		f, err := errorFormJSON(pair[0])
		if err != nil {
			return 0, p, &fbgd.PyError{Class: "ValueError", Msg: err.Error()}
		}
		p.forms[f] = true
		if truthy(pair[1]) {
			p.load[f] = true
		}
	}
	return bfs, p, nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case nil:
		return false
	}
	return true
}

// pyFmt is str(v) / f'{v}' for a JSON value.
func pyFmt(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return x
	case float64:
		return pyc.Repr(x)
	}
	return fmt.Sprint(v)
}

// pyReprAny is repr(v) for a JSON scalar.
func pyReprAny(v any) string {
	if s, ok := v.(string); ok {
		return pyStr(s)
	}
	return pyFmt(v)
}

// pyStr is repr(str).
func pyStr(s string) string { return fbgd.StrRepr(s) }

// PyPath is str(pathlib.Path(p)) on this OS: separators normalized (backslash on Windows), empty and '.' parts dropped.
func PyPath(p string) string {
	if runtime.GOOS != "windows" {
		return pyPathSep(p, "/")
	}
	return pyPathSep(strings.ReplaceAll(p, "/", "\\"), "\\")
}

func pyPathSep(p, sep string) string {
	drive := ""
	if sep == "\\" && len(p) >= 2 && p[1] == ':' {
		drive, p = p[:2], p[2:]
	}
	root := strings.HasPrefix(p, sep)
	var parts []string
	for _, x := range strings.Split(p, sep) {
		if x != "" && x != "." {
			parts = append(parts, x)
		}
	}
	s := drive
	if root {
		s += sep
	}
	s += strings.Join(parts, sep)
	if s == "" {
		return "."
	}
	return s
}

// ParseArgs parses fbgd_gate's command line (argparse rules: --opt VALUE or --opt=VALUE, unique prefixes accepted).
func ParseArgs(args []string) (Options, error) {
	o := Options{Top: 20}
	valued := []string{"--baseline", "--root", "--cache", "--json", "--top", "--whitelist", "--save-whitelist",
		"--baseline-summary", "--save-baseline-summary"}
	flags := []string{"--calibrate", "--baseline-errors", "--verbose", "--no-baseline", "--help"}
	all := append(append([]string{}, valued...), flags...)
	var demo []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") || a == "--" {
			if a == "--" {
				demo = append(demo, args[i+1:]...)
				break
			}
			if strings.HasPrefix(a, "-") && len(a) > 1 {
				return o, &UsageError{"unrecognized arguments: " + a}
			}
			demo = append(demo, a)
			continue
		}
		name, val, hasVal := strings.Cut(a, "=")
		var match []string
		for _, c := range all {
			if c == name {
				match = []string{c}
				break
			}
			if strings.HasPrefix(c, name) {
				match = append(match, c)
			}
		}
		if len(match) != 1 {
			if len(match) == 0 {
				return o, &UsageError{"unrecognized arguments: " + a}
			}
			return o, &UsageError{fmt.Sprintf("ambiguous option: %s could match %s", name, strings.Join(match, ", "))}
		}
		name = match[0]
		isValued := false
		for _, c := range valued {
			if c == name {
				isValued = true
			}
		}
		if isValued && !hasVal {
			if i+1 >= len(args) || (strings.HasPrefix(args[i+1], "-") && len(args[i+1]) > 1) {
				return o, &UsageError{"argument " + name + ": expected one argument"}
			}
			i++
			val = args[i]
		} else if !isValued && hasVal {
			return o, &UsageError{"argument " + name + ": ignored explicit argument '" + val + "'"}
		}
		switch name {
		case "--baseline":
			o.Baseline = val
		case "--root":
			o.Root = val
		case "--cache": // pickle scan cache: not used by the Go gate
		case "--json":
			o.JSON = val
		case "--top":
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return o, &UsageError{"argument --top: invalid int value: '" + val + "'"}
			}
			o.Top = n
		case "--whitelist":
			o.Whitelist = val
		case "--baseline-summary":
			o.BaselineSummary = val
		case "--baseline-errors":
			o.BaselineErrors = true
		case "--verbose":
			o.Verbose = true
		case "--no-baseline":
			o.NoBaseline = true
		case "--help":
			return o, &UsageError{"help"}
		case "--save-whitelist", "--save-baseline-summary", "--calibrate":
			return o, &UsageError{name + " is not ported to the Go gate (maintainer data-pack path; use fbgd_gate.py)"}
		}
	}
	if len(demo) > 1 {
		return o, &UsageError{"unrecognized arguments: " + strings.Join(demo[1:], " ")}
	}
	if len(demo) == 1 {
		o.Demo = demo[0]
	}
	return o, nil
}

// Usage is the short usage line printed with a UsageError.
const Usage = `usage: builder dev gate [DEMO] [--baseline FILE | --baseline-summary FILE | --no-baseline] [--baseline-errors]
                        [--whitelist FILE | --root ARCHIVE] [--json FILE] [--verbose] [--top N]`

// Main is `builder dev gate ARGS...`: fbgd_gate's command line; exit 0 clean, 1 ERROR, 2 usage.
func Main(args []string, stdout, stderr io.Writer) int {
	o, err := ParseArgs(args)
	if err == nil {
		var rc int
		rc, err = Run(o, stdout)
		if err == nil {
			return rc
		}
	}
	var ue *UsageError
	if errors.As(err, &ue) {
		fmt.Fprintln(stderr, Usage)
		if ue.Msg != "help" {
			fmt.Fprintln(stderr, "builder dev gate: error:", ue.Msg)
		}
		if ue.Msg == "help" {
			return 0
		}
		return 2
	}
	fmt.Fprintln(stderr, "builder dev gate:", err)
	return 1
}
