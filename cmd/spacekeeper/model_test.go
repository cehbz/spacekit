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

func startModelWith(t *testing.T, st *store.Store, grace time.Duration) (*bus.Bus, *bus.Subscription) {
	return startModelGraces(t, st, grace, putGrace)
}

func startModelGraces(t *testing.T, st *store.Store, grace, pg time.Duration) (*bus.Bus, *bus.Subscription) {
	b := bus.New()
	out := b.Subscribe("t", TargetChanged{}, PutWanted{})
	m := newModel(b, st)
	m.grace, m.putGrace = grace, pg
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.run(ctx)
	return b, out
}

func startModel(t *testing.T) (*bus.Bus, *bus.Subscription) {
	return startModelWith(t, tempStore(t), displayGrace)
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

func TestModelWindowQuietDecidesThatWindowOnly(t *testing.T) {
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

func TestModelDisplaySettledPutsAWindowOffTarget(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	shrunk := twoWindows()
	shrunk.windows[1].Frame = layout.Rect{X: 10, Y: 10, W: 100, H: 100}
	b.Publish(current(shrunk))
	b.Publish(DisplayQuiet{Stamp: event.Now()})
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

func TestModelFailedPutLeavesTarget(t *testing.T) {
	st := tempStore(t)
	b, out := startModelWith(t, st, displayGrace)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	shrunk := twoWindows()
	shrunk.windows[1].Frame = layout.Rect{X: 10, Y: 10, W: 100, H: 100}
	b.Publish(current(shrunk))
	b.Publish(DisplayQuiet{Stamp: event.Now()})
	decisionsOf(t, out, 2)                  // dirty, put
	b.Publish(PutEnded{Stamp: event.Now()}) // the put did not take: Current unchanged
	ds := decisionsOf(t, out, 1)
	if d := ds[0]; d.Kind != arrangement.Failed || d.Seen.Binding != 2 || d.Want.Frame != (layout.Rect{X: 10, Y: 10, W: 200, H: 200}) {
		t.Fatalf("after an unconfirmed put: %+v, want a failed put naming the target", d)
	}
	arr, err := st.Arrangement(displaySet(shrunk.spaces))
	if err != nil {
		t.Fatal(err)
	}
	rs, err := st.Recorded(arr, bootTime())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.Binding == 2 && (r.Frame != (layout.Rect{X: 10, Y: 10, W: 200, H: 200}) || !r.Dirty) {
			t.Fatalf("window 2 record %+v; want the target unchanged and still dirty", r)
		}
	}
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 2}) // still off target inside the grace period: reported again, no second put
	ds = decisionsOf(t, out, 1)
	if d := ds[0]; d.Kind != arrangement.Failed || d.Move || d.Resize {
		t.Fatalf("second settle of a failed window: %+v, want a failed put ordering nothing", d)
	}
	expectNone(t, out, 100*time.Millisecond) // in particular no PutWanted
}

func TestModelWindowSettledInsideDisplayGraceIsPut(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	b.Publish(DisplayQuiet{Stamp: event.Now()})
	moved := twoWindows()
	moved.windows[1].Frame.X = 400 // a late move by the window server or the app, hidden window 2
	b.Publish(current(moved))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 2})
	ds := decisionsOf(t, out, 2)
	if ds[0].Kind != arrangement.Dirty || ds[1].Kind != arrangement.Put || ds[1].Seen.Binding != 2 {
		t.Fatalf("inside the grace period: %v, want dirty and put for window 2", kindsOf(ds))
	}
}

func TestModelWindowSettledAfterDisplayGraceMovesTheTarget(t *testing.T) {
	b, out := startModelWith(t, tempStore(t), 0)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	b.Publish(DisplayQuiet{Stamp: event.Now()})
	moved := twoWindows()
	moved.windows[1].Frame.X = 400
	b.Publish(current(moved))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 2})
	ds := decisionsOf(t, out, 1)
	if ds[0].Kind != arrangement.Adopt || ds[0].Seen.Binding != 2 || ds[0].Seen.Frame.X != 400 {
		t.Fatalf("after the grace period: %+v, want the target to follow window 2", ds[0])
	}
}

