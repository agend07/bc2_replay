package captures

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"bc2replay/builder/internal/fbgd"
	"bc2replay/builder/internal/pyc"
)

// Small CPython behaviours the protocol text depends on (str(), repr(), str.strip/splitlines, sum(), pathlib str()).

// pyIsSpace is str.isspace for one code point (Go's unicode.IsSpace lacks \x1c-\x1f).
func pyIsSpace(r rune) bool { return (r >= 0x1c && r <= 0x1f) || unicode.IsSpace(r) }

func pyStrip(s string) string  { return strings.TrimFunc(s, pyIsSpace) }
func pyRstrip(s string) string { return strings.TrimRightFunc(s, pyIsSpace) }

// pySplitlines is str.splitlines() (no keepends).
func pySplitlines(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\x0b', '\x0c', '\x1c', '\x1d', '\x1e', '\x85', ' ', ' ':
		case '\r':
			out = append(out, string(rs[start:i]))
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
			continue
		default:
			continue
		}
		out = append(out, string(rs[start:i]))
		start = i + 1
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// universal is text-mode reading of child output (newline=None): \r\n and lone \r become \n; bytes decoded utf-8
// with errors='replace'.
func universal(b []byte) string {
	s := pyc.DecodeUTF8Replace(b)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// osText is what a Python child writes for s on a text-mode stream: '\n' -> os.linesep.
func osText(s string) string {
	if runtime.GOOS == "windows" {
		return strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s
}

// pyStr is str(v) for a JSON value (pyc.Loads types and the Go values this package builds).
func pyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return pyRepr(v)
}

// pyRepr is repr(v) for a JSON value.
func pyRepr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return fbgd.StrRepr(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint32:
		return strconv.FormatUint(uint64(x), 10)
	case *big.Int:
		return x.String()
	case float64:
		return pyc.Repr(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = pyRepr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []string:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = fbgd.StrRepr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *pyc.Object:
		parts := make([]string, 0, x.Len())
		for _, k := range x.Keys() {
			e, _ := x.Get(k)
			parts = append(parts, fbgd.StrRepr(k)+": "+pyRepr(e))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

// truthy is bool(v) for a JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case *big.Int:
		return x.Sign() != 0
	case []any:
		return len(x) > 0
	case *pyc.Object:
		return x.Len() > 0
	}
	return true
}

// get is d.get(k) for a JSON value that may not be a dict (nil then).
func get(o any, k string) any {
	if d, ok := o.(*pyc.Object); ok && d != nil {
		v, _ := d.Get(k)
		return v
	}
	return nil
}

func getObj(o any, k string) *pyc.Object { d, _ := get(o, k).(*pyc.Object); return d }

// num is float(v) for a JSON number (ok false for other types).
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case float64:
		return x, true
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		return f, true
	}
	return 0, false
}

// eqNum is Python == between a JSON value and an int.
func eqNum(v any, n int64) bool {
	switch x := v.(type) {
	case int64:
		return x == n
	case int:
		return int64(x) == n
	case float64:
		return x == float64(n)
	case bool:
		return (x && n == 1) || (!x && n == 0)
	}
	return false
}

// pySum is sum() over float values (CPython >= 3.12: Neumaier compensated summation); ints add exactly first.
func pySum(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s, c := vals[0], 0.0
	for _, x := range vals[1:] {
		t := s + x
		if math.Abs(s) >= math.Abs(x) {
			c += (s - t) + x
		} else {
			c += (x - t) + s
		}
		s = t
	}
	if c != 0 && !math.IsInf(c, 0) && !math.IsNaN(c) {
		s += c
	}
	return s
}

// roundInt is round(x) -> int (half-even).
func roundInt(x float64) int64 {
	n, err := pyc.RoundInt64(x)
	if err != nil {
		return 0
	}
	return n
}

// fmt0 is f'{x:.0f}'.
func fmt0(x float64) string { return strconv.FormatFloat(x, 'f', 0, 64) }

// isoNow / isoLocal: datetime.now() / fromtimestamp(t) .isoformat(timespec='seconds').
func isoLocal(t time.Time) string {
	return t.Round(time.Microsecond).Local().Format("2006-01-02T15:04:05")
}
func isoNow() string { return time.Now().Local().Format("2006-01-02T15:04:05") }

