package captures

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"bc2replay/builder/internal/capture"
)

// Say writes one protocol line the way Python's print(..., flush=True) does on a pipe (os.linesep endings).
type Say func(line string)

// Printer returns a Say writing to w.
func Printer(w io.Writer) Say {
	return func(line string) { io.WriteString(w, osText(line+"\n")) }
}

// ---------- extract ----------

// Extract is v3_captures.extract: bc2raw extract into <work dir>/<name> with the STEP / RESULT lines; the exit code.
func Extract(path, name, level, label string, say Say) int {
	p := pyPath(path)
	if !isFile(p) {
		say("RESULT: FAIL no such file " + p)
		return 1
	}
	if name == "" {
		name = "cap_" + stemOf(pathName(p))
	}
	if pathName(name) != name || name == "." || name == ".." {
		say("RESULT: FAIL bad --name " + name)
		return 1
	}
	out := join(WorkDir(), name)
	say(fmt.Sprintf("STEP 1/1 extract %s -> %s", pathName(p), out))
	m, err := capture.Extract(p, out, level, label)
	if err != nil {
		var pe *capture.PyError
		if errors.As(err, &pe) {
			say("RESULT: FAIL " + pe.Msg)
		} else {
			say("RESULT: FAIL " + osErrorText(err))
		}
		return 1
	}
	s := readJSON(join(out, "m-summary.json"))
	lvl := "-"
	if v, _ := m.Get("level"); truthy(v) {
		lvl = pyStr(v)
	}
	line := fmt.Sprintf("  level %s, %s packets, %s records, %s raw records", lvl, pyStr(get(s, "seq")),
		pyStr(get(s, "records")), pyStr(get(s, "raw_records")))
	if truthy(get(s, "truncated")) {
		line += ", TRUNCATED tail"
	}
	say(line)
	say("RESULT: OK " + out)
	return 0
}

// ---------- prune ----------

// RecentS: a raw written to within this many seconds may still be recording.
const RecentS = 120

// Prune is v3_captures.prune (capture retention for RawDir()); keep < 1 means no --keep. Returns the exit code.
func Prune(keep int, extracted, dryRun bool, extraRoots []string, say Say) int {
	if keep < 1 && !extracted {
		say("RESULT: FAIL nothing to do: give --keep N (N >= 1) and/or --extracted")
		return 1
	}
	d, files := RawDir(), rawFiles()
	type ent struct {
		name, path, rec, why string
		bytes                int64
		bad                  bool
	}
	ents := make([]*ent, 0, len(files))
	var total int64
	for _, p := range files {
		o := rawEntry(p)
		e := &ent{name: pyStr(get(o, "name")), path: p}
		if r, ok := get(o, "recorded").(string); ok {
			e.rec = r
		}
		if b, ok := get(o, "bytes").(int64); ok {
			e.bytes = b
		}
		e.why = pyStr(get(o, "reason"))
		e.bad = e.why != rawReason
		total += e.bytes
		ents = append(ents, e)
	}
	dd := "None"
	if d != "" {
		dd = d
	}
	say(fmt.Sprintf("STEP 1/1 prune %s (%d raw files, %d bytes)", dd, len(ents), total))
	if len(ents) == 0 {
		say("RESULT: OK nothing to delete (0 raw files)")
		return 0
	}
	sort.SliceStable(ents, func(i, j int) bool { // reverse=True on (recorded or '', name), stable
		if ents[i].rec != ents[j].rec {
			return ents[i].rec > ents[j].rec
		}
		return ents[i].name > ents[j].name
	})
	src := map[string]string{}
	if extracted {
		for k, v := range rawSources(CaptureDirs(Roots(extraRoots))) {
			if isFile(join(v, "m-log.bin")) {
				src[k] = v
			}
		}
	}
	type step struct {
		e    *ent
		cand int // -1 None (skip), 0 keep, 1 candidate
		why  string
	}
	var plan []step
	ranked := 0
	for _, e := range ents {
		capDir, isSrc := src[lower(e.name)]
		switch {
		case e.bad:
			plan = append(plan, step{e, -1, e.why})
		case isSrc:
			plan = append(plan, step{e, 1, "extracted -> " + pathName(capDir)})
		case keep < 1:
			plan = append(plan, step{e, 0, "not extracted"})
		default:
			ranked++
			if ranked > keep {
				plan = append(plan, step{e, 1, fmt.Sprintf("older than the newest %d", keep)})
			} else {
				plan = append(plan, step{e, 0, fmt.Sprintf("newest %d", keep)})
			}
		}
	}
	now := time.Now()
	nDel, nKeep, nSkip := 0, 0, 0
	var freed int64
	for _, s := range plan {
		act, why := "", s.why
		switch s.cand {
		case -1:
			act = "SKIP"
		case 0:
			act = "KEEP"
		default:
			st, err := os.Stat(s.e.path)
			switch {
			case err == nil && now.Sub(st.ModTime()).Seconds() < RecentS:
				act, why = "SKIP", "recent, may still be recording"
			case dryRun:
				act = "WOULD-DELETE"
			default:
				if err := os.Remove(s.e.path); err != nil {
					act, why = "SKIP", "in use or not deletable: "+osErrorText(err)
				} else {
					act = "DELETE"
				}
			}
		}
		switch act {
		case "DELETE", "WOULD-DELETE":
			nDel++
			freed += s.e.bytes
		case "KEEP":
			nKeep++
		default:
			nSkip++
		}
		say(fmt.Sprintf("%s %s %d %s", act, s.e.name, s.e.bytes, why))
	}
	t := len(ents)
	switch {
	case nDel == 0:
		say(fmt.Sprintf("RESULT: OK nothing to delete (%d raw files)", t))
	case dryRun:
		say(fmt.Sprintf("RESULT: OK would delete %d of %d raw files, %d bytes, kept %d, skipped %d", nDel, t, freed, nKeep, nSkip))
	default:
		say(fmt.Sprintf("RESULT: OK deleted %d of %d raw files, %d bytes freed, kept %d, skipped %d", nDel, t, freed, nKeep, nSkip))
	}
	return 0
}
