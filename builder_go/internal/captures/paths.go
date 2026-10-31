// Package captures is the Go port of the release builder's drivers (frozen oracle r277 + the r278 default-path rule):
// v3_captures.py (list / extract / prune / build with its lock, builds.json and the panel line protocol), v3_trial.py
// (phases, summary, trial/build/gate files, publish). The demo pipeline itself (v3_cl04c) is behind Pipeline. POV
// demos (v3_pov_list.py / v3_pov_build.py: player list, players.json, pov-build) were removed for the release.
//
// Panel-visible text (STEP / ESTIMATE / PROGRESS / DEMO / prune lines / RESULT) is byte-identical to the Python
// builder, except that a published build no longer has the STEP 3 players-list step (STEP 1/2, 2/2 always); stdout
// lines end in os.linesep like Python's print on a pipe.
package captures

import (
	"os"
	"path/filepath"
)

// EXTS are the 9 demo files (v3_trial.EXTS).
var EXTS = []string{"fbgd", "fbgk", "fbgdd", "fbgdk", "fbidd", "fbidk", "fbcdd", "fbcdk", "txt"}

const (
	manifestName = "capture.json"
	fps          = 30
)

// builderDir is the directory of builder.exe (Python: of the module); the r278 defaults hang off its parent.
func builderDir() string {
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		return filepath.Dir(exe)
	}
	return "."
}

// runtimeBuilder is <builder dir>/../runtime/builder (Path(__file__).resolve().parent.parent / 'runtime' / 'builder').
func runtimeBuilder() string { return join(filepath.Dir(builderDir()), "runtime", "builder") }

// archiveDir is BC2_ARCHIVE_DIR or <builder dir>/../runtime/builder/archive.
func archiveDir() string {
	if v := os.Getenv("BC2_ARCHIVE_DIR"); v != "" {
		return pyPath(v)
	}
	return join(runtimeBuilder(), "archive")
}

// WorkDir is v3_captures.PRIVATE: BC2_WORK_DIR (set, even empty -> '.') else <builder dir>/../runtime/builder.
func WorkDir() string {
	if v, ok := os.LookupEnv("BC2_WORK_DIR"); ok {
		return pyPath(v)
	}
	return runtimeBuilder()
}

// DemosDir is v3_captures.DEMOS (v3_pov_list.DEMOS): BC2_DEMOS_DIR or <archive>/demos.
func DemosDir() string {
	if v := os.Getenv("BC2_DEMOS_DIR"); v != "" {
		return pyPath(v)
	}
	return join(archiveDir(), "demos")
}

// OldDemosDir is v3_captures.OLD_DEMOS: BC2_OLD_DEMOS_DIR > <BC2_DATAPACK_DIR>/harvest > <archive>/old_demos.
func OldDemosDir() string {
	if v := os.Getenv("BC2_OLD_DEMOS_DIR"); v != "" {
		return pyPath(v)
	}
	if v := os.Getenv("BC2_DATAPACK_DIR"); v != "" {
		return join(v, "harvest")
	}
	return join(archiveDir(), "old_demos")
}

// datapackDir is BC2_DATAPACK_DIR ("" when unset or empty).
func datapackDir() string { return os.Getenv("BC2_DATAPACK_DIR") }

// RawDir is v3_captures.raw_dir(): BC2_CAPTURES_DIR, else %LOCALAPPDATA%\BC2Replay\captures; "" without either.
func RawDir() string {
	if v := os.Getenv("BC2_CAPTURES_DIR"); v != "" {
		return pyPath(v)
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		return join(la, "BC2Replay", "captures")
	}
	return ""
}

func lockPath() string { return join(WorkDir(), ".v3_build.lock") }

// complete is all(<folder>/<stem>.<ext> is a file) over the 9 extensions.
func complete(folder, stem string) bool {
	for _, e := range EXTS {
		if !isFile(join(folder, stem+"."+e)) {
			return false
		}
	}
	return true
}
