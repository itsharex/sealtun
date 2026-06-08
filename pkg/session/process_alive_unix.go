//go:build !windows

package session

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	return process.Signal(syscall.Signal(0)) == nil
}

// ProcessStartToken returns a best-effort, stable identifier of the process
// instance behind pid, used to detect PID reuse. On Linux it derives from the
// process start time in /proc/<pid>/stat (field 22). On platforms without a
// procfs (e.g. darwin) it returns an empty string, in which case callers fall
// back to a PID-only liveness check.
func ProcessStartToken(pid int) string {
	if pid <= 0 {
		return ""
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") // #nosec G304 -- procfs path built from an integer pid.
	if err != nil {
		return ""
	}
	// The comm field (field 2) is wrapped in parentheses and may contain
	// spaces, so split on the last ')' before tokenizing the remainder.
	content := string(data)
	rparen := strings.LastIndex(content, ")")
	if rparen < 0 || rparen+2 >= len(content) {
		return ""
	}
	fields := strings.Fields(content[rparen+2:])
	// After the comm field, field 3 is state; starttime is field 22 overall,
	// which is index 19 of the post-comm slice (0-based).
	const starttimeIndexAfterComm = 19
	if len(fields) <= starttimeIndexAfterComm {
		return ""
	}
	return fields[starttimeIndexAfterComm]
}
