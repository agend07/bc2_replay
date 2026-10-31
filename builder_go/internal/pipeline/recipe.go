// Package pipeline is the Go port of v3_cl04c.py: one capture (m-log) -> the 9 demo files (fbgd, fbgk, the six
// destruction companions, the .txt sidecar). Run is the whole build; the
// v3_cl04c module globals (os.environ RECIPE, the wire ENGINE, V.LOG, the create_chunk monkeypatch, the
// d['_create_ctx'] memo) are explicit inputs and state here.
package pipeline

import (
	"fmt"
	"strings"
)

// Recipe is v3_cl04c's RECIPE as typed fields. The Python semantics are kept: every field is a string, "" disables a
// flag (E(k) is None), anything else enables it / is parsed where Python parses it. DefaultRecipe holds the RECIPE
// literals; Set applies one env-style override.
type Recipe struct {
	SRC            string // same-map template demo stem (map pack set or 9-file SRC set)
	UPD            string // single-section classes sliced from the wire (initial state + client updates)
	REBASE         string // initial states with one origin-relative vec3 re-encoded origin-zero
	UPDREBASE      string // client updates of these classes rebased like REBASE
	C4UPD          string // r217: '1' adds C4 0x143aa80 to UPDREBASE
	DELNULL        string // r205/r284 (default on): null live component p12 / camera p3 refs to a deleted ghost
	CREBASE        string // r159/r190: origin-zero create re-encode classes
	AA80B0         string // r128: clear the last bit of CUT-type creates
	WIREMULTI      string // r135..r189: multi-section classes sliced from the wire
	WIREMULTISKIPV string // r300c triage: WIREMULTI_SKIPV=v419,v377 drops the creates of these variants
	REGWIRE        string // r136: registry updates from the wire
	REG0WIRE       string // r151: registry join row from the wire
	Z383           string // r138: event ghost p1 like every server demo
	JOINKEY        string // r138: join batch (frames) folded into key0; r289: 'auto' (default) = the wire join burst
	WIREDEL        string // r140: ghost deletions from the packets
	WIRECOMP       string // r143: destruction companions from the wire stream ('' = copy SRC's six files)
	VIEWERID       string // r203: the viewer's player id
	SHIFTF         string // r149: level-load frames before the join batch
	SRCUNPAIRED    string // r207: SRC is not the paired server demo (crebase_vs_src classification only)
}

// recipeLiterals are v3_cl04c RECIPE's literal defaults, in RECIPE order (TPL = a Path expression: no literal).
var recipeLiterals = [][2]string{
	{"SRC", "patched_20261023_165152_405"},
	{"TPL", "\x00"},
	{"UPD", "0x1431c0c,0x143b2b4,0x1431df8,0x1431d14,0x143aa80,0x143b17c,0x143956c,0x143b66c,0x1431940,0x14383cc,0x143800c," +
		"0x143b3bc,0x14377f8,0x143afd0,0x1436010,0x143a794,0x143ade4"},
	{"REBASE", "0x143aa80,0x143afd0,0x1436010,0x143a794,0x143ade4"},
	{"UPDREBASE", "0x143afd0,0x1436010,0x143a794,0x143ade4"},
	{"C4UPD", "1"},
	{"DELNULL", "1"},
	{"CREBASE", "0x143afd0,0x1436cbc,0x143b3bc"},
	{"AA80B0", "1"},
	{"WIREMULTI", "0x14399c8,0x1436cbc/7,0x1432098/9,0x1436cbc/29,0x1436cbc/37,0x1436cbc/10,0x1436cbc/15,0x1436cbc/4," +
		"0x1436cbc/8,0x1436cbc/14,0x1436cbc/35,0x1436cbc/40,0x1436cbc/54,0x1436cbc/68,0x1436cbc/76,0x1436cbc/13," +
		"0x1436cbc/45,0x1436cbc/47,0x1436cbc/5,0x1436cbc/61,0x1436cbc/80"}, // r295: + /5,/61,/80 (MP_007 layouts)
	{"WIREMULTI_SKIPV", ""},
	{"REGWIRE", "1"},
	{"REG0WIRE", "1"},
	{"Z383", "1"}, {"JOINKEY", "auto"},
	{"WIREDEL", "1"},
	{"WIRECOMP", "1"},
	{"VIEWERID", "1"},
	{"SHIFTF", "2000"},
	{"SRCUNPAIRED", ""},
}

// RecipeDefaults is v3_trial.recipe_defaults(): every RECIPE key with its literal default, nil for TPL (no literal;
// '--env TPL+=' is refused). The map is fresh on each call.
func RecipeDefaults() map[string]*string {
	m := make(map[string]*string, len(recipeLiterals))
	for _, kv := range recipeLiterals {
		if kv[1] == "\x00" {
			m[kv[0]] = nil
			continue
		}
		v := kv[1]
		m[kv[0]] = &v
	}
	return m
}

// RecipeKeys are the RECIPE keys in RECIPE order (v3_trial's --env K+=V needs their literals).
func RecipeKeys() []string {
	out := make([]string, len(recipeLiterals))
	for i, kv := range recipeLiterals {
		out[i] = kv[0]
	}
	return out
}