func TestModelAgentRestartTreatsDifferencesAsTheUsers(t *testing.T) {
	st := tempStore(t)
	b1, out1 := startModelWith(t, st, displayGrace)
	b1.Publish(current(twoWindows()))
	decisionsOf(t, out1, 2)
	b1.Close()
	moved := twoWindows()
	moved.windows[1].Frame.X = 400 // hidden window 2 moved while the agent was down
	b2, out2 := startModelWith(t, st, displayGrace)
	b2.Publish(current(moved))
	ds := decisionsOf(t, out2, 1)
	if ds[0].Kind != arrangement.Adopt || ds[0].Seen.Binding != 2 {
		t.Fatalf("restart: %+v, want the target to follow window 2", ds[0])
	}
	expectNone(t, out2, 100*time.Millisecond)
}

// failedPutOfWindow2 runs a display change that shrinks hidden window 2 and
// a put that does not take; it returns the shrunk picture. Window 2's target
// is its original 200x200 frame.
func failedPutOfWindow2(t *testing.T, b *bus.Bus, out *bus.Subscription) *snapshot {
	t.Helper()
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	shrunk := twoWindows()
	shrunk.windows[1].Frame = layout.Rect{X: 10, Y: 10, W: 100, H: 100}
	b.Publish(current(shrunk))
	b.Publish(DisplayQuiet{Stamp: event.Now()})
	decisionsOf(t, out, 2) // dirty, put
	return shrunk
}

// window2Target asserts window 2's stored target is its original frame and
// it is still dirty.
func window2Target(t *testing.T, st *store.Store, p *snapshot) {
	t.Helper()
	arr, err := st.Arrangement(displaySet(p.spaces))
	if err != nil {
		t.Fatal(err)
	}
	rs, err := st.Recorded(arr, bootTime())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.Binding == 2 && (r.Frame != (layout.Rect{X: 10, Y: 10, W: 200, H: 200}) || !r.Dirty) {
			t.Fatalf("window 2 record %+v; want the target unchanged and still dirty", r)
		}
	}
}

// A user settle held by a put does not make the put's own window the user's
// when the hold releases.
func TestModelHeldUserSettleDuringPutDoesNotAdoptTheFailedPut(t *testing.T) {
	st := tempStore(t)
	b, out := startModelWith(t, st, 0)
	shrunk := failedPutOfWindow2(t, b, out)
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1}) // held by the put; window 1 unchanged
	b.Publish(PutEnded{Stamp: event.Now()})               // the put did not take
	ds := decisionsOf(t, out, 1)
	if d := ds[0]; len(ds) != 1 || d.Kind != arrangement.Failed || d.Seen.Binding != 2 || d.Want.Frame != (layout.Rect{X: 10, Y: 10, W: 200, H: 200}) {
		t.Fatalf("release after the put: %+v, want only a failed put for window 2", ds)
	}
	expectNone(t, out, 100*time.Millisecond)
	window2Target(t, st, shrunk)
}

// The settle that trails a put's own moves is the put's while putGrace runs;
// with no put grace the same settle is the user's.
func TestModelTrailingSettleAfterPutIsThePuts(t *testing.T) {
	for _, c := range []struct {
		name string
		pg   time.Duration
		want arrangement.Kind
	}{
		{"inside put grace", time.Hour, arrangement.Failed},
		{"no put grace", 0, arrangement.Adopt},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, out := startModelGraces(t, tempStore(t), 0, c.pg)
			failedPutOfWindow2(t, b, out)
			b.Publish(PutEnded{Stamp: event.Now()})
			if d := decisionsOf(t, out, 1)[0]; d.Kind != arrangement.Failed {
				t.Fatalf("put ended: %+v, want a failed put", d)
			}
			b.Publish(WindowQuiet{Stamp: event.Now(), Window: 2})
			d := decisionsOf(t, out, 1)[0]
			if d.Kind != c.want || d.Seen.Binding != 2 || d.Move || d.Resize {
				t.Fatalf("trailing settle: %+v, want %v ordering nothing", d, c.want)
			}
			expectNone(t, out, 100*time.Millisecond) // in particular no PutWanted
		})
	}
}

