package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"bc2replay/builder/internal/captures"
	"bc2replay/builder/internal/pipeline"
)

// Release commands (the panel contract, = v3_captures.py):
//
//	builder list [--root DIR]... [--json]
//	builder extract <file.bc2raw> [--name DIR] [--level NAME] [--label TXT]
//	builder prune [--root DIR]... [--keep N] [--extracted] [--dry-run]
//	builder build <capture dir> [--suffix N] [--reference] [--publish] [--src STEM] [--env K=V|K+=V]... [--round K]
//
// Exit 0 only on RESULT: OK; argument errors exit 2 (argparse).

// newPipeline supplies the demo pipeline for build: the Go pipeline; the frozen Python core only when
// BC2_PY_PIPELINE names a Python builder dir (maintainer A/B).
var newPipeline = func() (captures.Pipeline, error) {
	p, err := captures.PyPipelineFromEnv()
	if err != nil {
		return nil, err
	}
	if p != nil {
		return p, nil
	}
	return &pipeline.Captures{}, nil
}

// lazyPipe creates the pipeline on first use, so argument / manifest failures never need one.
type lazyPipe struct {
	once sync.Once
	p    captures.Pipeline
	err  error
}

func (l *lazyPipe) get() (captures.Pipeline, error) {
	l.once.Do(func() { l.p, l.err = newPipeline() })
	return l.p, l.err
}

func (l *lazyPipe) RecipeDefaults() (map[string]*string, error) {
	p, err := l.get()
	if err != nil {
		return nil, err
	}
	return p.RecipeDefaults()
}

func (l *lazyPipe) Soldiers(ctx context.Context, in captures.Inputs) (captures.Result, error) {
	p, err := l.get()
	if err != nil {
		return captures.Result{RC: 1}, err
	}
	return p.Soldiers(ctx, in)
}

// SoldierFile is the pipeline's "soldier rows done" file (the Go stamp, or soldier_rows.pkl for the Python core and
// when no pipeline can be made: the run then fails at its first pipeline call anyway).
func (l *lazyPipe) SoldierFile(capDir string) string {
	if p, err := l.get(); err == nil {
		if x, ok := p.(interface{ SoldierFile(string) string }); ok {
			return x.SoldierFile(capDir)
		}
	}
	return filepath.Join(capDir, "soldier_rows.pkl")
}

func (l *lazyPipe) Build(ctx context.Context, in captures.Inputs) (captures.Result, error) {
	p, err := l.get()
	if err != nil {
		return captures.Result{RC: 1}, err
	}
	return p.Build(ctx, in)
}

type optSpec struct {
	name   string // --long
	valued bool
}

type parsed struct {
	vals  map[string][]string
	flags map[string]bool
	pos   []string
}

type argError struct{ msg string }

func (e *argError) Error() string { return e.msg }

var negNum = regexp.MustCompile(`^-\d+$|^-\d*\.\d+$`)

