package event

import (
	"testing"
	"time"
)

func TestName(t *testing.T) {
	cases := map[Event]string{
		WindowMoved{}: "window moved", DisplayReconfigured{}: "display reconfigured",
		SessionUnlocked{}: "session unlocked", PoweringOff{}: "powering off", OverviewEnded{}: "overview ended",
	}
	for e, want := range cases {
		if got := Name(e); got != want {
			t.Errorf("Name(%T) = %q, want %q", e, got, want)
		}
	}
}

func TestStamp(t *testing.T) {
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	var e Event = WindowAttached{Stamp: Stamp{At: at}, Window: 7, Space: 9}
	if !e.When().Equal(at) {
		t.Fatalf("When = %v", e.When())
	}
	if Now().At.IsZero() {
		t.Fatal("Now is zero")
	}
}
