package fbgd_test

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/wire"
)

// Vectors from gen/gen_fbgd_write_vectors.py (the Python modules on synthetic, name-free input; format in its doc).

type vChunk struct {
	Index *int     `json:"index"`
	Bits  string   `json:"bits"`
	Refs  [][2]int `json:"refs"`
}

func (c vChunk) out(t *testing.T, idx int) fbgd.OutChunk {
	t.Helper()
	v, err := bits.Parse(c.Bits)
	if err != nil {
		t.Fatal(err)
	}
	if c.Index != nil {
		idx = *c.Index
	}
	refs := make([]fbgd.Ref, len(c.Refs))
	for i, r := range c.Refs {
		refs[i] = fbgd.Ref{Target: r[0], Flag: r[1]}
	}
	return fbgd.OutChunk{Index: idx, Value: v, Refs: refs}
}

func chunkIn(c fbgd.OutChunk, index bool) vChunk {
	v := vChunk{Bits: c.Value.String(), Refs: [][2]int{}}
	for _, r := range c.Refs {
		v.Refs = append(v.Refs, [2]int{r.Target, r.Flag})
	}
	if index {
		v.Index = &c.Index
	}
	return v
}

func jsonEq(t *testing.T, a, b any) bool {
	t.Helper()
	norm := func(v any) string { // through any: object keys sorted, struct field order irrelevant
		x, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var u any
		if err := json.Unmarshal(x, &u); err != nil {
			t.Fatal(err)
		}
		y, _ := json.Marshal(u)
		return string(y)
	}
	return norm(a) == norm(b)
}

func vTime(t *testing.T, s string) float64 {
	t.Helper()
	if strings.Contains(s, "0x") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return float64(i)
}

func vType(t *testing.T, m json.RawMessage) fbgd.TypeKey {
	t.Helper()
	var s string
	if json.Unmarshal(m, &s) == nil {
		return s
	}
	var i int64
	if err := json.Unmarshal(m, &i); err != nil {
		t.Fatal(err)
	}
	return i
}

type vRow struct {
	K      string               `json:"k"`
	T      string               `json:"t"`
	ID     int                  `json:"id"`
	Type   json.RawMessage      `json:"type"`
	Create *vChunk              `json:"create"`
	Chunks [][2]json.RawMessage `json:"chunks"`
}

func (r vRow) row(t *testing.T) *fbgd.Row {
	t.Helper()
	out := &fbgd.Row{K: r.K[0], T: vTime(t, r.T), ID: r.ID}
	if r.K == "c" {
		out.Type = vType(t, r.Type)
		out.Create = r.Create.out(t, 0)
	}
	for _, kc := range r.Chunks {
		var key fbgd.ChunkKey
		var p int
		var sp [2]int
		tuple := false
		if json.Unmarshal(kc[0], &p) == nil {
			key = fbgd.ChunkKey{Sec: 0, Prop: p}
		} else if err := json.Unmarshal(kc[0], &sp); err == nil {
			key, tuple = fbgd.ChunkKey{Sec: sp[0], Prop: sp[1]}, true
		} else {
			t.Fatal(err)
		}
		var c vChunk
		if err := json.Unmarshal(kc[1], &c); err != nil {
			t.Fatal(err)
		}
		oc := c.out(t, key.Prop)
		out.Chunks = append(out.Chunks, fbgd.RowChunk{Key: key, Tuple: tuple, Value: oc.Value, Refs: oc.Refs})
	}
	return out
}

func rowIn(r *fbgd.Row) string {
	s := fmt.Sprintf("%c %v %d", r.K, r.T, r.ID)
	for _, c := range r.Chunks {
		s += fmt.Sprintf(" %v:%s:%v", c.Key, c.Value.String(), c.Refs)
	}
	return s
}

type vSection struct {
	Header   int64    `json:"header"`
	Count    int      `json:"count"`
	Mask     uint64   `json:"mask"`
	Defaults []vChunk `json:"defaults"`
	Chunks   []vChunk `json:"chunks"`
}

