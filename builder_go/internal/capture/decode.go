// Package capture reads the recorder's captures: the m-log record stream (live_recorder_v3.parse_log), its decode
// (recorder_v3.decode, v3_types.load, recorder_v3.to_v1_rows) and the .bc2raw container of the recorder DLL
// (bc2raw.py: header, frames, level_of, extract). Decoded values keep Python's dict/list order wherever the builder
// iterates them later; PyValue methods give the Python dicts for golden stage dumps.
package capture

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Record types (recorder_v3.T_*).
const (
	TP   = 1    // packet
	TG   = 2    // ghost entry
	TC   = 3    // config
	TM   = 4    // multi-section list of the preceding G
	TB   = 5    // bookmark tables of the preceding G (/M)
	TS   = 6    // session (.bc2raw)
	TT   = 7    // sync
	TE   = 8    // end
	TQ   = 9    // raw packet (observer)
	TO   = 10   // outgoing (reserved)
	TPad = 0xFF // ring tail filler
)

// Session flag bits (recorder_v3.S_*).
const (
	SLevelValid = 1
	SFragment   = 2
	SNoObserver = 4
)

// Soldier ghost / section vtables (recorder_v3.SOLDIER_*).
const (
	SoldierGhostVT   = 0x14378F0
	SoldierSectionVT = 0x14377F8
)

// Fixed body sizes (struct.calcsize of the recorder_v3 formats).
const (
	pHeadSize = 32 // P_HEAD '<8I' (also Q)
	gBodySize = 60 // G_BODY '<IIIIHHIIIIII3II'
	bHeadSize = 32 // B_HEAD '<8I'
	sHeadSize = 52 // S_HEAD '<9IQQ'
	tBodySize = 40 // T_BODY '<QQ6I'
	eBodySize = 20 // E_BODY '<IQII'
)

// Record is one (type, body) of a record stream (head stripped).
type Record struct {
	Type int
	Body []byte
}

// ParseLog is live_recorder_v3.parse_log: the concatenated complete records of an m-log -> [(type, body)]. Bodies
// alias raw. A bad head or a record past the end is "ValueError: corrupt log at <offset>"; 1-3 trailing bytes are
// ignored.
func ParseLog(raw []byte) ([]Record, error) {
	var out []Record
	for i := 0; i+4 <= len(raw); {
		head := binary.LittleEndian.Uint32(raw[i:])
		total := int(head >> 8)
		if total < 4 || total%4 != 0 || i+total > len(raw) {
			return nil, &PyError{"ValueError", fmt.Sprintf("corrupt log at %d", i)}
		}
		out = append(out, Record{int(head & 0xFF), raw[i+4 : i+total : i+total]})
		i += total
	}
	return out, nil
}

// PyError is a Python exception of the reference (Kind = exception class, Msg = str(e)); Error() is str(e), the text
// the v3_captures RESULT line prints.
type PyError struct{ Kind, Msg string }

func (e *PyError) Error() string { return e.Msg }

// need is struct.unpack_from's size check: size bytes at off of b.
func need(b []byte, off, size int64) error {
	if int64(len(b)) < off+size {
		return &PyError{"struct.error", fmt.Sprintf("unpack_from requires a buffer of at least %d bytes for unpacking %d bytes "+
			"at offset %d (actual buffer size is %d)", off+size, size, off, len(b))}
	}
	return nil
}

func u32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
func u64(b []byte, off int) uint64 { return binary.LittleEndian.Uint64(b[off:]) }

// u32s unpacks n little-endian u32 at off ('<%dI' % n); n may be any recorded count.
func u32s(b []byte, off int, n uint64) ([]uint32, error) {
	if err := need(b, int64(off), 4*int64(n)); err != nil {
		return nil, err
	}
	out := make([]uint32, n)
	for i := range out {
		out[i] = u32(b, off+4*i)
	}
	return out, nil
}

// tail is b[lo:lo+n] with Python slice clamping.
func tail(b []byte, lo int, n uint64) []byte {
	if lo > len(b) {
		return b[len(b):len(b):len(b)]
	}
	hi := uint64(lo) + n
	if hi > uint64(len(b)) {
		hi = uint64(len(b))
	}
	return b[lo:hi:hi]
}

// tornTime is (h1 if h1 == h2 else h2) << 32 | lo.
func tornTime(b []byte) uint64 {
	h1, lo, h2 := u32(b, 0), u32(b, 4), u32(b, 8)
	if h1 != h2 {
		h1 = h2
	}
	return uint64(h1)<<32 | uint64(lo)
}

