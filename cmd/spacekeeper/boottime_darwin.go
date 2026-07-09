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
