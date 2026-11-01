package run

import (
	"os"
	"strconv"
	"strings"
)

// processStart is when pid started, in clock ticks since boot (field 22
// of /proc/<pid>/stat), to tell it from a later process given the same
// pid; "" when it can't be read.
func processStart(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	// The command name, field 2, may hold spaces: count from after it.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return ""
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return ""
	}
	return f[19]
}
