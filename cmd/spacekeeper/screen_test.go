package main

import (
	"context"
	"errors"
	"slices"
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
	// unlisted windows are omitted from the window list while their frame
	// and spaces still read, as around an attach.
	unlisted map[uint32]bool
	visible  map[string]layout.Rect // each display's visible frame
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
func (f *fakeReader) visibleFrame(uuid string) (layout.Rect, error) {
	if r, ok := f.visible[uuid]; ok {
		return r, nil
	}
	return layout.Rect{}, errors.New("no screen")
}
func (f *fakeReader) window(wid uint32) (layout.LiveWindow, []uint64, error) {
	f.reads = append(f.reads, wid)
	if f.windowErr != nil {
		return layout.LiveWindow{}, nil, f.windowErr
	}
	if f.unlisted[wid] {
		return layout.LiveWindow{ID: wid, Frame: f.windows[wid].Frame}, f.spaces[wid], skylight.ErrNotListed
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

// A window Current does not hold leaves all when a read finds it unlisted.
func TestScreenUnlistedWindowLeavesAll(t *testing.T) {
	r := &fakeReader{pic: testPicture()} // no windows scripted: ErrNotListed
	r.pic.all = append(r.pic.all, layout.ListedWindow{ID: 7, OwnerName: "Tiny"})
	b, _, out := startScreen(t, r, WindowChanged{}, CurrentChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	b.Publish(event.WindowMoved{Stamp: event.Now(), Window: 7})
	expectNone(t, out, 150*time.Millisecond)
	b.Publish(event.SpaceSwitched{Stamp: event.Now()}) // a synchronisation point: Current is published
	pic := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current
	if len(pic.windows) != 1 || len(pic.all) != 1 || pic.lists(7) {
		t.Fatalf("windows %d, all %+v; want window 1 only", len(pic.windows), pic.all)
	}
}

// The window list omits a window for up to 200 ms after it attaches; a held
// window keeps its listing and takes the attach's space and frame.
func TestScreenUnlistedAttachKeepsAHeldWindow(t *testing.T) {
	r := &fakeReader{pic: testPicture(), windows: map[uint32]layout.LiveWindow{
		1: {ID: 1, Frame: layout.Rect{X: 500, Y: 0, W: 100, H: 100}},
	}, spaces: map[uint32][]uint64{1: {200}}, unlisted: map[uint32]bool{1: true}}
	b, sc, out := startScreen(t, r, WindowChanged{})
	b.Publish(event.WindowAttached{Stamp: event.Now(), Window: 1, Space: 200})
	c := expectEvent(t, out, WindowChanged{}, time.Second).(WindowChanged)
	if c.Gone || c.Window != 1 || c.Space != 200 || c.Frame.X != 500 {
		t.Fatalf("change = %+v, want window 1 on 200 at x=500", c)
	}
	w, held := sc.pic.window(1) // safe: the screen is idle after publishing
	if !held || w.Title != "one" || w.Frame.X != 500 || sc.pic.winSpace[1] != 200 || !sc.pic.lists(1) {
		t.Fatalf("Current window 1 = %+v held %v on %d; want it held on 200 at x=500, titled one", w, held, sc.pic.winSpace[1])
	}
}

// A detach leaves the window in transit: held, on no space, and decided by
// nothing.
func TestScreenDetachPutsAWindowInTransit(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, sc, out := startScreen(t, r, CurrentChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second) // startup
	b.Publish(event.WindowDetached{Stamp: event.Now(), Window: 1, Space: 100})
	pic := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current
	_, held := pic.window(1)
	_, placed := pic.winSpace[1]
	_, fs := pic.fsWindow[1]
	if !held || placed || fs || !pic.lists(1) || len(seenWindows(pic)) != 0 {
		t.Fatalf("after detach: held %v, placed %v, fullscreen %v, seen %d; want held in transit, seen by nothing",
			held, placed, fs, len(seenWindows(pic)))
	}
	if _, held := sc.pic.window(1); !held { // safe: the screen is idle after publishing
		t.Fatal("window 1 dropped from Current")
	}
}

// A fullscreen window detaches from its display's fullscreen space.
func TestScreenFullscreenDetachPutsAWindowInTransit(t *testing.T) {
	p := testPicture()
	p.fsSpace[300] = "D1"
	p.fsWindow[3] = "D1"
	p.windows = append(p.windows, layout.LiveWindow{ID: 3, OwnerPID: 12, OwnerName: "C", Title: "three", Frame: layout.Rect{W: 1440, H: 900}})
	p.all = append(p.all, layout.ListedWindow{ID: 3, OwnerName: "C", Title: "three"})
	r := &fakeReader{pic: p}
	b, _, out := startScreen(t, r, CurrentChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second) // startup
	b.Publish(event.WindowDetached{Stamp: event.Now(), Window: 3, Space: 300})
	pic := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current
	_, held := pic.window(3)
	_, fs := pic.fsWindow[3]
	if !held || fs {
		t.Fatalf("after detach: held %v, fullscreen %v; want held in transit", held, fs)
	}
}

// A read in transit finds the window on no space: nothing changes.
func TestScreenReadInTransitChangesNothing(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, _, out := startScreen(t, r, CurrentChanged{}, WindowChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second) // startup
	b.Publish(event.WindowDetached{Stamp: event.Now(), Window: 1, Space: 100})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	expectEvent(t, out, WindowChanged{}, time.Second)
	b.Publish(event.WindowResized{Stamp: event.Now(), Window: 1}) // no window scripted: unlisted, on no space
	expectNone(t, out, 150*time.Millisecond)
	b.Publish(event.SpaceSwitched{Stamp: event.Now()}) // a synchronisation point: Current is published
	pic := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current
	_, held := pic.window(1)
	_, placed := pic.winSpace[1]
	if !held || placed || !pic.lists(1) {
		t.Fatalf("after a read in transit: held %v, placed %v, listed %v; want held in transit", held, placed, pic.lists(1))
	}
}

// A window Current never held is not placed from an attach the list omits.
func TestScreenUnlistedAttachOfANewWindowPlacesNothing(t *testing.T) {
	r := &fakeReader{pic: testPicture(), windows: map[uint32]layout.LiveWindow{
		2: {ID: 2, Frame: layout.Rect{X: 1, Y: 1, W: 300, H: 300}},
	}, spaces: map[uint32][]uint64{2: {200}}, unlisted: map[uint32]bool{2: true}}
	b, _, out := startScreen(t, r, WindowChanged{}, CurrentChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second) // startup
	b.Publish(event.WindowAttached{Stamp: event.Now(), Window: 2, Space: 200})
	expectNone(t, out, 150*time.Millisecond)
	b.Publish(event.SpaceSwitched{Stamp: event.Now()}) // a synchronisation point: Current is published
	pic := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current
	if _, held := pic.window(2); held || len(pic.windows) != 1 {
		t.Fatalf("Current has %d windows, want window 2 left out", len(pic.windows))
	}
}

// The attach that ends a transit places the window, unlisted or not.
func TestScreenAttachAfterTransitPlacesTheWindow(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, sc, out := startScreen(t, r, CurrentChanged{}, WindowChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second) // startup
	b.Publish(event.WindowDetached{Stamp: event.Now(), Window: 1, Space: 100})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	expectEvent(t, out, WindowChanged{}, time.Second)
	r.windows = map[uint32]layout.LiveWindow{1: {ID: 1, Frame: layout.Rect{X: 500, Y: 0, W: 100, H: 100}}}
	r.spaces = map[uint32][]uint64{1: {200}}
	r.unlisted = map[uint32]bool{1: true}
	b.Publish(event.WindowAttached{Stamp: event.Now(), Window: 1, Space: 200})
	pic := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current
	c := expectEvent(t, out, WindowChanged{}, time.Second).(WindowChanged)
	if c.Gone || c.Window != 1 || c.Space != 200 || c.Frame.X != 500 {
		t.Fatalf("change = %+v, want window 1 on 200 at x=500", c)
	}
	if pic.winSpace[1] != 200 || len(seenWindows(pic)) != 1 || sc.pic.winSpace[1] != 200 {
		t.Fatalf("Current after attach: winSpace %v, seen %d; want window 1 on 200 and seen", pic.winSpace, len(seenWindows(pic)))
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

// fsDisplay is D1 with s1 (100), s2 (200) and the fullscreen space 300
// appended last, showing shown.
func fsDisplay(shown uint64) *skylight.Display {
	return &skylight.Display{UUID: "D1", CurrentSpace: skylight.Space{ManagedSpaceID: shown},
		Spaces: []skylight.Space{{ManagedSpaceID: 100, UUID: "s1"}, {ManagedSpaceID: 200, UUID: "s2"}, {ManagedSpaceID: 300, Type: 4}}}
}

// Entering fullscreen: the space is created and the window attaches to it;
// the change names the display the window is fullscreen on.
func TestScreenFullscreenAttachNamesTheDisplay(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, _, out := startScreen(t, r, CurrentChanged{}, WindowChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second) // startup
	r.display = fsDisplay(300)
	b.Publish(event.SpaceCreated{Stamp: event.Now(), Space: 300})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	r.windows = map[uint32]layout.LiveWindow{1: {ID: 1, OwnerPID: 10, OwnerName: "A", Title: "one", Frame: layout.Rect{W: 100, H: 100}}}
	r.spaces = map[uint32][]uint64{1: {300}}
	b.Publish(event.WindowAttached{Stamp: event.Now(), Window: 1, Space: 300})
	expectEvent(t, out, CurrentChanged{}, time.Second)
	c := expectEvent(t, out, WindowChanged{}, time.Second).(WindowChanged)
	if c.Window != 1 || c.Fullscreen != "D1" || c.Space != 0 {
		t.Fatalf("change = %+v, want window 1 fullscreen on D1", c)
	}
}

// A space event after which a display shows another space says so, once.
func TestScreenSpaceSwitchPublishesTheShownSpace(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, _, out := startScreen(t, r, SpaceShown{})
	r.display = fsDisplay(300)
	b.Publish(event.SpaceSwitched{Stamp: event.Now()})
	got := expectEvent(t, out, SpaceShown{}, time.Second).(SpaceShown)
	if got.Display != "D1" || got.Space != 300 {
		t.Fatalf("shown = %+v, want D1 showing 300", got)
	}
	b.Publish(event.SpaceSwitched{Stamp: event.Now()}) // D1 still shows 300
	expectNone(t, out, 100*time.Millisecond)
}

// Current keeps each display's spaces in Mission Control order, fullscreen
// ones included: the order Ctrl+Arrow steps through.
func TestScreenKeepsEachDisplaysSpaceOrder(t *testing.T) {
	r := &fakeReader{pic: testPicture()}
	b, _, out := startScreen(t, r, CurrentChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second) // startup
	r.display = fsDisplay(300)
	b.Publish(event.SpaceCreated{Stamp: event.Now(), Space: 300})
	pic := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current
	if got := pic.order["D1"]; !slices.Equal(got, []uint64{100, 200, 300}) || pic.fsSpace[300] != "D1" {
		t.Fatalf("order %v, fullscreen spaces %v; want 100 200 300 with 300 fullscreen", got, pic.fsSpace)
	}
}

// The space relist fills each display's visible frame into Current.
func TestReadSpacesFillsVisibleFrames(t *testing.T) {
	vis := layout.Rect{X: 0, Y: 30, W: 2560, H: 1410}
	r := &fakeReader{pic: testPicture(), visible: map[string]layout.Rect{"D1": vis}}
	b, _, out := startScreen(t, r, CurrentChanged{})
	expectEvent(t, out, CurrentChanged{}, time.Second) // startup
	b.Publish(event.SpaceCreated{Stamp: event.Now(), Space: 300})
	pic := expectEvent(t, out, CurrentChanged{}, time.Second).(CurrentChanged).Current
	if got, ok := pic.visible("D1"); !ok || got != vis {
		t.Fatalf("visible(D1) = %v, %v; want %v, true", got, ok, vis)
	}
}
