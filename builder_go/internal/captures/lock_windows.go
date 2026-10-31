package captures

import "syscall"

// pidAlive: OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION) + GetExitCodeProcess == STILL_ACTIVE; access denied = alive.
func pidAlive(pid int) bool {
	h, err := syscall.OpenProcess(0x1000, false, uint32(pid))
	if err != nil {
		return err == syscall.ERROR_ACCESS_DENIED
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == 259
}