// ------------------------------------------------------------------------------------------------- decoded values

// Packet is one P record (decode 'packets' value).
type Packet struct {
	T                  uint64
	Seq, Buffer, Limit uint32
	Payload            []byte
}

// Raw is one Q record (decode 'raws' value): P_HEAD layout incl. first, keyed by qseq.
type Raw struct {
	T                          uint64
	Qseq, Buffer, Limit, First uint32
	Payload                    []byte
}

// Config is one C record.
type Config struct {
	ChildRaw []byte
	Tables   [2][]uint32
}

// Bookmarks is a B record attached to its ghost.
type Bookmarks struct {
	Pre, Post, Result, SecCounter, PropCounter uint32
	Sections, Props                            [][2]uint32
}

// Ghost is one G record. Optional dict keys, in the order Python inserts them: Sections (multi only; nil = absent,
// a non-nil empty slice = the key with []), Bookmarks (nil = absent), then EndHint/span when the Decoded is Hinted
// (v3_types.load, ghosts only).
type Ghost struct {
	T                                              uint64
	Pseq                                           uint32
	ID, Phase                                      uint16
	GhostVT, SectionVT, EntryCursor, Cursor, Cache uint32
	DwordOffset                                    uint32
	OriginBits                                     [3]uint32
	CreateCur                                      uint32
	Config                                         int
	Sections                                       []uint32
	Bookmarks                                      *Bookmarks
	EndHint                                        int64 // v3_types.load: next same-packet entry_cursor - GAP when HasEndHint, else None
	HasEndHint                                     bool
}

// Session is one S record.
type Session struct {
	Ver, Flags, Conn, DispatcherVT, GhostBits, Qseq, Pseq, LevelPtr uint32
	Filetime, IT                                                    uint64
	Name                                                            string
}

// Sync is one T record.
type Sync struct {
	IT, Filetime                              uint64
	Qseq, Pseq, Prod, Drops, Contention, Skip uint32
}

// End is one E record.
type End struct {
	Reason     uint32
	IT         uint64
	Qseq, Pseq uint32
}

// Decoded is recorder_v3.decode's dict: packets/raws keep first-insertion order of their keys (a repeated seq replaces
// the value in place, like a dict assignment), ghosts/configs/multi/sessions/syncs/ends are lists in record order.
type Decoded struct {
	Packets    map[uint32]*Packet
	PacketSeqs []uint32 // dict key order
	Ghosts     []*Ghost
	Configs    []*Config
	Multi      []*Ghost
	Orphans    int
	BOrphans   int
	Sessions   []*Session
	Syncs      []*Sync
	Ends       []*End
	Raws       map[uint32]*Raw
	RawSeqs    []uint32
	Hinted     bool // v3_types.load added end_hint/span to every ghost
}

