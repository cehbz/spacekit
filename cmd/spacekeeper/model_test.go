package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/store"
)

func tempStore(t *testing.T) *store.Store {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// twoWindows: window 1 on the shown space s1, window 2 on hidden s2.
func twoWindows() *snapshot {
	p := testPicture()
	p.windows = append(p.windows, layout.LiveWindow{ID: 2, OwnerPID: 11, OwnerName: "B", BundleID: "b.app", Title: "two", Frame: layout.Rect{X: 10, Y: 10, W: 200, H: 200}})
	p.winSpace[2] = 200
	p.all = append(p.all, layout.ListedWindow{ID: 2, OwnerName: "B", Title: "two"})
	return p
}

func startModel(t *testing.T) (*bus.Bus, *bus.Subscription) {
	b := bus.New()
	out := b.Subscribe("t", TargetChanged{}, PutWanted{})
	m := newModel(b, tempStore(t))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.run(ctx)
	return b, out
}

func current(p *snapshot) CurrentChanged { return CurrentChanged{Stamp: event.Now(), Current: p} }

// decisionsOf collects decisions from TargetChanged events until n are seen.
func decisionsOf(t *testing.T, out *bus.Subscription, n int) []arrangement.Decision {
	t.Helper()
	var ds []arrangement.Decision
	deadline := time.After(2 * time.Second)
	for len(ds) < n {
		select {
		case e := <-out.C():
			if tc, ok := e.(TargetChanged); ok {
				ds = append(ds, tc.Decisions...)
			}
		case <-deadline:
			t.Fatalf("got %d of %d decisions", len(ds), n)
		}
	}
	return ds
}

func TestModelStartupAdoptsCurrent(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	for _, d := range decisionsOf(t, out, 2) {
		if d.Kind != arrangement.Adopt {
			t.Fatalf("startup decision %v, want adopt", d.Kind)
		}
	}
}

func TestModelWindowQuietJudgesThatWindowOnly(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	moved := twoWindows()
	moved.windows[0].Frame.X = 400 // window 1, shown: the user's
	moved.windows[1].Frame.X = 400 // window 2 too, but nobody asked about it
	b.Publish(current(moved))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1})
	ds := decisionsOf(t, out, 1)
	if d := ds[0]; d.Kind != arrangement.Adopt || d.Seen.Binding != 1 || d.Seen.Frame.X != 400 {
		t.Fatalf("decision %+v", d)
	}
	expectNone(t, out, 100*time.Millisecond)
}

func TestModelHoldDefersJudgement(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(event.OverviewShown{Stamp: event.Now()})
	moved := twoWindows()
	moved.windows[0].Frame.X = 400
	b.Publish(current(moved))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1})
	expectNone(t, out, 100*time.Millisecond)
	b.Publish(event.OverviewEnded{Stamp: event.Now()})
	expectNone(t, out, 100*time.Millisecond) // released only once the screen has reconciled
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "overview ended"})
	if d := decisionsOf(t, out, 1)[0]; d.Seen.Binding != 1 || d.Seen.Frame.X != 400 {
		t.Fatalf("deferred decision %+v", d)
	}
}

func TestModelDisplayQuietOwesAndWantsPut(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	shrunk := twoWindows()
	shrunk.windows[1].Frame = layout.Rect{X: 10, Y: 10, W: 100, H: 100} // hidden window 2 changed: not the user's
	b.Publish(current(shrunk))
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "display quiet"})
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-out.C():
			if pw, ok := e.(PutWanted); ok {
				if len(pw.Decisions) != 1 || pw.Decisions[0].Seen.Binding != 2 || !pw.Decisions[0].Resize {
					t.Fatalf("put wanted %+v", pw.Decisions)
				}
				if pw.Current == nil || len(pw.Spaces) != 2 {
					t.Fatalf("request carries Current %v and %d spaces", pw.Current != nil, len(pw.Spaces))
				}
				return
			}
		case <-deadline:
			t.Fatal("no PutWanted")
		}
	}
}

