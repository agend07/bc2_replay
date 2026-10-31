package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/wire"
	"bc2replay/builder/internal/wire/wirecheck"
)

// wireCheck is `builder dev wire-check` (maintainer): replays every r274 corpus run through the Go readers and
// compares pos/end/flags/err/refs/ovs (+ diagnostic calls / q50c650) with the recorded Unicorn output, like
// wire_py_check.py run mode; with --decoded also the site wrappers end to end (--sites). Exit 0 all equal, 1 any
// differ, 2 usage / no corpus.
func wireCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dev wire-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	logs := fs.String("logs", "", "comma list of corpus logs (default: all present)")
	reader := fs.String("reader", "", "only this reader VA (hex)")
	diffs := fs.Int("diffs", 5, "differing runs listed per row")
	corpus := fs.String("corpus", "", "corpus dir (default $"+wirecheck.CorpusEnv+" or the private sibling repo)")
	jsonOut := fs.String("json", "", "write the report as JSON")
	decoded := fs.String("decoded", "", "also replay the site wrappers end to end on these decoded m-logs (default $"+wirecheck.DecodedEnv+")")
	mlog := fs.Bool("mlog", false, "replay the site wrappers end to end on the Go decode of each corpus log's m-log (meta 'log')")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var va uint32
	if *reader != "" {
		v, err := strconv.ParseUint(strings.TrimPrefix(*reader, "0x"), 16, 32)
		if err != nil {
			fmt.Fprintln(stderr, "--reader:", err)
			return 2
		}
		va = uint32(v)
	}
	dir := *corpus
	if dir == "" {
		dir = wirecheck.DefaultCorpus(".")
	}
	var want []string
	if *logs != "" {
		want = strings.Split(*logs, ",")
	}
	names := wirecheck.Available(dir, want)
	if dir == "" || len(names) == 0 {
		fmt.Fprintln(stderr, "no wire corpus found (set --corpus or "+wirecheck.CorpusEnv+")")
		return 2
	}
	if *decoded == "" && !*mlog {
		*decoded = os.Getenv(wirecheck.DecodedEnv)
	}
	if *mlog && *decoded != "" {
		fmt.Fprintln(stderr, "--mlog and --decoded exclude each other")
		return 2
	}
	t0 := time.Now()
	runs, sites, err := wirecheck.Load(dir, names, *decoded != "" || *mlog)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	load := time.Since(t0)
	rep := wirecheck.Check(runs, va, *diffs)
	rep.Logs, rep.Load = names, load
	rep.Print(stdout, true)
	ok := rep.OK()
	if *decoded != "" || *mlog {
		load := func(name string) (*wire.Log, error) { return wirecheck.LoadDecoded(*decoded, name) }
		if *mlog {
			load = func(name string) (*wire.Log, error) {
				p, err := corpusMLog(dir, name)
				if err != nil {
					return nil, err
				}
				d, err := readMLog(p)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", name, err)
				}
				return capture.WireLog(d), nil
			}
		}
		srep, err := wirecheck.CheckSitesWith(load, names, runs, sites, *diffs)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		srep.Print(stdout)
		ok = ok && srep.OK()
	}
	if *jsonOut != "" {
		b, err := json.MarshalIndent(rep, "", " ")
		if err == nil {
			err = os.WriteFile(*jsonOut, b, 0o644)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if !ok {
		return 1
	}
	return 0
}

// corpusMLog is the m-log path in a corpus log's meta record (line 1: {"type":"meta", ..., "log": path}).
func corpusMLog(dir, name string) (string, error) {
	f, err := os.Open(filepath.Join(dir, name+".jsonl.gz"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	line, err := bufio.NewReaderSize(gz, 1<<16).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	var m struct{ Type, Log string }
	if err := json.Unmarshal(line, &m); err != nil || m.Type != "meta" || m.Log == "" {
		return "", fmt.Errorf("%s: no meta log path", name)
	}
	return m.Log, nil
}
