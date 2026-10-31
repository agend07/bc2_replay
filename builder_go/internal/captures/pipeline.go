package captures

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"bc2replay/builder/internal/pyc"
)

// Pipeline is the demo pipeline the drivers call (v3_trial's two children). Its text
// output is the contract today: v3_trial reads the 'templates ', 'wire multi|deletes|companions|registry',
// 'tplnew_bad', 'crebase_vs_src' lines and the last line from Stdout, and writes Stdout/Stderr as <stem>.build.txt.
type Pipeline interface {
	// RecipeDefaults are v3_cl04c's RECIPE literal defaults (for --env K+=V); a nil value = no literal default (TPL).
	RecipeDefaults() (map[string]*string, error)
	// Soldiers is v3_convert_soldiers.py: writes <dir of Inputs.Log>/soldier_rows.pkl (or the pipeline's
	// SoldierFile, see soldierFile). OutDir/Suffix are unused.
	Soldiers(ctx context.Context, in Inputs) (Result, error)
	// Build is v3_cl04c.py <OutDir> <Suffix>: the 9 files <OutDir>/cl04_<Suffix>.*.
	Build(ctx context.Context, in Inputs) (Result, error)
}

// soldierFiler is an optional Pipeline method: the file whose presence means "soldier rows done" for a capture dir.
type soldierFiler interface {
	SoldierFile(capDir string) string
}

// soldierFile is pipe's SoldierFile(capDir) when it has one (the Go pipeline's stamp), else <capDir>/soldier_rows.pkl
// (the Python core). The plan (PROGRESS phase count) and the 'present (N B)' line use it.
func soldierFile(pipe Pipeline, capDir string) string {
	if x, ok := pipe.(soldierFiler); ok {
		return x.SoldierFile(capDir)
	}
	return join(capDir, "soldier_rows.pkl")
}

// Inputs is one pipeline run: the child environment V3_LOG=Log, SRC=Src, then Env in order (later keys win, so a
// RECIPE override may replace SRC).
type Inputs struct {
	Log    string
	Src    string
	OutDir string
	Suffix string
	Env    [][2]string
}

// Result is the run's exit status and its text output (universal newlines, utf-8 'replace').
type Result struct {
	RC     int
	Stdout string
	Stderr string
}

// PyPipeline is the temporary adapter: it runs the frozen Python modules (v3_cl04c.py, v3_convert_soldiers.py)
// exactly as v3_trial does, so the Go drivers run end to end on the Python core until the Go pipeline replaces it.
type PyPipeline struct {
	Python string // python.exe
	Dir    string // dir holding v3_cl04c.py / v3_convert_soldiers.py / v3_trial.py
}

// PyPipelineEnv names the adapter's settings: BC2_PY_PIPELINE=<dir with the builder .py modules>, optional
// BC2_PY_PIPELINE_PYTHON (default <dir>/python.exe).
const (
	PyPipelineEnv       = "BC2_PY_PIPELINE"
	PyPipelinePythonEnv = "BC2_PY_PIPELINE_PYTHON"
)

// PyPipelineFromEnv returns the adapter configured by BC2_PY_PIPELINE, or nil when unset.
func PyPipelineFromEnv() (*PyPipeline, error) {
	dir := os.Getenv(PyPipelineEnv)
	if dir == "" {
		return nil, nil
	}
	py := os.Getenv(PyPipelinePythonEnv)
	if py == "" {
		py = join(dir, "python.exe")
	}
	if !isFile(join(dir, "v3_cl04c.py")) {
		return nil, fmt.Errorf("%s=%s has no v3_cl04c.py", PyPipelineEnv, dir)
	}
	if !isFile(py) {
		return nil, fmt.Errorf("python for %s not found: %s", PyPipelineEnv, py)
	}
	return &PyPipeline{Python: py, Dir: pyPath(dir)}, nil
}

// RecipeDefaults is v3_trial.recipe_defaults() evaluated by the adapter's Python.
func (p *PyPipeline) RecipeDefaults() (map[string]*string, error) {
	code := "import sys, json; sys.path.insert(0, sys.argv[1]); import v3_trial; " +
		"sys.stdout.write(json.dumps(v3_trial.recipe_defaults()))"
	cmd := exec.Command(p.Python, "-I", "-c", code, p.Dir)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("recipe defaults: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	v, err := pyc.Loads(out.Bytes())
	if err != nil {
		return nil, err
	}
	o, _ := v.(*pyc.Object)
	if o == nil {
		return nil, errors.New("recipe defaults: not a JSON object")
	}
	m := map[string]*string{}
	for _, k := range o.Keys() {
		x, _ := o.Get(k)
		if s, ok := x.(string); ok {
			m[k] = &s
		} else {
			m[k] = nil
		}
	}
	return m, nil
}

func (p *PyPipeline) run(ctx context.Context, in Inputs, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, p.Python, args...)
	env := append(os.Environ(), "V3_LOG="+in.Log, "SRC="+in.Src, "PYTHONIOENCODING=utf-8")
	for _, kv := range in.Env {
		env = append(env, kv[0]+"="+kv[1])
	}
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := Result{Stdout: universal(out.Bytes()), Stderr: universal(errb.Bytes())}
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return res, err
		}
		res.RC = ee.ExitCode()
	}
	return res, nil
}

// Soldiers runs v3_convert_soldiers.py.
func (p *PyPipeline) Soldiers(ctx context.Context, in Inputs) (Result, error) {
	return p.run(ctx, in, join(p.Dir, "v3_convert_soldiers.py"))
}

// Build runs v3_cl04c.py <OutDir> <Suffix>.
func (p *PyPipeline) Build(ctx context.Context, in Inputs) (Result, error) {
	return p.run(ctx, in, join(p.Dir, "v3_cl04c.py"), in.OutDir, in.Suffix)
}

// runPipe turns a launch error into the failed child Python's subprocess would report (rc 1, text on stderr).
func runPipe(f func(context.Context, Inputs) (Result, error), ctx context.Context, in Inputs) Result {
	r, err := f(ctx, in)
	if err != nil {
		if r.RC == 0 {
			r.RC = 1
		}
		r.Stderr += err.Error() + "\n"
	}
	return r
}
