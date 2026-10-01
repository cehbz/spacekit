package arrangement

import (
	"testing"

	"github.com/cehbz/spacekit/internal/layout"
)

const thisBoot = int64(1790652986)

func fresh(wid uint32, title string, f layout.Rect) Seen {
	return Seen{Binding: wid, PID: 5, Bundle: "com.google.Chrome", App: "Google Chrome", Title: title, Run: 200,
		Placement: Placement{Space: "S5", Frame: f}}
}

// left is a stored Chrome window from the app's previous run in this boot.
func left(win int64, title string, space string, f layout.Rect) Stored {
	return Stored{Window: win, Bundle: "com.google.Chrome", App: "Google Chrome", Title: title,
		Placement: Placement{Space: space, Frame: f}, Boot: thisBoot, Binding: uint32(win), Run: 100}
}

var (
	rightHalf = layout.Rect{X: 1280, Y: 30, W: 1280, H: 1410}
	odd       = layout.Rect{X: 300, Y: 200, W: 900, H: 700}
)

func TestBindByTitle(t *testing.T) {
	got := Bind([]Seen{fresh(900, "autobrr", full)}, []Stored{left(1, "autobrr", "S2", rightHalf), left(2, "Review", "S3", full)}, thisBoot, map[uint32]bool{900: true})
	if len(got) != 1 || got[900] != 1 {
		t.Fatalf("got %v, want 900->1", got)
	}
}

func TestBindTitleBeatsSharedFrame(t *testing.T) {
	// Both stored windows have the fresh window's frame; only one has its title.
	st := []Stored{left(1, "autobrr", "S2", full), left(2, "Review", "S3", full)}
	got := Bind([]Seen{fresh(900, "Review", full)}, st, thisBoot, map[uint32]bool{900: true})
	if got[900] != 2 {
		t.Fatalf("got %v, want 900->2", got)
	}
}

func TestBindByFrameWhenTheTitleChanged(t *testing.T) {
	st := []Stored{left(1, "old title", "S2", odd), left(2, "Review", "S3", full)}
	got := Bind([]Seen{fresh(900, "renamed tab", odd)}, st, thisBoot, map[uint32]bool{900: true})
	if got[900] != 1 {
		t.Fatalf("got %v, want 900->1", got)
	}
}

func TestBindNothingWithoutEvidence(t *testing.T) {
	got := Bind([]Seen{fresh(900, "brand new", odd)}, []Stored{left(1, "autobrr", "S2", full)}, thisBoot, map[uint32]bool{900: true})
	if len(got) != 0 {
		t.Fatalf("no shared title or frame: got %v", got)
	}
}

func TestBindNothingWhenAmbiguous(t *testing.T) {
	// Two untitled stored windows with the same frame: no telling which.
	st := []Stored{left(1, "", "S2", odd), left(2, "", "S3", odd)}
	if got := Bind([]Seen{fresh(900, "", odd)}, st, thisBoot, map[uint32]bool{900: true}); len(got) != 0 {
		t.Fatalf("ambiguous stored windows: got %v", got)
	}
	// Two fresh windows with the title of one stored window.
	two := []Seen{fresh(900, "New Tab", full), fresh(901, "New Tab", rightHalf)}
	if got := Bind(two, []Stored{left(1, "New Tab", "S2", odd)}, thisBoot, map[uint32]bool{900: true, 901: true}); len(got) != 0 {
		t.Fatalf("ambiguous fresh windows: got %v", got)
	}
}

func TestBindTitleAndFrameResolvesTwins(t *testing.T) {
	// Same title twice; frames tell them apart.
	st := []Stored{left(1, "New Tab", "S2", full), left(2, "New Tab", "S3", rightHalf)}
	two := []Seen{fresh(900, "New Tab", rightHalf), fresh(901, "New Tab", full)}
	got := Bind(two, st, thisBoot, map[uint32]bool{900: true, 901: true})
	if got[900] != 2 || got[901] != 1 {
		t.Fatalf("got %v, want 900->2 901->1", got)
	}
}

func TestBindSkipsLiveAndSameRunAndOtherApps(t *testing.T) {
	liveOne := left(1, "autobrr", "S2", full) // still on screen under its id
	sameRun := left(2, "autobrr", "S2", full) // closed during this run: not relaunch debris
	sameRun.Run = 200
	other := left(3, "autobrr", "S2", full)
	other.Bundle, other.App = "com.apple.Safari", "Safari"
	got := Bind([]Seen{fresh(900, "autobrr", full)}, []Stored{liveOne, sameRun, other}, thisBoot, map[uint32]bool{900: true, 1: true})
	if len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestBindAcrossBoots(t *testing.T) {
	prev := left(1, "autobrr", "S2", full)
	prev.Boot, prev.Run = thisBoot-86400, 200 // an earlier boot; a run id cannot collide across boots
	never := Stored{Window: 2, Bundle: "com.google.Chrome", App: "Google Chrome", Title: "Review", Placement: Placement{Space: "S3", Frame: rightHalf}}
	two := []Seen{fresh(900, "autobrr", full), fresh(901, "Review", rightHalf)}
	got := Bind(two, []Stored{prev, never}, thisBoot, map[uint32]bool{900: true, 901: true})
	if got[900] != 1 || got[901] != 2 {
		t.Fatalf("got %v, want 900->1 901->2", got)
	}
}

func TestBindEachStoredWindowOnce(t *testing.T) {
	st := []Stored{left(1, "autobrr", "S2", full)}
	two := []Seen{fresh(900, "autobrr", full), fresh(901, "other", full)}
	got := Bind(two, st, thisBoot, map[uint32]bool{900: true, 901: true})
	if len(got) != 1 || got[900] != 1 {
		t.Fatalf("title and frame beats frame alone: got %v, want only 900->1", got)
	}
}
