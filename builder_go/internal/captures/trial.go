package captures

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/gate"
	"bc2replay/builder/internal/pyc"
)

// ---------- v3_trial.py ----------

// baselinesDefault is v3_trial.BASELINES (last loadable client demo per map).
var baselinesDefault = [][2]string{{"Levels/MP_006CQ", "cl04_1088"}, {"Levels/MP_005", "cl04_1081"}, {"Levels/MP_SP_005CQ", "cl04_1084"}}

var unseenRules = []string{"unseen-widths", "unseen-indices", "unseen-forms", "unseen-shape"}

const ghostTop = 8

// TrialOpts is v3_trial's command line.
type TrialOpts struct {
	Trial        string
	Src          string
	Suffix       string
	Env          []string // K=V | K+=V
	Out          string   // default <trial>/builds
	Baseline     string
	ExpectEqual  string
	Publish      bool
	ForcePublish bool
	AllowUnseen  bool
	Whitelist    string
}

// trialRun carries one v3_trial run: emit gets every stdout line (PHASE markers live, the summary at the end).
type trialRun struct {
	ctx  context.Context
	pipe Pipeline
	o    TrialOpts
	emit func(string)
	S    []string
}

func (t *trialRun) phase(name string) { t.emit("PHASE " + name) }

func (t *trialRun) finish() {
	t.phase("done")
	for _, l := range strings.Split(strings.Join(t.S, "\n"), "\n") {
		t.emit(l)
	}
}

// RunTrial is v3_trial.main(): returns (exit code, stderr text). A SystemExit(msg) of the reference is rc 1 with
// msg + '\n' on stderr; stdout lines go to emit as they are printed.
func RunTrial(ctx context.Context, pipe Pipeline, o TrialOpts, emit func(string)) (rc int, stderr string) {
	t := &trialRun{ctx: ctx, pipe: pipe, o: o, emit: emit}
	rc, err := t.main()
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			return 1, ee.msg + "\n"
		}
		return 1, "Traceback (most recent call last):\n" + err.Error() + "\n"
	}
	return rc, ""
}

// trialLevelOf is v3_trial.level_of (map pack in DEMOS only, then the .txt sidecar via demo_file).
func trialLevelOf(stem string) (string, error) {
	e, err := packLookup(stem, DemosDir())
	if err != nil {
		return "", err
	}
	if e != nil {
		if l, ok := packLevel(e).(string); ok && l != "" {
			return l, nil
		}
		return "", &exitError{"no Level: line in the map pack entry " + stem}
	}
	txt := demoFile(stem, "txt")
	if !isFile(txt) {
		return "", &exitError{"no sidecar " + txt}
	}
	s, err := readTextReplace(txt)
	if err != nil {
		return "", err
	}
	if l, ok := levelFromText(s); ok {
		return l, nil
	}
	return "", &exitError{"no Level: line in " + txt}
}

// demoFile is v3_trial.demo_file: DEMOS/<stem>.<ext>, else <pack>/baselines/<name> when that exists.
func demoFile(stem, ext string) string {
	p := join(DemosDir(), stem+"."+ext)
	if pack := datapackDir(); !isFile(p) && pack != "" && isFile(join(pack, "baselines", pathName(p))) {
		return join(pack, "baselines", pathName(p))
	}
	return p
}

// packFile is v3_trial.pack_file(rel): <pack>/<rel> if it is a file, else "".
func packFile(rel string) string {
	if pack := datapackDir(); pack != "" && isFile(join(pack, rel)) {
		return join(pack, rel)
	}
	return ""
}

// baselines is v3_trial.baselines(): <pack>/baselines/baselines.json 'levels' when present, else BASELINES.
func baselines() (*pyc.Object, error) {
	if pack := datapackDir(); pack != "" {
		j := join(pack, "baselines", "baselines.json")
		if isFile(j) {
			b, err := os.ReadFile(j)
			if err != nil {
				return nil, err
			}
			v, err := pyc.Loads(b)
			if err != nil {
				return nil, err
			}
			l := getObj(v, "levels")
			if l == nil {
				return nil, fmt.Errorf("KeyError: 'levels'")
			}
			return l, nil
		}
	}
	o := pyc.NewObject()
	for _, kv := range baselinesDefault {
		o.Set(kv[0], kv[1])
	}
	return o, nil
}

