package main

import (
	"testing"
	"time"
)

// A login session begins after the boot it runs in, and not later than now.
func TestLoginTimeIsAfterBootAndNotInTheFuture(t *testing.T) {
	pending(t)
	login, boot := loginTime(), bootTime()
	if !login.After(boot) {
		t.Fatalf("login session began %v, want after boot %v", login, boot)
	}
	if now := time.Now(); login.After(now) {
		t.Fatalf("login session began %v, after now %v", login, now)
	}
}
