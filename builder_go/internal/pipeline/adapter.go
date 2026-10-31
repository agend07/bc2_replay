package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/captures"
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/soldier"
)

// SoldierStamp is the file the Go Soldiers step writes next to the m-log in place of soldier_rows.pkl (the Go build
// recomputes the rows in memory; the stamp only keeps the driver's phase plan and its 'present' line).
const SoldierStamp = "soldier_rows.go.json"

// Captures adapts the Go pipeline to captures.Pipeline (the v3_trial driver's view of v3_cl04c and
// v3_convert_soldiers). The child-environment rules are kept: the process environment, then V3_LOG / SRC, then
// Inputs.Env in order (later keys win).
type Captures struct {
	Workers int
	Environ func() []string // the process environment (nil = os.Environ)
}

var _ captures.Pipeline = (*Captures)(nil)

// RecipeDefaults is v3_trial.recipe_defaults().
func (c *Captures) RecipeDefaults() (map[string]*string, error) { return RecipeDefaults(), nil }

// SoldierFile is the file whose presence means "soldier rows done" for capDir (captures: in place of
// <capDir>/soldier_rows.pkl).
func (c *Captures) SoldierFile(capDir string) string { return filepath.Join(capDir, SoldierStamp) }

// env is the child environment as an ordered lookup: process env, V3_LOG, SRC, then in.Env.
func (c *Captures) env(in captures.Inputs) func(string) (string, bool) {
	envf := c.Environ
	if envf == nil {
		envf = os.Environ
	}
	m := map[string]string{}
	for _, kv := range envf() {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			m[strings.ToUpper(k)] = v // Windows env names are case-insensitive
		}
	}
	m["V3_LOG"], m["SRC"] = in.Log, in.Src
	for _, kv := range in.Env {
		m[strings.ToUpper(kv[0])] = kv[1]
	}
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// stampName is SoldierStamp keyed by round (r287, v3_types.cache_name as in v3_convert_soldiers: soldier_rows.go_r<K>.json
// on a multi-round capture; the open round counted up to m-summary.json "seq").
func stampName(mlog, round string) (string, error) {
	dir := filepath.Dir(mlog)
	rl, err := os.ReadFile(filepath.Join(dir, "raw-log.bin"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		rl = nil
	}
	var seq *int64
	if b, err := os.ReadFile(filepath.Join(dir, "m-summary.json")); err == nil {
		var m struct {
			Seq *int64 `json:"seq"`
		}
		if json.Unmarshal(b, &m) == nil {
			seq = m.Seq
		}
	}
	rs, err := capture.Rounds(rl, seq)
	if err != nil {
		return "", err
	}
	return capture.CacheName(SoldierStamp, rs, round)
}

// failed is a non-zero exit: rc 1, the message as the last stderr line (Python's SystemExit / traceback tail).
func failed(stdout string, err error) captures.Result {
	msg := err.Error()
	var se *SystemExit
	if !errors.As(err, &se) && !strings.Contains(msg, ": ") {
		msg = "Error: " + msg
	}
	return captures.Result{RC: 1, Stdout: stdout, Stderr: msg + "\n"}
}

// Soldiers is v3_convert_soldiers.py: the Go convert of <Log> (in memory) and the TOTAL line; it writes the stamp
// SoldierStamp next to the m-log (the rows themselves are recomputed by Build).
func (c *Captures) Soldiers(ctx context.Context, in captures.Inputs) (captures.Result, error) {
	lookup := c.env(in)
	if v, ok := lookup("V3_SOLDIER_ENGINE"); ok && v != "" && v != "py" {
		return failed("", &MaintainerOnly{"V3_SOLDIER_ENGINE", v, "only the pure soldier_py convert is ported"}), nil
	}
	raw, err := os.ReadFile(in.Log)
	if err != nil {
		return failed("", err), nil
	}
	round, _ := lookup(capture.RoundEnv)
	dec, _, err := capture.LoadFile(in.Log, round) // r282/r287: level session as v3_types.load
	if err != nil {
		return failed("", err), nil
	}
	if err := ctx.Err(); err != nil {
		return captures.Result{}, err
	}
	rows, st, err := soldier.Convert(dec, c.Workers)
	if err != nil {
		return failed("", err), nil
	}
	total := "TOTAL " + st.Repr(len(rows))
	o := pyc.NewObject()
	o.Set("format", 1)
	o.Set("engine", "go")
	o.Set("log_bytes", len(raw))
	o.Set("rows", len(rows))
	o.Set("total", total)
	js, err := pyc.Dumps(o, pyc.DumpOptions{Indent: 1})
	if err != nil {
		return failed("", err), nil
	}
	stamp, err := stampName(in.Log, round)
	if err != nil {
		return failed("", err), nil
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(in.Log), stamp), []byte(js+"\n"), 0o644); err != nil {
		return failed("", err), nil
	}
	return captures.Result{Stdout: total + "\n"}, nil
}

// Build is v3_cl04c.py <OutDir> <Suffix>.
func (c *Captures) Build(ctx context.Context, in captures.Inputs) (captures.Result, error) {
	lookup := c.env(in)
	rec, err := RecipeFromLookup(lookup)
	if err != nil {
		return failed("", err), nil
	}
	round, _ := lookup(capture.RoundEnv)
	get := func(k string) string { v, _ := lookup(k); return v }
	res, err := Run(ctx, Inputs{Log: in.Log, Datapack: get("BC2_DATAPACK_DIR"), DemosDir: get("BC2_DEMOS_DIR"),
		MapPack: get("BC2_MAPPACK"), OutDir: in.OutDir, Suffix: in.Suffix, Recipe: rec, Workers: c.Workers,
		Round: round})
	if err != nil {
		if ctx.Err() != nil {
			return captures.Result{Stdout: res.Stdout}, ctx.Err()
		}
		return failed(res.Stdout, err), nil
	}
	return captures.Result{Stdout: res.Stdout}, nil
}
