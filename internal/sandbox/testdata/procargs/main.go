// SPDX-License-Identifier: Elastic-2.0

//go:build darwin

// Command procargs is the test helper for named gap C1
// (TestJailCanReadAnotherProcessesStartupEnvironment). It is a USER-BUILT
// binary on purpose: Apple platform binaries are protected from the read
// it demonstrates, and corral is not one.
//
//	procargs sleep        stay alive, so a jailed reader has a target
//	procargs read <pid>   print pid's exec-time arguments and environment
//	                      (sysctl KERN_PROCARGS2), one string per line
package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "sleep" {
		time.Sleep(5 * time.Minute)
		return
	}
	if len(os.Args) != 3 || os.Args[1] != "read" {
		fmt.Fprintln(os.Stderr, "usage: procargs sleep | procargs read <pid>")
		os.Exit(2)
	}
	pid, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sysctl kern.procargs2:", err)
		os.Exit(1)
	}
	os.Stdout.Write(bytes.ReplaceAll(raw, []byte{0}, []byte{'\n'}))
}