func TestModelHoldsWhilePuttingThenGivesUp(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	shrunk := twoWindows()
	shrunk.windows[1].Frame = layout.Rect{X: 10, Y: 10, W: 100, H: 100}
	b.Publish(current(shrunk))
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "display quiet"})
	expectEvent(t, out, TargetChanged{}, 2*time.Second)
	expectEvent(t, out, PutWanted{}, 2*time.Second)
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1})
	expectNone(t, out, 100*time.Millisecond) // held while putting
	b.Publish(PutEnded{Stamp: event.Now()})  // the put did not take: Current unchanged
	ds := decisionsOf(t, out, 1)
	if ds[0].Kind != arrangement.GiveUp || ds[0].Seen.Binding != 2 {
		t.Fatalf("after an unconfirmed put: %+v, want give up on window 2", ds[0])
	}
}

// A put that confirmed nothing produced no window events: its PutEnded
// judges the put's windows, and the owed window is given up.
func TestModelPutEndedJudgesPutWindows(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	shrunk := twoWindows()
	shrunk.windows[1].Frame = layout.Rect{X: 10, Y: 10, W: 100, H: 100}
	b.Publish(current(shrunk))
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "display quiet"})
	expectEvent(t, out, TargetChanged{}, 2*time.Second)
	expectEvent(t, out, PutWanted{}, 2*time.Second)
	b.Publish(PutEnded{Stamp: event.Now()}) // nothing confirmed, nothing pending: Current unchanged
	ds := decisionsOf(t, out, 1)
	if len(ds) != 1 || ds[0].Kind != arrangement.GiveUp || ds[0].Seen.Binding != 2 {
		t.Fatalf("after a put that confirmed nothing: %+v, want give up on window 2 only", ds)
	}
	expectNone(t, out, 100*time.Millisecond)
}

// A boot: the same windows under new ids, in a fresh "boot" the store has no
// bindings for. Window 2 comes back as 12 with its title, at the wrong frame
// and space; window 1 as 11 with its frame, on the wrong space; 13 is new.
func TestModelBootBindsByEvidenceAndOwes(t *testing.T) {
	st := tempStore(t)
	b := bus.New()
	out := b.Subscribe("t", TargetChanged{}, PutWanted{})
	m := newModel(b, st)
	m.boot = func() time.Time { return time.Date(2026, 11, 7, 7, 0, 0, 0, time.UTC) }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.run(ctx)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	cancel()

	m2 := newModel(b, st)
	m2.boot = func() time.Time { return time.Date(2026, 11, 8, 7, 0, 0, 0, time.UTC) }
	ctx2, cancel2 := context.WithCancel(context.Background())
	t.Cleanup(cancel2)
	go m2.run(ctx2)
	after := twoWindows()
	after.windows[0] = layout.LiveWindow{ID: 11, OwnerPID: 20, OwnerName: "A", Title: "renamed", Frame: layout.Rect{X: 0, Y: 0, W: 100, H: 100}}
	after.windows[1] = layout.LiveWindow{ID: 12, OwnerPID: 21, OwnerName: "B", BundleID: "b.app", Title: "two", Frame: layout.Rect{X: 300, Y: 300, W: 50, H: 50}}
	after.windows = append(after.windows, layout.LiveWindow{ID: 13, OwnerPID: 22, OwnerName: "C", Title: "new", Frame: layout.Rect{X: 5, Y: 5, W: 60, H: 60}})
	after.winSpace = map[uint32]uint64{11: 200, 12: 100, 13: 100} // 11 keeps its frame but is on the wrong space
	after.all = []layout.ListedWindow{{ID: 11, OwnerName: "A"}, {ID: 12, OwnerName: "B"}, {ID: 13, OwnerName: "C"}}
	b.Publish(current(after))
	kinds := map[uint32]arrangement.Kind{}
	var put *PutWanted
	deadline := time.After(2 * time.Second)
	for put == nil {
		select {
		case e := <-out.C():
			switch e := e.(type) {
			case TargetChanged:
				for _, d := range e.Decisions {
					kinds[d.Seen.Binding] = d.Kind
				}
			case PutWanted:
				p := e
				put = &p
			}
		case <-deadline:
			t.Fatalf("no PutWanted; decisions %v", kinds)
		}
	}
	if kinds[13] != arrangement.Adopt {
		t.Fatalf("unknown window 13 %v, want adopt", kinds[13])
	}
	owed := map[uint32]bool{}
	for _, d := range put.Decisions {
		owed[d.Seen.Binding] = true
	}
	if !owed[12] || !owed[11] || owed[13] {
		t.Fatalf("put wanted for %v; want 11 (frame evidence) and 12 (title evidence)", owed)
	}
	for _, d := range put.Decisions {
		switch d.Seen.Binding {
		case 12:
			if !d.Resize || !d.Move {
				t.Fatalf("window 12: %+v, want move and resize", d)
			}
		case 11:
			if !d.Move || d.Resize {
				t.Fatalf("window 11: %+v, want move only", d)
			}
		}
	}
}