// pyPath is str(pathlib.Path(p)): separators normalized, empty and '.' parts dropped ('..' kept, unlike Clean).
func pyPath(p string) string {
	sep := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		p = strings.ReplaceAll(p, "/", "\\")
	}
	drive := ""
	if runtime.GOOS == "windows" && len(p) >= 2 && p[1] == ':' {
		drive, p = p[:2], p[2:]
	}
	root := strings.HasPrefix(p, sep)
	var parts []string
	for _, x := range strings.Split(p, sep) {
		if x != "" && x != "." {
			parts = append(parts, x)
		}
	}
	s := drive
	if root {
		s += sep
	}
	s += strings.Join(parts, sep)
	if s == "" {
		return "."
	}
	return s
}

// join is Path(a) / b... as a string (no '..' collapsing).
func join(a string, b ...string) string {
	return pyPath(strings.Join(append([]string{a}, b...), string(filepath.Separator)))
}

// pathName is PurePath(p).name for a path string of this OS (Windows also splits on '/').
func pathName(p string) string {
	p = pyPath(p)
	if runtime.GOOS == "windows" && len(p) >= 2 && p[1] == ':' {
		p = p[2:]
	}
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		p = p[i+1:]
	}
	if p == "." {
		return ""
	}
	return p
}

// stemOf is PurePath(name).stem.
func stemOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 && i < len(name)-1 {
		return name[:i]
	}
	return name
}

// resolve is Path.resolve() (non-strict): absolute, symlinks and on-disk name case resolved where the path exists.
func resolve(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return pyPath(p)
	}
	if r, err := filepath.EvalSymlinks(a); err == nil {
		return pyPath(r)
	}
	return pyPath(a)
}

func isFile(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }
func isDir(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }
func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// readJSON is read_json(p, default): json.loads of the utf-8 text, default (nil here) on OSError / ValueError.
func readJSON(p string) any {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	v, err := pyc.Loads(b)
	if err != nil {
		return nil
	}
	return v
}

// writeText is Path.write_text(s, encoding='utf-8') (text mode: '\n' -> os.linesep).
func writeText(p, s string) error { return os.WriteFile(p, []byte(osText(s)), 0o666) }

// dumpsIndent1 is json.dumps(v, ensure_ascii=False, indent=1).
func dumpsIndent1(v any) string {
	s, err := pyc.Dumps(v, pyc.DumpOptions{Indent: 1})
	if err != nil {
		panic(err) // only values this package builds or loaded with pyc.Loads
	}
	return s
}

// globPrefix is any(Path(dir).glob(prefix + '*')) / the sorted matches: case-insensitive on Windows like pathlib.
func globPrefix(dir, prefix string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if hasPrefixFold(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func hasPrefixFold(s, prefix string) bool {
	if runtime.GOOS == "windows" {
		return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
	}
	return strings.HasPrefix(s, prefix)
}

// sortNames sorts like sorted(Path...) in one dir: case-folded names on Windows.
func sortNames(names []string) {
	if runtime.GOOS != "windows" {
		slices.Sort(names)
		return
	}
	slices.SortStableFunc(names, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
}

// osErrorText is str(OSError) for a Go file error: '[Errno 2] No such file or directory: 'p” / '[WinError n] msg: 'p”.
func osErrorText(err error) string {
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		var lerr *os.LinkError
		if errors.As(err, &lerr) {
			pe = &fs.PathError{Op: lerr.Op, Path: lerr.Old, Err: lerr.Err}
		} else {
			return err.Error()
		}
	}
	path := fbgd.StrRepr(pyPath(pe.Path))
	if errors.Is(pe.Err, fs.ErrNotExist) {
		return "[Errno 2] No such file or directory: " + path
	}
	var en syscall.Errno
	if errors.As(pe.Err, &en) {
		msg := strings.TrimRightFunc(en.Error(), func(r rune) bool { return r <= ' ' || r == '.' })
		if runtime.GOOS == "windows" {
			return fmt.Sprintf("[WinError %d] %s: %s", uintptr(en), msg, path)
		}
		return fmt.Sprintf("[Errno %d] %s: %s", uintptr(en), msg, path)
	}
	return pe.Err.Error() + ": " + path
}

// copyFile is shutil.copyfile (content only; the destination is truncated / created).
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o666)
}

func lower(s string) string { return strings.ToLower(s) }

// pathEq is Path == Path (case-insensitive on Windows).
func pathEq(a, b string) bool {
	a, b = pyPath(a), pyPath(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
