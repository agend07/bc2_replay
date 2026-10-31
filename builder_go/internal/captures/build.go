package captures

import (
	"context"
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
	"bc2replay/builder/internal/pyc"
)

// ---------- build (v3_captures.build) ----------

// Phases are the v3_trial phases in order.
var Phases = []string{"soldiers", "build", "gate", "compare", "publish"}

const (
	estPerPacket = 0.007
	estMin       = 10
)

// BuildOpts is `build <capture> [--suffix] [--reference] [--publish] [--src] [--env K=V|K+=V]...`.
type BuildOpts struct {
	Capture   string
	Suffix    string
	Reference bool
	Publish   bool
	Src       string
	Env       []string
	Round     *int // r287: 1-based round (--round K), nil = the default round
}

// ensureSrc copies a harvest set from old_demos into demos when missing there; info text or "".
func ensureSrc(stem string) (string, error) {
	demos, old := DemosDir(), OldDemosDir()
	if complete(demos, stem) {
		return "", nil
	}
	if packEntry(stem) != nil {
		return "src " + stem + " from the map pack (no harvest set copied)", nil
	}
	if !complete(old, stem) {
		return "", errors.New("src " + stem + " not in demos or old_demos")
	}
	for _, e := range EXTS {
		if !exists(join(demos, stem+"."+e)) {
			if err := copyFile(join(old, stem+"."+e), join(demos, stem+"."+e)); err != nil {
				return "", errors.New(osErrorText(err))
			}
		}
	}
	return "copied data pack " + stem + " (9 files) from old_demos to demos", nil
}

// capLevelOf is v3_captures.level_of (map pack with demos + old_demos, then DEMOS/<stem>.txt); nil = None.
func capLevelOf(stem string) (any, error) {
	if e := packEntry(stem); e != nil {
		return packLevel(e), nil
	}
	s, err := readTextReplace(join(DemosDir(), stem+".txt"))
	if err != nil {
		return nil, err
	}
	if l, ok := levelFromText(s); ok {
		return l, nil
	}
	return nil, nil
}

var (
	reNonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)
	reTrialNum = regexp.MustCompile(`trial-(\d+)`)
)

// nextSuffix is next_suffix: c<NN>_<n> (trial number), r287 c<NN>r<K>_<n> when round > 0 (a capture with more than
// one non-empty round), reference ref<digits>[_n].
func nextSuffix(capDir, out, ref string, round int) string {
	var base string
	if ref != "" {
		parts := strings.SplitN(ref, "_", 2)
		base = "ref" + reNonAlnum.ReplaceAllString(parts[len(parts)-1], "")
		if base == "ref" {
			base = "refx"
		}
	} else {
		name := pathName(capDir)
		if m := reTrialNum.FindStringSubmatch(name); m != nil {
			base = "c" + m[1]
		} else {
			r := []rune(reNonAlnum.ReplaceAllString(name, ""))
			base = "c" + string(r[:min(12, len(r))])
		}
		if round > 0 {
			base += fmt.Sprintf("r%d", round)
		}
	}
	taken := func(s string) bool {
		return len(globPrefix(out, "cl04_"+s+".")) > 0 || len(globPrefix(DemosDir(), "cl04_"+s+".")) > 0
	}
	if ref != "" && !taken(base) {
		return base
	}
	n := 1
	for taken(fmt.Sprintf("%s_%d", base, n)) {
		n++
	}
	return fmt.Sprintf("%s_%d", base, n)
}

// plan is the v3_trial phases this run will have (r287: soldiers = the round's soldier rows file is missing).
func plan(pipe Pipeline, capDir string, reference, publish bool, rs []capture.Round, round int) []string {
	sf := soldierFile(pipe, capDir)
	if len(rs) > 0 {
		if n, err := capture.CacheName(filepath.Base(sf), rs, strconv.Itoa(round)); err == nil {
			sf = join(filepath.Dir(sf), n)
		}
	}
	var out []string
	for _, p := range Phases {
		if (p != "soldiers" || !isFile(sf)) && (p != "compare" || reference) && (p != "publish" || publish) {
			out = append(out, p)
		}
	}
	return out
}

