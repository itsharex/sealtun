//go:build windows

package session

import (
	"strconv"
	"syscall"
)

const stillActive = 259

func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)

	var exitCode uint32
	if err := syscall.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}

	return exitCode == stillActive
}

// ProcessStartToken returns a best-effort, stable identifier of the process
// instance behind pid, used to detect PID reuse. On Windows it derives from the
// process creation time. If the creation time cannot be queried it returns an
// empty string, in which case callers fall back to a PID-only liveness check.
func ProcessStartToken(pid int) string {
	if pid <= 0 {
		return ""
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(handle)

	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return ""
	}
	return strconv.FormatInt(creation.Nanoseconds(), 10)
}
