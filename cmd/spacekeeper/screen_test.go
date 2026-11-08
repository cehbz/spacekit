package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/skylight"
)

// fakeReader answers reads from a scripted session.
type fakeReader struct {
	pic     *snapshot
	windows map[uint32]layout.LiveWindow
	spaces  map[uint32][]uint64
	reads   []uint32
	gathers int
	// gatherErrs fails that many leading gathers.
	gatherErrs int
	windowErr  error             // window() fails with this, if set
	display    *skylight.Display // nil: the two spaces of testPicture
}

func (f *fakeReader) gather() (*snapshot, error) {
	f.gathers++
	if f.gatherErrs > 0 {
		f.gatherErrs--
		return nil, errors.New("gather failed")
	}
	return f.pic.clone(), nil
}
func (f *fakeReader) spaceList() ([]skylight.Display, error) {
	if f.display != nil {
		return []skylight.Display{*f.display}, nil
	}
	return []skylight.Display{{UUID: "D1", CurrentSpace: skylight.Space{ManagedSpaceID: 100},
		Spaces: []skylight.Space{{ManagedSpaceID: 100, UUID: "s1"}, {ManagedSpaceID: 200, UUID: "s2"}}}}, nil
}
func (f *fakeReader) window(wid uint32) (layout.LiveWindow, []uint64, error) {
	f.reads = append(f.reads, wid)
	if f.windowErr != nil {
		return layout.LiveWindow{}, nil, f.windowErr
	}
	w, ok := f.windows[wid]
	if !ok {
		return layout.LiveWindow{}, nil, skylight.ErrNotListed
	}
	return w, f.spaces[wid], nil
}

// testPicture: display D1 with spaces s1 (100, shown) and s2 (200); window 1 on s1.
func testPicture() *snapshot {
	return &snapshot{
		winSpace: map[uint32]uint64{1: 100}, idToKey: map[uint64]string{100: "s1", 200: "s2"},
		current: map[uint64]bool{100: true}, currentOf: map[string]uint64{"D1": 100},
		fsSpace: map[uint64]string{}, fsWindow: map[uint32]string{},
		spaces:   []layout.SavedSpace{{UUID: "s1", DisplayUUID: "D1"}, {UUID: "s2", DisplayUUID: "D1", Index: 1}},
		displays: []layout.CurrentDisplay{{UUID: "D1", Spaces: []layout.CurrentSpace{{ID: 100, UUID: "s1"}, {ID: 200, UUID: "s2"}}}},
		windows:  []layout.LiveWindow{{ID: 1, OwnerPID: 10, OwnerName: "A", Title: "one", Frame: layout.Rect{X: 0, Y: 0, W: 100, H: 100}}},
		all:      []layout.ListedWindow{{ID: 1, OwnerName: "A", Title: "one"}},
	}
}

func startScreen(t *testing.T, r *fakeReader, types ...event.Event) (*bus.Bus, *screen, *bus.Subscription) {
	b := bus.New()
	out := b.Subscribe("t", types...)
	sc := newScreen(b, r, func(fn func()) { fn() })
	sc.send = func([]uint32) error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go sc.run(ctx)
	return b, sc, out
}

func TestScreenStartsFromGatherAndPublishesCopies(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	_, sc, out := startScreen(t, r, CurrentChanged{})
	e := expectEvent(t, out, CurrentChanged{}, time.Second)
	pic := e.(CurrentChanged).Current
	if len(pic.windows) != 1 || r.gathers != 1 {
		t.Fatalf("Current has %d windows after %d gathers", len(pic.windows), r.gathers)
	}
	pic.windows[0].Title = "mutated"
	if sc.pic.windows[0].Title != "one" { // safe: the screen is idle after publishing
		t.Fatal("published Current shares memory with the screen's")
	}
}

func TestScreenMoveReadsThatWindowOnly(t *testing.T) {
	r := &fakeReader{pic: testPicture(), windows: map[uint32]layout.LiveWindow{
		1: {ID: 1, OwnerPID: 10, OwnerName: "A", Title: "one", Frame: layout.Rect{X: 50, Y: 60, W: 100, H: 100}},
	}, spaces: map[uint32][]uint64{1: {100}}}
	b, _, out := startScreen(t, r, WindowChanged{})
	b.Publish(event.WindowMoved{Stamp: event.Now(), Window: 1})
	c := expectEvent(t, out, WindowChanged{}, time.Second).(WindowChanged)
	if c.Window != 1 || c.Frame.X != 50 || c.Space != 100 {
		t.Fatalf("change = %+v", c)
	}
	if len(r.reads) != 1 || r.reads[0] != 1 || r.gathers != 1 {
		t.Fatalf("reads %v, gathers %d; want one read of window 1 and the startup gather", r.reads, r.gathers)
	}
}

