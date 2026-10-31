package capture

import "testing"

// r291: a capture without a C record gets DefaultConfig; one with a C record is left alone.
func TestEnsureConfig(t *testing.T) {
	g := Record{TG, make([]byte, 64)}
	recs, injected := EnsureConfig([]Record{g})
	if !injected || len(recs) != 2 || recs[0].Type != TC || len(recs[0].Body) != 96 || recs[1].Type != TG {
		t.Fatalf("inject: %v %d", injected, len(recs))
	}
	d, err := Decode(recs[:1])
	if err != nil || len(d.Configs) != 1 || len(d.Configs[0].Tables[0]) != 7 || len(d.Configs[0].Tables[1]) != 7 {
		t.Fatalf("default config decode: %v %+v", err, d)
	}
	same, injected := EnsureConfig(recs)
	if injected || len(same) != 2 {
		t.Fatalf("second pass changed the records")
	}
	if _, injected = EnsureConfig(nil); !injected {
		t.Fatalf("empty log: expected an injected config")
	}
}