// Decode is recorder_v3.decode: records -> packets, ghosts (single section), configs, multi (section_vt 0 with their
// M list), orphans, borphans, sessions, syncs, ends, raws. Each ghost's Config is the index of the latest preceding
// C; a G is an orphan without its P or without any C. S/T/E/Q/O are transparent to the G->M / G->B pairing. Errors
// are struct.error texts of a short body.
func Decode(records []Record) (*Decoded, error) {
	d := &Decoded{Packets: map[uint32]*Packet{}, Raws: map[uint32]*Raw{}}
	var multi []*Ghost
	var wantM, last *Ghost // multi G awaiting its M (the very next record); ghost a B may still attach to
	for _, r := range records {
		b := r.Body
		if r.Type >= TS && r.Type <= TO {
			if err := d.decodeR247(r.Type, b); err != nil {
				return nil, err
			}
			continue
		}
		prev := wantM
		wantM = nil
		if r.Type == TB {
			if err := need(b, 0, bHeadSize); err != nil {
				return nil, err
			}
			pre, post, gvt, res := u32(b, 0), u32(b, 4), u32(b, 8), u32(b, 12)
			cs, cp, ns, np := u32(b, 16), u32(b, 20), uint64(u32(b, 24)), uint64(u32(b, 28))
			v, err := u32s(b, bHeadSize, 2*(ns+np))
			if err != nil {
				return nil, err
			}
			if last != nil && pre == last.Cursor && gvt == last.GhostVT {
				pairs := func(v []uint32) [][2]uint32 {
					out := make([][2]uint32, len(v)/2)
					for i := range out {
						out[i] = [2]uint32{v[2*i], v[2*i+1]}
					}
					return out
				}
				last.Bookmarks = &Bookmarks{pre, post, res, cs, cp, pairs(v[:2*ns]), pairs(v[2*ns:])}
			} else {
				d.BOrphans++
			}
			last = nil
			continue
		}
		if r.Type != TM {
			last = nil
		}
		switch r.Type {
		case TP:
			if err := need(b, 0, pHeadSize); err != nil {
				return nil, err
			}
			seq := u32(b, 12)
			p := &Packet{T: tornTime(b), Seq: seq, Buffer: u32(b, 16), Limit: u32(b, 20), Payload: tail(b, pHeadSize, uint64(u32(b, 28)))}
			if _, ok := d.Packets[seq]; !ok {
				d.PacketSeqs = append(d.PacketSeqs, seq)
			}
			d.Packets[seq] = p
		case TC:
			if err := need(b, 0, 8); err != nil {
				return nil, err
			}
			n0, n1 := uint64(u32(b, 0)), uint64(u32(b, 4))
			c := &Config{ChildRaw: tail(b, 8, 32)}
			var err error
			if c.Tables[0], err = u32s(b, 40, n0); err != nil {
				return nil, err
			}
			if c.Tables[1], err = u32s(b, 40+4*int(n0), n1); err != nil {
				return nil, err
			}
			d.Configs = append(d.Configs, c)
		case TG:
			if err := need(b, 0, gBodySize); err != nil {
				return nil, err
			}
			pseq := u32(b, 12)
			if _, ok := d.Packets[pseq]; !ok || len(d.Configs) == 0 {
				d.Orphans++
				continue
			}
			g := &Ghost{T: tornTime(b), Pseq: pseq, ID: binary.LittleEndian.Uint16(b[16:]),
				Phase: binary.LittleEndian.Uint16(b[18:]), GhostVT: u32(b, 20), SectionVT: u32(b, 24),
				EntryCursor: u32(b, 28), Cursor: u32(b, 32), Cache: u32(b, 36), DwordOffset: u32(b, 40),
				OriginBits: [3]uint32{u32(b, 44), u32(b, 48), u32(b, 52)}, CreateCur: u32(b, 56),
				Config: len(d.Configs) - 1}
			last = g
			if g.SectionVT != 0 {
				d.Ghosts = append(d.Ghosts, g)
			} else { // multi-section ghosts kept apart: 'ghosts' consumers assume one section
				multi = append(multi, g)
				wantM = g
			}
		case TM:
			if err := need(b, 0, 4); err != nil {
				return nil, err
			}
			if prev == nil {
				d.Orphans++
				break
			}
			s, err := u32s(b, 4, uint64(u32(b, 0)))
			if err != nil {
				return nil, err
			}
			prev.Sections = s
		}
	}
	for _, g := range multi {
		if g.Sections != nil {
			d.Multi = append(d.Multi, g)
		}
	}
	return d, nil
}

func (d *Decoded) decodeR247(typ int, b []byte) error {
	switch typ {
	case TS:
		if err := need(b, 0, sHeadSize); err != nil {
			return err
		}
		d.Sessions = append(d.Sessions, &Session{Ver: u32(b, 0), Flags: u32(b, 4), Conn: u32(b, 8), DispatcherVT: u32(b, 12),
			GhostBits: u32(b, 16), Qseq: u32(b, 20), Pseq: u32(b, 24), LevelPtr: u32(b, 28), Filetime: u64(b, 36),
			IT: u64(b, 44), Name: asciiReplace(tail(b, sHeadSize, uint64(u32(b, 32))))})
	case TT:
		if err := need(b, 0, tBodySize); err != nil {
			return err
		}
		d.Syncs = append(d.Syncs, &Sync{IT: u64(b, 0), Filetime: u64(b, 8), Qseq: u32(b, 16), Pseq: u32(b, 20),
			Prod: u32(b, 24), Drops: u32(b, 28), Contention: u32(b, 32), Skip: u32(b, 36)})
	case TE:
		if err := need(b, 0, eBodySize); err != nil {
			return err
		}
		d.Ends = append(d.Ends, &End{Reason: u32(b, 0), IT: u64(b, 4), Qseq: u32(b, 12), Pseq: u32(b, 16)})
	case TQ:
		if err := need(b, 0, pHeadSize); err != nil {
			return err
		}
		q := &Raw{T: tornTime(b), Qseq: u32(b, 12), Buffer: u32(b, 16), Limit: u32(b, 20), First: u32(b, 24),
			Payload: tail(b, pHeadSize, uint64(u32(b, 28)))}
		if _, ok := d.Raws[q.Qseq]; !ok {
			d.RawSeqs = append(d.RawSeqs, q.Qseq)
		}
		d.Raws[q.Qseq] = q
	}
	return nil // TO: skipped
}

