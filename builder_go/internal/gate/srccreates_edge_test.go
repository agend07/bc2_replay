package gate

import "testing"

// r295: the unpaired type-31 classification tolerates positions up to edgeBand outside the SRC box (a base the
// empty-server SRC round never spawned), while un-rebased rows (>= 60 m off, r207) stay implausible.
func TestExcessEdgeBand(t *testing.T) {
	box := &[2][3]float64{{-10, 100, -10}, {10, 200, 10}}
	in := &pos{xyz: [][3]float64{{0, 150, 0}}}
	edge := &pos{xyz: [][3]float64{{0, 88, 0}}} // 12 m below the box
	far := &pos{xyz: [][3]float64{{0, 30, 0}}}  // 70 m below the box
	for _, c := range []struct {
		q    *pos
		want float64
	}{{in, 0}, {edge, 12}, {far, 70}} {
		ex, ok := excess(c.q, box)
		if !ok || ex != c.want {
			t.Fatalf("excess(%v) = %v,%v want %v,true", c.q.xyz, ex, ok, c.want)
		}
	}
	if _, ok := excess(nil, box); ok {
		t.Fatal("nil position must not be plausible")
	}
	if ex, _ := excess(edge, box); !(ex > 0 && ex <= edgeBand) {
		t.Fatalf("edge case %v outside the band %v", ex, edgeBand)
	}
	if ex, _ := excess(far, box); ex <= edgeBand {
		t.Fatalf("far case %v inside the band %v", ex, edgeBand)
	}
}