// estimate is (expected v3_trial seconds, [(phase, seconds)]) from the last build in builds.json, else packets.
// r287: on a multi-round capture only builds of round count, and packets = that round's.
func estimate(capDir string, soldiers bool, rs []capture.Round, round int) (int64, [][2]any) {
	multi := len(rs) > 1
	b := buildsOf(capDir)
	for i := len(b) - 1; i >= 0; i-- {
		if multi && buildRound(b[i], capture.DefaultOf(rs)) != round {
			continue
		}
		var ph [][2]any
		var vals []float64
		if po := getObj(b[i], "phases"); po != nil && truthy(po) {
			for _, k := range po.Keys() {
				if soldiers || k != "soldiers" {
					v, _ := po.Get(k)
					ph = append(ph, [2]any{k, v})
					f, _ := num(v)
					vals = append(vals, f)
				}
			}
		}
		if len(ph) > 0 {
			return max(estMin, roundInt(pySum(vals))), ph
		}
		if s := get(b[i], "seconds"); truthy(s) {
			f, _ := num(s)
			return max(estMin, roundInt(f)), nil
		}
	}
	var f float64
	if multi {
		f = float64(rs[round-1].Packets)
	} else {
		f, _ = num(get(readJSON(join(capDir, "m-summary.json")), "seq"))
	}
	return max(estMin, roundInt(f*estPerPacket)), nil
}

func estimateLine(total int64, phases [][2]any) string {
	parts := []string{fmt.Sprintf("ESTIMATE %d", total)}
	for _, p := range phases {
		parts = append(parts, pyStr(p[0])+"="+pyStr(p[1]))
	}
	return strings.Join(parts, " ")
}

var (
	reGate   = regexp.MustCompile(`(?m)^gate vs (\S+) .*?: (\d+) ERROR \((\d+) unseen-\* on (\d+) ghosts, (\d+) structural\)`)
	reEqual  = regexp.MustCompile(`(?m)^equal to \S+: (\d/9)`)
	rePub    = regexp.MustCompile(`(?m)^publish: 9 files`)
	reFailLn = regexp.MustCompile(`\bFAIL\b|ERROR structural|REFUSED|differ:|publish: skipped|^gate vs .*: [1-9]\d* ERROR`)
)

// gateCounts is gate_counts(stdout): the 'gate vs' SUMMARY numbers, nil when missing.
func gateCounts(stdout string) *pyc.Object {
	m := reGate.FindStringSubmatch(stdout)
	if m == nil {
		return nil
	}
	o := pyc.NewObject()
	o.Set("baseline", m[1])
	for i, k := range []string{"errors", "unseen", "ghosts", "structural"} {
		o.Set(k, int64(atoi(m[i+2])))
	}
	return o
}

func gateText(gc *pyc.Object) string {
	if gc == nil {
		return ""
	}
	if !truthy(get(gc, "errors")) {
		return "gate clean"
	}
	return fmt.Sprintf("gate %s unseen on %s ghosts, %s structural", pyStr(get(gc, "unseen")), pyStr(get(gc, "ghosts")), pyStr(get(gc, "structural")))
}

// failReason is fail_reason(stdout, stderr).
func failReason(stdout, stderr string) string {
	var lines, bad []string
	for _, l := range pySplitlines(stdout) {
		if pyStrip(l) != "" && !strings.HasPrefix(l, "RESULT:") && !strings.HasPrefix(l, "PHASE ") {
			lines = append(lines, pyStrip(l))
		}
	}
	for _, l := range lines {
		if reFailLn.MatchString(l) {
			bad = append(bad, l)
		}
	}
	slices.SortStableFunc(bad, func(a, b string) int {
		ka, kb := !strings.Contains(a, "ERROR structural"), !strings.Contains(b, "ERROR structural")
		switch {
		case ka == kb:
			return 0
		case !ka:
			return -1
		}
		return 1
	})
	if len(bad) > 0 {
		return pyStrip(bad[0])
	}
	if el := pySplitlines(pyStrip(stderr)); len(el) > 0 {
		return pyStrip(el[len(el)-1])
	}
	if len(lines) > 0 {
		return pyStrip(lines[len(lines)-1])
	}
	return "v3_trial gave no output"
}

