package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/soldier"
)

// devSoldier is `builder dev soldier [--workers N] [--repeat N] <dumpdir|root>` (maintainer): the soldier convert over
// Go's decode of each golden dump's m-log against the dump's soldier_rows stage. A dir holding summary.json is one
// dump, else a root of dumps. Exit 0 all equal, 1 any differ / error, 2 usage / no dumps.
func devSoldier(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dev soldier", flag.ContinueOnError)
	fs.SetOutput(stderr)
	workers := fs.Int("workers", 0, "convert goroutines (0 = GOMAXPROCS)")
	repeat := fs.Int("repeat", 1, "convert this many times, report the fastest")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: builder dev soldier [--workers N] [--repeat N] <dumpdir|root>")
		return 2
	}
	root, want := filepath.Clean(fs.Arg(0)), []string(nil)
	if _, err := os.Stat(filepath.Join(root, "summary.json")); err == nil {
		root, want = filepath.Dir(root), []string{filepath.Base(root)}
	}
	dumps, err := capture.GoldenDumps(root, want)
	if err != nil || len(dumps) == 0 {
		fmt.Fprintln(stderr, "no golden dumps found under", root, err)
		return 2
	}
	ok := true
	fmt.Fprintf(stdout, "%-24s %8s %8s %8s %9s %10s  %s\n", "dump", "in", "out", "equal", "load ms", "convert ms", "result")
	for _, g := range dumps {
		c, err := soldier.CheckDump(g, *workers)
		if err != nil {
			fmt.Fprintf(stdout, "%-24s ERROR %v\n", g.Name, err)
			ok = false
			continue
		}
		best := c.Convert
		for range *repeat - 1 {
			raw, err := os.ReadFile(g.MLog)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			d, err := capture.Load(raw)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			t0 := time.Now()
			if _, _, err := soldier.Convert(d, *workers); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			best = min(best, time.Since(t0))
		}
		mark := "equal"
		if c.Diff != "" {
			mark, ok = "DIFFER", false
		}
		ms := func(t time.Duration) string { return fmt.Sprintf("%.1f", t.Seconds()*1000) }
		fmt.Fprintf(stdout, "%-24s %8d %8d %8d %9s %10s  %s\n", g.Name, c.In, c.Out, c.Lines, ms(c.Load), ms(best), mark)
		if c.Diff != "" {
			fmt.Fprintln(stdout, "    "+c.Diff)
		}
		fmt.Fprintln(stdout, "    TOTAL", c.Total)
	}
	if !ok {
		return 1
	}
	return 0
}
