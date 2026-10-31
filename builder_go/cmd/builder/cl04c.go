package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"bc2replay/builder/internal/pipeline"
)

// devCl04c is `builder dev cl04c [--time] [--names a,b] [--work DIR] <dumpdir|dumproot>...` (maintainer): the Go
// pipeline on each golden dump's m-log with the dump's recipe; every P2-E checkpoint compared in order (stops at the
// first differing path), then the 9 files' sha256 vs outputs.json and the cl04c lines of trial.txt. SRC sets outside the map pack need BC2_DEMOS_DIR.
// Flags go before the dump arguments (Go's flag package stops at the first positional). Exit 0 all equal, 1 any differ or fail, 2 usage / nothing to check.
func devCl04c(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dev cl04c", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tm := fs.Bool("time", false, "also time a plain (checkpoint-free) run")
	names := fs.String("names", "", "only these dumps of a dump root (comma list)")
	work := fs.String("work", "", "scratch dir for the outputs (default: the system temp dir)")
	datapack := fs.String("datapack", "", "data pack dir (default BC2_DATAPACK_DIR, else <root>/../datapack_oracle_r277)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var want []string
	if *names != "" {
		want = strings.Split(*names, ",")
	}
	n, ok := 0, true
	fmt.Fprintf(stdout, "%-28s %9s %9s %6s %6s %8s %8s  %s\n", "dump", "ckpts", "records", "files", "lines", "s", "plain s", "first difference")
	for _, a := range fs.Args() {
		ds, err := pipeline.DevDumps(a, want)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		for _, g := range ds {
			for _, r := range pipeline.DevCheck(context.Background(), g.Dir, g.MLog, pipeline.DevOptions{Datapack: *datapack, Work: *work, Time: *tm}) {
				n++
				ok = ok && r.OK()
				msg := r.FirstBad
				for _, x := range []string{r.FileDiff, r.LineDiff} {
					if msg == "" {
						msg = x
					}
				}
				if r.Loose > 0 {
					msg = fmt.Sprintf("(%d records equal under the loose-t projection: %s) %s", r.Loose, strings.Join(r.LooseAt, ","), msg)
				}
				if r.Err != nil {
					msg = "ERROR " + r.Err.Error() + "; " + msg
				}
				fmt.Fprintf(stdout, "%-28s %4d/%-4d %9d %3d/%-2d %3d/%-2d %8.2f %8.2f  %s\n", r.Name, r.Ckpts, r.CkptsPy, r.Records,
					r.Files, r.FilesPy, r.Lines, r.LinesPy, r.Took.Seconds(), r.Plain.Seconds(), msg)
			}
		}
	}
	if n == 0 {
		fmt.Fprintln(stderr, "usage: builder dev cl04c [--time] [--names a,b] <dumpdir|dumproot>... (no dump found)")
		return 2
	}
	if !ok {
		return 1
	}
	return 0
}
