package main

import (
	"time"

	"golang.org/x/sys/unix"
)

// bootTime returns when the current session booted, or zero if the kernel
// won't say (callers then fall back to newest-snapshot selection).
func bootTime() time.Time {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1000)
}

// processStart returns when a process started, in unix microseconds, or 0 if
// the kernel will not say. It identifies one run of an app.
func processStart(pid int) int64 {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0
	}
	tv := kp.Proc.P_starttime
	return int64(tv.Sec)*1_000_000 + int64(tv.Usec)
}