// A display run holds judgements, and the owe-all after it owes back a shown
// window the window server moved, which a plain judgement would adopt.
func TestModelDisplayRunHoldsThenOwesShownWindow(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	moved := twoWindows()
	moved.windows[0].Frame.X = 400 // window 1, on the shown space
	b.Publish(current(moved))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1})
	expectNone(t, out, 100*time.Millisecond) // held by the display run
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "display quiet"})
	tc := expectEvent(t, out, TargetChanged{}, 2*time.Second).(TargetChanged)
	kinds := map[uint32]arrangement.Kind{}
	for _, d := range tc.Decisions {
		kinds[d.Seen.Binding] = d.Kind
	}
	if kinds[1] != arrangement.Repair {
		t.Fatalf("window 1 %v after a display run, want repair", kinds[1])
	}
	pw := expectEvent(t, out, PutWanted{}, 2*time.Second).(PutWanted)
	if len(pw.Decisions) != 1 || pw.Decisions[0].Seen.Binding != 1 {
		t.Fatalf("put wanted %+v, want window 1", pw.Decisions)
	}
}

// A cause deferred before the first Current is not judged later.
func TestModelStartupDropsEarlierCause(t *testing.T) {
	b, out := startModel(t)
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1}) // no Current yet: held
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	moved := twoWindows()
	moved.windows[0].Frame.X = 400
	b.Publish(current(moved))
	b.Publish(PutEnded{Stamp: event.Now()}) // a release
	expectNone(t, out, 100*time.Millisecond)
}

// restartModel runs a model on st for the boot at that day of November 2026,
// on its own bus.
func restartModel(t *testing.T, st *store.Store, day int) (*bus.Bus, *bus.Subscription) {
	b := bus.New()
	out := b.Subscribe("t", TargetChanged{}, PutWanted{})
	m := newModel(b, st)
	m.boot = func() time.Time { return time.Date(2026, 11, day, 7, 0, 0, 0, time.UTC) }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.run(ctx)
	return b, out
}

// A window quiet binds by the evidence of every unclaimed window: a rival of
// the same app and title makes the binding ambiguous.
func TestModelWindowQuietBindsAgainstRivals(t *testing.T) {
	for _, c := range []struct {
		name  string
		rival bool
		want  arrangement.Kind
	}{
		{"rival present", true, arrangement.Adopt},
		{"no rival", false, arrangement.Repair},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := tempStore(t)
			b1, out1 := restartModel(t, st, 7)
			b1.Publish(current(twoWindows())) // records window 2: B | two on s2
			decisionsOf(t, out1, 2)

			b, out := restartModel(t, st, 8)
			empty := testPicture()
			empty.windows, empty.winSpace, empty.all = nil, map[uint32]uint64{}, nil
			b.Publish(current(empty))
			after := testPicture()
			after.windows = []layout.LiveWindow{{ID: 12, OwnerPID: 21, OwnerName: "B", BundleID: "b.app", Title: "two", Frame: layout.Rect{X: 300, Y: 300, W: 50, H: 50}}}
			after.winSpace = map[uint32]uint64{12: 100}
			after.all = []layout.ListedWindow{{ID: 12, OwnerName: "B"}}
			if c.rival {
				after.windows = append(after.windows, layout.LiveWindow{ID: 14, OwnerPID: 22, OwnerName: "B", BundleID: "b.app", Title: "two", Frame: layout.Rect{X: 300, Y: 300, W: 50, H: 50}})
				after.winSpace[14] = 100
				after.all = append(after.all, layout.ListedWindow{ID: 14, OwnerName: "B"})
			}
			b.Publish(current(after))
			b.Publish(WindowQuiet{Stamp: event.Now(), Window: 12})
			ds := decisionsOf(t, out, 1)
			if len(ds) != 1 || ds[0].Seen.Binding != 12 || ds[0].Kind != c.want {
				t.Fatalf("decisions %+v, want %v for window 12", ds, c.want)
			}
		})
	}
}

