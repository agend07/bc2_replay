//go:build !windows

package captures

import (
	"errors"
	"os"
	"syscall"
)

// pidAlive is os.kill(pid, 0): ESRCH = gone, EPERM = exists.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