func TestScreenAttachAddsPlaceableWindow(t *testing.T) {
	r := &fakeReader{pic: testPicture(), windows: map[uint32]layout.LiveWindow{
		2: {ID: 2, OwnerPID: 11, OwnerName: "B", Title: "two", Frame: layout.Rect{X: 1, Y: 1, W: 300, H: 300}},
	}, spaces: map[uint32][]uint64{2: {200}}}
	b := bus.New()
	out := b.Subscribe("t", CurrentChanged{})
	sc := newScreen(b, r, func(fn func()) { fn() })
	var sent []uint32
	sc.send = func(ids []uint32) error { sent = ids; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go sc.run(ctx)
	expectEvent(t, out, CurrentChanged{}, time.Second) // startup
	b.Publish(event.WindowAttached{Stamp: event.Now(), Window: 2, Space: 200})
	pic := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current
	if len(pic.windows) != 2 || pic.winSpace[2] != 200 {
		t.Fatalf("Current after attach: %+v", pic.windows)
	}
	if len(sent) != 2 {
		t.Fatalf("request set %v, want both windows", sent)
	}
}

func TestScreenDestroyRemoves(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, sc, out := startScreen(t, r, WindowChanged{})
	b.Publish(event.WindowDestroyed{Stamp: event.Now(), Window: 1})
	c := expectEvent(t, out, WindowChanged{}, time.Second).(WindowChanged)
	if !c.Gone || c.Window != 1 {
		t.Fatalf("change = %+v, want gone", c)
	}
	if len(sc.pic.windows) != 0 { // safe: the screen is idle after publishing
		t.Fatal("window still in Current")
	}
}

func TestScreenReconcilesOnTick(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, _, out := startScreen(t, r, Difference{}, CurrentChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second) // startup gather done
	r.pic.windows[0].Frame.X = 500                     // the session moved behind the screen's back
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectEvent(t, out, Difference{}, time.Second)
	pic := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current
	if pic.windows[0].Frame.X != 500 {
		t.Fatal("Current not corrected")
	}
	if r.gathers != 2 {
		t.Fatalf("gathers %d, want 2", r.gathers)
	}
}

func TestScreenCorrectsOverviewFromMarker(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, _, out := startScreen(t, r, event.OverviewShown{}, event.OverviewEnded{}, CurrentChanged{}, Reconciled{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	r.pic.all = append(r.pic.all, layout.ListedWindow{ID: 99, OwnerName: "WindowManager", Title: "ExposeShieldWindow"})
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectEvent(t, out, event.OverviewShown{}, time.Second) // the Dock never said so
	expectCause(t, out, "tick")
	b.Publish(Tick{Stamp: event.Now(), N: 2})
	expectCause(t, out, "tick")              // Current is kept while the overview is open
	expectNone(t, out, 100*time.Millisecond) // and no CurrentChanged
	r.pic.all = r.pic.all[:1]
	b.Publish(Tick{Stamp: event.Now(), N: 3})
	expectEvent(t, out, event.OverviewEnded{}, time.Second)
	expectCause(t, out, "tick")
	expectEvent(t, out, CurrentChanged{}, time.Second)
	if e := expectEvent(t, out, Reconciled{}, time.Second).(Reconciled); e.Cause != "overview ended" {
		t.Fatalf("cause %q", e.Cause)
	}
	expectNone(t, out, 150*time.Millisecond)
}

func TestScreenStartupInsideOverviewSaysSo(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	r.pic.all = append(r.pic.all, layout.ListedWindow{ID: 99, OwnerName: "WindowManager", Title: "ExposeShieldWindow"})
	_, _, out := startScreen(t, r, event.OverviewShown{}, CurrentChanged{})
	expectEvent(t, out, event.OverviewShown{}, time.Second)
	expectEvent(t, out, CurrentChanged{}, time.Second)
}

// Current goes out before the change, so whatever a consumer publishes in
// reaction to the change reaches the model after the Current it describes.
func TestScreenPublishesCurrentBeforeChange(t *testing.T) {
	r := &fakeReader{pic: testPicture(), windows: map[uint32]layout.LiveWindow{
		1: {ID: 1, OwnerPID: 10, OwnerName: "A", Title: "one", Frame: layout.Rect{X: 50, Y: 60, W: 100, H: 100}},
	}, spaces: map[uint32][]uint64{1: {100}}}
	b, _, out := startScreen(t, r, WindowChanged{}, CurrentChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second) // startup
	b.Publish(event.WindowMoved{Stamp: event.Now(), Window: 1})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	expectEvent(t, out, WindowChanged{}, time.Second)
}

// expectCause expects the next event to be Reconciled for this cause.
func expectCause(t *testing.T, out *bus.Subscription, cause string) {
	t.Helper()
	if e := expectEvent(t, out, Reconciled{}, time.Second).(Reconciled); e.Cause != cause {
		t.Fatalf("cause %q, want %q", e.Cause, cause)
	}
}

func TestScreenReconciledAfterFailedGather(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, _, out := startScreen(t, r, CurrentChanged{}, Reconciled{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	r.gatherErrs = 1 // safe: the screen is idle after publishing
	b.Publish(DisplayQuiet{Stamp: event.Now()})
	expectCause(t, out, "display quiet")
	expectNone(t, out, 100*time.Millisecond)
}

func TestScreenQuietBeforeStartupIsReconciled(t *testing.T) {
	r := &fakeReader{pic: testPicture(), gatherErrs: 1}
	b, _, out := startScreen(t, r, CurrentChanged{}, Reconciled{})
	b.Publish(DisplayQuiet{Stamp: event.Now()})
	expectCause(t, out, "display quiet")
	expectNone(t, out, 100*time.Millisecond)
}

func TestScreenReconciledNamesCause(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, _, out := startScreen(t, r, Difference{}, CurrentChanged{}, Reconciled{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	r.pic.windows[0].Frame.X = 500
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectEvent(t, out, Difference{}, time.Second)
	expectEvent(t, out, CurrentChanged{}, time.Second)
	if e := expectEvent(t, out, Reconciled{}, time.Second).(Reconciled); e.Cause != "tick" {
		t.Fatalf("cause %q", e.Cause)
	}
}

func TestScreenSpaceEventRereadsSpaceList(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, _, out := startScreen(t, r, CurrentChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	r.display = &skylight.Display{UUID: "D1", CurrentSpace: skylight.Space{ManagedSpaceID: 200},
		Spaces: []skylight.Space{{ManagedSpaceID: 100, UUID: "s1"}, {ManagedSpaceID: 200, UUID: "s2"}}}
	b.Publish(event.SpaceSwitched{Stamp: event.Now()})
	pic := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current
	if pic.currentOf["D1"] != 200 || !pic.current[200] || len(pic.windows) != 1 {
		t.Fatalf("currentOf %v, current %v, windows %d", pic.currentOf, pic.current, len(pic.windows))
	}
}

func TestScreenUnplaceableWindowStaysListed(t *testing.T) {
	r := &fakeReader{pic: testPicture(), windows: map[uint32]layout.LiveWindow{1: {ID: 1}},
		spaces: map[uint32][]uint64{1: {100, 200}}} // sticky
	b, sc, out := startScreen(t, r, WindowChanged{})
	b.Publish(event.WindowMoved{Stamp: event.Now(), Window: 1})
	expectEvent(t, out, WindowChanged{}, time.Second)
	if len(sc.pic.windows) != 0 || len(sc.pic.all) != 1 { // safe: the screen is idle after publishing
		t.Fatalf("windows %d, all %d; want 0 and 1", len(sc.pic.windows), len(sc.pic.all))
	}
}

func TestScreenNotPlaceableKeepsAll(t *testing.T) {
	r := &fakeReader{pic: testPicture(), windowErr: errNotPlaceable}
	b, sc, out := startScreen(t, r, WindowChanged{})
	b.Publish(event.WindowMoved{Stamp: event.Now(), Window: 1})
	expectEvent(t, out, WindowChanged{}, time.Second)
	if len(sc.pic.windows) != 0 || len(sc.pic.all) != 1 {
		t.Fatalf("windows %d, all %d; want 0 and 1", len(sc.pic.windows), len(sc.pic.all))
	}
}

func TestScreenDestroyPrunesListedOnly(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	r.pic.all = append(r.pic.all, layout.ListedWindow{ID: 7, OwnerName: "Tiny"})
	b, _, out := startScreen(t, r, WindowChanged{}, CurrentChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	b.Publish(event.WindowDestroyed{Stamp: event.Now(), Window: 7})
	expectNone(t, out, 150*time.Millisecond)
	b.Publish(event.SpaceSwitched{Stamp: event.Now()}) // a synchronisation point: Current is published
	all := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current.all
	if len(all) != 1 || all[0].ID != 1 {
		t.Fatalf("all = %+v", all)
	}
}

func TestScreenUnlistedWindowLeavesAll(t *testing.T) {
	r := &fakeReader{pic: testPicture()} // no windows scripted: ErrNotListed
	b, sc, out := startScreen(t, r, WindowChanged{})
	b.Publish(event.WindowMoved{Stamp: event.Now(), Window: 1})
	expectEvent(t, out, WindowChanged{}, time.Second)
	if len(sc.pic.windows) != 0 || len(sc.pic.all) != 0 {
		t.Fatalf("windows %d, all %d; want 0 and 0", len(sc.pic.windows), len(sc.pic.all))
	}
}

func TestScreenRetriesStartupOnTick(t *testing.T) {
	r := &fakeReader{pic: testPicture(), gatherErrs: 1}
	b, _, out := startScreen(t, r, CurrentChanged{})
	expectNone(t, out, 100*time.Millisecond)
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectEvent(t, out, CurrentChanged{}, time.Second)
}

func TestWatchedSetCaps(t *testing.T) {
	var w watched
	ids := make([]uint32, 1100)
	for i := range ids {
		ids[i] = uint32(i + 1)
	}
	w.Set(ids)
	if n := len(w.IDs()); n != watchedCap {
		t.Fatalf("IDs = %d, want %d", n, watchedCap)
	}
	if w.Overflow() != 76 {
		t.Fatalf("Overflow = %d, want 76", w.Overflow())
	}
}
