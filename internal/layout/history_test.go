package layout

import (
	"testing"
	"time"
)

func TestStatsPerDisplay(t *testing.T) {
	l := Layout{
		Spaces: []SavedSpace{
			{UUID: "a", DisplayUUID: "D1", Index: 0},
			{UUID: "b", DisplayUUID: "D1", Index: 1},
			{UUID: "c", DisplayUUID: "D2", Index: 0},
		},
		Windows: make([]SavedWindow, 5),
	}
	st := l.Stats()
	if st.Windows != 5 || st.DisplayCount() != 2 {
		t.Fatalf("windows=%d displays=%d, want 5/2", st.Windows, st.DisplayCount())
	}
	if st.Displays[0].DisplayUUID != "D1" || st.Displays[0].Spaces != 2 {
		t.Fatalf("display0 = %+v, want D1/2", st.Displays[0])
	}
	if st.Displays[1].DisplayUUID != "D2" || st.Displays[1].Spaces != 1 {
		t.Fatalf("display1 = %+v, want D2/1", st.Displays[1])
	}
}

func TestRicherDisplaysBeatWindows(t *testing.T) {
	// Two displays with few windows beats one display packed with windows.
	twoDisp := Layout{Spaces: []SavedSpace{{DisplayUUID: "D1"}, {DisplayUUID: "D2"}}, Windows: make([]SavedWindow, 3)}
	oneDisp := Layout{Spaces: []SavedSpace{{DisplayUUID: "D1"}}, Windows: make([]SavedWindow, 30)}
	if !Richer(twoDisp, oneDisp) {
		t.Fatal("more displays should rank richer than more windows")
	}
}

func TestRicherWindowsThenRecency(t *testing.T) {
	older := Layout{Spaces: []SavedSpace{{DisplayUUID: "D1"}}, Windows: make([]SavedWindow, 10), SavedAt: time.Unix(100, 0)}
	newer := Layout{Spaces: []SavedSpace{{DisplayUUID: "D1"}}, Windows: make([]SavedWindow, 10), SavedAt: time.Unix(200, 0)}
	if !Richer(newer, older) {
		t.Fatal("equal displays+windows should break ties by recency")
	}
	more := Layout{Spaces: []SavedSpace{{DisplayUUID: "D1"}}, Windows: make([]SavedWindow, 11), SavedAt: time.Unix(100, 0)}
	if !Richer(more, newer) {
		t.Fatal("more windows should beat more recent")
	}
}

// mkSnap builds a layout saved at savedAt during a session that booted at
// bootedAt (zero bootedAt = legacy snapshot without the field).
func mkSnap(savedAt, bootedAt time.Time, windows int) Layout {
	return Layout{
		SavedAt:  savedAt,
		BootedAt: bootedAt,
		Spaces:   []SavedSpace{{UUID: "s", DisplayUUID: "D1", Index: 0}},
		Windows:  make([]SavedWindow, windows),
	}
}

func TestDefaultRestorePrefersSettledPreviousBoot(t *testing.T) {
	boot := time.Unix(10000, 0) // current session boot
	prevBoot := time.Unix(1000, 0)
	settle := 10 * time.Minute
	ls := []Layout{
		mkSnap(boot.Add(2*time.Minute), boot, 23),      // this boot: scramble
		mkSnap(boot.Add(-5*time.Minute), prevBoot, 28), // previous boot, settled (booted long before)
		mkSnap(boot.Add(-3*time.Hour), prevBoot, 30),   // previous boot, settled, older
	}
	if got := DefaultRestoreIndex(ls, boot, settle); got != 1 {
		t.Fatalf("got index %d, want 1 (newest settled previous-boot)", got)
	}
}

