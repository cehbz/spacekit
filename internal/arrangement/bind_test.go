package arrangement

import (
	"testing"

	"github.com/cehbz/spacekit/internal/layout"
)

const thisBoot = int64(1790652986)

func fresh(wid uint32, title string, f layout.Rect) Seen {
	return Seen{Binding: wid, PID: 5, Bundle: "com.google.Chrome", App: "Google Chrome", Title: title,
		Placement: Placement{Space: "S5", Frame: f}}
}

// left is a stored Chrome window bound earlier in this boot.
func left(win int64, title string, space string, f layout.Rect) Stored {
	return Stored{Window: win, Bundle: "com.google.Chrome", App: "Google Chrome", Title: title,
		Placement: Placement{Space: space, Frame: f}, Placed: true, Boot: thisBoot, Binding: uint32(win)}
}

// elsewhere is a stored Chrome window with a record only in another
// arrangement: no placement here, last bound in an earlier boot.
func elsewhere(win int64, title string) Stored {
	return Stored{Window: win, Bundle: "com.google.Chrome", App: "Google Chrome", Title: title,
		Boot: thisBoot - 86400, Binding: uint32(win)}
}

func TestBindUnplacedCandidateOnTitle(t *testing.T) {
	got := Bind([]Seen{fresh(900, "autobrr", full)}, []Stored{elsewhere(1, "autobrr")}, thisBoot, map[uint32]bool{900: true})
	if got[900] != 1 {
		t.Fatalf("got %v, want 900->1: a record from another arrangement binds on title", got)
	}
}

func TestBindUnplacedCandidateNeverOnFrame(t *testing.T) {
	// An unplaced candidate has no frame here; a window near the origin must
	// not match its empty one.
	tiny := layout.Rect{X: 0, Y: 0, W: 2, H: 2}
	// A second fresh window keeps the app alone from deciding.
	two := []Seen{fresh(900, "renamed", tiny), fresh(901, "other", odd)}
	got := Bind(two, []Stored{elsewhere(1, "autobrr")}, thisBoot, map[uint32]bool{900: true, 901: true})
	if len(got) != 0 {
		t.Fatalf("got %v, want nothing: no placement here means no frame evidence", got)
	}
}

func TestBindUnplacedTwinsAreAmbiguous(t *testing.T) {
	st := []Stored{elsewhere(1, "autobrr"), elsewhere(2, "autobrr")}
	got := Bind([]Seen{fresh(900, "autobrr", full)}, st, thisBoot, map[uint32]bool{900: true})
	if len(got) != 0 {
		t.Fatalf("got %v, want nothing: two unplaced records with the title", got)
	}
}

var (
	rightHalf = layout.Rect{X: 1280, Y: 30, W: 1280, H: 1410}
	odd       = layout.Rect{X: 300, Y: 200, W: 900, H: 700}
)