// A lock that begins during a put and outlasts putGrace: the release decides
// each pending window by its own cause, the put's window as the put's and the
// user's settle as the user's.
func TestModelLockDuringPutOutlastingPutGraceKeepsTheFailedPut(t *testing.T) {
	st := tempStore(t)
	b, out := startModelGraces(t, st, 0, 50*time.Millisecond)
	shrunk := failedPutOfWindow2(t, b, out)
	b.Publish(event.SessionLocked{Stamp: event.Now()})
	moved := twoWindows()
	moved.windows[1].Frame = shrunk.windows[1].Frame
	moved.windows[0].Frame.X = 400
	b.Publish(current(moved))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1}) // held
	b.Publish(PutEnded{Stamp: event.Now()})               // the put did not take
	time.Sleep(150 * time.Millisecond)
	b.Publish(event.SessionUnlocked{Stamp: event.Now()})
	ds := decisionsOf(t, out, 2)
	byWindow := map[uint32]arrangement.Decision{}
	for _, d := range ds {
		byWindow[d.Seen.Binding] = d
	}
	one, two := byWindow[1], byWindow[2]
	if len(ds) != 2 || one.Kind != arrangement.Adopt || one.Seen.Frame.X != 400 ||
		two.Kind != arrangement.Failed || two.Want.Frame != (layout.Rect{X: 10, Y: 10, W: 200, H: 200}) {
		t.Fatalf("release after the lock: %+v, want adopt for window 1 and a failed put for window 2", ds)
	}
	expectNone(t, out, 100*time.Millisecond) // in particular no PutWanted
	window2Target(t, st, shrunk)
}

// A put that ends inside a hold with nothing else pending decides only the
// put's windows when the hold ends.
func TestModelPutEndedHeldWithNothingPendingDecidesOnlyThePutsWindows(t *testing.T) {
	b, out := startModelWith(t, tempStore(t), 0)
	shrunk := failedPutOfWindow2(t, b, out)
	b.Publish(event.SessionLocked{Stamp: event.Now()})
	moved := twoWindows()
	moved.windows[1].Frame = shrunk.windows[1].Frame
	moved.windows[0].Frame.X = 400 // nothing settles window 1
	b.Publish(current(moved))
	b.Publish(PutEnded{Stamp: event.Now()}) // held; the put did not take
	b.Publish(event.SessionUnlocked{Stamp: event.Now()})
	ds := decisionsOf(t, out, 1)
	if len(ds) != 1 || ds[0].Kind != arrangement.Failed || ds[0].Seen.Binding != 2 {
		t.Fatalf("release after the lock: %+v, want only a failed put for window 2", ds)
	}
	expectNone(t, out, 100*time.Millisecond)
}

// A display change held by sleep stays the system's when a user settle is
// held after it.
func TestModelHeldSystemDecideSurvivesALaterHeldUserSettle(t *testing.T) {
	b, out := startModelWith(t, tempStore(t), 0)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(event.ScreensSlept{Stamp: event.Now()})
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	shrunk := twoWindows()
	shrunk.windows[1].Frame = layout.Rect{X: 10, Y: 10, W: 100, H: 100}
	b.Publish(current(shrunk))
	b.Publish(DisplayQuiet{Stamp: event.Now()}) // held by sleep
	moved := twoWindows()
	moved.windows[1].Frame = shrunk.windows[1].Frame
	moved.windows[0].Frame.X = 400
	b.Publish(current(moved))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1}) // held by sleep
	b.Publish(event.ScreensWoke{Stamp: event.Now()})
	tc := expectEvent(t, out, TargetChanged{}, 2*time.Second).(TargetChanged)
	var two []arrangement.Kind
	for _, d := range tc.Decisions {
		if d.Seen.Binding == 2 {
			two = append(two, d.Kind)
		}
	}
	if len(two) != 2 || two[0] != arrangement.Dirty || two[1] != arrangement.Put {
		t.Fatalf("release after sleep: %+v, want dirty and put for window 2", tc.Decisions)
	}
}

