package capture

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"bc2replay/builder/internal/pyc"
)

// .bc2raw (bc2raw.py): 128 B header + the drained ring records verbatim (u32 head = type | len<<8, len incl. head,
// 4-aligned, PAD stripped): the legacy m-log records (P/G/C/M/B) interleaved with S/T/E/Q/O.

const (
	rawMagic      = "BC2RAW\x00\x00"
	rawFormat     = 1
	HeaderSize    = 128
	filetimeEpoch = 116444736000000000 // 1601-01-01 -> 1970-01-01 in 100 ns
)

// typeNames is bc2raw.NAMES (others print as their decimal type).
var typeNames = map[int]string{TP: "P", TG: "G", TC: "C", TM: "M", TB: "B", TS: "S", TT: "T", TE: "E", TQ: "Q", TO: "O", TPad: "PAD"}

func typeName(t int) string {
	if n, ok := typeNames[t]; ok {
		return n
	}
	return fmt.Sprint(t)
}

// legacy are the m-log record types extract writes to m-log.bin (bc2raw.LEGACY).
func legacy(t int) bool { return t == TP || t == TG || t == TC || t == TM || t == TB }

// Header is bc2raw.parse_header's dict.
type Header struct {
	Magic         string
	Format, Flags uint16
	ExeSHA256     string
	DLLVersion    uint32
	SessionGUID   string
	Filetime      uint64
	Recorded      string // "" = None
	InterruptTime uint64
	RingSize      uint32
}

// ParseHeader is bc2raw.parse_header (ValueError texts of the reference).
func ParseHeader(b []byte) (*Header, error) {
	if len(b) < HeaderSize {
		return nil, &PyError{"ValueError", fmt.Sprintf("short header (%d bytes)", len(b))}
	}
	if string(b[:8]) != rawMagic {
		return nil, &PyError{"ValueError", "bad magic " + pyBytesRepr(b[:8])}
	}
	h := &Header{Magic: strings.TrimRight(string(b[:8]), "\x00"), Format: binary.LittleEndian.Uint16(b[8:]),
		Flags: binary.LittleEndian.Uint16(b[10:]), ExeSHA256: hex.EncodeToString(b[12:44]), DLLVersion: u32(b, 44),
		SessionGUID: guidLE(b[48:64]), Filetime: u64(b, 64), InterruptTime: u64(b, 72), RingSize: u32(b, 80)}
	if h.Format != rawFormat {
		return nil, &PyError{"ValueError", fmt.Sprintf("unsupported format %d", h.Format)}
	}
	h.Recorded = FiletimeISO(h.Filetime)
	return h, nil
}

// guidLE is str(uuid.UUID(bytes_le=b)).
func guidLE(b []byte) string {
	x := []byte{b[3], b[2], b[1], b[0], b[5], b[4], b[7], b[6]}
	x = append(x, b[8:16]...)
	s := hex.EncodeToString(x)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

// Python's local-time conversion (datetime.astimezone on Windows) accepts 0 <= unix seconds < this; outside it
// raises (OSError / OverflowError) and filetime_iso returns None.
const maxLocalUnix = 32536800000

// FiletimeISO is bc2raw.filetime_iso: FILETIME (100 ns since 1601 UTC) -> naive local ISO text truncated to seconds;
// "" (None) for 0 or a time the reference cannot convert.
func FiletimeISO(ft uint64) string {
	if ft == 0 || ft < filetimeEpoch {
		return ""
	}
	us := (ft - filetimeEpoch) / 10
	if us/1_000_000 >= maxLocalUnix {
		return ""
	}
	return isoLocal(time.Unix(int64(us/1_000_000), 0))
}

func isoLocal(t time.Time) string { return t.Local().Format("2006-01-02T15:04:05") }

// pyBytesRepr is repr(bytes).
func pyBytesRepr(b []byte) string {
	q := byte('\'')
	if slices.Contains(b, '\'') && !slices.Contains(b, '"') {
		q = '"'
	}
	var s strings.Builder
	s.WriteString("b")
	s.WriteByte(q)
	for _, c := range b {
		switch {
		case c == q || c == '\\':
			s.WriteByte('\\')
			s.WriteByte(c)
		case c == '\t':
			s.WriteString(`\t`)
		case c == '\n':
			s.WriteString(`\n`)
		case c == '\r':
			s.WriteString(`\r`)
		case c < 0x20 || c >= 0x7F:
			fmt.Fprintf(&s, `\x%02x`, c)
		default:
			s.WriteByte(c)
		}
	}
	s.WriteByte(q)
	return s.String()
}

// Frame is one record of a .bc2raw: Raw = head + body.
type Frame struct {
	Type int
	Raw  []byte
}

// Body is the record body (head stripped).
func (f Frame) Body() []byte { return f.Raw[4:] }

// ReadFrames is bc2raw.iter_frames over the data after the header: records in file order (PAD dropped) and whether
// the data ended mid-record or at a zero/invalid head (1-3 trailing bytes count as truncated too). Frames alias data.
func ReadFrames(data []byte) ([]Frame, bool) {
	var out []Frame
	for i := 0; ; {
		rem := len(data) - i
		if rem < 4 {
			return out, rem > 0
		}
		head := binary.LittleEndian.Uint32(data[i:])
		typ, total := int(head&0xFF), int(head>>8)
		if head == 0 || total < 4 || total%4 != 0 || rem < total {
			return out, true
		}
		if typ != TPad {
			out = append(out, Frame{typ, data[i : i+total : i+total]})
		}
		i += total
	}
}

// Frames is bc2raw.frames: the header, the records and the truncation flag of a .bc2raw file.
func Frames(path string) (*Header, []Frame, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false, err
	}
	h, err := ParseHeader(b[:min(len(b), HeaderSize)])
	if err != nil {
		return nil, nil, false, err
	}
	fr, trunc := ReadFrames(b[HeaderSize:])
	return h, fr, trunc, nil
}

