package captures

import (
	"errors"
	"io/fs"
	"os"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/pyc"
)

// ---------- r287: capture rounds ----------
//
// One capture = one server connection; consecutive rounds on it (level reloaded on the same connection) are rounds
// k = 1.. of capture.Segments(level cuts of raw-log.bin). list shows them, build --round K builds one (V3_ROUND=K
// in the pipeline env), the default is the round with the most packets (ties -> later).
//
// Caches (v3_types.cache_name): a multi-round capture keys them by round, <stem>_r<K>.<ext> (the soldier rows stamp
// soldier_rows.go_r<K>.json), never reused across rounds; a single-round capture keeps the old names. The build
// driver sets V3_ROUND=K in its own process env for the whole build (pipeline runs).

const roundEnv = capture.RoundEnv

// capRounds is v3_types.rounds of a capture dir (packets of the open round from m-summary.json "seq"); error for an
// unreadable raw-log.bin.
func capRounds(cap string) ([]capture.Round, error) {
	rl, err := os.ReadFile(join(cap, "raw-log.bin"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		rl = nil
	}
	var seq *int64
	if f, ok := num(get(readJSON(join(cap, "m-summary.json")), "seq")); ok {
		n := int64(f)
		seq = &n
	}
	return capture.Rounds(rl, seq)
}

// roundsOf is v3_captures.rounds_of: capRounds, nil without m-log.bin or on a bad raw-log.bin (list never breaks).
func roundsOf(cap string) []capture.Round {
	if !isFile(join(cap, "m-log.bin")) {
		return nil
	}
	rs, err := capRounds(cap)
	if err != nil {
		return nil
	}
	return rs
}

// buildRound is v3_captures.build_round: a builds.json entry's round, the default for an entry before r287.
func buildRound(b any, def int) int {
	if f, ok := num(get(b, "round")); ok {
		return int(f)
	}
	return def
}

// nonEmpty is the number of rounds with packets.
func nonEmpty(rs []capture.Round) int {
	n := 0
	for _, r := range rs {
		if r.Packets > 0 {
			n++
		}
	}
	return n
}

// roundsJSON is the list item's "rounds": [{round, lo, hi (null = open end), packets, seconds, level}].
func roundsJSON(rs []capture.Round, capLevel any) []any {
	out := make([]any, 0, len(rs))
	for i, r := range rs {
		o := pyc.NewObject()
		o.Set("round", int64(i+1))
		o.Set("lo", int64(r.Lo))
		if r.Open {
			o.Set("hi", nil)
		} else {
			o.Set("hi", int64(r.Hi))
		}
		if r.HasPackets {
			sec, _ := pyc.RoundFloat(float64(r.Packets)/fps, 1)
			o.Set("packets", r.Packets)
			o.Set("seconds", sec)
		} else {
			o.Set("packets", nil)
			o.Set("seconds", nil)
		}
		if r.Level != "" {
			o.Set("level", r.Level)
		} else {
			o.Set("level", capLevel)
		}
		out = append(out, o)
	}
	return out
}