func kindsOf(ds []arrangement.Decision) []arrangement.Kind {
	out := make([]arrangement.Kind, len(ds))
	for i, d := range ds {
		out[i] = d.Kind
	}
	return out
}

// A boot: the same windows under new ids, in a fresh "boot" the store has no
// bindings for. Window 2 comes back as 12 with its title, at the wrong frame
// and space; window 1 as 11 with its frame, on the wrong space; 13 is new.
// A reappearance is a system cause: the bound windows are put back.
func TestModelBootBindsByEvidenceAndPuts(t *testing.T) {
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
	kinds := map[uint32][]arrangement.Kind{}
	var put *PutWanted
	deadline := time.After(2 * time.Second)
	for put == nil {
		select {
		case e := <-out.C():
			switch e := e.(type) {
			case TargetChanged:
				for _, d := range e.Decisions {
					kinds[d.Seen.Binding] = append(kinds[d.Seen.Binding], d.Kind)
				}
			case PutWanted:
				p := e
				put = &p
			}
		case <-deadline:
			t.Fatalf("no PutWanted; decisions %v", kinds)
		}
	}
	if ks := kinds[13]; len(ks) != 1 || ks[0] != arrangement.Adopt {
		t.Fatalf("unknown window 13 %v, want adopt", ks)
	}
	for _, wid := range []uint32{11, 12} { // binding marks them dirty: the decide puts them
		if ks := kinds[wid]; len(ks) != 1 || ks[0] != arrangement.Put {
			t.Fatalf("bound window %d %v, want put", wid, ks)
		}
	}
	wanted := map[uint32]bool{}
	for _, d := range put.Decisions {
		wanted[d.Seen.Binding] = true
	}
	if !wanted[12] || !wanted[11] || wanted[13] {
		t.Fatalf("put wanted for %v; want 11 (frame evidence) and 12 (title evidence)", wanted)
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

// A display run holds decides, and the decide when displays settle puts back
// a shown window the window server moved, which a decide with no system
// cause would adopt.
func TestModelDisplayRunHoldsThenPutsShownWindow(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	moved := twoWindows()
	moved.windows[0].Frame.X = 400 // window 1, on the shown space
	b.Publish(current(moved))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1})
	expectNone(t, out, 100*time.Millisecond) // held by the display run
	b.Publish(DisplayQuiet{Stamp: event.Now()})
	tc := expectEvent(t, out, TargetChanged{}, 2*time.Second).(TargetChanged)
	if ks := kindsOf(tc.Decisions); len(ks) != 2 || ks[0] != arrangement.Dirty || ks[1] != arrangement.Put || tc.Decisions[1].Seen.Binding != 1 {
		t.Fatalf("after a display run: %+v, want dirty and put for window 1", tc.Decisions)
	}
	pw := expectEvent(t, out, PutWanted{}, 2*time.Second).(PutWanted)
	if len(pw.Decisions) != 1 || pw.Decisions[0].Seen.Binding != 1 {
		t.Fatalf("put wanted %+v, want window 1", pw.Decisions)
	}
}

// A cause deferred before the first Current is not decided later.
func TestModelStartupDropsEarlierCause(t *testing.T) {
	b, out := startModel(t)
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1}) // no Current yet: held
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	moved := twoWindows()
	moved.windows[0].Frame.X = 400
	b.Publish(current(moved))
	b.Publish(PutEnded{Stamp: event.Now()}) // nothing was put: no decide, only a release
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