func TestBindByTitle(t *testing.T) {
	got := Bind([]Seen{fresh(900, "autobrr", full)}, []Stored{left(1, "autobrr", "S2", rightHalf), left(2, "Review", "S3", odd)}, thisBoot, map[uint32]bool{900: true})
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
	// Two fresh windows of the app, so the app alone decides nothing either.
	two := []Seen{fresh(900, "brand new", odd), fresh(901, "also new", rightHalf)}
	got := Bind(two, []Stored{left(1, "autobrr", "S2", full)}, thisBoot, map[uint32]bool{900: true, 901: true})
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

func TestBindSkipsLiveAndOtherApps(t *testing.T) {
	liveOne := left(1, "autobrr", "S2", full) // still listed under its id
	other := left(3, "autobrr", "S2", full)
	other.Bundle, other.App = "com.apple.Safari", "Safari"
	got := Bind([]Seen{fresh(900, "autobrr", full)}, []Stored{liveOne, other}, thisBoot, map[uint32]bool{900: true, 1: true})
	if len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestBindInTheSameProcess(t *testing.T) {
	// The app rebuilt its window without restarting: the record whose id
	// left the list is a candidate.
	rebuilt := left(16, "WhatsApp", "S2", full)
	got := Bind([]Seen{fresh(24389, "WhatsApp", odd)}, []Stored{rebuilt}, thisBoot, map[uint32]bool{24389: true})
	if got[24389] != 16 {
		t.Fatalf("got %v, want 24389->16", got)
	}
}

func TestBindNothingBetweenTwoDeadTwins(t *testing.T) {
	st := []Stored{left(1, "WhatsApp", "S2", full), left(2, "WhatsApp", "S3", rightHalf)}
	if got := Bind([]Seen{fresh(900, "WhatsApp", odd)}, st, thisBoot, map[uint32]bool{900: true}); len(got) != 0 {
		t.Fatalf("two dead records with the title: got %v", got)
	}
}

func TestBindAcrossBoots(t *testing.T) {
	prev := left(1, "autobrr", "S2", full)
	prev.Boot = thisBoot - 86400 // an earlier boot; its id says nothing about this one
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

func TestBindTwinsToTheLaterBoot(t *testing.T) {
	// Twins left by earlier failed binds: same title, same frame. The one
	// bound more recently is the window.
	later := left(1, "PocketSmith", "S2", odd)
	later.Boot = thisBoot - 86400
	earlier := left(2, "PocketSmith", "S3", odd)
	earlier.Boot = thisBoot - 2*86400
	got := Bind([]Seen{fresh(900, "PocketSmith", odd)}, []Stored{later, earlier}, thisBoot, map[uint32]bool{900: true})
	if len(got) != 1 || got[900] != 1 {
		t.Fatalf("got %v, want 900->1: the twin bound in the later boot", got)
	}
}

func TestBindTitleIgnoresCountPrefix(t *testing.T) {
	got := Bind([]Seen{fresh(900, "(3) Facebook", full)}, []Stored{left(1, "(1) Facebook", "S2", odd)}, thisBoot, map[uint32]bool{900: true})
	if got[900] != 1 {
		t.Fatalf("got %v, want 900->1: titles differ only in the unread count", got)
	}
}

func TestBindTitleIgnoresInfixedCount(t *testing.T) {
	got := Bind([]Seen{fresh(900, "Inbox (34) - x", full)}, []Stored{left(1, "Inbox (2) - x", "S2", odd)}, thisBoot, map[uint32]bool{900: true})
	if got[900] != 1 {
		t.Fatalf("got %v, want 900->1: titles differ only in the infixed count", got)
	}
}

func TestBindTitleIgnoresDroppedPairCount(t *testing.T) {
	got := Bind([]Seen{fresh(900, "Home | FetLife", full)}, []Stored{left(1, "(6/0) Home | FetLife", "S2", odd)}, thisBoot, map[uint32]bool{900: true})
	if got[900] != 1 {
		t.Fatalf("got %v, want 900->1: the stored title had a count the fresh one dropped", got)
	}
}

func TestBindCountRuleKeepsOtherText(t *testing.T) {
	for _, c := range []struct{ fresh, stored string }{
		{"New Tab", "(2) New Tab Settings"}, // differ beyond the count
		{"Foo", "(beta) Foo"},               // not a count
		{"(3)", "(1)"},                      // nothing left to compare
	} {
		// A second fresh window keeps the app alone from deciding.
		got := Bind([]Seen{fresh(900, c.fresh, full), fresh(901, "other", rightHalf)}, []Stored{left(1, c.stored, "S2", odd)}, thisBoot, map[uint32]bool{900: true, 901: true})
		if len(got) != 0 {
			t.Fatalf("%q vs %q: got %v, want nothing", c.fresh, c.stored, got)
		}
	}
}

func TestBindLaterBootBreaksTitleOnlyAndFrameOnlyTies(t *testing.T) {
	// Two dead records from different boots that share only a title, or only
	// a frame: the one bound in the later boot is the window.
	later := left(1, "New Tab", "S2", full)
	later.Boot = thisBoot - 86400
	earlier := left(2, "New Tab", "S3", rightHalf)
	earlier.Boot = thisBoot - 2*86400
	if got := Bind([]Seen{fresh(900, "New Tab", odd)}, []Stored{later, earlier}, thisBoot, map[uint32]bool{900: true}); got[900] != 1 {
		t.Fatalf("title only: got %v, want 900->1, the later boot", got)
	}
	earlier.Title = "Review"
	earlier.Frame = full
	if got := Bind([]Seen{fresh(900, "Settings", full)}, []Stored{later, earlier}, thisBoot, map[uint32]bool{900: true}); got[900] != 1 {
		t.Fatalf("frame only: got %v, want 900->1, the later boot", got)
	}
}

// A fullscreen window has no frame to compare: a fullscreen candidate binds
// a fullscreen window on title only.
func TestBindFullscreenOffersNoFrameEvidence(t *testing.T) {
	stored := []Stored{{Window: 1, Bundle: "com.google.Chrome", App: "Google Chrome", Title: "autobrr",
		Placement: FullscreenOn("D1"), Placed: true, Boot: thisBoot - 86400, Binding: 1}}
	renamed := Seen{Binding: 900, PID: 5, Bundle: "com.google.Chrome", App: "Google Chrome", Title: "renamed", Placement: FullscreenOn("D1")}
	other := fresh(901, "other", odd) // a second fresh window, so the app alone decides nothing
	if got := Bind([]Seen{renamed, other}, stored, thisBoot, map[uint32]bool{900: true, 901: true}); len(got) != 0 {
		t.Fatalf("got %v, want nothing: fullscreen windows offer no frame evidence", got)
	}
	same := renamed
	same.Title = "autobrr"
	if got := Bind([]Seen{same}, stored, thisBoot, map[uint32]bool{900: true}); got[900] != 1 {
		t.Fatalf("got %v, want 900->1 on title", got)
	}
}

// Frame evidence outranks a title match (user decision 2026-11-21): Chrome's
// loading title matched a stale popup record while the frame named the real
// window.
func TestBindFrameBeatsTitle(t *testing.T) {
	st := []Stored{left(165, "edgeplay Mail", "S1", odd), left(371, "Follow The Money - edgeplay Mail", "S1", full)}
	got := Bind([]Seen{fresh(900, "edgeplay Mail", full)}, st, thisBoot, map[uint32]bool{900: true})
	if got[900] != 371 {
		t.Fatalf("got %v, want 900->371: the frame, not the loading title", got)
	}
}

// Stale twins at one frame (VS Code's old windows) are narrowed to the one
// bound in the later boot when a renamed window reappears at that frame.
func TestBindFrameTiesNarrowToTheLaterBoot(t *testing.T) {
	later := left(23, "decisions.md — monitor", "S3", full)
	later.Boot = thisBoot - 86400
	earlier := left(21, "Release Notes: 1.145.0 — lube", "S3", full)
	earlier.Boot = thisBoot - 2*86400
	got := Bind([]Seen{fresh(900, "Release Notes: 1.147.0 — monitor", full)}, []Stored{later, earlier}, thisBoot, map[uint32]bool{900: true})
	if got[900] != 23 {
		t.Fatalf("got %v, want 900->23: the twin bound in the later boot", got)
	}
}

func TestBindTitleTiesNarrowToTheLaterBoot(t *testing.T) {
	later := left(1, "PocketSmith", "S2", odd)
	later.Boot = thisBoot - 86400
	earlier := left(2, "PocketSmith", "S3", rightHalf)
	earlier.Boot = thisBoot - 2*86400
	got := Bind([]Seen{fresh(900, "PocketSmith", full)}, []Stored{later, earlier}, thisBoot, map[uint32]bool{900: true})
	if got[900] != 1 {
		t.Fatalf("got %v, want 900->1: the record bound in the later boot", got)
	}
}

// A lone fresh window of an app with no title or frame evidence (LINE's
// untitled login window) binds to the app's record bound in the latest boot.
func TestBindLoneWindowOfAnAppOnAppAlone(t *testing.T) {
	line := func(win int64, title string, f layout.Rect, boot int64) Stored {
		return Stored{Window: win, Bundle: "jp.naver.line.mac", App: "LINE", Title: title,
			Placement: Placement{Space: "S5", Frame: f}, Placed: true, Boot: boot, Binding: uint32(win)}
	}
	login := Seen{Binding: 900, PID: 7, Bundle: "jp.naver.line.mac", App: "LINE", Title: "",
		Placement: Placement{Space: "S1", Frame: layout.Rect{X: 588, Y: 341, W: 604, H: 421}}}
	main := line(1, "LINE", layout.Rect{X: 588, Y: 341, W: 1280, H: 705}, thisBoot-86400)
	old := line(341, "", layout.Rect{X: 1021, Y: 540, W: 604, H: 421}, thisBoot-2*86400)
	if got := Bind([]Seen{login}, []Stored{main, old}, thisBoot, map[uint32]bool{900: true}); got[900] != 1 {
		t.Fatalf("got %v, want 900->1: the app's record bound in the later boot", got)
	}
	if got := Bind([]Seen{login}, []Stored{old}, thisBoot, map[uint32]bool{900: true}); got[900] != 341 {
		t.Fatalf("got %v, want 900->341: the app's only record", got)
	}
}

// The app alone counts only for an app's only fresh window: Chrome's many
// windows never bind on it, even when one of them is left without evidence.
func TestBindAppAloneNeedsTheAppsOnlyFreshWindow(t *testing.T) {
	st := []Stored{left(1, "autobrr", "S2", full), left(2, "Review", "S3", rightHalf)}
	two := []Seen{fresh(900, "autobrr", full), fresh(901, "loading…", odd)}
	got := Bind(two, st, thisBoot, map[uint32]bool{900: true, 901: true})
	if len(got) != 1 || got[900] != 1 {
		t.Fatalf("got %v, want only 900->1: 901 has no evidence and is not the app's only window", got)
	}
}

// A twin bound in the same boot as its rival stays ambiguous at every level.
func TestBindSameBootTwinsStayAmbiguousOnAppAlone(t *testing.T) {
	st := []Stored{left(1, "a", "S2", full), left(2, "b", "S3", rightHalf)}
	if got := Bind([]Seen{fresh(900, "new", odd)}, st, thisBoot, map[uint32]bool{900: true}); len(got) != 0 {
		t.Fatalf("got %v, want nothing: two records of one boot", got)
	}
}

// Two same-boot twins on the strongest evidence stay ambiguous, and a record
// with weaker evidence from a later boot does not step in below them.
func TestBindAmbiguityAtAStrongerLevelBlocksWeakerOnes(t *testing.T) {
	twinA := left(1, "PocketSmith", "S2", odd)
	twinA.Boot = thisBoot - 2*86400
	twinB := left(2, "PocketSmith", "S3", odd)
	twinB.Boot = thisBoot - 2*86400
	other := left(3, "Unrelated", "S4", rightHalf)
	other.Boot = thisBoot - 86400
	if got := Bind([]Seen{fresh(900, "PocketSmith", odd)}, []Stored{twinA, twinB, other}, thisBoot, map[uint32]bool{900: true}); len(got) != 0 {
		t.Fatalf("got %v, want nothing: the twins are ambiguous and the later record is weaker evidence", got)
	}
}
