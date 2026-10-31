// SPDX-License-Identifier: Elastic-2.0

package hoststat

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const supported = true

// readLoad is the 1-minute load average, the first field of loadavg.
func readLoad(root string) (float64, error) {
	path := filepath.Join(root, "loadavg")
	// #nosec G304 -- root is the constant "/proc" in every production path
	// (StartContext); only this package's tests pass another, a temp dir.
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, unreadable(path, err)
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, unreadable(path, fmt.Errorf("empty"))
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, unreadable(path, err)
	}
	return v, nil
}

// readMemAvailable is meminfo's MemAvailable line, in bytes (the file states
// kB).
func readMemAvailable(root string) (int64, error) {
	path := filepath.Join(root, "meminfo")
	// #nosec G304 -- same as readLoad: a constant "/proc" outside tests.
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, unreadable(path, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(line, "MemAvailable:")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			break
		}
		kb, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return 0, unreadable(path, err)
		}
		return kb * 1024, nil
	}
	return 0, unreadable(path, fmt.Errorf("no MemAvailable line"))
}

// usage is the CPU time (user plus system, this process and its waited
// children) in milliseconds and the children's largest resident set in bytes
// (Linux reports ru_maxrss in kB).
func usage() (cpuMillis, maxRSSBytes int64, err error) {
	var self, kids syscall.Rusage
	if err = syscall.Getrusage(syscall.RUSAGE_SELF, &self); err != nil {
		return 0, 0, fmt.Errorf("getrusage(self): %v", err)
	}
	if err = syscall.Getrusage(syscall.RUSAGE_CHILDREN, &kids); err != nil {
		return 0, 0, fmt.Errorf("getrusage(children): %v", err)
	}
	tv := func(t syscall.Timeval) int64 { return int64(t.Sec)*1000 + int64(t.Usec)/1000 }
	cpuMillis = tv(self.Utime) + tv(self.Stime) + tv(kids.Utime) + tv(kids.Stime)
	return cpuMillis, int64(kids.Maxrss) * 1024, nil
}
