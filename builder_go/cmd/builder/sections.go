package main

import (
	"fmt"
	"io"
	"time"

	"bc2replay/builder/internal/sections"
)

// devSections is `builder dev sections <dumpdir|root>...` (maintainer): the sections exam on golden stage dumps
// (wire_sites kinds camera_init / create_rebase / run_section / chunks_of / audit888_rebase / vehsec in call
// order, build_rows with the patched create, next_entries, the 'wire multi' stats). Exit 0 all equal, 1 any differ
// or fail, 2 usage / nothing to check.
func devSections(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: builder dev sections <dumpdir|dumproot>...")
		return 2
	}
	ok, n := true, 0
	fmt.Fprintf(stdout, "%-28s %9s %9s %6s %7s %8s %8s %8s %8s %8s %8s %6s %7s %7s %7s\n", "dump", "creates", "plain",
		"rows", "camera", "crebase", "run_sec", "chunks", "rebase", "vehsec", "tail", "next", "dec ms", "rows ms", "wm ms")
	for _, a := range args {
		t0 := time.Now()
		checks, err := sections.CheckRoot(a, nil)
		for _, c := range checks {
			n++
			k := func(s string) string {
				x := c.Kinds[s]
				if x.Differ != 0 || x.Equal != x.Py {
					return fmt.Sprintf("%d!%d", x.Equal, x.Py)
				}
				return fmt.Sprint(x.Py)
			}
			rows := "equal"
			if !c.RowsEqual || !c.StatsEqual {
				rows = "DIFF"
			}
			fmt.Fprintf(stdout, "%-28s %9s %9s %6s %7s %8s %8s %8s %8s %8s %8d %6s %7.0f %7.0f %7.0f\n", c.Name,
				fmt.Sprintf("%d/%d", c.CreatesEqual, c.Creates), fmt.Sprintf("%d/%d", c.PlainEqual, c.Creates), rows,
				k("camera_init"), k("create_rebase"), k("run_section"), k("chunks_of"), k("audit888_rebase"), k("vehsec"),
				c.Tail, c.NextEntries, c.Decode.Seconds()*1000, c.Build.Seconds()*1000, c.Multi.Seconds()*1000)
			if c.MultiStats == "DIFF" {
				fmt.Fprintln(stdout, "  wire multi stats DIFF")
			}
			for _, d := range c.Diffs {
				fmt.Fprintln(stdout, "  "+d)
			}
			ok = ok && c.OK()
		}
		if err != nil {
			fmt.Fprintf(stdout, "%s: FAIL %v\n", a, err)
			ok = false
		}
		fmt.Fprintf(stdout, "%s: %d dumps in %.1f s\n", a, len(checks), time.Since(t0).Seconds())
	}
	if n == 0 {
		fmt.Fprintln(stderr, "no dump found (a dump dir holds summary.json)")
		return 2
	}
	if !ok {
		return 1
	}
	return 0
}