// packWhitelist is v3_trial.pack_whitelist.
func packWhitelist(explicit string) string {
	if explicit != "" {
		return pyPath(explicit)
	}
	pack := datapackDir()
	for _, n := range []string{"gate_whitelist.json.zlib", "gate_whitelist.json", "gate_whitelist.pkl.zlib"} {
		if pack != "" && isFile(join(pack, n)) {
			return join(pack, n)
		}
	}
	return ""
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func (t *trialRun) main() (int, error) {
	o := t.o
	trial := resolve(o.Trial)
	log := join(trial, "m-log.bin")
	if !isFile(log) {
		return 1, &exitError{"no m-log.bin in " + trial}
	}
	out := join(trial, "builds")
	if o.Out != "" {
		out = o.Out
	}
	out = resolve(out)
	if err := os.MkdirAll(out, 0o777); err != nil {
		return 1, err
	}
	stem := "cl04_" + o.Suffix
	base := join(out, stem)
	for _, e := range EXTS {
		if exists(join(out, stem+"."+e)) {
			return 1, &exitError{stem + " exists in " + out + "; pick another --suffix"}
		}
	}
	level, err := trialLevelOf(o.Src)
	if err != nil {
		return 1, err
	}
	defaults, err := t.pipe.RecipeDefaults()
	if err != nil {
		return 1, err
	}
	var overrides [][2]string
	setOv := func(k, v string) {
		for i := range overrides {
			if overrides[i][0] == k {
				overrides[i][1] = v
				return
			}
		}
		overrides = append(overrides, [2]string{k, v})
	}
	for _, kv := range o.Env {
		if k, v, ok := strings.Cut(kv, "+="); ok {
			d := defaults[k]
			if d == nil {
				return 1, &exitError{"--env " + k + "+=: no literal default for " + k}
			}
			if *d != "" {
				setOv(k, *d+","+v)
			} else {
				setOv(k, v)
			}
		} else if k, v, ok := strings.Cut(kv, "="); ok {
			setOv(k, v)
		} else {
			return 1, &exitError{"bad --env " + kv}
		}
	}
	in := Inputs{Log: log, Src: o.Src, Env: overrides}
	t.S = append(t.S, fmt.Sprintf("trial %s  log %d B  src %s (%s)  out %s", pathName(trial), fileSize(log), o.Src, level, base))
	if len(overrides) > 0 {
		t.S = append(t.S, "env "+kvLine(overrides))
	}
	fail := false

	// 1. soldier rows (r287: the round's file on a multi-round capture, V3_ROUND from --env or the process env)
	pkl := soldierFile(t.pipe, trial)
	rv := os.Getenv(roundEnv)
	for _, kv := range overrides {
		if kv[0] == roundEnv {
			rv = kv[1]
		}
	}
	rs, err := capRounds(trial)
	if err != nil {
		return 1, err
	}
	n, err := capture.CacheName(filepath.Base(pkl), rs, rv)
	if err != nil {
		t.S = append(t.S, "  FAIL "+err.Error())
		t.finish()
		return 1, nil
	}
	pkl = join(filepath.Dir(pkl), n)
	if len(rs) > 1 {
		k, _ := capture.RoundOf(rs, rv)
		r := rs[k-1]
		hi, pk := "end", "None"
		if !r.Open {
			hi = strconv.FormatUint(uint64(r.Hi), 10)
		}
		if r.HasPackets {
			pk = strconv.FormatInt(r.Packets, 10)
		}
		t.S = append(t.S, fmt.Sprintf("round %d of %d: pseq %d..%s (%s packets)", k, len(rs), r.Lo, hi, pk))
	}
	if isFile(pkl) {
		t.S = append(t.S, fmt.Sprintf("soldier rows: present (%d B)", fileSize(pkl)))
	} else {
		t.phase("soldiers")
		r, dt := t.run(t.pipe.Soldiers, in, join(out, stem+".soldiers.txt"))
		lines := pySplitlines(r.Stdout)
		tot := ""
		found := false
		for _, l := range lines {
			if strings.HasPrefix(l, "TOTAL") {
				tot, found = l, true
				break
			}
		}
		if !found {
			sl := pySplitlines(pyStrip(r.Stdout))
			if len(sl) > 0 {
				tot = sl[len(sl)-1]
			} else {
				tot = "?"
			}
		}
		t.S = append(t.S, fmt.Sprintf("soldier rows: converted in %s s -> %s", fmt0(dt), tot))
		if r.RC != 0 || !isFile(pkl) {
			last := "" // '%s' % (stderr.strip().splitlines()[-1:] or ''): a one-item list repr or ''
			if el := pySplitlines(pyStrip(r.Stderr)); len(el) > 0 {
				last = "[" + pyRepr(el[len(el)-1]) + "]"
			}
			t.S = append(t.S, fmt.Sprintf("  FAIL v3_convert_soldiers.py rc=%d: %s", r.RC, last))
			t.finish()
			return 1, nil
		}
	}

	// 2. build
	t.phase("build")
	bin := in
	bin.OutDir, bin.Suffix = out, o.Suffix
	r, dt := t.run(t.pipe.Build, bin, join(out, stem+".build.txt"))
	lines := pySplitlines(r.Stdout)
	if r.RC != 0 {
		el := pySplitlines(pyStrip(r.Stderr))
		last := "?"
		if len(el) > 0 {
			last = el[len(el)-1]
		}
		t.S = append(t.S, fmt.Sprintf("build: FAIL rc=%d (%s s): %s", r.RC, fmt0(dt), last))
		t.finish()
		return 1, nil
	}
	tl := ""
	for _, l := range lines {
		if strings.HasPrefix(l, "templates ") {
			tl = l
			break
		}
	}
	lastLine := ""
	if len(lines) > 0 {
		lastLine = lines[len(lines)-1]
	}
	t.S = append(t.S, fmt.Sprintf("build: ok in %s s; %s", fmt0(dt), lastLine))
	if tl != "" && !strings.HasSuffix(pyRstrip(tl), "set()") {
		t.S = append(t.S, "  WARN "+tl+"  (classes without a template -> no_create updates)")
	}
	for _, l := range lines {
		for _, p := range []string{"wire multi", "wire deletes", "wire companions", "wire registry", "tplnew_bad", "crebase_vs_src"} {
			if strings.HasPrefix(l, p) {
				t.S = append(t.S, "  "+l)
				break
			}
		}
		if strings.HasPrefix(l, "crebase_vs_src matched") {
			v, msg := srcVerdict(l)
			if v != "" {
				t.S = append(t.S, "  "+v+" "+msg)
				fail = fail || v == "FAIL"
			}
		}
		if strings.HasPrefix(l, "tplnew_bad") {
			f := strings.FieldsFunc(l, pyIsSpace)
			if len(f) < 2 {
				return 1, errors.New("IndexError: list index out of range")
			}
			if f[1] != "0" {
				t.S = append(t.S, "  FAIL tplnew_bad: delta NEW written from template defaults (r178/r182)")
				fail = true
			}
		}
	}
	var missing []string
	for _, e := range EXTS {
		if !isFile(join(out, stem+"."+e)) {
			missing = append(missing, e)
		}
	}
	if len(missing) > 0 {
		t.S = append(t.S, "  FAIL missing outputs "+pyRepr(missing))
		t.finish()
		return 1, nil
	}

	// 3. gate
	bls, err := baselines()
	if err != nil {
		return 1, err
	}
	bl := o.Baseline
	_, hasEntry := bls.Get(level)
	if bl == "" {
		bl = o.Src
		if v, ok := bls.Get(level); ok {
			bl = pyStr(v)
		}
	}
	var blp string // "" = None
	if strings.HasSuffix(bl, ".fbgd") {
		blp = pyPath(bl)
	} else {
		blp = demoFile(bl, "fbgd")
	}
	bsum := ""
	if !strings.HasSuffix(bl, ".fbgd") && !isFile(blp) && bl != "baselines" {
		bsum = packFile("baselines/" + bl + ".json")
	}
	noBaseline := false
	srcPack, err := packLookup(o.Src, DemosDir())
	if err != nil {
		return 1, err
	}
	switch {
	case o.Baseline == "" && !hasEntry && bl == o.Src && srcPack != nil: // r295: a baselines.json entry may name the SRC stem itself (first build on a map)
		noBaseline, blp, bsum = true, "", ""
		t.S = append(t.S, fmt.Sprintf("  baseline none: %s has no baselines.json entry and src %s is a map pack entry", level, o.Src))
	case bsum != "":
		blp = bsum
		t.S = append(t.S, fmt.Sprintf("  baseline %s: summary %s (data pack, no load-chunk warnings)", bl, bsum))
		lv := ""
		for _, k := range bls.Keys() { // {v: k for k, v in baselines().items()}.get(bl): the last level wins
			if v, _ := bls.Get(k); pyStr(v) == bl {
				lv = k
			}
		}
		if lv != "" && lv != level {
			t.S = append(t.S, fmt.Sprintf("  WARN baseline %s is %s, build is %s", bl, lv, level))
		}
	default:
		if !strings.HasSuffix(bl, ".fbgd") && !pathEq(blp, join(DemosDir(), bl+".fbgd")) {
			t.S = append(t.S, fmt.Sprintf("  baseline %s (data pack)", blp))
		}
		if !isFile(blp) {
			return 1, &exitError{"baseline " + blp + " missing"}
		}
		bst := stemOf(pathName(blp))
		lb, err := trialLevelOf(bst)
		if err != nil {
			return 1, err
		}
		if lb != level {
			t.S = append(t.S, fmt.Sprintf("  WARN baseline %s is %s, build is %s", bst, lb, level))
		}
	}
	bname := "none"
	if bsum != "" {
		bname = bl
	} else if !noBaseline {
		bname = stemOf(pathName(blp))
	}
	wl := packWhitelist(o.Whitelist)
	if wl != "" {
		t.S = append(t.S, fmt.Sprintf("  whitelist %s (no corpus scan)", wl))
	}
	t.phase("gate")
	gopt := gate.Options{Demo: base + ".fbgd", Verbose: true, JSON: base + ".gate.json", Whitelist: wl, Top: 20}
	switch {
	case noBaseline:
		gopt.NoBaseline = true
	case bsum != "":
		gopt.BaselineSummary, gopt.BaselineErrors = blp, true
	default:
		gopt.Baseline, gopt.BaselineErrors = blp, true
	}
	t0 := time.Now()
	var gbuf bytes.Buffer
	grc, gerr := gate.Run(gopt, &gbuf)
	gstderr := ""
	if gerr != nil {
		var ue *gate.UsageError
		if errors.As(gerr, &ue) {
			gstderr = gate.Usage + "\nfbgd_gate.py: error: " + ue.Msg + "\n"
		} else {
			gstderr = "Traceback (most recent call last):\n" + gerr.Error() + "\n"
			grc = 1
		}
	}
	gdt := time.Since(t0).Seconds()
	gout := gbuf.String()
	if err := gate.WriteLog(base+".gate.txt", gout, gstderr); err != nil {
		return 1, err
	}
	summ := ""
	hasSumm := false
	for _, l := range pySplitlines(gout) {
		if strings.HasPrefix(l, "SUMMARY") {
			summ, hasSumm = l, true
			break
		}
	}
	gj := base + ".gate.json"
	var g any
	if !hasSumm || !isFile(gj) {
		el := pySplitlines(pyStrip(gstderr))
		last := "no SUMMARY"
		if len(el) > 0 {
			last = el[len(el)-1]
		}
		t.S = append(t.S, fmt.Sprintf("gate: FAIL rc=%d: %s", grc, last))
		fail = true
	} else {
		rest := ""
		if len(summ) > len("SUMMARY: ") {
			rest = summ[len("SUMMARY: "):]
		}
		t.S = append(t.S, fmt.Sprintf("gate vs %s (%s s): %s", bname, fmt0(gdt), rest))
		b, err := os.ReadFile(gj)
		if err != nil {
			return 1, err
		}
		if g, err = pyc.Loads(b); err != nil {
			return 1, err
		}
		errs, _ := get(g, "errors").([]any)
		var structural counter
		for _, e := range errs {
			r := pyStr(get(e, "rule"))
			if !slices.Contains(unseenRules, r) {
				structural.add(r)
			}
		}
		unseen := len(errs) - structural.total()
		ghosts, _ := get(g, "ghosts").([]any)
		if structural.total() > 0 {
			t.S = append(t.S, "  ERROR structural: "+structural.join(structural.mostCommon()))
		}
		if structural.total() > 0 || (unseen > 0 && !o.AllowUnseen) {
			fail = true
		} else if unseen > 0 {
			t.S = append(t.S, fmt.Sprintf("  unseen accepted: %d unseen-* on %d ghosts, 0 structural (--allow-unseen)", unseen, len(ghosts)))
		}
		for i, row := range ghosts {
			if i >= ghostTop {
				break
			}
			shape := ""
			if sh := get(row, "shape"); truthy(sh) {
				shape = fmt.Sprintf(" (%d sec)", pyLen(sh))
			}
			cws, _ := get(row, "corpus_widths").([]any)
			parts := make([]string, len(cws))
			for j, w := range cws {
				parts[j] = pyStr(w)
			}
			cw := strings.Join(parts, "/")
			if cw == "" {
				cw = "none"
			}
			same := "any sections"
			if truthy(get(row, "same_sections")) {
				same = "same sections"
			}
			sig := pyStr(get(row, "signature"))
			hint := "create known"
			if !truthy(get(row, "known_create")) {
				hint = fmt.Sprintf("corpus creates %s/%s (%s)", strings.SplitN(sig, "/", 2)[0], cw, same)
			}
			t.S = append(t.S, fmt.Sprintf("  GHOST %s  %s%s  frame %s  %s err  %s slots  %s", rjust(pyStr(get(row, "ghost")), 5),
				sig, shape, rjust(pyStr(get(row, "frame")), 6), rjust(pyStr(get(row, "errors")), 6), pyStr(get(row, "slots")), hint))
		}
		type sigAgg struct {
			sig            string
			ghosts, errors int64
		}
		var bysig []*sigAgg
		idx := map[string]*sigAgg{}
		for _, row := range ghosts {
			sig := pyStr(get(row, "signature"))
			a := idx[sig]
			if a == nil {
				a = &sigAgg{sig: sig}
				idx[sig] = a
				bysig = append(bysig, a)
			}
			a.ghosts++
			if n, ok := get(row, "errors").(int64); ok {
				a.errors += n
			}
		}
		slices.SortStableFunc(bysig, func(a, b *sigAgg) int { return cmpInt(b.errors, a.errors) })
		for _, a := range bysig {
			if a.ghosts > 1 {
				t.S = append(t.S, fmt.Sprintf("  %d ghosts %s: %d err", a.ghosts, a.sig, a.errors))
			}
		}
		wg, _ := get(g, "warning_ghosts").([]any)
		if len(wg) > 0 {
			ws, _ := get(g, "warnings").([]any)
			var top []string
			for i, w := range wg {
				if i >= ghostTop {
					break
				}
				top = append(top, pyStr(get(w, "ghost"))+":"+pyStr(get(w, "warnings")))
			}
			t.S = append(t.S, fmt.Sprintf("  load warnings: %d on %d ghosts; top %s", len(ws), len(wg), strings.Join(top, ", ")))
		}
	}

	// 4. companion re-set check
	if hasSumm && isFile(gj) && g != nil {
		errs, _ := get(g, "errors").([]any)
		ws, _ := get(g, "warnings").([]any)
		var ce []any
		var by counter
		for _, e := range errs {
			if r := pyStr(get(e, "rule")); strings.HasPrefix(r, "companion-") {
				ce = append(ce, e)
				by.add(r)
			}
		}
		ncw := 0
		for _, w := range ws {
			if pyStr(get(w, "rule")) == "companion-reset-key0" {
				ncw++
			}
		}
		if len(ce) > 0 {
			t.S = append(t.S, "companions: "+by.join(by.keys)+"  (first: "+pyRepr(ce[0])+")")
		} else {
			l := "companions: 0 re-sets, keys consistent"
			if ncw > 0 {
				l += fmt.Sprintf("  (%d delta-0 re-sets = key0 parts in the first delta, as in server demos)", ncw)
			}
			t.S = append(t.S, l)
		}
	}

	// 5. regression compare
	if o.ExpectEqual != "" {
		t.phase("compare")
		var same, diff []string
		how := ""
		rh := packFile("reference_hashes.json")
		var hashes *pyc.Object
		if rh != "" {
			if b, err := os.ReadFile(rh); err == nil {
				if v, err := pyc.Loads(b); err == nil {
					hashes, _ = v.(*pyc.Object)
				} else {
					return 1, err
				}
			} else {
				return 1, err
			}
		}
		_, inHashes := func() (any, bool) {
			if hashes == nil {
				return nil, false
			}
			return hashes.Get(o.ExpectEqual)
		}()
		if !isFile(demoFile(o.ExpectEqual, "fbgd")) && rh != "" && inHashes {
			want := getObj(hashes, o.ExpectEqual)
			for _, e := range EXTS {
				p := join(out, stem+"."+e)
				h, ok := get(want, e).(string)
				if isFile(p) && ok && sha256File(p) == h {
					same = append(same, e)
				} else {
					diff = append(diff, e)
				}
			}
			how = " (sha256, data pack reference_hashes.json)"
		} else {
			for _, e := range EXTS {
				p, q := join(out, stem+"."+e), demoFile(o.ExpectEqual, e)
				if isFile(q) && filesEqual(p, q) {
					same = append(same, e)
				} else {
					diff = append(diff, e)
				}
			}
		}
		l := fmt.Sprintf("equal to %s: %d/9%s", o.ExpectEqual, len(same), how)
		if len(diff) > 0 {
			l += "  differ: " + pyRepr(diff)
			fail = true
		}
		t.S = append(t.S, l)
	}

	// 6. publish
	if o.Publish || o.ForcePublish {
		t.phase("publish")
		demos := DemosDir()
		if fail && !o.ForcePublish {
			t.S = append(t.S, "publish: skipped (checks failed; --force-publish to override)")
		} else {
			clash := false
			for _, e := range EXTS {
				if exists(join(demos, stem+"."+e)) {
					clash = true
				}
			}
			if clash {
				t.S = append(t.S, fmt.Sprintf("publish: REFUSED, %s already in %s", stem, demos))
				fail = true
			} else {
				for _, e := range EXTS {
					if err := copyFile(join(out, stem+"."+e), join(demos, stem+"."+e)); err != nil {
						return 1, err
					}
				}
				t.S = append(t.S, fmt.Sprintf("publish: 9 files -> %s.*", join(demos, stem)))
			}
		}
	}
	res := "OK"
	if fail {
		res = "FAIL"
	}
	l := fmt.Sprintf("RESULT: %s  (%s)", res, stem)
	if noBaseline {
		l += "  gate without baseline"
	}
	t.S = append(t.S, l)
	t.finish()
	if fail {
		return 1, nil
	}
	return 0, nil
}

// run is v3_trial.run(cmd, env, log): one pipeline child, its log file, the wall time.
func (t *trialRun) run(f func(context.Context, Inputs) (Result, error), in Inputs, logPath string) (Result, float64) {
	t0 := time.Now()
	r := runPipe(f, t.ctx, in)
	if logPath != "" {
		_ = gate.WriteLog(logPath, r.Stdout, r.Stderr)
	}
	return r, time.Since(t0).Seconds()
}

func kvLine(kv [][2]string) string {
	parts := make([]string, len(kv))
	for i, x := range kv {
		parts[i] = x[0] + "=" + x[1]
	}
	return strings.Join(parts, " ")
}

func pyLen(v any) int {
	switch x := v.(type) {
	case string:
		return len([]rune(x))
	case []any:
		return len(x)
	case *pyc.Object:
		return x.Len()
	}
	return 0
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// counter is collections.Counter over strings (insertion order kept for ties, like most_common).
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

func (c *counter) total() int {
	s := 0
	for _, v := range c.n {
		s += v
	}
	return s
}

func (c *counter) mostCommon() []string {
	ks := slices.Clone(c.keys)
	slices.SortStableFunc(ks, func(a, b string) int { return c.n[b] - c.n[a] })
	return ks
}

// join is ', '.join(f'{n} {k}' for k in keys).
func (c *counter) join(keys []string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d %s", c.n[k], k)
	}
	return strings.Join(parts, ", ")
}

func sha256File(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func filesEqual(p, q string) bool {
	a, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	b, err := os.ReadFile(q)
	if err != nil {
		return false
	}
	return bytes.Equal(a, b)
}

// ---------- v3_src_creates.verdict ----------

var (
	reSrcPhase  = regexp.MustCompile(`\b(game|load) ((?:t\d+ \d+/\d+ ?)+)`)
	reSrcType   = regexp.MustCompile(`t(\d+) (\d+)/(\d+)`)
	reSrcUnpair = regexp.MustCompile(`\bunpaired t31 plausible (\d+) implausible (\d+)`)
	reSrcEdge   = regexp.MustCompile(`\bedge (\d+) \(max ([0-9.]+) m`) // r295: plausible within the edge band
)

// srcVerdict is v3_src_creates.verdict(line): ("FAIL"|"WARN"|"", message).
func srcVerdict(l string) (string, string) {
	l = strings.SplitN(l, " first unmatched ", 2)[0]
	// {phase: {type: (a, b)}}: a later body of the same phase replaces the earlier one
	last := map[string]int{}
	for _, m := range reSrcPhase.FindAllStringSubmatch(l, -1) {
		v := 0
		for _, t := range reSrcType.FindAllStringSubmatch(m[2], -1) {
			if atoi(t[1]) == 31 {
				v = atoi(t[3])
			}
		}
		last[m[1]] = v
	}
	u := last["game"] + last["load"]
	if up := reSrcUnpair.FindStringSubmatch(l); up != nil {
		pl, im := atoi(up[1]), atoi(up[2])
		if im != 0 {
			return "FAIL", fmt.Sprintf("crebase_vs_src (unpaired SRC): %d spawner (type 31) creates outside the SRC position box (un-rebased origin vec3? r190/r207); %d plausible", im, pl)
		}
		if u != 0 {
			edge := ""
			if e := reSrcEdge.FindStringSubmatch(l); e != nil {
				edge = fmt.Sprintf(", %s of them up to %s m outside the SRC box (r295 edge band)", e[1], e[2])
			}
			return "WARN", fmt.Sprintf("crebase_vs_src (unpaired SRC): %d spawner (type 31) creates not in SRC, %d plausible / 0 implausible (r207)%s", u, pl, edge)
		}
		return "", ""
	}
	if u != 0 {
		return "FAIL", fmt.Sprintf("crebase_vs_src: %d spawner (type 31) creates not in SRC (un-rebased origin vec3? r190/r192)", u)
	}
	return "", ""
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