// A full judgement covers a cause deferred before it: a later release does
// not judge again.
func TestModelFullJudgementTakesDeferredCause(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1}) // held by the display run
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "display quiet"})
	expectEvent(t, out, TargetChanged{}, 2*time.Second) // owed and in place: released, nothing to put
	moved := twoWindows()
	moved.windows[0].Frame.X = 400
	b.Publish(current(moved))
	b.Publish(event.OverviewShown{Stamp: event.Now()})
	b.Publish(event.OverviewEnded{Stamp: event.Now()})
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "overview ended"}) // an unrelated release
	expectNone(t, out, 100*time.Millisecond)
}

// The display hold releases on the screen's Reconciled even when the
// reconcile changed nothing and published no Current.
func TestModelDisplayQuietWithoutCurrentReleases(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "display quiet"})
	for _, d := range decisionsOf(t, out, 2) {
		if d.Kind != arrangement.Release {
			t.Fatalf("decision %+v, want release: owed by the display run and in place", d)
		}
	}
}

// An overview the screen's marker reopens keeps the hold through the
// Reconciled for the stale end; the real end releases it.
func TestModelOverviewReopenedKeepsHold(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(event.OverviewShown{Stamp: event.Now()})
	b.Publish(event.OverviewEnded{Stamp: event.Now()})
	b.Publish(event.OverviewShown{Stamp: event.Now()}) // the marker is still listed
	moved := twoWindows()
	moved.windows[0].Frame.X = 400
	b.Publish(current(moved))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1}) // deferred
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "overview ended"})
	expectNone(t, out, 100*time.Millisecond)
	b.Publish(event.OverviewEnded{Stamp: event.Now()})
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "overview ended"})
	if d := decisionsOf(t, out, 1)[0]; d.Seen.Binding != 1 || d.Seen.Frame.X != 400 {
		t.Fatalf("deferred decision %+v", d)
	}
}

// A space run holds judgements; the space change's full judgement covers the
// cause it deferred.
func TestModelSpaceRunHolds(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(SpaceBurstBegan{Stamp: event.Now()})
	moved := twoWindows()
	moved.windows[0].Frame.X = 400
	b.Publish(current(moved))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1})
	expectNone(t, out, 100*time.Millisecond) // held by the space run
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "space quiet"})
	tc := expectEvent(t, out, TargetChanged{}, 2*time.Second).(TargetChanged)
	if tc.Cause != "space change" || len(tc.Decisions) != 1 || tc.Decisions[0].Seen.Binding != 1 || tc.Decisions[0].Seen.Frame.X != 400 {
		t.Fatalf("%q: %+v, want the space change judging window 1", tc.Cause, tc.Decisions)
	}
	expectNone(t, out, 100*time.Millisecond)
}

// A space picked from inside the overview ends the overview inside a space
// run: the overview's release waits for the space quiet.
func TestModelOverviewEndInsideSpaceRunWaits(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(event.OverviewShown{Stamp: event.Now()})
	moved := twoWindows()
	moved.windows[0].Frame.X = 400
	b.Publish(current(moved))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1}) // deferred by the overview
	b.Publish(SpaceBurstBegan{Stamp: event.Now()})
	b.Publish(event.OverviewEnded{Stamp: event.Now()})
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "overview ended"})
	expectNone(t, out, 100*time.Millisecond) // the space run still holds
	b.Publish(Reconciled{Stamp: event.Now(), Cause: "space quiet"})
	tc := expectEvent(t, out, TargetChanged{}, 2*time.Second).(TargetChanged)
	if tc.Cause != "space change" || len(tc.Decisions) != 1 || tc.Decisions[0].Seen.Binding != 1 {
		t.Fatalf("%q: %+v, want the space change judging window 1", tc.Cause, tc.Decisions)
	}
	expectNone(t, out, 100*time.Millisecond)
}