// LevelOf is bc2raw.level_of: the level name of the first S with the level-valid flag and a name; "" when none or
// when the file, its header or that S cannot be read.
func LevelOf(path string) string {
	h, fr, _, err := Frames(path)
	if err != nil || h == nil {
		return ""
	}
	for _, f := range fr {
		if f.Type != TS {
			continue
		}
		d, err := Decode([]Record{{TS, f.Body()}})
		if err != nil {
			return ""
		}
		if s := d.Sessions[0]; s.Flags&SLevelValid != 0 && s.Name != "" {
			return s.Name
		}
	}
	return ""
}

// Extract is bc2raw.extract: split a .bc2raw into a v3 capture dir (refuses an existing dir): m-log.bin (the
// P/G/C/M/B records verbatim), raw-log.bin (S/T/E/Q/O verbatim), m-summary.json and capture.json (the returned
// manifest: level from the first valid S, else level; label; recorded = header FILETIME as local ISO, else the
// file's mtime; exe {build, vt_translated} when the header names a known exe). A Steam capture's vtable fields are
// rewritten to retail values first (NormalizeSteam), so every later stage sees retail values only.
func Extract(path, outDir, level, label string) (*pyc.Object, error) {
	out := pathStr(outDir)
	if _, err := os.Stat(out); err == nil {
		return nil, &PyError{"FileExistsError", out + " exists; refusing to overwrite"}
	}
	h, fr, trunc, err := Frames(path)
	if err != nil {
		return nil, err
	}
	build, nvt := ExeBuild(h.ExeSHA256), 0
	if build == ExeSteam { // the single Steam -> retail point: everything after extract reads these files
		if nvt, err = NormalizeSteam(fr); err != nil {
			return nil, err
		}
	}
	recs := make([]Record, len(fr))
	var mlog, rlog []byte
	nLegacy, counts := 0, pyc.NewObject()
	for i, f := range fr {
		recs[i] = Record{f.Type, f.Body()}
		if legacy(f.Type) {
			mlog = append(mlog, f.Raw...)
			nLegacy++
		} else {
			rlog = append(rlog, f.Raw...)
		}
		n, _ := counts.Get(typeName(f.Type))
		c, _ := n.(int)
		counts.Set(typeName(f.Type), c+1)
	}
	dec, err := Decode(recs)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, s := range dec.Sessions {
		if s.Flags&SLevelValid != 0 && s.Name != "" {
			names = append(names, s.Name)
		}
	}
	lvl := level
	if len(names) > 0 {
		lvl = names[0]
	}
	var lastT Sync // syncs[-1] or {} (.get(k, 0))
	if n := len(dec.Syncs); n > 0 {
		lastT = *dec.Syncs[n-1]
	}
	if err := os.MkdirAll(out, 0o777); err != nil {
		return nil, err
	}
	mlogPath := filepath.Join(out, "m-log.bin")
	if err := os.WriteFile(mlogPath, mlog, 0o666); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(out, "raw-log.bin"), rlog, 0o666); err != nil {
		return nil, err
	}
	var seq uint32
	for _, s := range dec.PacketSeqs {
		seq = max(seq, s)
	}
	levels := slices.Compact(slices.Sorted(slices.Values(names)))
	sum := pyc.NewObject()
	sum.Set("drops", lastT.Drops)
	sum.Set("contention", lastT.Contention)
	sum.Set("skip", lastT.Skip)
	sum.Set("seq", seq)
	sum.Set("records", nLegacy)
	sum.Set("bytes", len(mlog))
	sum.Set("raw_records", len(fr)-nLegacy)
	sum.Set("counts", counts)
	sum.Set("truncated", trunc)
	sum.Set("levels", toAny(levels))
	sum.Set("reason", "bc2raw")
	sum.Set("log", mlogPath)
	if err := writeJSON(filepath.Join(out, "m-summary.json"), sum, pyc.DumpOptions{EnsureASCII: true, Indent: 2}); err != nil {
		return nil, err
	}
	rec := h.Recorded
	if rec == "" {
		st, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		rec = isoLocal(st.ModTime())
	}
	src := pyc.NewObject()
	src.Set("file", filepath.Base(path))
	src.Set("session_guid", h.SessionGUID)
	m := pyc.NewObject()
	m.Set("version", 1)
	m.Set("level", lvl)
	m.Set("label", label)
	m.Set("paired", false)
	m.Set("recorded", rec)
	m.Set("source", src)
	if build != "" { // r302: the recording exe; vt_translated = vtable fields rewritten Steam -> retail
		exe := pyc.NewObject()
		exe.Set("build", build)
		exe.Set("vt_translated", nvt)
		m.Set("exe", exe)
	}
	if err := writeJSON(filepath.Join(out, "capture.json"), m, pyc.DumpOptions{Indent: 1}); err != nil {
		return nil, err
	}
	return m, nil
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}

// pathStr is str(Path(p)) for the paths the builder passes (separators normalized, no trailing separator). Unlike
// pathlib, '..' components are collapsed too.
func pathStr(p string) string { return filepath.Clean(p) }

// writeJSON is Path.write_text(json.dumps(v, ...), encoding='utf-8'): text mode, so '\n' -> '\r\n' on Windows.
func writeJSON(path string, v any, o pyc.DumpOptions) error {
	s, err := pyc.Dumps(v, o)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	return os.WriteFile(path, []byte(s), 0o666)
}
