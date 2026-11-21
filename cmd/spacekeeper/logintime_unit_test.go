package main

import (
	"testing"
	"time"
)

// A login clock keeps only a time read from the console entry: while there is
// none it answers with the fallback and reads again next time; once it has
// one it stops reading.
func TestLoginClockKeepsOnlyAConsoleEntry(t *testing.T) {
	boot := time.Unix(1794325436, 0)
	entries := []int64{0, 1795232373}
	reads := 0
	c := &loginClock{
		read: func() int64 {
			reads++
			if len(entries) == 0 {
				return 0
			}
			e := entries[0]
			entries = entries[1:]
			return e
		},
		fallback: func() time.Time { return boot },
	}
	if got, console := c.start(); !got.Equal(boot) || console {
		t.Fatalf("before the console entry: %v, %v; want %v from the fallback", got, console, boot)
	}
	want := time.Unix(1795232373, 0)
	for i := 0; i < 2; i++ {
		if got, console := c.start(); !got.Equal(want) || !console {
			t.Fatalf("call %d: %v, %v; want %v from the console entry", i+2, got, console, want)
		}
	}
	if reads != 2 {
		t.Fatalf("reads = %d, want 2: a console entry once read is kept", reads)
	}
}
