package run

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// processStart is when pid started, as the OS says, to tell it from a
// later process given the same pid; "" when it can't be read.
func processStart(pid int) string {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || k.Proc.P_starttime.Sec == 0 {
		return ""
	}
	t := k.Proc.P_starttime
	return strconv.FormatInt(t.Sec, 10) + "." + strconv.FormatInt(int64(t.Usec), 10)
}