// asciiReplace is bytes.decode('ascii', 'replace'): every byte >= 0x80 -> U+FFFD.
func asciiReplace(b []byte) string {
	var s strings.Builder
	s.Grow(len(b))
	for _, c := range b {
		if c < 0x80 {
			s.WriteByte(c)
		} else {
			s.WriteRune('�')
		}
	}
	return s.String()
}

// GAP is v3_types.GAP: bits between a ghost entry's end and the next entry cursor.
const GAP = 14

// Load is v3_types.load with the V3_LOG global as the explicit raw m-log: Decode(ParseLog(raw)) plus end_hint (the
// next ghost's entry_cursor - GAP when it is in the same packet, else None) and span None on every ghost.
func Load(raw []byte) (*Decoded, error) {
	recs, err := ParseLog(raw)
	if err != nil {
		return nil, err
	}
	d, err := Decode(recs)
	if err != nil {
		return nil, err
	}
	d.Hint()
	return d, nil
}

// levelSessions are the S records of a capture's raw-log.bin bytes, decoded.
func levelSessions(rawLog []byte) ([]*Session, error) {
	recs, err := ParseLog(rawLog)
	if err != nil {
		return nil, err
	}
	var ss []Record
	for _, r := range recs {
		if r.Type == TS {
			ss = append(ss, r)
		}
	}
	d, err := Decode(ss)
	if err != nil {
		return nil, err
	}
	return d.Sessions, nil
}

// cutsOf is the level-change P seqs of S records, consecutive duplicates collapsed (see LevelCuts).
func cutsOf(ss []*Session) []uint32 {
	var cuts []uint32
	var ptr uint32
	valid := false
	for _, s := range ss {
		if valid && (s.Flags&SLevelValid == 0 || s.LevelPtr != ptr) && (len(cuts) == 0 || cuts[len(cuts)-1] != s.Pseq) {
			cuts = append(cuts, s.Pseq)
		}
		valid, ptr = s.Flags&SLevelValid != 0, s.LevelPtr
	}
	return cuts
}

// LevelCuts is v3_types.level_cuts (r282/r287) on the bytes of a capture's raw-log.bin: the P seqs of the level
// changes in its S records (nil = none). A level change = an S without SLevelValid, or with another level_ptr, after
// an S with it (level unloaded / reloaded on the same connection, e.g. a round restart on the same map): the client
// drops its whole ghost world there and the server re-creates everything under reassigned ids. The first level load
// does not count (its S may come after the first packets, r247). Consecutive duplicates are collapsed.
func LevelCuts(rawLog []byte) ([]uint32, error) {
	ss, err := levelSessions(rawLog)
	if err != nil {
		return nil, err
	}
	return cutsOf(ss), nil
}

// LevelCut is v3_types.level_cut: the last level change P seq (0 = none).
func LevelCut(rawLog []byte) (uint32, error) {
	cuts, err := LevelCuts(rawLog)
	if err != nil || len(cuts) == 0 {
		return 0, err
	}
	return cuts[len(cuts)-1], nil
}

// Range is one level session in P seq: lo < pseq <= Hi (Open = no upper bound, v3_types' hi None).
type Range struct {
	Lo, Hi uint32
	Open   bool
}

// Has reports lo < q <= hi.
func (r Range) Has(q uint32) bool { return q > r.Lo && (r.Open || q <= r.Hi) }

// Segments are the rounds of cuts (r287): bounds [0] + cuts + [open]; round k (1-based) = Segments[k-1]. Every
// segment counts, empty ones too (stable numbering).
func Segments(cuts []uint32) []Range {
	out := make([]Range, 0, len(cuts)+1)
	lo := uint32(0)
	for _, c := range cuts {
		out = append(out, Range{Lo: lo, Hi: c})
		lo = c
	}
	return append(out, Range{Lo: lo, Open: true})
}