// Build is v3_captures.build: the whole build with the line protocol; returns the exit code.
func Build(ctx context.Context, pipe Pipeline, o BuildOpts, say Say) int {
	capDir := resolve(o.Capture)
	t0 := time.Now()
	publish := o.Publish && !o.Reference
	const n = 2
	fail := func(text string) int { say("RESULT: FAIL " + text); return 1 }

	say(fmt.Sprintf("STEP 1/%d resolve %s", n, pathName(capDir)))
	mv, hasM, err := readManifest(capDir)
	if err != nil {
		return fail("bad capture.json: " + err.Error())
	}
	if !hasM {
		return fail("no capture.json")
	}
	m, _ := mv.(*pyc.Object)
	if m == nil {
		m = pyc.NewObject()
	}
	if !isFile(join(capDir, "m-log.bin")) {
		return fail("no m-log.bin")
	}
	rounds := roundsOf(capDir) // r287
	if o.Round != nil && (*o.Round < 1 || *o.Round > len(rounds)) {
		return fail(fmt.Sprintf("--round %d: the capture has %d round(s)", *o.Round, len(rounds)))
	}
	round := capture.DefaultOf(rounds)
	if o.Round != nil {
		round = *o.Round
	} else if round == 0 {
		round = 1
	}
	multi := nonEmpty(rounds) > 1 // suffix c<NN>r<K>_<n>
	// r294: the level of the BUILT round. capture.json's level is the first level session's (bc2raw.level_of);
	// a later round may be another map (one client process, a map rotation: MP_003 pre-round -> MP_007 round 2),
	// and the SRC set (companion config, templates, baseline) must be that map's, or the wire readers decode the
	// stream with the wrong config (`pseq 159: composite: EOFError`, cap_20261031_002547_52296 round 2).
	level := get(m, "level")
	otherMap := false
	if round > 1 && round <= len(rounds) && rounds[round-1].Level != "" && rounds[0].Level != "" && rounds[round-1].Level != rounds[0].Level {
		level, otherMap = rounds[round-1].Level, true
		say(fmt.Sprintf("  round %d is %s (capture.json: %s): src from the map pack for that level", round, pyStr(level), pyStr(get(m, "level"))))
	}
	src := o.Src
	fromIndex := false
	if src == "" {
		var s any
		var fi bool
		if otherMap {
			s = get(harvestIndex(), pyStr(level))
			fi = truthy(s)
		} else {
			s, fi = srcOf(m)
		}
		if !truthy(s) {
			return fail("no data pack for " + pyStr(level))
		}
		src, fromIndex = pyStr(s), fi
	}
	paired := truthy(get(m, "paired")) && !fromIndex && get(m, "src") == any(src) && !otherMap
	ref := ""
	hasRef := false
	if r := get(m, "reference"); truthy(r) {
		if s := get(r, "stem"); truthy(s) {
			ref, hasRef = pyStr(s), true
		}
	}
	if o.Reference && !hasRef {
		return fail("capture.json has no reference")
	}
	over := pyc.NewObject()
	for _, kv := range o.Env {
		var k, v string
		var ok bool
		plus := false
		if strings.Contains(kv, "+=") {
			k, v, ok = strings.Cut(kv, "+=")
			plus = true
		} else {
			k, v, ok = strings.Cut(kv, "=")
		}
		if !ok || k == "" {
			return fail("bad --env " + kv)
		}
		if plus {
			k += "+"
		}
		over.Set(k, v)
	}
	benv := buildEnv(m, paired, o.Reference)
	for _, k := range over.Keys() {
		v, _ := over.Get(k)
		benv.Set(k, v)
	}
	if lock := takeLock(); lock != "" {
		return fail(lock)
	}
	defer releaseLock()
	oldRound, hadRound := os.LookupEnv(roundEnv)
	_ = os.Setenv(roundEnv, strconv.Itoa(round)) // r287: v3_trial and its pipeline runs
	defer func() {
		if hadRound {
			_ = os.Setenv(roundEnv, oldRound)
		} else {
			_ = os.Unsetenv(roundEnv)
		}
	}()

	info, err := ensureSrc(src)
	if err != nil {
		return fail(err.Error())
	}
	if info != "" {
		say("  " + info)
	}
	lvl, err := capLevelOf(src)
	if err != nil {
		say("RESULT: FAIL " + osErrorText(err)) // an uncaught OSError in the reference (traceback, rc 1)
		return 1
	}
	if truthy(level) && !pyEqual(lvl, level) {
		what := "capture.json says"
		if otherMap {
			what = fmt.Sprintf("round %d is", round)
		}
		return fail(fmt.Sprintf("src %s is %s, %s %s", src, pyStr(lvl), what, pyStr(level)))
	}
	out := join(capDir, "builds")
	_ = os.Mkdir(out, 0o777)
	suffix := o.Suffix
	if suffix == "" {
		r := ""
		if o.Reference {
			r = ref
		}
		sr := 0
		if multi && !o.Reference {
			sr = round
		}
		suffix = nextSuffix(capDir, out, r, sr)
	}
	stem := "cl04_" + suffix
	if len(globPrefix(out, stem+".")) > 0 || len(globPrefix(DemosDir(), stem+".")) > 0 {
		return fail(stem + " exists in builds or demos; pick another --suffix")
	}
	pr := "unpaired"
	if paired {
		pr = "paired"
	}
	envs := "-"
	if benv.Len() > 0 {
		var parts []string
		for _, k := range benv.Keys() {
			v, _ := benv.Get(k)
			parts = append(parts, k+"="+pyStr(v))
		}
		envs = strings.Join(parts, " ")
	}
	say(fmt.Sprintf("  level %s, src %s (%s), env %s", pyStr(lvl), src, pr, envs))
	if len(rounds) > 1 {
		r := rounds[round-1]
		hi, pk, sec := "end", "None", "None"
		if !r.Open {
			hi = strconv.FormatUint(uint64(r.Hi), 10)
		}
		if r.HasPackets {
			f, _ := pyc.RoundFloat(float64(r.Packets)/fps, 1)
			pk, sec = strconv.FormatInt(r.Packets, 10), pyStr(f)
		}
		empty := ""
		if r.Packets == 0 {
			empty = " - EMPTY round"
		}
		say(fmt.Sprintf("  round %d of %d: pseq %d..%s, %s packets (%s s)%s", round, len(rounds), r.Lo, hi, pk, sec, empty))
	}
	phases := plan(pipe, capDir, o.Reference, publish, rounds, round)
	say(estimateLine(estimate(capDir, slices.Contains(phases, "soldiers"), rounds, round)))

	line := fmt.Sprintf("STEP 2/%d build %s (v3_trial: build, gate", n, stem)
	if o.Reference {
		line += ", compare with " + ref
	}
	if publish {
		line += ", publish"
	}
	say(line + "; ~0.5-3 min)")
	topt := TrialOpts{Trial: capDir, Src: src, Suffix: suffix, Out: out, Env: envArgs(benv), Publish: publish}
	if o.Reference {
		topt.ExpectEqual = ref
	} else {
		topt.AllowUnseen = true
	}
	t := time.Now()
	var got []string
	type mark struct {
		name string
		at   time.Time
	}
	var marks []mark
	onLine := func(l string) {
		got = append(got, l)
		if strings.HasPrefix(l, "PHASE ") {
			name := pyStrip(l[6:])
			marks = append(marks, mark{name, time.Now()})
			if name != "done" {
				i := slices.Index(phases, name) + 1
				if i == 0 {
					i = min(len(marks), len(phases))
				}
				say(fmt.Sprintf("PROGRESS %s %d/%d", name, i, len(phases)))
			}
		} else if pyStrip(l) != "" {
			say("  " + pyRstrip(l))
		}
	}
	rc, stderr := RunTrial(ctx, pipe, topt, onLine)
	tEnd := time.Now()
	stdout := strings.Join(got, "\n")
	if len(got) > 0 {
		stdout += "\n"
	}
	took := pyc.NewObject()
	for j, mk := range marks {
		if mk.name == "done" {
			continue
		}
		end := tEnd
		if j+1 < len(marks) {
			end = marks[j+1].at
		}
		r, _ := pyc.RoundFloat(end.Sub(mk.at).Seconds(), 1)
		took.Set(mk.name, r)
	}
	// the reference reads the child's stderr raw (os.linesep kept), then write_text translates '\n' again
	text := stdout
	if stderr != "" {
		text += "\n--- stderr ---\n" + osText(stderr)
	}
	if err := writeText(join(out, stem+".trial.txt"), text); err != nil {
		return fail(osErrorText(err))
	}
	say(fmt.Sprintf("  v3_trial rc=%d in %s s; summary %s.trial.txt", rc, fmt0(tEnd.Sub(t).Seconds()), join(out, stem)))
	ok := false
	if rc == 0 {
		for _, l := range got {
			if strings.HasPrefix(l, "RESULT: OK") {
				ok = true
			}
		}
	}
	if !ok {
		return fail(stem + ": " + failReason(stdout, stderr))
	}
	var eq any
	if mm := reEqual.FindStringSubmatch(stdout); mm != nil {
		eq = mm[1]
	}
	gc := gateCounts(stdout)
	published := publish && rePub.MatchString(stdout)
	if publish && !published {
		return fail(stem + ": v3_trial did not publish")
	}
	d, _ := readJSON(join(capDir, "builds.json")).(*pyc.Object)
	if d == nil {
		d = pyc.NewObject()
	}
	bl, _ := get(d, "builds").([]any)
	if _, has := d.Get("builds"); !has {
		bl = []any{}
	}
	b := pyc.NewObject()
	b.Set("stem", stem)
	b.Set("when", isoNow())
	b.Set("round", int64(round))
	b.Set("reference", o.Reference)
	b.Set("published", published)
	b.Set("src", src)
	b.Set("env", benv)
	b.Set("equal", eq)
	if gc != nil {
		b.Set("gate", gc)
	} else {
		b.Set("gate", nil)
	}
	secs, _ := pyc.RoundFloat(tEnd.Sub(t).Seconds(), 1)
	b.Set("seconds", secs)
	b.Set("phases", took)
	d.Set("builds", append(bl, b))
	if err := writeText(join(capDir, "builds.json"), dumpsIndent1(d)); err != nil {
		return fail(osErrorText(err))
	}
	if published {
		say("DEMO " + stem)
	}
	res := "RESULT: OK " + stem
	if eq != nil {
		res += fmt.Sprintf(" equal %s vs %s", eq, ref)
	}
	if published {
		res += " published"
	}
	if gt := gateText(gc); gt != "" {
		res += "; " + gt
	}
	say(res + fmt.Sprintf(" (%s s)", fmt0(time.Since(t0).Seconds())))
	return 0
}

