package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/events"
)

// devEvents is `builder dev events <dumpdir|dumproot>...` (maintainer): Go's wire deletes, registry and destruction
// companions from Go's own decode of each golden dump's m-log plus the dump's inputs, against the stages
// wire_deletes / registry / wire_companions and the six companion files of outputs.json. Inputs outside a dump:
// $BC2_DATAPACK_DIR (else <root>/../datapack_oracle_r277) and $BC2_DEMOS_DIR (else $BC2_ARCHIVE_DIR/demos).
// Exit 0 all equal, 1 any differ or fail, 2 usage / nothing to check.
func devEvents(args []string, stdout, stderr io.Writer) int {
	var dumps []capture.GoldenDump
	var root string
	for _, a := range args {
		var ds []capture.GoldenDump
		var err error
		if _, e := os.Stat(filepath.Join(a, "summary.json")); e == nil {
			root = filepath.Dir(filepath.Clean(a))
			ds, err = capture.GoldenDumps(root, []string{filepath.Base(filepath.Clean(a))})
		} else {
			root = a
			ds, err = capture.GoldenDumps(a, nil)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		dumps = append(dumps, ds...)
	}
	slices.SortFunc(dumps, func(a, b capture.GoldenDump) int { return strings.Compare(a.Dir, b.Dir) })
	if len(dumps) == 0 {
		fmt.Fprintln(stderr, "usage: builder dev events <dumpdir|dumproot>... (no golden dump with its m-log found)")
		return 2
	}
	opt := events.DefaultOptions(root)
	ok := true
	fmt.Fprintf(stdout, "%-28s %9s %9s %9s %6s %8s %8s %8s %8s\n", "dump", "deletes", "registry", "comp", "files",
		"load ms", "del ms", "reg ms", "comp ms")
	for _, g := range dumps {
		c, err := events.CheckDump(g, opt)
		if err != nil {
			fmt.Fprintf(stdout, "%-28s FAIL %v\n", g.Name, err)
			ok = false
			continue
		}
		col := map[string]string{}
		for _, n := range events.StageNames {
			col[n] = "-"
		}
		for _, s := range c.Stages {
			col[s.Stage] = fmt.Sprintf("%d", s.Lines)
			if s.Diff != "" {
				col[s.Stage] = "DIFF"
			}
		}
		files := "-"
		if c.Files >= 0 {
			files = fmt.Sprintf("%d/6", c.Files)
		}
		ms := func(d interface{ Seconds() float64 }) float64 { return d.Seconds() * 1000 }
		fmt.Fprintf(stdout, "%-28s %9s %9s %9s %6s %8.0f %8.1f %8.1f %8.1f\n", g.Name, col["wire_deletes"], col["registry"],
			col["wire_companions"], files, ms(c.Decode), ms(c.Deletes), ms(c.Registry), ms(c.Companions))
		for _, s := range c.Stages {
			if s.Diff != "" {
				fmt.Fprintf(stdout, "  %s %s\n", s.Stage, s.Diff)
			}
		}
		if c.Skip != "" {
			fmt.Fprintf(stdout, "  skipped: %s\n", c.Skip)
		}
		if !c.OK() {
			ok = false
		}
	}
	if !ok {
		return 1
	}
	return 0
}
