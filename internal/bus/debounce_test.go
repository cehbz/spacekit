package bus

import (
	"testing"
	"time"
)

func TestDebounceRunsPerKey(t *testing.T) {
	d := NewDebounce(500 * time.Millisecond)
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	if !d.Note(uint32(1), t0) {
		t.Fatal("first note of a key should be leading")
	}
	if d.Note(uint32(1), t0.Add(100*time.Millisecond)) {
		t.Fatal("a note inside a run is not leading")
	}
	d.Note(uint32(2), t0.Add(300*time.Millisecond))
	d.Note(uint32(1), t0.Add(400*time.Millisecond))
	if next, ok := d.Next(); !ok || !next.Equal(t0.Add(800*time.Millisecond)) {
		t.Fatalf("Next = %v %v, want key 2's deadline at +800ms", next, ok)
	}
	if due := d.Due(t0.Add(850 * time.Millisecond)); len(due) != 1 || due[0] != uint32(2) {
		t.Fatalf("Due at +850ms = %v, want [2]", due)
	}
	if due := d.Due(t0.Add(time.Second)); len(due) != 1 || due[0] != uint32(1) {
		t.Fatalf("Due at +1s = %v, want [1]", due)
	}
	if d.Open() {
		t.Fatal("nothing should be open")
	}
	d.Note(struct{}{}, t0)
	if !d.Open() {
		t.Fatal("a constant key opens a run like any other")
	}
	d.Clear()
	if _, ok := d.Next(); ok {
		t.Fatal("Clear should drop every run")
	}
}