// A settled window binds by the evidence of every unclaimed window: a rival of
// the same app and title makes the binding ambiguous.
func TestModelWindowQuietBindsAgainstRivals(t *testing.T) {
	for _, c := range []struct {
		name  string
		rival bool
		want  arrangement.Kind
	}{
		{"rival present", true, arrangement.Adopt},
		{"no rival", false, arrangement.Put},
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

// Settled displays with every window at its target decide nothing; the
// display hold has ended, so a later settle is decided.
func TestModelDisplaySettledWithEverythingInPlaceDecidesNothing(t *testing.T) {
	b, out := startModel(t)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	b.Publish(DisplayQuiet{Stamp: event.Now()})
	expectNone(t, out, 200*time.Millisecond)
	moved := twoWindows()
	moved.windows[0].Frame.X = 400
	b.Publish(current(moved))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 1})
	if ds := decisionsOf(t, out, 1); ds[0].Seen.Binding != 1 {
		t.Fatalf("after displays settled: %+v, want window 1 decided", ds[0])
	}
}

// dualPicture is testPicture with a second display D2 showing space s9, and
// window 2 (Chrome "autobrr") on it.
func dualPicture() *snapshot {
	p := testPicture()
	p.spaces = append(p.spaces, layout.SavedSpace{UUID: "s9", DisplayUUID: "D2"})
	p.displays = append(p.displays, layout.CurrentDisplay{UUID: "D2", Spaces: []layout.CurrentSpace{{ID: 900, UUID: "s9"}}})
	p.idToKey[900] = "s9"
	p.current[900] = true
	p.currentOf["D2"] = 900
	p.windows = append(p.windows, layout.LiveWindow{ID: 2, OwnerPID: 11, OwnerName: "Google Chrome", BundleID: "com.google.Chrome", Title: "autobrr", Frame: dualFrame})
	p.winSpace[2] = 900
	p.all = append(p.all, layout.ListedWindow{ID: 2, OwnerName: "Google Chrome", Title: "autobrr"})
	return p
}

var (
	dualFrame   = layout.Rect{X: 1300, Y: 30, W: 1200, H: 1400}
	laptopFrame = layout.Rect{X: 100, Y: 60, W: 900, H: 700}
)

func chromeWindow(wid uint32, f layout.Rect) layout.LiveWindow {
	return layout.LiveWindow{ID: wid, OwnerPID: 30, OwnerName: "Google Chrome", BundleID: "com.google.Chrome", Title: "autobrr", Frame: f}
}

func mustArr(t *testing.T, st *store.Store, set string) int64 {
	t.Helper()
	arr, err := st.Arrangement(set)
	if err != nil {
		t.Fatal(err)
	}
	return arr
}

