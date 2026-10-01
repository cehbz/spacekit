package arrangement

import (
	"testing"

	"github.com/cehbz/spacekit/internal/layout"
)

var (
	full   = layout.Rect{X: 0, Y: 30, W: 1280, H: 1410}
	shrunk = layout.Rect{X: 0, Y: 517, W: 735, H: 923}
)

func TestInPlaceHysteresis(t *testing.T) {
	p := Placement{Space: "S1", Frame: full}
	cases := []struct {
		name string
		q    Placement
		want bool
	}{
		{"identical", Placement{"S1", full}, true},
		{"three points off on every edge", Placement{"S1", layout.Rect{X: 3, Y: 27, W: 1280, H: 1410}}, true},
		{"width one point narrower", Placement{"S1", layout.Rect{X: 0, Y: 30, W: 1279, H: 1410}}, true},
		{"four points off", Placement{"S1", layout.Rect{X: 4, Y: 30, W: 1280, H: 1410}}, false},
		{"far edge four points off", Placement{"S1", layout.Rect{X: 0, Y: 30, W: 1284, H: 1410}}, false},
		{"shrunk", Placement{"S1", shrunk}, false},
		{"other space, same frame", Placement{"S2", full}, false},
	}
	for _, c := range cases {
		if got := p.InPlace(c.q); got != c.want {
			t.Errorf("%s: InPlace = %v, want %v", c.name, got, c.want)
		}
	}
}

func rec(binding uint32, space string, f layout.Rect) Recorded {
	return Recorded{Window: int64(binding) + 100, Binding: binding, Placed: true, Placement: Placement{space, f}}
}

func saw(binding uint32, space string, f layout.Rect) Seen {
	return Seen{Binding: binding, PID: 1, App: "A", Placement: Placement{space, f}}
}

func vis(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func kinds(ds []Decision) []Kind {
	var ks []Kind
	for _, d := range ds {
		ks = append(ks, d.Kind)
	}
	return ks
}

func wantKinds(t *testing.T, ds []Decision, want ...Kind) {
	t.Helper()
	got := kinds(ds)
	if len(got) != len(want) {
		t.Fatalf("decisions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decisions = %v, want %v", got, want)
		}
	}
}

func TestDecideFirstSightingAdopts(t *testing.T) {
	ds := Decide(nil, Look{Windows: []Seen{saw(1, "S1", full)}, Visible: vis("S9")})
	wantKinds(t, ds, Adopt)
	if ds[0].Window != 0 {
		t.Fatalf("a never-seen window has no store identity yet, got %d", ds[0].Window)
	}
}

func TestDecideBoundWithoutPlacementAdoptsForTheSameWindow(t *testing.T) {
	r := Recorded{Window: 7, Binding: 1}
	ds := Decide([]Recorded{r}, Look{Windows: []Seen{saw(1, "S1", full)}, Visible: vis("S1")})
	wantKinds(t, ds, Adopt)
	if ds[0].Window != 7 {
		t.Fatalf("Window = %d, want 7", ds[0].Window)
	}
}

func TestDecideInPlaceIsSilent(t *testing.T) {
	jitter := layout.Rect{X: 1, Y: 30, W: 1279, H: 1410}
	ds := Decide([]Recorded{rec(1, "S1", full)}, Look{Windows: []Seen{saw(1, "S1", jitter)}, Visible: vis("S1")})
	wantKinds(t, ds)
}

func TestDecideChangeOnAVisibleSpaceIsAdopted(t *testing.T) {
	ds := Decide([]Recorded{rec(1, "S1", full)}, Look{Windows: []Seen{saw(1, "S1", shrunk)}, Visible: vis("S1")})
	wantKinds(t, ds, Adopt)
	if ds[0].Window != 101 {
		t.Fatalf("Window = %d, want 101", ds[0].Window)
	}
}

func TestDecideMoveOffAVisibleSpaceIsAdopted(t *testing.T) {
	ds := Decide([]Recorded{rec(1, "S1", full)}, Look{Windows: []Seen{saw(1, "S2", full)}, Visible: vis("S1")})
	wantKinds(t, ds, Adopt)
}

