package sections

import (
	"os"
	"path/filepath"
	"testing"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// TestVectors replays the synthetic vectors of gen/gen_sections_vectors.py (results and exception messages).
func TestVectors(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "sections", "vectors.jsonl.gz")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	n, bad := 0, 0
	f3 := func(v any) [3]float64 {
		l := v.([]any)
		return [3]float64{l[0].(float64), l[1].(float64), l[2].(float64)}
	}
	i64 := func(v any) int { return int(v.(int64)) }
	err := eachRecord(path, nil, func(o *pyc.Object) error {
		n++
		fn := get(o, "fn").(string)
		a := get(o, "args").(*pyc.Object)
		var res any
		var err error
		switch fn {
		case "rebase_create":
			var nb string
			var info *CreateInfo
			nb, info, err = RebaseCreate(get(a, "bits").(string), f3(get(a, "origin")), get(a, "trunc").(bool))
			if info == nil {
				res = []any{nb, nil}
			} else {
				res = []any{nb, info}
			}
		case "parse", "to_demo_form":
			sh := PhysShape{get(a, "sparse").(bool), i64(get(a, "inner"))}
			b, pos, org := get(a, "bits").(string), i64(get(a, "pos")), f3(get(a, "origin"))
			if fn == "parse" {
				var r *P2
				r, err = ParseP2(b, pos, org, Table, sh)
				res = r
			} else {
				var body string
				var r *P2
				body, r, err = ToDemoForm(b, pos, org, Table, sh)
				res = []any{body, r}
			}
		case "section":
			var tpl *fbgd.TplSection
			if l, ok := get(a, "tpl").([]any); ok {
				tpl = &fbgd.TplSection{}
				for _, w := range l {
					v, _ := bits.FromUint(0, i64(w))
					tpl.Defaults = append(tpl.Defaults, fbgd.OutChunk{Value: v})
				}
			}
			lay := 0
			if v, ok := get(a, "layout").(int64); ok {
				lay = int(v)
			}
			res, err = Section(get(a, "bits").(string), i64(get(a, "cur")), uint32(get(a, "vt").(int64)),
				i64(get(a, "count")), f3(get(a, "origin")), Table, i64(get(a, "cfg")), get(a, "rebase").(bool), tpl, lay)
		case "audit_rebase":
			var ovs []AuOvec
			for _, x := range get(a, "ovs").([]any) {
				l := x.([]any)
				ovs = append(ovs, AuOvec{i64(l[0]), i64(l[1]), f3(l[2])})
			}
			var s string
			var ok bool
			s, ok, err = Rebase(get(a, "body").(string), ovs, Table)
			if ok {
				res = s
			}
		case "chunks_of":
			var pos []uint32
			for _, x := range get(a, "pos").([]any) {
				pos = append(pos, uint32(x.(int64)))
			}
			var refs []wire.Ref
			for _, x := range get(a, "refs").([]any) {
				r := x.(*pyc.Object)
				refs = append(refs, wire.Ref{Start: uint32(i64(get(r, "start"))), End: uint32(i64(get(r, "end")))})
			}
			var ovs []wire.Ovec
			for _, x := range get(a, "ovs").([]any) {
				v := x.(*pyc.Object)
				ovs = append(ovs, wire.Ovec{Start: uint32(i64(get(v, "start"))), End: uint32(i64(get(v, "end"))), XYZ: f3(get(v, "xyz"))})
			}
			var cs []AuChunk
			cs, err = ChunksOf(get(a, "bits").(string), pos, uint32(i64(get(a, "end"))), refs, ovs)
			l := make([]any, len(cs))
			for i := range cs {
				l[i] = &cs[i]
			}
			res = l
		default:
			t.Fatalf("unknown fn %s", fn)
		}
		var got, want string
		if exc, ok := get(o, "exc").(string); ok {
			want = "exc " + exc
		} else {
			want = line(get(o, "result"))
		}
		if err != nil {
			got = "exc " + excOf(err)
		} else {
			got = line(res)
		}
		if got != want {
			bad++
			if bad <= 10 {
				t.Errorf("%s %s:\n go %.400s\n py %.400s", fn, line(a), got, want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d vectors, %d differ", n, bad)
}

// TestRebaseCreatePairs is v3_create_rebase.selftest: client creates and their server-demo counterparts (public
// source vectors, trial 06 / 08 objects).
func TestRebaseCreatePairs(t *testing.T) {
	pairs := []struct {
		name, c string
		o       [3]float64
		demo    string
		posOnly bool
	}{
		{"proj c151->d150", "0001101000100111010010000000001000110000000000010011000000000111111110101101010111111100001001001", [3]float64{-105, 65, 139},
			"0001101000100111010010000000001000110000000000010011000000000111111111011100101011010100100000011011010001100001001", false},
		{"proj c152->d152", "0001101000100111010010000000001100110000000000010110000000000111100110101101001011111101001000101", [3]float64{-105, 65, 139},
			"0001101000100111010010000000001100110000000000010110000000000111100111011100101011001000100000011100010001100000101", false},
		{"veh29 c136->d138", "00001110001001010111001110001111001110011000100010111011110000000000000001100001100110110010110010010110100000000000000000011111110100010000000000000000000000000", [3]float64{246, 81, 416},
			"00001110001001010111100001110011111011000010001000000100110110111011110000000000000001100001100110110010110010010110100000000000000000011111110100010000000000000000000000000", false},
		{"veh29 c150->d149 (pos only)", "0000111000100101011100000110011000111101010011010111101101001001111111111001111111001001010000000000000000000000000", [3]float64{-105, 65, 139},
			"00001110001001010111011101000111100100011101110010010111010011010100000000001100111111100000111111111100111101110011000000000110111111101110010000000000000000000000000", true},
		{"spawner c105 (3-comp quat)", "01111110010001010110100000001101001100111111011011101000011100001100010001101100110100101000011", [3]float64{-366, 187, -413},
			"01111110010001010110100000001101001100111111011110110100001100011100001000101011000001101011110010111010101000011", false},
		{"spawner c123 (1-comp quat)", "0111111000010111101001101001100100010101010110101000000001111100111111101010001010101", [3]float64{-477, 159, -23},
			"0111111000010111101001101001100110110010011011011010101000101000001111001111111110111100001010101", false},
	}
	for _, p := range pairs {
		nb, info, err := RebaseCreate(p.c, p.o, true)
		if err != nil || info == nil {
			t.Fatalf("%s: %v", p.name, err)
		}
		eq := nb == p.demo
		if p.posOnly {
			e1, _, _ := readVec(nb, 19, 5)
			e2, _, _ := readVec(p.demo, 19, 5)
			eq = nb[:e1] == p.demo[:e2]
		}
		if !eq {
			t.Errorf("%s: rebased create != demo", p.name)
		}
		if z, _, err := RebaseCreate(p.demo, [3]float64{}, true); err != nil || z != p.demo {
			t.Errorf("%s: origin-0 re-encode of the demo create is not the identity", p.name)
		}
	}
}
