package settle

import (
	"testing"
	"time"
)

func TestNoteOpensThenExtends(t *testing.T) {
	t0 := time.Date(2026, 9, 2, 10, 46, 7, 0, time.UTC)
	w := New(10 * time.Second)
	if w.Open() {
		t.Fatal("new window should be closed")
	}
	if !w.Note(t0) {
		t.Fatal("first Note should open the burst")
	}
	if w.Note(t0.Add(6 * time.Second)) {
		t.Fatal("second Note should not report a new burst")
	}
	if got := w.Start(); !got.Equal(t0) {
		t.Fatalf("Start = %v, want %v", got, t0)
	}
	if got, want := w.Deadline(), t0.Add(16*time.Second); !got.Equal(want) {
		t.Fatalf("Deadline = %v, want %v", got, want)
	}
}

func TestCloseResets(t *testing.T) {
	t0 := time.Date(2026, 9, 2, 10, 46, 7, 0, time.UTC)
	w := New(10 * time.Second)
	w.Note(t0)
	w.Close()
	if w.Open() || !w.Deadline().IsZero() {
		t.Fatal("Close should leave the window closed with no deadline")
	}
	if !w.Note(t0.Add(time.Minute)) {
		t.Fatal("Note after Close should open a new burst")
	}
}

func TestResumeKeepsStartAndDefersDeadline(t *testing.T) {
	start := time.Date(2026, 9, 2, 10, 46, 7, 0, time.UTC)
	now := start.Add(30 * time.Second)
	w := New(10 * time.Second)
	w.Resume(start, now)
	if !w.Open() || !w.Start().Equal(start) {
		t.Fatal("Resume should open a burst at the given start")
	}
	if got, want := w.Deadline(), now.Add(10*time.Second); !got.Equal(want) {
		t.Fatalf("Deadline = %v, want %v", got, want)
	}
}