// Boot 1 with two displays adopts Chrome on D2. Boot 2 on the laptop alone
// restores Chrome under a new id at the laptop's default frame: it binds to
// its record (the same window row) and takes a laptop placement. D2
// returning then puts it back at the recorded dual frame.
func TestModelLaptopBootBindsDualOnlyRecordAndDualReturnPutsItBack(t *testing.T) {
	st := tempStore(t)
	b := bus.New()
	out := b.Subscribe("t", TargetChanged{}, PutWanted{})
	m := newModel(b, st)
	m.boot = func() time.Time { return time.Date(2026, 11, 7, 7, 0, 0, 0, time.UTC) }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.run(ctx)
	b.Publish(current(dualPicture()))
	decisionsOf(t, out, 2) // startup adopts windows 1 and 2
	cancel()
	var chrome int64
	rs, err := st.Recorded(mustArr(t, st, "D1,D2"), m.boot())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.Binding == 2 {
			chrome = r.Window
		}
	}
	if chrome == 0 {
		t.Fatal("Chrome has no record after boot 1")
	}

	m2 := newModel(b, st)
	m2.boot = func() time.Time { return time.Date(2026, 11, 8, 7, 0, 0, 0, time.UTC) }
	ctx2, cancel2 := context.WithCancel(context.Background())
	t.Cleanup(cancel2)
	go m2.run(ctx2)
	air := testPicture() // D1 only
	air.windows = append(air.windows, chromeWindow(22, laptopFrame))
	air.winSpace[22] = 100
	air.all = append(air.all, layout.ListedWindow{ID: 22, OwnerName: "Google Chrome", Title: "autobrr"})
	b.Publish(current(air))
	ds := decisionsOf(t, out, 1)
	var bound bool
	for _, d := range ds {
		if d.Seen.Binding == 22 {
			bound = d.Kind == arrangement.Adopt && d.Window == chrome
		}
	}
	if !bound {
		t.Fatalf("laptop boot: decisions %+v; want Chrome adopted onto window %d", ds, chrome)
	}
	rs, _ = st.Recorded(mustArr(t, st, "D1"), m2.boot())
	var placed bool
	for _, r := range rs {
		if r.Binding == 22 {
			placed = r.Window == chrome && r.Placed && r.Space == "s1" && r.Frame == laptopFrame
		}
	}
	if !placed {
		t.Fatalf("laptop records %+v; want Chrome's row placed at the laptop frame on s1", rs)
	}

	// D2 returns: Chrome is back on s9 but still at the laptop frame.
	dual := dualPicture()
	dual.windows[1] = chromeWindow(22, laptopFrame)
	delete(dual.winSpace, 2)
	dual.winSpace[22] = 900
	dual.all = []layout.ListedWindow{{ID: 1, OwnerName: "A", Title: "one"}, {ID: 22, OwnerName: "Google Chrome", Title: "autobrr"}}
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	b.Publish(current(dual))
	b.Publish(DisplayQuiet{Stamp: event.Now()})
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-out.C():
			if p, ok := e.(PutWanted); ok {
				for _, d := range p.Decisions {
					if d.Seen.Binding == 22 && d.Window == chrome && d.Want.Space == "s9" && d.Want.Frame == dualFrame {
						return
					}
				}
				t.Fatalf("PutWanted %+v; want Chrome put back at its dual frame", p.Decisions)
			}
		case <-deadline:
			t.Fatal("no PutWanted after D2 returned")
		}
	}
}

// A settled display change is a fresh cause: a window whose put failed is put
// again.
func TestModelDisplaySettledRetriesAFailedPut(t *testing.T) {
	b, out := startModel(t)
	failedPutOfWindow2(t, b, out)
	b.Publish(PutEnded{Stamp: event.Now()}) // the put did not take
	if d := decisionsOf(t, out, 1)[0]; d.Kind != arrangement.Failed {
		t.Fatalf("put ended: %+v, want a failed put", d)
	}
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	b.Publish(DisplayQuiet{Stamp: event.Now()}) // Current unchanged: window 2 still off target
	tc := expectEvent(t, out, TargetChanged{}, 2*time.Second).(TargetChanged)
	if len(tc.Decisions) != 1 || tc.Decisions[0].Kind != arrangement.Put || tc.Decisions[0].Seen.Binding != 2 || !tc.Decisions[0].Resize {
		t.Fatalf("display settled after a failed put: %+v, want a put resizing window 2", tc.Decisions)
	}
	pw := expectEvent(t, out, PutWanted{}, 2*time.Second).(PutWanted)
	if len(pw.Decisions) != 1 || pw.Decisions[0].Seen.Binding != 2 {
		t.Fatalf("put wanted %+v, want window 2", pw.Decisions)
	}
}

