package events

// Port of v3_wire_deletes (r140/r172/r197): ghost deletions read from the logged client packets.

import (
	"fmt"
	"slices"

	"bc2replay/builder/internal/capture"
	"bc2replay/builder/internal/fbgd"
)

// DeleteWidth is deletes()' default id width.
const DeleteWidth = 13

// originTable is v3_wire_deletes.ORIGIN_TABLE (quant table 0x1415820).
var originTable = [7]int{1, 3, 5, 7, 9, 11, 13}

// Deletion is one packet's delete list: (t, pseq, [ids]).
type Deletion struct {
	T    uint64
	Pseq uint32
	IDs  []int
}

// PyValue is the tuple (t, pseq, ids).
func (x Deletion) PyValue() any {
	ids := make([]any, len(x.IDs))
	for i, v := range x.IDs {
		ids[i] = v
	}
	return []any{x.T, x.Pseq, ids}
}

// preambleEndsAt is preamble_ends_at: the origin sparse vec3 (v3_phys_p2.P.sparse(ORIGIN_TABLE, 0)) read from x
// ends exactly at E; any overrun is False.
func preambleEndsAt(b []byte, x, E int) bool {
	u := func(n int) bool { // P.u: ValueError past the end
		if n == 0 {
			return true
		}
		if x+n > len(b) {
			return false
		}
		x += n
		return true
	}
	w := -1
	for range 3 {
		if x+1 > len(b) {
			return false
		}
		present := b[x] == '1'
		x++
		if !present {
			continue
		}
		if w < 0 {
			if x+3 > len(b) {
				return false
			}
			sel, _ := intOf(b, x, x+3)
			x += 3
			if sel == 0 {
				return u(96) && x == E
			}
			w = originTable[sel-1]
		}
		if !u(w) {
			return false
		}
	}
	return x == E
}

// lifeEvent is a logged record of an id: (pseq, kind), kind 1 create, 2 update (0 = a delete, in the sort).
type lifeEvent struct{ s, k int }

// lifetimeViolations is lifetime_violations: create-while-alive, update-while-dead and delete-while-dead.
func lifetimeViolations(events []lifeEvent, dels []int) int {
	all := slices.Clone(events)
	for _, x := range dels {
		all = append(all, lifeEvent{x, 0})
	}
	slices.SortFunc(all, func(a, b lifeEvent) int {
		if a.s != b.s {
			return a.s - b.s
		}
		return a.k - b.k
	})
	bad, alive := 0, false
	for _, e := range all {
		switch e.k {
		case 1:
			if alive {
				bad++
			}
			alive = true
		case 2:
			if !alive {
				bad++
			}
		default:
			if !alive {
				bad++
			}
			alive = false
		}
	}
	return bad
}

// lifetimePick is lifetime_pick: the one reading of packet s whose ids have no lifetime violation, else nil.
func lifetimePick(cands [][]int, s int, events map[int][]lifeEvent, dels map[int][]int) ([]int, bool) {
	var ids []int // set().union(*cands); the order does not matter (a sum)
	seen := map[int]bool{}
	for _, c := range cands {
		for _, i := range c {
			if !seen[i] {
				seen[i] = true
				ids = append(ids, i)
			}
		}
	}
	var ok [][]int
	for _, c := range cands {
		sum := 0
		for _, i := range ids {
			dl := slices.Clone(dels[i])
			if slices.Contains(c, i) {
				dl = append(dl, s)
			}
			sum += lifetimeViolations(events[i], dl)
		}
		if sum == 0 {
			ok = append(ok, c)
		}
	}
	if len(ok) == 1 {
		return ok[0], true
	}
	return nil, false
}