func TestDefaultRestoreSkipsUnsettledPreviousBoot(t *testing.T) {
	boot := time.Unix(100000, 0)
	settle := 10 * time.Minute
	prevBoot := time.Unix(90000, 0)
	olderBoot := time.Unix(1000, 0)
	ls := []Layout{
		// previous boot but saved 2 min after ITS boot: a quick-reboot scramble
		mkSnap(prevBoot.Add(2*time.Minute), prevBoot, 23),
		// older session, saved hours into it: settled
		mkSnap(olderBoot.Add(5*time.Hour), olderBoot, 28),
	}
	if got := DefaultRestoreIndex(ls, boot, settle); got != 1 {
		t.Fatalf("got index %d, want 1 (settled beats newer-but-unsettled)", got)
	}
}

func TestDefaultRestoreFallsBackToUnsettledThenNewest(t *testing.T) {
	boot := time.Unix(100000, 0)
	settle := 10 * time.Minute
	prevBoot := time.Unix(90000, 0)
	onlyUnsettled := []Layout{
		mkSnap(boot.Add(1*time.Minute), boot, 5),
		mkSnap(prevBoot.Add(2*time.Minute), prevBoot, 23),
	}
	if got := DefaultRestoreIndex(onlyUnsettled, boot, settle); got != 1 {
		t.Fatalf("got %d, want 1 (unsettled previous-boot beats this-boot)", got)
	}
	onlyThisBoot := []Layout{
		mkSnap(boot.Add(1*time.Minute), boot, 5),
		mkSnap(boot.Add(3*time.Minute), boot, 7),
	}
	if got := DefaultRestoreIndex(onlyThisBoot, boot, settle); got != 1 {
		t.Fatalf("got %d, want 1 (newest overall as last resort)", got)
	}
}

func TestDefaultRestoreLegacySnapshotsAreSettled(t *testing.T) {
	boot := time.Unix(100000, 0)
	ls := []Layout{
		mkSnap(boot.Add(-1*time.Minute), time.Time{}, 28), // legacy, previous boot
		mkSnap(boot.Add(1*time.Minute), time.Time{}, 23),  // legacy, this boot
	}
	if got := DefaultRestoreIndex(ls, boot, 10*time.Minute); got != 0 {
		t.Fatalf("got %d, want 0 (legacy previous-boot treated as settled)", got)
	}
}

func TestDefaultRestoreEdgeCases(t *testing.T) {
	if got := DefaultRestoreIndex(nil, time.Unix(1, 0), time.Minute); got != -1 {
		t.Fatalf("empty: got %d, want -1", got)
	}
	// Zero boot (sysctl failed): tier 3, newest overall.
	ls := []Layout{
		mkSnap(time.Unix(100, 0), time.Time{}, 1),
		mkSnap(time.Unix(200, 0), time.Time{}, 1),
	}
	if got := DefaultRestoreIndex(ls, time.Time{}, time.Minute); got != 1 {
		t.Fatalf("zero boot: got %d, want 1 (newest)", got)
	}
}

func TestSignatureOrderIndependent(t *testing.T) {
	a := Layout{
		SavedAt: time.Unix(1, 0),
		Spaces:  []SavedSpace{{UUID: "x", DisplayUUID: "D1", Index: 0}, {UUID: "y", DisplayUUID: "D1", Index: 1}},
		Windows: []SavedWindow{{BundleID: "com.a", Title: "1"}, {BundleID: "com.b", Title: "2"}},
	}
	b := Layout{
		SavedAt: time.Unix(999, 0), // different timestamp must not matter
		Spaces:  []SavedSpace{{UUID: "y", DisplayUUID: "D1", Index: 1}, {UUID: "x", DisplayUUID: "D1", Index: 0}},
		Windows: []SavedWindow{{BundleID: "com.b", Title: "2"}, {BundleID: "com.a", Title: "1"}},
	}
	if a.Signature() != b.Signature() {
		t.Fatal("reordered identical content should share a signature")
	}
}

func TestSignatureDistinguishesContent(t *testing.T) {
	a := Layout{Windows: []SavedWindow{{BundleID: "com.a", Title: "1", SpaceUUID: "s1"}}}
	b := Layout{Windows: []SavedWindow{{BundleID: "com.a", Title: "1", SpaceUUID: "s2"}}}
	if a.Signature() == b.Signature() {
		t.Fatal("different space assignment should change the signature")
	}
}