// DefaultRecipe is RECIPE with no override (the cl04_1019 recipe + the r217 C4UPD default).
func DefaultRecipe() Recipe {
	var r Recipe
	for _, kv := range recipeLiterals {
		if f := r.field(kv[0]); f != nil {
			*f = kv[1]
		}
	}
	return r
}

func (r *Recipe) field(k string) *string {
	switch k {
	case "SRC":
		return &r.SRC
	case "UPD":
		return &r.UPD
	case "REBASE":
		return &r.REBASE
	case "UPDREBASE":
		return &r.UPDREBASE
	case "C4UPD":
		return &r.C4UPD
	case "DELNULL":
		return &r.DELNULL
	case "CREBASE":
		return &r.CREBASE
	case "AA80B0":
		return &r.AA80B0
	case "WIREMULTI":
		return &r.WIREMULTI
	case "WIREMULTI_SKIPV":
		return &r.WIREMULTISKIPV
	case "REGWIRE":
		return &r.REGWIRE
	case "REG0WIRE":
		return &r.REG0WIRE
	case "Z383":
		return &r.Z383
	case "JOINKEY":
		return &r.JOINKEY
	case "WIREDEL":
		return &r.WIREDEL
	case "WIRECOMP":
		return &r.WIRECOMP
	case "VIEWERID":
		return &r.VIEWERID
	case "SHIFTF":
		return &r.SHIFTF
	case "SRCUNPAIRED":
		return &r.SRCUNPAIRED
	}
	return nil
}

// MaintainerOnly is the error of a key / value only the Python builder supports.
type MaintainerOnly struct{ Key, Value, Why string }

func (e *MaintainerOnly) Error() string {
	return fmt.Sprintf("%s=%s is maintainer-only (Python builder): %s", e.Key, e.Value, e.Why)
}

// maintainerKeys are the env keys v3_cl04c (or its convert child) reads that the Go pipeline does not port. A
// non-empty value fails (an empty one is Python's "off"); TPL fails on any value.
var maintainerKeys = map[string]string{
	"TPL":      "templates come from the data pack (templates_pack.json)",
	"COMPDUMP": "companion state from a client memory dump (v3_companion_state)",
}

// Get is os.environ[k] as v3_cl04c sees it after RECIPE's setdefault (ok false for a key the recipe does not hold).
func (r *Recipe) Get(k string) (string, bool) {
	if f := r.field(k); f != nil {
		return *f, true
	}
	return "", false
}

// Lookup is Get as an os.LookupEnv-like function (sections.NewState reads CREBASE / AA80B0 / WIREMULTI through it).
func (r *Recipe) Lookup(k string) (string, bool) { return r.Get(k) }

// Set applies one env override k=v (later overrides win, like the child environment). Unknown keys are ignored
// (the Python child ignores env it does not read), except: the maintainer-only keys (TPL, COMPDUMP non-empty),
// V3_WIRE_ENGINE other than "" / 'py' and V3_SOLDIER_ENGINE other than "" / 'py' fail with a *MaintainerOnly error.
func (r *Recipe) Set(k, v string) error {
	if why, ok := maintainerKeys[k]; ok {
		if v != "" || k == "TPL" {
			return &MaintainerOnly{k, v, why}
		}
		return nil
	}
	switch k {
	case "V3_WIRE_ENGINE":
		if v != "" && v != "py" {
			return &MaintainerOnly{k, v, "the Unicorn wire engine needs the game exe"}
		}
		return nil
	case "V3_SOLDIER_ENGINE":
		if v != "" && v != "py" {
			return &MaintainerOnly{k, v, "only the pure soldier_py convert is ported"}
		}
		return nil
	}
	if f := r.field(k); f != nil {
		*f = v
	}
	return nil
}

// Apply is Set for each pair in order (captures.Inputs.Env); the first error stops.
func (r *Recipe) Apply(env [][2]string) error {
	for _, kv := range env {
		if err := r.Set(kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// RecipeFromLookup is the recipe v3_cl04c reads from a process environment: RECIPE's setdefault over lookup (an
// unset key takes the literal default, a set one - even "" - wins), plus the engine / maintainer checks of Set.
func RecipeFromLookup(lookup func(string) (string, bool)) (Recipe, error) {
	r := DefaultRecipe()
	keys := append(RecipeKeys(), "COMPDUMP", "V3_WIRE_ENGINE", "V3_SOLDIER_ENGINE")
	for _, k := range keys {
		if v, ok := lookup(k); ok {
			if err := r.Set(k, v); err != nil {
				return r, err
			}
		}
	}
	return r, nil
}

// on is E(k) != None.
func on(v string) bool { return v != "" }

// hexSet parses 'a,b,...' hex ints ("" = empty) like {int(x, 16) for x in s.split(',') if x}.
func hexSet(s string, skipEmpty bool) (map[uint32]bool, error) {
	out := map[uint32]bool{}
	if s == "" {
		return out, nil
	}
	for _, x := range strings.Split(s, ",") {
		if x == "" && skipEmpty {
			continue
		}
		v, err := parseHex(x)
		if err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, nil
}