// parseOpts is argparse for long options (unique prefixes, --opt=VALUE, -h/--help) and positionals.
func parseOpts(args []string, specs []optSpec) (*parsed, error) {
	p := &parsed{vals: map[string][]string{}, flags: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.pos = append(p.pos, args[i+1:]...)
			break
		}
		if a == "-h" || a == "--help" {
			return p, &argError{"help"}
		}
		if !strings.HasPrefix(a, "-") || a == "-" || negNum.MatchString(a) {
			p.pos = append(p.pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(a, "=")
		var match []optSpec
		for _, s := range specs {
			if s.name == name {
				match = []optSpec{s}
				break
			}
			if strings.HasPrefix(name, "--") && strings.HasPrefix(s.name, name) {
				match = append(match, s)
			}
		}
		if len(match) == 0 {
			return p, &argError{"unrecognized arguments: " + a}
		}
		if len(match) > 1 {
			var names []string
			for _, m := range match {
				names = append(names, m.name)
			}
			return p, &argError{fmt.Sprintf("ambiguous option: %s could match %s", name, strings.Join(names, ", "))}
		}
		s := match[0]
		if !s.valued {
			if hasVal {
				return p, &argError{fmt.Sprintf("argument %s: ignored explicit argument '%s'", s.name, val)}
			}
			p.flags[s.name] = true
			continue
		}
		if !hasVal {
			if i+1 >= len(args) || (strings.HasPrefix(args[i+1], "-") && !negNum.MatchString(args[i+1]) && args[i+1] != "-") {
				return p, &argError{fmt.Sprintf("argument %s: expected one argument", s.name)}
			}
			i++
			val = args[i]
		}
		p.vals[s.name] = append(p.vals[s.name], val)
	}
	return p, nil
}

func (p *parsed) last(name string) string {
	v := p.vals[name]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

const releaseUsage = `usage: builder list [--root DIR]... [--json]
       builder extract <file.bc2raw> [--name DIR] [--level NAME] [--label TXT]
       builder prune [--root DIR]... [--keep N] [--extracted] [--dry-run]
       builder build <capture dir> [--suffix N] [--reference] [--publish] [--src STEM] [--env K=V|K+=V]... [--round K]`

// argFail prints an argparse-style error (exit 2) or the help (exit 0).
func argFail(cmd string, err error, stdout, stderr io.Writer) int {
	if err.Error() == "help" {
		fmt.Fprintln(stdout, releaseUsage)
		return 0
	}
	fmt.Fprintln(stderr, releaseUsage)
	fmt.Fprintf(stderr, "builder %s: error: %s\n", cmd, err.Error())
	return 2
}

// releaseCmd runs one release command; ok false when args[0] is not one.
func releaseCmd(args []string, stdout, stderr io.Writer) (int, bool) {
	say := captures.Printer(stdout)
	ctx := context.Background()
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "list":
		p, err := parseOpts(rest, []optSpec{{name: "--root", valued: true}, {name: "--json"}})
		if err == nil && len(p.pos) > 0 {
			err = &argError{"unrecognized arguments: " + strings.Join(p.pos, " ")}
		}
		if err != nil {
			return argFail(cmd, err, stdout, stderr), true
		}
		caps := captures.List(p.vals["--root"])
		if p.flags["--json"] {
			say(captures.ListJSON(caps))
		} else {
			for _, l := range captures.ListText(caps) {
				say(l)
			}
		}
		return 0, true
	case "extract":
		p, err := parseOpts(rest, []optSpec{{name: "--name", valued: true}, {name: "--level", valued: true}, {name: "--label", valued: true}})
		if err == nil && len(p.pos) != 1 {
			if len(p.pos) == 0 {
				err = &argError{"the following arguments are required: file"}
			} else {
				err = &argError{"unrecognized arguments: " + strings.Join(p.pos[1:], " ")}
			}
		}
		if err != nil {
			return argFail(cmd, err, stdout, stderr), true
		}
		return captures.Extract(p.pos[0], p.last("--name"), p.last("--level"), p.last("--label"), say), true
	case "prune":
		p, err := parseOpts(rest, []optSpec{{name: "--root", valued: true}, {name: "--keep", valued: true}, {name: "--extracted"}, {name: "--dry-run"}})
		if err == nil && len(p.pos) > 0 {
			err = &argError{"unrecognized arguments: " + strings.Join(p.pos, " ")}
		}
		keep := 0
		if err == nil && len(p.vals["--keep"]) > 0 {
			k, cerr := strconv.Atoi(strings.TrimSpace(p.last("--keep")))
			if cerr != nil {
				err = &argError{fmt.Sprintf("argument --keep: invalid int value: '%s'", p.last("--keep"))}
			}
			keep = k
			if keep < 1 {
				keep = 0
			}
		}
		if err != nil {
			return argFail(cmd, err, stdout, stderr), true
		}
		return captures.Prune(keep, p.flags["--extracted"], p.flags["--dry-run"], p.vals["--root"], say), true
	case "build":
		p, err := parseOpts(rest, []optSpec{{name: "--suffix", valued: true}, {name: "--reference"}, {name: "--publish"},
			{name: "--src", valued: true}, {name: "--env", valued: true}, {name: "--round", valued: true}})
		if err == nil && len(p.pos) != 1 {
			if len(p.pos) == 0 {
				err = &argError{"the following arguments are required: capture"}
			} else {
				err = &argError{"unrecognized arguments: " + strings.Join(p.pos[1:], " ")}
			}
		}
		var round *int // r287: 1-based round, nil = default
		if err == nil && len(p.vals["--round"]) > 0 {
			k, cerr := strconv.Atoi(strings.TrimSpace(p.last("--round")))
			if cerr != nil {
				err = &argError{fmt.Sprintf("argument --round: invalid int value: '%s'", p.last("--round"))}
			}
			round = &k
		}
		if err != nil {
			return argFail(cmd, err, stdout, stderr), true
		}
		o := captures.BuildOpts{Capture: p.pos[0], Suffix: p.last("--suffix"), Reference: p.flags["--reference"],
			Publish: p.flags["--publish"], Src: p.last("--src"), Env: p.vals["--env"], Round: round}
		return captures.Build(ctx, &lazyPipe{}, o, say), true
	}
	return 0, false
}
