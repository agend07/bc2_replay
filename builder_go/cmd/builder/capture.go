package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/pyc"
)

// decodeCheck is `builder dev decode-check` (maintainer): Go's parse / decode / to_v1_rows of every golden dump's
// m-log against the frozen Python's golden stage dumps. Exit 0 all equal, 1 any differ, 2 usage / no dumps.
func decodeCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dev decode-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("golden", "", "golden dump root (default $"+capture.GoldenEnv+" or the private sibling repo)")
	names := fs.String("names", "", "comma list of dump dirs (default: every complete dump)")
	repeat := fs.Int("repeat", 1, "time read + load + to_v1_rows this many times, report the fastest")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		*root = capture.DefaultGolden(".")
	}
	var want []string
	if *names != "" {
		want = strings.Split(*names, ",")
	}
	dumps, err := capture.GoldenDumps(*root, want)
	if *root == "" || err != nil || len(dumps) == 0 {
		fmt.Fprintln(stderr, "no golden dumps found (set --golden or "+capture.GoldenEnv+")", err)
		return 2
	}
	ok := true
	fmt.Fprintf(stdout, "%-16s %8s %8s %8s %8s %8s %8s %8s %8s %8s  %s\n", "dump", "records", "ghosts", "multi", "packets",
		"read ms", "parse", "decode", "v1rows", "total", "stages (lines equal)")
	for _, g := range dumps {
		res, best, d, err := capture.CheckDump(g)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", g.Name, err)
			return 1
		}
		for range *repeat - 1 { // timing only
			tm, err := capture.TimeLoad(g.MLog)
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", g.Name, err)
				return 1
			}
			if tm.Total() < best.Total() {
				best = tm
			}
		}
		var cols []string
		var diffs []string
		nrec := 0
		for _, c := range res {
			mark := "="
			if c.Diff != "" {
				mark, ok = "DIFFER", false
				diffs = append(diffs, fmt.Sprintf("    %s %s", c.Stage, c.Diff))
			}
			if c.Stage == "parse" {
				nrec = c.Lines
			}
			cols = append(cols, fmt.Sprintf("%s %d %s", c.Stage, c.Lines, mark))
		}
		ms := func(t time.Duration) string { return fmt.Sprintf("%.1f", t.Seconds()*1000) }
		fmt.Fprintf(stdout, "%-16s %8d %8d %8d %8d %8s %8s %8s %8s %8s  %s\n", g.Name, nrec, len(d.Ghosts), len(d.Multi),
			len(d.PacketSeqs), ms(best.Read), ms(best.Parse), ms(best.Decode), ms(best.V1Rows), ms(best.Total()),
			strings.Join(cols, ", "))
		for _, l := range diffs {
			fmt.Fprintln(stdout, l)
		}
	}
	if !ok {
		return 1
	}
	return 0
}

// extractCmd is `builder dev extract [--level NAME] [--label TXT] <file.bc2raw> <dir>` = bc2raw.py extract: prints the
// manifest as one JSON line (ensure_ascii=False).
func extractCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dev extract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	level := fs.String("level", "", "level when no S carries a valid name")
	label := fs.String("label", "", "capture label")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: builder dev extract [--level NAME] [--label TXT] <file.bc2raw> <dir>")
		return 2
	}
	m, err := capture.Extract(fs.Arg(0), fs.Arg(1), *level, *label)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	s, err := pyc.Dumps(m, pyc.DumpOptions{})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, s)
	return 0
}

// readMLog reads an m-log and decodes it like v3_types.load.
func readMLog(path string) (*capture.Decoded, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return capture.Load(raw)
}
