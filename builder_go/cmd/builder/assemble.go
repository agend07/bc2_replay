package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"bc2replay/builder/internal/fbgd"
)

// devAssemble is `builder dev assemble <dumpdir>...` (maintainer): Go's assemble from each golden assemble_args dump
// against assemble_out (fbgd/fbgk sha, info), plus the six companion files encoded from the wire_companions dump
// against outputs.json. A dir without assemble_args is a dump root: every subdir that has one is checked.
// Exit 0 all equal, 1 any differ or fail, 2 usage / nothing to check.
func devAssemble(args []string, stdout, stderr io.Writer) int {
	var dirs []string
	for _, a := range args {
		if _, err := os.Stat(filepath.Join(a, "assemble_args.jsonl.gz")); err == nil {
			dirs = append(dirs, a)
			continue
		}
		subs, _ := filepath.Glob(filepath.Join(a, "*", "assemble_args.jsonl.gz"))
		for _, s := range subs {
			dirs = append(dirs, filepath.Dir(s))
		}
	}
	slices.Sort(dirs)
	if len(dirs) == 0 {
		fmt.Fprintln(stderr, "usage: builder dev assemble <dumpdir|dumproot>... (no assemble_args.jsonl.gz found)")
		return 2
	}
	yn := func(b bool) string {
		if b {
			return "equal"
		}
		return "DIFF"
	}
	ok := true
	fmt.Fprintf(stdout, "%-30s %7s %6s %6s %6s %5s %8s %8s %8s\n", "dump", "rows", "fbgd", "fbgk", "info", "comp", "load ms", "asm ms", "comp ms")
	for _, d := range dirs {
		c, err := fbgd.CheckAssemble(d)
		if err != nil {
			fmt.Fprintf(stdout, "%-30s FAIL %v\n", filepath.Base(d), err)
			ok = false
			continue
		}
		comp := fmt.Sprintf("%d/6", c.Companions)
		if c.Companions < 0 {
			comp = "-"
		}
		fmt.Fprintf(stdout, "%-30s %7d %6s %6s %6s %5s %8.0f %8.1f %8.1f\n", filepath.Base(d), c.Rows, yn(c.FBGD), yn(c.FBGK),
			yn(c.Info), comp, c.Load.Seconds()*1000, c.Run.Seconds()*1000, c.CompRun.Seconds()*1000)
		if !c.Info {
			fmt.Fprintf(stdout, "  info go %s\n  info py %s\n", c.GotInfo, c.WantInfo)
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