// buildEnv is v3_pov_list.build_env(dict(m, paired=paired), reference) without the POV env: SRCUNPAIRED=1 when not
// paired, then the manifest env, reference.env.
func buildEnv(m *pyc.Object, paired, reference bool) *pyc.Object {
	env := pyc.NewObject()
	if !paired {
		env.Set("SRCUNPAIRED", "1")
	}
	upd := func(o *pyc.Object) {
		if o == nil {
			return
		}
		for _, k := range o.Keys() {
			v, _ := o.Get(k)
			env.Set(k, v)
		}
	}
	upd(getObj(m, "env"))
	if reference {
		upd(getObj(getObj(m, "reference"), "env"))
	}
	return env
}

// envArgs is env_args(benv) as v3_trial --env values: {'K': v, 'K+': v} -> K=v, K+=v.
func envArgs(env *pyc.Object) []string {
	var out []string
	for _, k := range env.Keys() {
		v, _ := env.Get(k)
		if strings.HasSuffix(k, "+") {
			out = append(out, k[:len(k)-1]+"+="+pyStr(v))
		} else {
			out = append(out, k+"="+pyStr(v))
		}
	}
	return out
}

// pyEqual is == for two JSON-ish scalars.
func pyEqual(a, b any) bool {
	if fa, ok := num(a); ok {
		if fb, ok := num(b); ok {
			return fa == fb
		}
		return false
	}
	return a == b
}
