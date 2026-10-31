package pipeline

import (
	"strings"
	"testing"

	"bc2replay/builder/internal/bits"
	"bc2replay/builder/internal/fbgd"
)

func plRow(id int, wid uint64, t float64) *fbgd.Row {
	v, _ := bits.Parse("0000010" + bits.FormatBin(wid, 8) + strings.Repeat("0", 10))
	return &fbgd.Row{K: 'c', T: t, ID: id, Type: fbgd.TypeKey(int64(playerVT)), Create: fbgd.OutChunk{Value: v}}
}

func compRow(id int, t float64, refs ...int) *fbgd.Row {
	r := &fbgd.Row{K: 'c', T: t, ID: id, Type: fbgd.TypeKey(int64(compVT))}
	for _, x := range refs {
		r.Create.Refs = append(r.Create.Refs, fbgd.Ref{Target: x})
	}
	return r
}

func TestComponentOwner(t *testing.T) {
	a, b2, c := plRow(120, 1, 10), plRow(121, 8, 20), plRow(122, 3, 30)
	pl := []*fbgd.Row{a, b2, c}
	other := &fbgd.Row{K: 'c', T: 1, ID: 200, Type: fbgd.TypeKey(int64(playerVT + 1)), Create: fbgd.OutChunk{Refs: []fbgd.Ref{{Target: 120}}}}
	// later component refs c; earlier one refs a non-player then b2 -> b2
	rows := []*fbgd.Row{a, b2, c, other, compRow(300, 50, 122), compRow(301, 40, 999, 121)}
	if got := componentOwner(rows, pl); got != b2 {
		t.Fatalf("componentOwner = %v, want id 121", got)
	}
	// a component create without a player ref is skipped
	rows = []*fbgd.Row{a, b2, compRow(300, 5, 999), compRow(301, 60, 120)}
	if got := componentOwner(rows, pl); got != a {
		t.Fatalf("componentOwner = %v, want id 120", got)
	}
	// fallback: no component create, or none refs a player
	if got := componentOwner([]*fbgd.Row{a, b2, other}, pl); got != nil {
		t.Fatalf("componentOwner = %v, want nil", got)
	}
	if got := componentOwner([]*fbgd.Row{a, compRow(300, 5, 999)}, pl); got != nil {
		t.Fatalf("componentOwner = %v, want nil", got)
	}
}

func TestSwapPlayerID(t *testing.T) {
	vp, o, z := plRow(121, 8, 20), plRow(120, 1, 10), plRow(122, 3, 30)
	pl := []*fbgd.Row{o, vp, z}
	p7 := func(id int, v uint64) *fbgd.Row {
		b, _ := bits.FromUint(v, 8)
		return &fbgd.Row{K: 'u', T: 99, ID: id, Chunks: []fbgd.RowChunk{{Key: fbgd.ChunkKey{Prop: 7}, Value: b}}}
	}
	u1, u2, u3 := p7(120, 1), p7(120, 5), p7(122, 1)
	rows := []*fbgd.Row{o, vp, z, u1, u2, u3}
	vs := &fbgd.Counter{}
	if err := swapPlayerID(rows, pl, vp, 8, 1, vs); err != nil {
		t.Fatal(err)
	}
	wid := func(r *fbgd.Row) int {
		n, _ := int2(pySlice(r.Create.Value.String(), 7, 15))
		return int(n)
	}
	if wid(o) != 8 || wid(vp) != 8 || wid(z) != 3 { // vp's own field is renumbered by viewerID, not here
		t.Fatalf("create ids = %d %d %d", wid(o), wid(vp), wid(z))
	}
	val := func(r *fbgd.Row) int {
		n, _ := int2(r.Chunks[0].Value.String())
		return int(n)
	}
	if val(u1) != 8 || val(u2) != 5 || val(u3) != 1 {
		t.Fatalf("p7 = %d %d %d", val(u1), val(u2), val(u3))
	}
	if vs.Get("swap_create") != 1 || vs.Get("swap_p7") != 1 {
		t.Fatalf("stat = %v", vs)
	}
}
