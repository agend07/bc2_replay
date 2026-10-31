package captures

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// takeLock is v3_captures.take_lock: "" when this process holds the build lock now, else the refusal text. A lock of
// a dead pid is taken over.
func takeLock() string {
	lock := lockPath()
	_ = os.MkdirAll(WorkDir(), 0o777)
	for range 2 {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
		if err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return "cannot take the build lock " + lock
			}
			pid := 0
			if b, err := os.ReadFile(lock); err == nil {
				if s := strings.TrimSpace(string(b)); s != "" {
					if n, err := strconv.Atoi(s); err == nil {
						pid = n
					}
				}
			}
			if pid != 0 && pidAlive(pid) {
				return "another build is running (pid " + strconv.Itoa(pid) + ")"
			}
			_ = os.Remove(lock)
			continue
		}
		_, _ = f.WriteString(strconv.Itoa(os.Getpid()))
		f.Close()
		return ""
	}
	return "cannot take the build lock " + lock
}

func releaseLock() { _ = os.Remove(lockPath()) }