// owe marks windows dirty with no put tried; the next tick puts them back
// whatever moved them.
func TestModelTickPutsWindowsMarkedByOwe(t *testing.T) {
	st := tempStore(t)
	b, out := startModelWith(t, st, displayGrace)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	moved := twoWindows()
	moved.windows[1].Frame.X = 400 // hidden window 2, off target
	b.Publish(current(moved))
	if _, err := st.OweAll(mustArr(t, st, displaySet(moved.spaces)), bootTime(), time.Now(), "owe", []uint32{2}); err != nil {
		t.Fatal(err)
	}
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	tc := expectEvent(t, out, TargetChanged{}, 2*time.Second).(TargetChanged)
	if len(tc.Decisions) != 1 || tc.Decisions[0].Kind != arrangement.Put || tc.Decisions[0].Seen.Binding != 2 || !tc.Decisions[0].Resize {
		t.Fatalf("tick after owe: %+v, want a put of window 2 only", tc.Decisions)
	}
	pw := expectEvent(t, out, PutWanted{}, 2*time.Second).(PutWanted)
	if len(pw.Decisions) != 1 || pw.Decisions[0].Seen.Binding != 2 {
		t.Fatalf("put wanted %+v, want window 2", pw.Decisions)
	}
}

// oneSpace is twoWindows after s2 was destroyed: D1 shows only s1, and
// window 2 sits on s1 at frame f.
func oneSpace(f layout.Rect) *snapshot {
	p := twoWindows()
	p.spaces = p.spaces[:1]
	p.displays = []layout.CurrentDisplay{{UUID: "D1", Spaces: []layout.CurrentSpace{{ID: 100, UUID: "s1"}}}}
	delete(p.idToKey, 200)
	p.winSpace[2] = 100
	p.windows[1].Frame = f
	return p
}

// A put whose target space is gone can order nothing: it is a failed put,
// the mark records the attempt so the tick stops deciding it, and a user
// move of the window adopts it.
func TestModelPutOntoAGoneSpaceIsAFailedPut(t *testing.T) {
	st := tempStore(t)
	b, out := startModelWith(t, st, displayGrace)
	b.Publish(current(twoWindows())) // window 2's target is s2
	decisionsOf(t, out, 2)
	gone := oneSpace(layout.Rect{X: 10, Y: 10, W: 200, H: 200})
	b.Publish(current(gone))
	if _, err := st.OweAll(mustArr(t, st, displaySet(gone.spaces)), bootTime(), time.Now(), "owe", []uint32{2}); err != nil {
		t.Fatal(err)
	}
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	tc := expectEvent(t, out, TargetChanged{}, 2*time.Second).(TargetChanged)
	if len(tc.Decisions) != 1 || tc.Decisions[0].Kind != arrangement.Failed || tc.Decisions[0].Seen.Binding != 2 || tc.Decisions[0].Want.Space != "s2" {
		t.Fatalf("tick with window 2's space gone: %+v, want a failed put naming s2", tc.Decisions)
	}
	expectNone(t, out, 100*time.Millisecond) // in particular no PutWanted
	b.Publish(Tick{Stamp: event.Now(), N: 2})
	expectNone(t, out, 200*time.Millisecond) // the attempt is recorded: nothing left for the tick
	b.Publish(current(oneSpace(layout.Rect{X: 400, Y: 10, W: 200, H: 200})))
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 2})
	ds := decisionsOf(t, out, 1)
	if len(ds) != 1 || ds[0].Kind != arrangement.Adopt || ds[0].Seen.Binding != 2 || ds[0].Seen.Frame.X != 400 {
		t.Fatalf("user move after the failed put: %+v, want the target to follow window 2", ds)
	}
}

// A tick inside a display run reads nothing from the store: the display set
// seen mid-run is not recorded as an arrangement. Arrangement ids are
// sequential, so a probe created after the tick is the next id after a probe
// created before it only when the tick added no row.
func TestModelTickDuringAHoldRecordsNoArrangement(t *testing.T) {
	st := tempStore(t)
	b, out := startModelWith(t, st, displayGrace)
	b.Publish(current(twoWindows()))
	decisionsOf(t, out, 2)
	before := mustArr(t, st, "probe before")
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	b.Publish(current(dualPicture())) // mid-run: D2 has appeared
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectNone(t, out, 200*time.Millisecond)
	if after := mustArr(t, st, "probe after"); after != before+1 {
		t.Fatalf("probe arrangements %d then %d: the held tick recorded an arrangement", before, after)
	}
}
