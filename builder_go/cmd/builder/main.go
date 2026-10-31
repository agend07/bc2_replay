// Command builder is the Go port of the release demo builder (v3_captures).
// Phase P1: `version [--json]` and the maintainer exam `dev wire-check`.
package main

import (
	"fmt"
	"os"
	"runtime"

	"bc2replay/builder/internal/pyc"
)

// version is set at link time: -ldflags "-X main.version=...".
var version = "dev"

const usage = releaseUsage + `
       builder version [--json]
       builder dev wire-check [--logs a,b] [--reader VA] [--diffs N] [--corpus DIR] [--json OUT] [--decoded DIR | --mlog]
       builder dev decode-check [--golden DIR] [--names a,b] [--repeat N]
       builder dev extract [--level NAME] [--label TXT] <file.bc2raw> <dir>`

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if rc, ok := releaseCmd(args, os.Stdout, os.Stderr); ok { // list / extract / prune / build (release.go)
		return rc
	}
	switch args[0] {
	case "version":
		if len(args) > 1 && args[1] == "--json" {
			o := pyc.NewObject()
			o.Set("name", "bc2-builder")
			o.Set("engine", "go")
			o.Set("version", version)
			o.Set("go", runtime.Version())
			o.Set("format", 1)
			s, err := pyc.Dumps(o, pyc.DumpOptions{})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			fmt.Println(s)
			return 0
		}
		fmt.Println("bc2-builder", version, "(go)")
		return 0
	case "dev":
		if len(args) > 1 {
			switch args[1] {
			case "wire-check":
				return wireCheck(args[2:], os.Stdout, os.Stderr)
			case "decode-check":
				return decodeCheck(args[2:], os.Stdout, os.Stderr)
			case "extract":
				return extractCmd(args[2:], os.Stdout, os.Stderr)
			case "soldier":
				return devSoldier(args[2:], os.Stdout, os.Stderr)
			case "sections":
				return devSections(args[2:], os.Stdout, os.Stderr)
			case "events":
				return devEvents(args[2:], os.Stdout, os.Stderr)
			case "assemble":
				return devAssemble(args[2:], os.Stdout, os.Stderr)
			case "cl04c":
				return devCl04c(args[2:], os.Stdout, os.Stderr)
			case "gate", "src-creates":
				return devGate(args[1:])
			}
		}
	}
	fmt.Fprintln(os.Stderr, usage)
	return 2
}