// DefaultRound is v3_types.level_range's choice (r287) as a 1-based round: the segment of cuts with the most P seqs
// of pseqs, ties -> later.
func DefaultRound(pseqs []uint32, cuts []uint32) int {
	segs := Segments(cuts)
	best, bn := 0, -1
	for i, r := range segs {
		n := 0
		for _, q := range pseqs {
			if r.Has(q) {
				n++
			}
		}
		if n >= bn {
			best, bn = i, n
		}
	}
	return best + 1
}

// LevelRange is v3_types.level_range(pseqs, cuts): the level session with the most P packets (ties -> later).
func LevelRange(pseqs []uint32, cuts []uint32) Range {
	return Segments(cuts)[DefaultRound(pseqs, cuts)-1]
}

// Round is one capture round (r287, v3_types.rounds): P seq range, Packets = hi - lo (open end: hi = the capture's last
// P seq; HasPackets false when unknown) and the name of the first valid named S with lo <= pseq <= hi ("" = none).
type Round struct {
	Range
	Packets    int64
	HasPackets bool
	Level      string
}

// Rounds is v3_types.rounds: the rounds of a capture, numbered from 1 (index + 1), empty ones included. rawLog = its
// raw-log.bin bytes (nil = none -> one open round); seq = the last P seq (m-summary.json "seq"), nil = unknown.
func Rounds(rawLog []byte, seq *int64) ([]Round, error) {
	var ss []*Session
	if rawLog != nil {
		var err error
		if ss, err = levelSessions(rawLog); err != nil {
			return nil, err
		}
	}
	segs := Segments(cutsOf(ss))
	out := make([]Round, len(segs))
	for i, r := range segs {
		out[i].Range = r
		if !r.Open {
			out[i].Packets, out[i].HasPackets = int64(r.Hi)-int64(r.Lo), true
		} else if seq != nil {
			out[i].Packets, out[i].HasPackets = max(0, *seq-int64(r.Lo)), true
		}
		for _, s := range ss {
			if s.Flags&SLevelValid != 0 && s.Name != "" && s.Pseq >= r.Lo && (r.Open || s.Pseq <= r.Hi) {
				out[i].Level = s.Name
				break
			}
		}
	}
	return out, nil
}

// DefaultOf is v3_types.default_round: the 1-based round with the most packets (unknown = 0; ties -> later), 0 for
// no rounds.
func DefaultOf(rs []Round) int {
	best := 0
	for i, r := range rs {
		if best == 0 || r.Packets >= rs[best-1].Packets {
			best = i + 1
		}
	}
	return best
}

// RoundOf is v3_types.round_of: the chosen round, v = V3_ROUND ("" = DefaultOf; a non-number or out of range is the
// reference's SystemExit text as error).
func RoundOf(rs []Round, v string) (int, error) {
	if v == "" {
		return DefaultOf(rs), nil
	}
	k, err := strconv.Atoi(v)
	if err != nil || strings.TrimLeft(v, "0123456789") != "" || k < 1 || k > len(rs) {
		return 0, fmt.Errorf("%s=%s: the capture has %d round(s)", RoundEnv, v, len(rs))
	}
	return k, nil
}

// CacheName is v3_types.cache_name (r287): the per-round name of a capture-dir cache file: name for a single-round
// capture, else <stem>_r<K>.<ext> (K = RoundOf(rs, k)), so a cache of one round is never reused for another.
func CacheName(name string, rs []Round, k string) (string, error) {
	if len(rs) <= 1 {
		return name, nil
	}
	r, err := RoundOf(rs, k)
	if err != nil {
		return "", err
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return fmt.Sprintf("%s_r%d%s", name[:i], r, name[i:]), nil
	}
	return fmt.Sprintf("%s_r%d", name, r), nil
}

// Keep keeps only the records in r (packets in key order, ghosts, multi): v3_types.load's level session filter.
func (d *Decoded) Keep(r Range) {
	seqs := d.PacketSeqs[:0:0]
	for _, s := range d.PacketSeqs {
		if r.Has(s) {
			seqs = append(seqs, s)
		} else {
			delete(d.Packets, s)
		}
	}
	d.PacketSeqs = seqs
	keep := func(gs []*Ghost) []*Ghost {
		out := gs[:0:0]
		for _, g := range gs {
			if r.Has(g.Pseq) {
				out = append(out, g)
			}
		}
		return out
	}
	d.Ghosts, d.Multi = keep(d.Ghosts), keep(d.Multi)
}

// Cut keeps only the records after P seq cut (r282); 0 = no change.
func (d *Decoded) Cut(cut uint32) {
	if cut != 0 {
		d.Keep(Range{Lo: cut, Open: true})
	}
}

