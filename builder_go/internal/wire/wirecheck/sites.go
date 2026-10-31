package wirecheck

import (
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"bc2replay/builder/internal/pyc"
	"bc2replay/builder/internal/wire"
)

// DecodedEnv names the directory of decoded m-logs (<log>.json.gz, private) the site exam reads until the Go capture
// decoder exists: {packets: {seq: [payload hex, limit]}, configs: [[child hex, t0, t1]], ghosts: [...], multi: [...]}
// with recorder_v3.decode's ghost fields.
const DecodedEnv = "BC2_WIRE_DECODED"

type decodedGhost struct {
	T           uint64    `json:"t"`
	Pseq        int64     `json:"pseq"`
	ID          int64     `json:"id"`
	Phase       int64     `json:"phase"`
	SectionVT   uint32    `json:"section_vt"`
	EntryCursor int64     `json:"entry_cursor"`
	Cursor      int64     `json:"cursor"`
	Cache       uint32    `json:"cache"`
	DwordOffset int64     `json:"dword_offset"`
	OriginBits  [3]uint32 `json:"origin_bits"`
	CreateCur   int64     `json:"create_cur"`
	Config      int       `json:"config"`
	Sections    []uint32  `json:"sections"`
}

// LoadDecoded reads <dir>/<name>.json.gz into a wire.Log.
func LoadDecoded(dir, name string) (*wire.Log, error) {
	f, err := os.Open(filepath.Join(dir, name+".json.gz"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Packets map[string][2]json.RawMessage
		Configs [][3]json.RawMessage
		Ghosts  []decodedGhost
		Multi   []decodedGhost
	}
	if err := json.NewDecoder(io.Reader(gz)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	d := &wire.Log{Packets: map[int64]*wire.Packet{}}
	for k, v := range raw.Packets {
		seq, err := strconv.ParseInt(k, 10, 64)
		if err != nil {
			return nil, err
		}
		var h string
		p := &wire.Packet{}
		if err := json.Unmarshal(v[0], &h); err != nil {
			return nil, err
		}
		if p.Payload, err = hex.DecodeString(h); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(v[1], &p.Limit); err != nil {
			return nil, err
		}
		d.Packets[seq] = p
	}
	for _, c := range raw.Configs {
		var h string
		cfg := &wire.Config{}
		if err := json.Unmarshal(c[0], &h); err != nil {
			return nil, err
		}
		if cfg.ChildRaw, err = hex.DecodeString(h); err != nil {
			return nil, err
		}
		for i := range 2 {
			if err := json.Unmarshal(c[1+i], &cfg.Tables[i]); err != nil {
				return nil, err
			}
		}
		d.Configs = append(d.Configs, cfg)
	}
	conv := func(gs []decodedGhost) []*wire.Ghost {
		out := make([]*wire.Ghost, len(gs))
		for i, g := range gs {
			out[i] = &wire.Ghost{T: g.T, Pseq: g.Pseq, ID: g.ID, Phase: g.Phase, SectionVT: g.SectionVT,
				EntryCursor: g.EntryCursor, Cursor: g.Cursor, Cache: g.Cache, DwordOffset: g.DwordOffset,
				OriginBits: g.OriginBits, CreateCur: g.CreateCur, Config: g.Config, Sections: g.Sections}
		}
		return out
	}
	d.Ghosts, d.Multi = conv(raw.Ghosts), conv(raw.Multi)
	return d, nil
}

// SiteCounts is one site-kind row of the site exam (wire_py_check check_sites).
type SiteCounts struct {
	Sites, Equal, Differ, Unported int
	First                          []Diff
}

// SiteReport is the end-to-end site exam.
type SiteReport struct {
	ByKind map[string]*SiteCounts
	Notes  []string
	Time   time.Duration
}

var failedDrop = []string{"end", "properties", "reference_reads", "origin_vectors", "chunks"}

// failedView is wire_py_check._failed_view: a site Unicorn failed compares the error only (slice_record dict with
// 'error' set loses end/properties/refs/ovs/chunks; a run_section 5-list with err set keeps err only).
func failedView(res, rec any) any {
	if ro, ok := rec.(*pyc.Object); ok {
		if e, _ := ro.Get("error"); e != nil {
			if o, ok := res.(*pyc.Object); ok {
				c := pyc.NewObject()
				for _, k := range o.Keys() {
					if !slices.Contains(failedDrop, k) {
						v, _ := o.Get(k)
						c.Set(k, v)
					}
				}
				return c
			}
		}
	}
	if rl, ok := rec.([]any); ok && len(rl) == 5 && rl[4] != nil {
		if l, ok := res.([]any); ok && len(l) == 5 {
			return l[4]
		}
	}
	return res
}

func ghostKey(pseq, entryCursor, cursor, phase, id int64) [5]int64 {
	return [5]int64{pseq, entryCursor, cursor, phase, id}
}

func argInt(o *pyc.Object, k string) int64 {
	n, _ := toInt(get(o, k))
	return n
}

// CheckSites replays every site record whose runs use only ported readers end to end through the wire site
// wrappers on the decoded m-log, and compares the result verbatim (failed view), like wire_py_check --sites.
func CheckSites(decodedDir string, names []string, runs [][]*Run, sites [][]*Site, ndiff int) (*SiteReport, error) {
	return CheckSitesWith(func(name string) (*wire.Log, error) { return LoadDecoded(decodedDir, name) }, names, runs,
		sites, ndiff)
}

// CheckSitesWith is CheckSites with the decoded m-log of each corpus log from load (an os.ErrNotExist error skips
// that log's sites).
func CheckSitesWith(load func(name string) (*wire.Log, error), names []string, runs [][]*Run, sites [][]*Site, ndiff int) (*SiteReport, error) {
	rep := &SiteReport{ByKind: map[string]*SiteCounts{}}
	t0 := time.Now()
	for li, name := range names {
		d, err := load(name)
		if errors.Is(err, os.ErrNotExist) {
			rep.Notes = append(rep.Notes, name+": decoded m-log absent, sites skipped")
			continue
		} else if err != nil {
			return nil, err
		}
		widths, err := wire.InferIDWidths(d)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		by := map[[5]int64]*wire.Ghost{}
		for _, g := range slices.Concat(d.Ghosts, d.Multi) {
			by[ghostKey(g.Pseq, g.EntryCursor, g.Cursor, g.Phase, g.ID)] = g
		}
		var ac []*wire.Ghost
		for _, g := range d.Ghosts {
			if g.Phase == 0 && g.SectionVT == wire.AC38VT {
				ac = append(ac, g)
			}
		}
		entryOf := map[int64]uint32{}
		for _, r := range runs[li] {
			entryOf[r.I] = r.Entry
		}
		ai := 0
		for _, s := range sites[li] {
			c := rep.ByKind[s.Kind]
			if c == nil {
				c = &SiteCounts{}
				rep.ByKind[s.Kind] = c
			}
			c.Sites++
			var g *wire.Ghost
			if s.Kind == "ac38" {
				g = ac[ai]
				ai++
				if g.Cursor != argInt(s.Args, "cursor") {
					return nil, fmt.Errorf("%s site %d: AC38 order mismatch", name, s.Idx)
				}
			} else {
				ga, _ := get(s.Args, "g").(*pyc.Object)
				g = by[ghostKey(argInt(ga, "pseq"), argInt(ga, "entry_cursor"), argInt(ga, "cursor"), argInt(ga, "phase"), argInt(ga, "id"))]
				if g == nil {
					return nil, fmt.Errorf("%s site %d: ghost not in the decoded log", name, s.Idx)
				}
			}
			ported := len(s.Runs) > 0
			for _, i := range s.Runs {
				ported = ported && wire.IsPorted(entryOf[i])
			}
			if !ported {
				c.Unported++
				continue
			}
			var res pyc.Valuer
			switch s.Kind {
			case "upd_create", "rebase_create", "upd_update":
				res, err = wire.SliceRecord(d, g, widths)
			case "wiremulti":
				res, err = wire.RunSection(d, g, uint32(argInt(s.Args, "vt")), argInt(s.Args, "cur"), widths)
			case "camera_create":
				res, err = wire.CameraInit(d, g, widths)
			case "ac38":
				var row *wire.Row
				if row, err = wire.RowOf(d, g); err == nil {
					res, err = wire.SliceRow(row, wire.AC38VT)
				}
			default:
				c.Unported++
				continue
			}
			var un *wire.ErrUnported
			if errors.As(err, &un) {
				c.Unported++
				continue
			}
			var got any
			if err == nil {
				got = res.PyValue()
			}
			goS, recS := js(failedView(got, s.Result)), js(failedView(s.Result, s.Result))
			if goS == recS && (err == nil) == (s.Exc == nil) {
				c.Equal++
				continue
			}
			c.Differ++
			if len(c.First) < ndiff {
				if err != nil {
					goS = "exception: " + err.Error()
				}
				c.First = append(c.First, Diff{Log: name, I: s.Idx, Site: s.Kind, Go: goS, Rec: recS})
			}
		}
	}
	rep.Time = time.Since(t0)
	return rep, nil
}

// Print writes the per-site-kind table.
func (rep *SiteReport) Print(w io.Writer) {
	for _, n := range rep.Notes {
		fmt.Fprintln(w, n)
	}
	fmt.Fprintf(w, "%-14s %7s %7s %7s %8s\n", "site (e2e)", "sites", "equal", "differ", "unported")
	kinds := make([]string, 0, len(rep.ByKind))
	for k := range rep.ByKind {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	for _, k := range kinds {
		c := rep.ByKind[k]
		fmt.Fprintf(w, "%-14s %7d %7d %7d %8d\n", k, c.Sites, c.Equal, c.Differ, c.Unported)
		for _, d := range c.First {
			fmt.Fprintf(w, "    %s sidx=%d\n      go:  %.600s\n      rec: %.600s\n", d.Log, d.I, d.Go, d.Rec)
		}
	}
	fmt.Fprintf(w, "seconds sites %.2f\n", rep.Time.Seconds())
}

// OK reports whether no site differs.
func (rep *SiteReport) OK() bool {
	for _, c := range rep.ByKind {
		if c.Differ != 0 {
			return false
		}
	}
	return true
}