// Deletes is v3_wire_deletes.deletes(d, width): every packet's delete list (packets in decode order) and the stats
// Counter (no_entries, unresolved, resolved, deletions, ambiguous, lifetime_tiebreak in first-touch order). Packets
// with no reading, or several that the r197 lifetime check does not settle, are skipped and counted.
func Deletes(d *capture.Decoded, width int) ([]Deletion, *fbgd.Counter, error) {
	ent, _ := entryCursors(d)
	st := &fbgd.Counter{}
	type amb struct {
		t     uint64
		s     uint32
		cands [][]int
	}
	var out []*Deletion // nil = an ambiguous packet's placeholder
	var buf []byte
	var idbuf []int
	var ambs []amb
	for _, s := range d.PacketSeqs {
		p := d.Packets[s]
		es, ok := ent[s]
		if !ok {
			st.Add("no_entries", 1)
			continue
		}
		buf = appendBits(buf, p.Payload)
		E, n, b := slices.Min(es), len(es), buf
		bit := func(i int) (byte, error) {
			if i >= len(b) {
				return 0, fmt.Errorf("IndexError: pseq %d: bit %d of %d", s, i, len(b))
			}
			return b[i], nil
		}
		var found [][]int
		for q := 0; q < E-32-width; q++ {
			bq, err := bit(q)
			if err != nil {
				return nil, nil, err
			}
			if bq != '1' {
				continue
			}
			cu, err := intOf(b, q+32, q+32+width)
			if err != nil {
				return nil, nil, err
			}
			c := int(cu)
			x := q + 32 + width
			ids := idbuf[:0]
			for x < E {
				bx, err := bit(x)
				if err != nil {
					return nil, nil, err
				}
				if bx != '1' || len(ids) > c {
					break
				}
				id, err := intOf(b, x+1, x+1+width)
				if err != nil {
					return nil, nil, err
				}
				ids = append(ids, int(id))
				idbuf = ids
				x += 1 + width
			}
			if x < E {
				bx, err := bit(x)
				if err != nil {
					return nil, nil, err
				}
				if bx == '0' && c-len(ids) == n && preambleEndsAt(b, x+1, E) &&
					!slices.ContainsFunc(found, func(f []int) bool { return slices.Equal(f, ids) }) {
					found = append(found, slices.Clone(ids))
				}
			}
		}
		switch {
		case len(found) == 0:
			st.Add("unresolved", 1)
		case len(found) > 1:
			slices.SortFunc(found, slices.Compare)
			ambs = append(ambs, amb{p.T, s, found})
			out = append(out, nil)
		default:
			st.Add("resolved", 1)
			if ids := found[0]; len(ids) > 0 {
				out = append(out, &Deletion{p.T, s, ids})
				st.Add("deletions", len(ids))
			}
		}
	}
	if len(ambs) > 0 { // r197: lifetime tie-break against the logged records and the unambiguous deletes
		events, dels := map[int][]lifeEvent{}, map[int][]int{}
		for _, gs := range [2][]*capture.Ghost{d.Ghosts, d.Multi} {
			for _, g := range gs {
				k := 2
				if g.Phase == 1 {
					k = 1
				}
				events[int(g.ID)] = append(events[int(g.ID)], lifeEvent{int(g.Pseq), k})
			}
		}
		for _, x := range out {
			if x != nil {
				for _, i := range x.IDs {
					dels[i] = append(dels[i], int(x.Pseq))
				}
			}
		}
		pick := map[uint32]*Deletion{}
		for _, a := range ambs {
			c, ok := lifetimePick(a.cands, int(a.s), events, dels)
			if !ok {
				st.Add("ambiguous", 1)
				continue
			}
			st.Add("resolved", 1)
			st.Add("lifetime_tiebreak", 1)
			if len(c) > 0 {
				pick[a.s] = &Deletion{a.t, a.s, slices.Clone(c)}
				st.Add("deletions", len(c))
			}
		}
		k := 0
		for i, x := range out {
			if x == nil {
				out[i] = pick[ambs[k].s]
				k++
			}
		}
	}
	res := []Deletion{}
	for _, x := range out {
		if x != nil {
			res = append(res, *x)
		}
	}
	return res, st, nil
}