func (s vSection) mask(t *testing.T) bits.Bits {
	m, err := bits.FromUint(s.Mask, s.Count)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

type vRec struct {
	Error  *string  `json:"error"`
	Flags  uint32   `json:"flags"`
	End    uint32   `json:"end"`
	Ovs    [][2]int `json:"origin_vectors"`
	Chunks []struct {
		Property   int    `json:"property"`
		Bits       string `json:"bits"`
		BodyStart  int64  `json:"body_start"`
		References []int  `json:"references"`
	} `json:"chunks"`
}

func (r vRec) rec(t *testing.T) *wire.SliceResult {
	out := &wire.SliceResult{Flags: r.Flags, End: r.End}
	if r.Error != nil {
		out.Error = *r.Error
	}
	for _, o := range r.Ovs {
		out.OriginVectors = append(out.OriginVectors, wire.Ovec{Start: uint32(o[0]), End: uint32(o[1])})
	}
	for _, c := range r.Chunks {
		v, err := bits.Parse(c.Bits)
		if err != nil {
			t.Fatal(err)
		}
		wc := wire.Chunk{Property: c.Property, Value: v, BodyStart: c.BodyStart}
		for _, s := range c.References {
			wc.References = append(wc.References, wire.Ref{Start: uint32(s)})
		}
		out.Chunks = append(out.Chunks, wc)
	}
	return out
}

type vectors struct {
	Assemble []struct {
		Name    string
		T0      string
		KeySnap bool
		Tpl     [][2]json.RawMessage
		Rows    []vRow
		FBGD    string
		FBGK    string
		Info    *struct {
			Frames, Keyframes int
			TplNewBad         [][2]int `json:"tplnew_bad"`
		}
		Error string
	}
	Delta []struct {
		Counts [][2]json.RawMessage
		D      struct {
			DT, Sim uint64
			Deleted []int
			Dirty   [][3]json.RawMessage
		}
		Payload     string
		NBits       int
		CountsAfter [][2]json.RawMessage `json:"counts_after"`
		Error       string
	}
	DelNull []struct {
		Rows    []vRow
		Types   [][2]json.RawMessage
		Soldier int64
		OutRows []vRow `json:"out_rows"`
		Hits    [][4]json.RawMessage
		Stat    [][2]json.RawMessage
	}
	Refmap []struct {
		Fn        string
		Chunk     vChunk
		Map       [][2]int
		Positions []int
		Result    json.RawMessage
		Error     string
	}
	Property []struct {
		Rec          vRec
		VT           uint32
		Seed         int
		WidenP12     bool `json:"widen_p12"`
		CameraP5Flag bool `json:"camera_p5_flag"`
		Result       [][2]json.RawMessage
		Error        string
	}
	Initial []struct {
		Rec    vRec
		Result *[]int
	}
}

func loadVectors(t *testing.T) *vectors {
	f, err := os.Open("../../testdata/fbgd_write/vectors.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.NewDecoder(gz).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return &v
}

func un[T any](t *testing.T, m json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(m, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func pyErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, ": "); i >= 0 {
		return s[i+2:]
	}
	return s
}

func TestAssembleVectors(t *testing.T) {
	v := loadVectors(t)
	bad := 0
	for _, c := range v.Assemble {
		tpl := fbgd.Templates{}
		for _, kv := range c.Tpl {
			var tt struct{ Sections []vSection }
			if err := json.Unmarshal(kv[1], &tt); err != nil {
				t.Fatal(err)
			}
			tp := &fbgd.Template{}
			for _, s := range tt.Sections {
				ts := fbgd.TplSection{Header: uint32(s.Header), Mask: s.mask(t)}
				for j, d := range s.Defaults {
					ts.Defaults = append(ts.Defaults, d.out(t, j))
				}
				tp.Sections = append(tp.Sections, ts)
			}
			tpl[vType(t, kv[0])] = tp
		}
		var rows []*fbgd.Row
		for _, r := range c.Rows {
			rows = append(rows, r.row(t))
		}
		data, k, info, err := fbgd.Assemble(rows, tpl, vTime(t, c.T0), c.KeySnap)
		if c.Error != "" || err != nil {
			t.Errorf("%s: go err %v, py %q", c.Name, err, c.Error)
			continue
		}
		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != c.FBGD || hex.EncodeToString(k) != c.FBGK {
			t.Errorf("%s (keysnap %v): fbgd/fbgk differ", c.Name, c.KeySnap)
		}
		if info.Frames != c.Info.Frames || info.Keyframes != c.Info.Keyframes || !slices.Equal(info.TplNewBad, c.Info.TplNewBad) {
			t.Errorf("%s: info go %+v py %+v", c.Name, info, *c.Info)
		}
		bad += len(info.TplNewBad)
	}
	t.Logf("%d cases, %d tplnew_bad", len(v.Assemble), bad)
}

func TestEncodeDeltaVectors(t *testing.T) {
	v := loadVectors(t)
	for i, c := range v.Delta {
		counts := map[int][]int{}
		for _, kv := range c.Counts {
			counts[un[int](t, kv[0])] = un[[]int](t, kv[1])
		}
		d := &fbgd.OutDelta{DT: c.D.DT, Sim: c.D.Sim, Deleted: c.D.Deleted}
		for _, x := range c.D.Dirty {
			e := fbgd.OutDirty{ID: un[int](t, x[0])}
			if un[string](t, x[1]) == "new" {
				var b struct {
					Create   vChunk
					Sections []vSection
				}
				b = un[struct {
					Create   vChunk
					Sections []vSection
				}](t, x[2])
				g := &fbgd.OutGhost{ID: e.ID, Create: b.Create.out(t, 0)}
				for _, s := range b.Sections {
					os := fbgd.OutSection{Header: uint32(s.Header), Mask: s.mask(t)}
					for _, ch := range s.Chunks {
						os.Chunks = append(os.Chunks, ch.out(t, 0))
					}
					g.Sections = append(g.Sections, os)
				}
				e.New = g
			} else {
				for _, ch := range un[[][3]json.RawMessage](t, x[2]) {
					e.Changes = append(e.Changes, fbgd.OutChange{Sec: un[int](t, ch[0]), Prop: un[int](t, ch[1]),
						Chunk: un[vChunk](t, ch[2]).out(t, 0)})
				}
			}
			d.Dirty = append(d.Dirty, e)
		}
		pl, nb, err := fbgd.EncodeDelta(d, counts)
		if err != nil || c.Error != "" {
			t.Errorf("case %d: go %v py %q", i, err, c.Error)
			continue
		}
		want := map[int][]int{}
		for _, kv := range c.CountsAfter {
			want[un[int](t, kv[0])] = un[[]int](t, kv[1])
		}
		if hex.EncodeToString(pl) != c.Payload || nb != c.NBits || !jsonEq(t, counts, want) {
			t.Errorf("case %d: payload/nbits/counts differ", i)
		}
	}
}

func TestDelNullVectors(t *testing.T) {
	v := loadVectors(t)
	hits := 0
	for i, c := range v.DelNull {
		var rows []*fbgd.Row
		for _, r := range c.Rows {
			rows = append(rows, r.row(t))
		}
		types := map[int]fbgd.TypeKey{}
		for _, kv := range c.Types {
			types[un[int](t, kv[0])] = vType(t, kv[1])
		}
		stat := &fbgd.Counter{}
		n0 := len(rows)
		out, hs, err := fbgd.DelNull(rows, types, c.Soldier, stat, nil, fbgd.DTTicks)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		var got, want []string
		for _, r := range out[n0:] {
			got = append(got, rowIn(r))
		}
		for _, r := range c.OutRows {
			want = append(want, rowIn(r.row(t)))
		}
		if !slices.Equal(got, want) {
			t.Errorf("case %d: rows\n go %v\n py %v", i, got, want)
		}
		var gh, wh []string
		for _, h := range hs {
			gh = append(gh, fmt.Sprint(h.T, h.Holder, h.Prop, h.Deleted))
		}
		for _, h := range c.Hits {
			wh = append(wh, fmt.Sprint(vTime(t, un[string](t, h[0])), un[int](t, h[1]), un[int](t, h[2]), un[int](t, h[3])))
		}
		if !slices.Equal(gh, wh) {
			t.Errorf("case %d: hits go %v py %v", i, gh, wh)
		}
		var ws, gs []string
		for _, kv := range c.Stat {
			ws = append(ws, fmt.Sprint(un[string](t, kv[0]), un[int](t, kv[1])))
		}
		for _, k := range stat.Keys() {
			gs = append(gs, fmt.Sprint(k, stat.Get(k)))
		}
		if !slices.Equal(gs, ws) {
			t.Errorf("case %d: stat go %v py %v", i, gs, ws)
		}
		hits += len(hs)
	}
	t.Logf("%d cases, %d hits", len(v.DelNull), hits)
}

func TestRefmapVectors(t *testing.T) {
	v := loadVectors(t)
	errs := 0
	for i, c := range v.Refmap {
		mp := map[int]int{}
		for _, kv := range c.Map {
			mp[kv[0]] = kv[1]
		}
		ch := c.Chunk.out(t, 0)
		var res any
		var err error
		switch c.Fn {
		case "positions_from_table":
			res, err = fbgd.PositionsFromTable(&ch)
		case "create_positions":
			p := fbgd.CreatePositions(&ch)
			if p == nil {
				p = []int{}
			}
			res = p
		case "remap_server_chunk":
			var o fbgd.OutChunk
			o, err = fbgd.RemapServerChunk(ch, func(x int) int { return mp[x] })
			res = chunkIn(o, true)
		case "remap":
			var o fbgd.OutChunk
			o, err = fbgd.Remap(ch, c.Positions, func(x int) int { return mp[x] % 8192 })
			res = chunkIn(o, true)
		}
		if c.Error != "" || err != nil {
			errs++
			if err == nil || pyErr(err) != c.Error {
				t.Errorf("case %d %s: go %v py %q", i, c.Fn, err, c.Error)
			}
			continue
		}
		var want any
		if err := json.Unmarshal(c.Result, &want); err != nil {
			t.Fatal(err)
		}
		if !jsonEq(t, res, want) {
			g, _ := json.Marshal(res)
			t.Errorf("case %d %s: go %s py %s", i, c.Fn, g, c.Result)
		}
	}
	t.Logf("%d cases, %d errors", len(v.Refmap), errs)
}

func TestPropertyChunksVectors(t *testing.T) {
	v := loadVectors(t)
	errs := 0
	for i, c := range v.Property {
		s := c.Seed
		fmap := func(o int) int {
			if (o^s)%3 == 0 {
				return 0
			}
			return (o*7+s)%8191 + 1
		}
		res, err := fbgd.PropertyChunks(c.Rec.rec(t), c.VT, fmap, fbgd.Conversion{WidenP12: c.WidenP12, CameraP5Flag: c.CameraP5Flag})
		if c.Error != "" || err != nil {
			errs++
			if err == nil || pyErr(err) != c.Error {
				t.Errorf("case %d: go %v py %q", i, err, c.Error)
			}
			continue
		}
		var got [][2]any
		for _, rc := range res {
			got = append(got, [2]any{rc.Key.Prop, chunkIn(fbgd.OutChunk{Value: rc.Value, Refs: rc.Refs}, false)})
		}
		var want [][2]any
		for _, kv := range c.Result {
			want = append(want, [2]any{un[int](t, kv[0]), un[vChunk](t, kv[1])})
		}
		if !jsonEq(t, got, want) {
			t.Errorf("case %d: go %v py %v", i, got, want)
		}
	}
	t.Logf("%d cases, %d errors", len(v.Property), errs)
}

func TestInitialPropertyStateVectors(t *testing.T) {
	v := loadVectors(t)
	for i, c := range v.Initial {
		r := fbgd.InitialPropertyState(c.Rec.rec(t))
		if (r == nil) != (c.Result == nil) {
			t.Errorf("case %d: go nil %v py nil %v", i, r == nil, c.Result == nil)
			continue
		}
		if r == nil {
			continue
		}
		var props []int
		for _, ch := range r.Chunks {
			props = append(props, ch.Property)
		}
		if !slices.Equal(props, *c.Result) {
			t.Errorf("case %d: go %v py %v", i, props, *c.Result)
		}
	}
}
