package sysevents

import "testing"

func TestSessionUnlockedString(t *testing.T) {
	if got, want := SessionUnlocked.String(), "session unlock"; got != want {
		t.Errorf("SessionUnlocked.String() = %q, want %q", got, want)
	}
}
