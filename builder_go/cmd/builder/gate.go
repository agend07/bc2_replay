package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/gate"
	"bc2replay/builder/internal/pyc"
)

// devGate dispatches the maintainer subcommands of phase P3 (args[0] is "gate" or "src-creates").
//
//	builder dev gate [DEMO] ...                      = fbgd_gate.py's command line (see internal/gate)
//	builder dev src-creates BUILD SRC [N] [--rows] [--unpaired] [--mappack FILE]
//	                                                 = v3_src_creates.py; --mappack: SRC is a set stem of that map pack
func devGate(args []string) int {
	if args[0] == "gate" {
		return gate.Main(args[1:], os.Stdout, os.Stderr)
	}
	return srcCreates(args[1:], os.Stdout, os.Stderr)
}

func srcCreates(args []string, stdout, stderr io.Writer) int {
	var pos []string
	rows, unpaired, pack := false, false, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--rows":
			rows = true
		case "--unpaired":
			unpaired = true
		case "--mappack":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "--mappack needs a file")
				return 2
			}
			i++
			pack = args[i]
		default:
			pos = append(pos, args[i])
		}
	}
	if len(pos) < 2 {
		fmt.Fprintln(stderr, "usage: builder dev src-creates BUILD.fbgd SRC [N] [--rows] [--unpaired] [--mappack FILE]")
		return 2
	}
	first := 5
	if len(pos) > 2 {
		if n, err := strconv.Atoi(pos[2]); err == nil && n >= 0 {
			first = n
		}
	}
	var src *gate.SrcIndex
	var err error
	if pack != "" {
		src, err = packIndex(pack, pos[1])
	} else {
		var data []byte
		if data, err = os.ReadFile(gate.ResolveSrc(pos[1])); err == nil {
			src, err = gate.SrcIndexBytes(data)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "src-creates:", err)
		return 1
	}
	data, err := os.ReadFile(pos[0])
	if err != nil {
		fmt.Fprintln(stderr, "src-creates:", err)
		return 1
	}
	res, err := gate.CheckSrcCreates(data, src, !unpaired)
	if err != nil {
		var pe *fbgd.PyError
		if errors.As(err, &pe) { // as v3_cl04c logs it: crebase_vs_src error <repr(e)>
			fmt.Fprintln(stderr, "crebase_vs_src error", pe.Repr())
		} else {
			fmt.Fprintln(stderr, "src-creates:", err)
		}
		return 1
	}
	fmt.Fprintln(stdout, res.Line(first))
	if rows {
		for _, r := range res.Rows() {
			fmt.Fprintln(stdout, " ", r)
		}
	}
	return 0
}

// packIndex is mappack.creates(pack['sets'][stem]).
func packIndex(path, stem string) (*gate.SrcIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := pyc.Loads(data)
	if err != nil {
		return nil, err
	}
	bad := fmt.Errorf("map pack %s has no set %s", path, stem)
	top, _ := v.(*pyc.Object)
	if top == nil {
		return nil, bad
	}
	sv, _ := top.Get("sets")
	sets, _ := sv.(*pyc.Object)
	if sets == nil {
		return nil, bad
	}
	ev, _ := sets.Get(stem)
	entry, _ := ev.(*pyc.Object)
	if entry == nil {
		return nil, bad
	}
	cv, _ := entry.Get("creates")
	l, _ := cv.([]any)
	rows := make([]gate.PackCreate, 0, len(l))
	for _, x := range l {
		r, _ := x.([]any)
		if len(r) != 2 {
			return nil, fmt.Errorf("map pack %s: bad create row in %s", path, stem)
		}
		b, ok1 := r[0].(int64)
		h, ok2 := r[1].(string)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("map pack %s: bad create row in %s", path, stem)
		}
		rows = append(rows, gate.PackCreate{Bits: int(b), Hex: h})
	}
	return gate.SrcIndexCreates(rows)
}
