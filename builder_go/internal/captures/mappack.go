package captures

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"bc2replay/builder/internal/pyc"
)

// The read side of mappack.py the drivers need: lookup (selection rule), index, level.

const mapPackFormat = 1

type packCache struct {
	mu    sync.Mutex
	key   string
	mtime time.Time
	size  int64
	pack  *pyc.Object
}

var packs packCache

// exitError is a Python SystemExit(msg) raised inside a helper (the drivers decide where it ends up).
type exitError struct{ msg string }

func (e *exitError) Error() string { return e.msg }

func mapPackPath() string {
	if d := datapackDir(); d != "" {
		return join(d, "mappack.json")
	}
	return ""
}

// mapPack is mappack.pack(): the loaded map pack, nil without BC2_DATAPACK_DIR or the file; error = ValueError.
func mapPack() (*pyc.Object, error) {
	p := mapPackPath()
	if p == "" {
		return nil, nil
	}
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return nil, nil
	}
	packs.mu.Lock()
	defer packs.mu.Unlock()
	if packs.pack != nil && packs.key == p && packs.mtime.Equal(st.ModTime()) && packs.size == st.Size() {
		return packs.pack, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	v, err := pyc.Loads(b)
	if err != nil {
		return nil, err
	}
	o, _ := v.(*pyc.Object)
	if o == nil || !eqNum(get(o, "format"), mapPackFormat) {
		return nil, fmt.Errorf("%s: map pack format %s, want %d", pyPath(p), pyStr(get(o, "format")), mapPackFormat)
	}
	packs.key, packs.mtime, packs.size, packs.pack = p, st.ModTime(), st.Size(), o
	return o, nil
}

func packMode() string {
	switch os.Getenv("BC2_MAPPACK") {
	case "0":
		return "never"
	case "1":
		return "always"
	}
	return "auto"
}

// packLookup is mappack.lookup(stem, *folders): the entry to use for SRC stem or nil; *exitError in 'always' mode.
func packLookup(stem string, folders ...string) (*pyc.Object, error) {
	m := packMode()
	if m == "never" {
		return nil, nil
	}
	p, err := mapPack()
	if err != nil {
		return nil, err
	}
	sets := getObj(p, "sets")
	if m == "always" {
		if p == nil {
			where := mapPackPath()
			if where == "" {
				where = "BC2_DATAPACK_DIR/mappack.json"
			}
			return nil, &exitError{"BC2_MAPPACK=1 but no " + where}
		}
		if sets == nil {
			return nil, &exitError{"BC2_MAPPACK=1: " + stem + " not in the map pack"}
		}
		if _, ok := sets.Get(stem); !ok {
			return nil, &exitError{"BC2_MAPPACK=1: " + stem + " not in the map pack"}
		}
		return getObj(sets, stem), nil
	}
	if p == nil || sets == nil {
		return nil, nil
	}
	if _, ok := sets.Get(stem); !ok {
		return nil, nil
	}
	for _, f := range folders {
		if complete(f, stem) {
			return nil, nil
		}
	}
	return getObj(sets, stem), nil
}

// packIndex is mappack.index(): {level: stem}, empty without a pack.
func packIndex() (*pyc.Object, error) {
	if packMode() == "never" {
		return pyc.NewObject(), nil
	}
	p, err := mapPack()
	if err != nil || p == nil {
		return pyc.NewObject(), err
	}
	if i := getObj(p, "index"); i != nil {
		return i, nil
	}
	return pyc.NewObject(), nil
}

// levelFromText is the 'Level:' line of a .txt sidecar (MP.level / the level_of loops); ok false when none.
func levelFromText(txt string) (string, bool) {
	for _, line := range pySplitlines(txt) {
		if strings.HasPrefix(line, "Level:") {
			return pyStrip(line[len("Level:"):]), true
		}
	}
	return "", false
}

// packLevel is mappack.level(entry) (nil = None).
func packLevel(e *pyc.Object) any {
	t, _ := get(e, "txt").(string)
	if l, ok := levelFromText(t); ok {
		return l
	}
	return nil
}

// readTextReplace is Path.read_text(encoding='utf-8', errors='replace') (universal newlines).
func readTextReplace(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return universal(b), nil
}