func TestDecideChangeOnAnUnseenSpaceIsOwed(t *testing.T) {
	ds := Decide([]Recorded{rec(1, "S1", full)}, Look{Windows: []Seen{saw(1, "S1", shrunk)}, Visible: vis("S9")})
	wantKinds(t, ds, Owe, Repair)
	if ds[1].Move || !ds[1].Resize || ds[1].Want.Frame != full {
		t.Fatalf("on the right space at the wrong frame: resize, wherever its space is: %+v", ds[1])
	}
	if p, ok := ds[1].Progress(); !ok || p != Attempted {
		t.Fatalf("Progress = %v %v, want Attempted", p, ok)
	}
}

func TestDecideUnseenSpaceChangeIsMovedBack(t *testing.T) {
	ds := Decide([]Recorded{rec(1, "S1", full)}, Look{Windows: []Seen{saw(1, "S2", full)}, Visible: vis("S9")})
	wantKinds(t, ds, Owe, Repair)
	if !ds[1].Move || ds[1].Resize || ds[1].Want.Space != "S1" {
		t.Fatalf("want a move back to S1 only, got %+v", ds[1])
	}
	if p, ok := ds[1].Progress(); !ok || p != Attempted {
		t.Fatalf("frame already right, so the move completes the repair: got %v %v", p, ok)
	}
}

func owedRec(binding uint32, space string, f layout.Rect, p Progress) Recorded {
	r := rec(binding, space, f)
	r.Owed, r.Progress = true, p
	return r
}

func TestDecideOwedSeenInPlaceIsReleased(t *testing.T) {
	ds := Decide([]Recorded{owedRec(1, "S1", full, Untried)}, Look{Windows: []Seen{saw(1, "S1", full)}, Visible: vis("S9")})
	wantKinds(t, ds, Release)
}

func TestDecideOwedIsResizedWhereverItsSpaceIs(t *testing.T) {
	ds := Decide([]Recorded{owedRec(1, "S1", full, Untried)}, Look{Windows: []Seen{saw(1, "S1", shrunk)}, Visible: vis("S9")})
	wantKinds(t, ds, Repair)
	if ds[0].Move || !ds[0].Resize || ds[0].Want.Frame != full {
		t.Fatalf("want a resize to the recorded frame, got %+v", ds[0])
	}
	if p, _ := ds[0].Progress(); p != Attempted {
		t.Fatalf("Progress = %v, want Attempted", p)
	}
}

func TestDecideOwedDriftingUnseenIsResized(t *testing.T) {
	drift := layout.Rect{X: 0, Y: 33, W: 735, H: 923}
	ds := Decide([]Recorded{owedRec(1, "S1", full, Untried)}, Look{Windows: []Seen{saw(1, "S1", drift)}, Visible: vis("S9")})
	wantKinds(t, ds, Repair)
	if ds[0].Move || !ds[0].Resize {
		t.Fatalf("want a resize, got %+v", ds[0])
	}
}

func TestDecideOwedOnTheWrongSpaceAtTheWrongFrameIsMovedAndResized(t *testing.T) {
	ds := Decide([]Recorded{owedRec(1, "S1", full, Untried)}, Look{Windows: []Seen{saw(1, "S2", shrunk)}, Visible: vis("S9")})
	wantKinds(t, ds, Repair)
	if !ds[0].Move || !ds[0].Resize {
		t.Fatalf("want move and resize, got %+v", ds[0])
	}
	if p, _ := ds[0].Progress(); p != Attempted {
		t.Fatalf("Progress = %v, want Attempted", p)
	}
}

func TestDecideGivesUpAfterAFullAttempt(t *testing.T) {
	ds := Decide([]Recorded{owedRec(1, "S1", full, Attempted)}, Look{Windows: []Seen{saw(1, "S1", shrunk)}, Visible: vis("S1")})
	wantKinds(t, ds, GiveUp)
}

func TestDecideGivesUpWhenStillOnTheWrongSpaceAfterAnAttempt(t *testing.T) {
	ds := Decide([]Recorded{owedRec(1, "S1", full, Attempted)}, Look{Windows: []Seen{saw(1, "S2", full)}, Visible: vis("S9")})
	wantKinds(t, ds, GiveUp)
}

func TestDecideIgnoresRecordedWindowsNotSeen(t *testing.T) {
	ds := Decide([]Recorded{rec(1, "S1", full)}, Look{Visible: vis("S1")})
	wantKinds(t, ds)
}
