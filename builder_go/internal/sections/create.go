package sections

import (
	"fmt"
	"strconv"
	"strings"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/wire"
)

// Recipe defaults of the keys this package reads (v3_cl04c RECIPE).
const (
	DefaultCrebase   = "0x143afd0,0x1436cbc,0x143b3bc"
	DefaultAA80B0    = "1"
	DefaultWireMulti = "0x14399c8,0x1436cbc/7,0x1432098/9,0x1436cbc/29,0x1436cbc/37,0x1436cbc/10,0x1436cbc/15,0x1436cbc/4," +
		"0x1436cbc/8,0x1436cbc/14,0x1436cbc/35,0x1436cbc/40,0x1436cbc/54,0x1436cbc/68,0x1436cbc/76,0x1436cbc/13," +
		"0x1436cbc/45,0x1436cbc/47,0x1436cbc/5,0x1436cbc/61,0x1436cbc/80" // r295: + /5,/61,/80 (MP_007 layouts)
)

// Cut is v3_cl04c.CUT: create widths of the legacy cut classes (r106/r128/r197; the last kept bit is the 32-bit
// tail flag). The camera is rebased, never truncated.
var Cut = map[uint32]int{0x143aa80: 75, 0x143ac38: 184, 0x1439e3c: 184, 0x143a794: 75, 0x143ade4: 75}

// Site is one observed call of a ported function (the golden wire_sites kinds camera_init, create_rebase,
// run_section, chunks_of, audit888_rebase, vehsec): Args are the call's Go arguments in Python order, Result its
// return (a pyc.Valuer or plain value), Err the exception.
type Site struct {
	Kind   string
	Args   []any
	Result any
	Err    error
}

// TplRef names the template section a vehsec call reads (the 'tpl' argument): template key and section index.
type TplRef struct {
	Key string
	Sec int
}

// State is the cl04c module state the create path and WIREMULTI share, made explicit: recipe switches, the
// 'create rebase' counter (_crs) and the payload bit strings. Trace, when set, sees every Site. A State
// belongs to one build (one *wire.Log): its bit-string cache is keyed by packet seq.
type State struct {
	Crebase   map[uint32]bool // CREBASE vts ('' = none)
	AA80B0    bool            // r128: clear the last bit of every CUT-type create
	WireMulti []MultiKey      // WIREMULTI entries
	// SkipVariants (r300c triage, env WIREMULTI_SKIPV=v419,v377): variant suffixes whose creates are dropped.
	SkipVariants map[string]bool
	Crs          *fbgd.Counter // 'create rebase' stats: '%x:changed|same'
	Trace        func(Site)
	// Item, when set, is called by WireMulti before each item i of its (t, pseq, entry_cursor)-sorted multi list
	// and once after the loop with i = -1 and g = nil (maintainer checkpoints: per-item rows / stat increments).
	Item func(i int, g *wire.Ghost, res *MultiResult)

	bitsOf map[int64]string
}

// MultiKey is one WIREMULTI entry 'vt' (NSec 0 = any section count) or 'vt/nsec'.
type MultiKey struct {
	VT   uint32
	NSec int
}

// parseHex is int(x, 16) (Python accepts an optional 0x prefix and surrounding whitespace).
func parseHex(x string) (uint32, error) {
	s := strings.TrimSpace(x)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	v, err := strconv.ParseUint(strings.ReplaceAll(s, "_", ""), 16, 32)
	if err != nil || s == "" {
		return 0, fmt.Errorf("ValueError: invalid literal for int() with base 16: %q", x)
	}
	return uint32(v), nil
}

// NewState reads CREBASE, AA80B0 and WIREMULTI through lookup (os.LookupEnv-like; an absent key takes the RECIPE
// default, an empty value disables it like cl04c's E()).
func NewState(lookup func(string) (string, bool)) (*State, error) {
	get := func(k, def string) string {
		if v, ok := lookup(k); ok {
			return v
		}
		return def
	}
	st := &State{Crebase: map[uint32]bool{}, Crs: &fbgd.Counter{}, SkipVariants: map[string]bool{}}
	for _, v := range strings.Split(get("WIREMULTI_SKIPV", ""), ",") {
		if v = strings.TrimSpace(v); v != "" {
			st.SkipVariants["/"+strings.TrimPrefix(v, "/")] = true
		}
	}
	if v := get("CREBASE", DefaultCrebase); v != "" {
		for _, x := range strings.Split(v, ",") {
			vt, err := parseHex(x)
			if err != nil {
				return nil, err
			}
			st.Crebase[vt] = true
		}
	}
	st.AA80B0 = get("AA80B0", DefaultAA80B0) != ""
	if v := get("WIREMULTI", DefaultWireMulti); v != "" {
		for _, x := range strings.Split(v, ",") {
			a, n, _ := strings.Cut(x, "/")
			vt, err := parseHex(a)
			if err != nil {
				return nil, err
			}
			k := MultiKey{VT: vt}
			if n != "" {
				if k.NSec, err = strconv.Atoi(strings.TrimSpace(n)); err != nil {
					return nil, fmt.Errorf("ValueError: invalid literal for int() with base 10: %q", n)
				}
			}
			st.WireMulti = append(st.WireMulti, k)
		}
	}
	return st, nil
}

