package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/skylight"
)

// fakeReader answers reads from a scripted session.
type fakeReader struct {
	pic       *snapshot
	windows   map[uint32]layout.LiveWindow
	spaces    map[uint32][]uint64
	reads     []uint32
	fullReads int
	// fullReadErrs fails that many leading full reads.
	fullReadErrs int
	windowErr    error             // window() fails with this, if set
	display      *skylight.Display // nil: the two spaces of testPicture
}

func (f *fakeReader) readAll() (*snapshot, error) {
	f.fullReads++
	if f.fullReadErrs > 0 {
		f.fullReadErrs--
		return nil, errors.New("read failed")
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

func TestScreenStartsFromTheSeedReadAndPublishesCopies(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	_, sc, out := startScreen(t, r, CurrentChanged{})
	e := expectEvent(t, out, CurrentChanged{}, time.Second)
	pic := e.(CurrentChanged).Current
	if len(pic.windows) != 1 || r.fullReads != 1 {
		t.Fatalf("Current has %d windows after %d full reads", len(pic.windows), r.fullReads)
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
	if len(r.reads) != 1 || r.reads[0] != 1 || r.fullReads != 1 {
		t.Fatalf("reads %v, full reads %d; want one read of window 1 and the seed read", r.reads, r.fullReads)
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
	r := &fakeReader{pic: testPicture(), fullReadErrs: 1}
	b, _, out := startScreen(t, r, CurrentChanged{})
	expectNone(t, out, 100*time.Millisecond)
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectEvent(t, out, CurrentChanged{}, time.Second)
}

// A seed read taken inside Mission Control holds thumbnail frames: it is
// skipped and the tick takes the next one.
func TestScreenSeedWaitsOutTheOverview(t *testing.T) {
	thumbs := testPicture()
	thumbs.windows[0].Frame = layout.Rect{X: 40, Y: 40, W: 25, H: 25}
	thumbs.all = append(thumbs.all, layout.ListedWindow{ID: 9, OwnerName: "WindowManager", Title: "ExposeShieldWindow"})
	r := &fakeReader{pic: thumbs}
	b := bus.New()
	out := b.Subscribe("t", CurrentChanged{})
	called := make(chan struct{}, 8)
	sc := newScreen(b, r, func(fn func()) { fn(); called <- struct{}{} })
	sc.send = func([]uint32) error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go sc.run(ctx)
	<-called // the seed's read
	expectNone(t, out, 300*time.Millisecond)
	r.pic = testPicture()
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	pic := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current
	if len(pic.windows) != 1 || pic.windows[0].Frame.W != 100 {
		t.Fatalf("seeded %+v, want window 1 at its real frame", pic.windows)
	}
}

// shifted is testPicture with window 1 drawn 2624 points left: a read taken
// during a space slide.
func shifted() *snapshot {
	p := testPicture()
	p.windows[0].Frame.X -= 2624
	return p
}

func TestScreenValidateDoesNotReplaceCurrent(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, sc, out := startScreen(t, r, CurrentChanged{}, Difference{})
	expectEvent(t, out, CurrentChanged{}, time.Second) // the seed
	r.pic = shifted()
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	d := expectEvent(t, out, Difference{}, time.Second).(Difference)
	if !strings.Contains(d.Text, "window 1") {
		t.Fatalf("difference %q, want it to name window 1", d.Text)
	}
	expectNone(t, out, 200*time.Millisecond)
	if sc.pic.windows[0].Frame.X != 0 {
		t.Fatalf("Current window 1 at x=%v after one differing validation, want 0", sc.pic.windows[0].Frame.X)
	}
}

func TestScreenValidateAdoptsOnlyARepeatedDifference(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, sc, out := startScreen(t, r, CurrentChanged{}, WindowChanged{}, Difference{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	moved := testPicture()
	moved.windows[0].Frame.X = 300
	r.pic = moved
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectEvent(t, out, Difference{}, time.Second)
	expectNone(t, out, 200*time.Millisecond)
	other := testPicture()
	other.windows[0].Frame.X = 600 // a different difference: still not adopted
	r.pic = other
	b.Publish(Tick{Stamp: event.Now(), N: 2})
	expectEvent(t, out, Difference{}, time.Second)
	expectNone(t, out, 200*time.Millisecond)
	b.Publish(Tick{Stamp: event.Now(), N: 3}) // the same difference again: adopted
	expectEvent(t, out, Difference{}, time.Second)
	expectEvent(t, out, CurrentChanged{}, time.Second)
	c := expectEvent(t, out, WindowChanged{}, time.Second).(WindowChanged)
	if c.Window != 1 || c.Frame.X != 600 || sc.pic.windows[0].Frame.X != 600 {
		t.Fatalf("adopted %+v, Current x=%v; want window 1 at 600", c, sc.pic.windows[0].Frame.X)
	}
}

func TestScreenValidateAdoptsAMissedWindow(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, sc, out := startScreen(t, r, CurrentChanged{}, WindowChanged{}, Difference{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	with := testPicture()
	with.windows = append(with.windows, layout.LiveWindow{ID: 2, OwnerPID: 11, OwnerName: "B", Title: "two", Frame: layout.Rect{X: 1, Y: 1, W: 300, H: 300}})
	with.winSpace[2] = 200
	with.all = append(with.all, layout.ListedWindow{ID: 2, OwnerName: "B", Title: "two"})
	r.pic = with
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectEvent(t, out, Difference{}, time.Second)
	expectNone(t, out, 200*time.Millisecond)
	b.Publish(Tick{Stamp: event.Now(), N: 2})
	expectEvent(t, out, Difference{}, time.Second)
	expectEvent(t, out, CurrentChanged{}, time.Second)
	c := expectEvent(t, out, WindowChanged{}, time.Second).(WindowChanged)
	if c.Window != 2 || c.Space != 200 || len(sc.pic.windows) != 2 {
		t.Fatalf("adopted %+v, Current has %d windows; want window 2 on 200", c, len(sc.pic.windows))
	}
}

func TestScreenValidateRemovesAWindowGoneTwice(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, sc, out := startScreen(t, r, CurrentChanged{}, WindowChanged{}, Difference{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	empty := testPicture()
	empty.windows = nil
	empty.all = nil
	delete(empty.winSpace, 1)
	r.pic = empty
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectEvent(t, out, Difference{}, time.Second)
	expectNone(t, out, 200*time.Millisecond)
	b.Publish(Tick{Stamp: event.Now(), N: 2})
	expectEvent(t, out, Difference{}, time.Second)
	expectEvent(t, out, CurrentChanged{}, time.Second)
	c := expectEvent(t, out, WindowChanged{}, time.Second).(WindowChanged)
	if c.Window != 1 || !c.Gone || len(sc.pic.windows) != 0 {
		t.Fatalf("removed %+v, Current has %d windows; want window 1 gone", c, len(sc.pic.windows))
	}
}

// Mission Control held open across two validations shows the same
// thumbnail frames to both: neither is compared, so nothing is adopted.
func TestScreenValidateInsideOverviewChangesNothing(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, sc, out := startScreen(t, r, CurrentChanged{}, WindowChanged{}, Difference{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	thumbs := testPicture()
	thumbs.windows[0].Frame = layout.Rect{X: 40, Y: 40, W: 25, H: 25}
	thumbs.all = append(thumbs.all, layout.ListedWindow{ID: 9, OwnerName: "WindowManager", Title: "ExposeShieldWindow"})
	r.pic = thumbs
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectNone(t, out, 200*time.Millisecond)
	b.Publish(Tick{Stamp: event.Now(), N: 2})
	expectNone(t, out, 200*time.Millisecond)
	if sc.pic.windows[0].Frame.W != 100 {
		t.Fatalf("Current took the thumbnail frame %v", sc.pic.windows[0].Frame)
	}
}

// A run of the same difference breaks when a validation agrees with Current.
func TestScreenValidateRunBreaksOnAgreement(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, sc, out := startScreen(t, r, CurrentChanged{}, WindowChanged{}, Difference{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	moved := testPicture()
	moved.windows[0].Frame.X = 300
	r.pic = moved
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectEvent(t, out, Difference{}, time.Second)
	expectNone(t, out, 200*time.Millisecond)
	r.pic = testPicture()
	b.Publish(Tick{Stamp: event.Now(), N: 2})
	b.Publish(event.SpaceSwitched{Stamp: event.Now()}) // a synchronisation point: tick 2 is done when Current is published
	expectEvent(t, out, CurrentChanged{}, time.Second)
	r.pic = moved
	b.Publish(Tick{Stamp: event.Now(), N: 3})
	expectEvent(t, out, Difference{}, time.Second)
	expectNone(t, out, 200*time.Millisecond)
	if sc.pic.windows[0].Frame.X != 0 {
		t.Fatalf("Current window 1 at x=%v after a broken run, want 0", sc.pic.windows[0].Frame.X)
	}
}

// Spaces follow space events: a window read on a space Current does not
// know is not adopted.
func TestScreenValidateSkipsAWindowOnAnUnknownSpace(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, sc, out := startScreen(t, r, CurrentChanged{}, WindowChanged{}, Difference{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	with := testPicture()
	with.windows = append(with.windows, layout.LiveWindow{ID: 2, OwnerPID: 11, OwnerName: "B", Title: "two", Frame: layout.Rect{X: 1, Y: 1, W: 300, H: 300}})
	with.winSpace[2] = 300
	with.all = append(with.all, layout.ListedWindow{ID: 2, OwnerName: "B", Title: "two"})
	r.pic = with
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectEvent(t, out, Difference{}, time.Second)
	expectNone(t, out, 200*time.Millisecond)
	b.Publish(Tick{Stamp: event.Now(), N: 2})
	expectEvent(t, out, Difference{}, time.Second)
	expectNone(t, out, 200*time.Millisecond)
	if len(sc.pic.windows) != 1 {
		t.Fatalf("Current has %d windows, want window 2 left out", len(sc.pic.windows))
	}
}

// A listed window that was never placeable sends no destroy event once it
// is out of the request set; two reads without it drop it from the list.
func TestScreenValidateDropsAWindowUnlistedTwice(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	r.pic.all = append(r.pic.all, layout.ListedWindow{ID: 7, OwnerName: "Tiny"})
	b, sc, out := startScreen(t, r, CurrentChanged{}, WindowChanged{}, Difference{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	r.pic = testPicture()
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	expectNone(t, out, 200*time.Millisecond)
	if !sc.pic.lists(7) {
		t.Fatal("window 7 dropped after one read without it")
	}
	b.Publish(Tick{Stamp: event.Now(), N: 2})
	all := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current.all
	if len(all) != 1 || all[0].ID != 1 || sc.pic.lists(7) {
		t.Fatalf("all = %+v, want window 7 dropped", all)
	}
	expectNone(t, out, 200*time.Millisecond)
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