// RoundEnv is the pipeline env naming the round to keep (r287, 1-based; unset = DefaultRound).
const RoundEnv = "V3_ROUND"

// LoadFile is v3_types.load(path) (r282/r287): Load of the m-log at path, keeping one level session of the
// raw-log.bin next to it (no raw-log.bin or no level change = everything) before the end_hint pass. round = V3_ROUND
// (RoundOf: "" = the round with the most packets, the open end counted up to the last P seq of the m-log).
func LoadFile(mlog string, round string) (*Decoded, Levels, error) {
	var lv Levels
	raw, err := os.ReadFile(mlog)
	if err != nil {
		return nil, lv, err
	}
	recs, err := ParseLog(raw)
	if err != nil {
		return nil, lv, err
	}
	recs, lv.ConfigInjected = EnsureConfig(recs)
	d, err := Decode(recs)
	if err != nil {
		return nil, lv, err
	}
	var rl []byte
	if rl, err = os.ReadFile(filepath.Join(filepath.Dir(mlog), "raw-log.bin")); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, lv, err
		}
		rl = nil
	}
	var top int64
	for _, q := range d.PacketSeqs {
		top = max(top, int64(q))
	}
	rs, err := Rounds(rl, &top)
	if err != nil {
		return nil, lv, err
	}
	if lv.Round, err = RoundOf(rs, round); err != nil {
		return nil, lv, err
	}
	if len(rs) > 1 {
		for _, r := range rs[1:] {
			lv.Cuts = append(lv.Cuts, r.Lo)
		}
		r := rs[lv.Round-1].Range
		lv.Kept = &r
		d.Keep(r)
	}
	d.Hint()
	return d, lv, nil
}

// Levels is what LoadFile cut: the level changes and the kept range (nil = no level change, everything kept).
type Levels struct {
	Cuts           []uint32
	Kept           *Range
	Round          int  // the 1-based round kept
	ConfigInjected bool // r291: the m-log had no C record; DefaultConfig was prepended
}

// DefaultConfig is the body of the recorder's C (config) record for the supported exe: the ghost class tables are a
// property of the game build, not of the map or server (identical in every capture seen: MP_006CQ, MP_005, MP_012CQ).
// The recorder cave writes C once per config object; a second connection in the same client process reuses the object
// (DLL builds before 0.1.0-preview.3), so that capture has no C record and would decode to nothing.
var DefaultConfig = mustHex("0700000007000000f0204301400000000700000006000000d4204301000000005803cf085803cf08" +
	"01000000030000000500000007000000090000000b0000000d000000" +
	"01000000030000000500000007000000090000000b0000000d000000")

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// EnsureConfig prepends DefaultConfig as a C record when recs holds none (r291); the second result says whether it did.
func EnsureConfig(recs []Record) ([]Record, bool) {
	for _, r := range recs {
		if r.Type == TC {
			return recs, false
		}
	}
	return append([]Record{{TC, DefaultConfig}}, recs...), true
}

// Hint adds v3_types.load's end_hint/span to the ghosts.
func (d *Decoded) Hint() {
	for i, g := range d.Ghosts {
		g.EndHint, g.HasEndHint = 0, i+1 < len(d.Ghosts) && d.Ghosts[i+1].Pseq == g.Pseq
		if g.HasEndHint {
			g.EndHint = int64(d.Ghosts[i+1].EntryCursor) - GAP
		}
	}
	d.Hinted = true
}

// ------------------------------------------------------------------------------------------------- to_v1_rows

// V1Row is one recorder_v3.to_v1_rows row: dict(marker=1, t, id, phase=0, cursor, cache, dword_offset, flags=0,
// limit, payload, child_raw, count0, tables, origin_bits) read through the decode's own objects (Python's row shares
// payload / child_raw / tables with them too); Packet is the packet of the ghost's pseq when the rows were made.
type V1Row struct {
	G *Ghost
	P *Packet
	C *Config
}

// ToV1Rows is recorder_v3.to_v1_rows: the soldier update ghosts (soldier ghost + section vt, phase 0) as v1 rows.
func ToV1Rows(d *Decoded) []V1Row {
	var rows []V1Row
	for _, g := range d.Ghosts {
		if g.GhostVT == SoldierGhostVT && g.SectionVT == SoldierSectionVT && g.Phase == 0 {
			rows = append(rows, V1Row{g, d.Packets[g.Pseq], d.Configs[g.Config]})
		}
	}
	return rows
}