func (st *State) trace(kind string, res any, err error, args ...any) {
	if st.Trace != nil {
		st.Trace(Site{kind, args, res, err})
	}
}

// payloadBits is payload_bits(packet payload) as a '0'/'1' string (cached per pseq).
func (st *State) payloadBits(d *wire.Log, g *wire.Ghost) (string, error) {
	if s, ok := st.bitsOf[g.Pseq]; ok {
		return s, nil
	}
	p := d.Packets[g.Pseq]
	if p == nil {
		return "", &PyError{"KeyError", strconv.FormatInt(g.Pseq, 10)}
	}
	if st.bitsOf == nil {
		st.bitsOf = map[int64]string{}
	}
	s := p.Bits().String()
	st.bitsOf[g.Pseq] = s
	return s, nil
}

// RebaseCreate is cl04c's crebase(vt, cb, g) (r159): a CREBASE class create's origin-relative vec3s re-encoded
// origin-zero (no-op for other vts); counts '%x:changed|same' in Crs.
func (st *State) RebaseCreate(vt uint32, cb string, g *wire.Ghost) (string, error) {
	if !st.Crebase[vt] {
		return cb, nil
	}
	origin := OriginOf(g)
	nb, info, err := RebaseCreate(cb, origin, true)
	if st.Trace != nil {
		var res any
		if err == nil {
			res = []any{nb, info}
			if info == nil {
				res = []any{nb, nil}
			}
		}
		st.trace("create_rebase", res, err, cb, origin, true)
	}
	if err != nil {
		return "", err
	}
	what := "same"
	if nb != cb {
		what = "changed"
	}
	st.Crs.Add(fmt.Sprintf("%x:%s", vt, what), 1)
	return nb, nil
}

// chunkOf is dict(index=32, bits=len(b), value=int(b, 2), refs=[]) (int of an empty string raises, like Python).
func chunkOf(b string) (fbgd.OutChunk, error) {
	if b == "" {
		return fbgd.OutChunk{}, &PyError{"ValueError", "invalid literal for int() with base 2: ''"}
	}
	v, err := bits.Parse(b)
	if err != nil {
		return fbgd.OutChunk{}, err
	}
	return fbgd.OutChunk{Index: 32, Value: v, Refs: []fbgd.Ref{}}, nil
}

// Create is cl04c's patched create_chunk cc(d, g), the create build_rows calls (fbgd.BuildRows' create argument):
// camera creates 0x1431c0c -> the origin-zero camera init (wire.CameraInit, ids widths inferred per call); else the
// plain create slice, CREBASE classes re-encoded origin-zero, CUT classes truncated to their width, and with AA80B0
// the last bit of every CUT-class create cleared.
func (st *State) Create(d *wire.Log, g *wire.Ghost) (fbgd.OutChunk, error) {
	vt := g.SectionVT
	if vt == wire.CameraVT {
		r, err := wire.CameraInit(d, g, nil)
		st.trace("camera_init", r, err, g, nil)
		if err != nil {
			return fbgd.OutChunk{}, err
		}
		return fbgd.OutChunk{Index: 32, Value: r.Value, Refs: []fbgd.Ref{}}, nil
	}
	c, err := fbgd.CreateChunk(d, g)
	if err != nil {
		return c, err
	}
	if st.Crebase[vt] {
		nb, err := st.RebaseCreate(vt, c.Value.String(), g)
		if err != nil {
			return c, err
		}
		if c, err = chunkOf(nb); err != nil {
			return c, err
		}
	}
	w, cut := Cut[vt]
	if cut && w < c.Value.Len() {
		c.Value = c.Value.Slice(0, w) // the first w bits
	}
	if cut && st.AA80B0 && c.Value.Len() > 0 {
		c.Value = bits.Concat(c.Value.Slice(0, c.Value.Len()-1), zeroBit) // value & ~1
	}
	return c, nil
}

var zeroBit, _ = bits.FromUint(0, 1)
