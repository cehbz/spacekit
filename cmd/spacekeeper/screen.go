package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/skylight"
)

// reader is the screen's view of the window server; liveReader is the real
// one and tests script their own.
type reader interface {
	gather() (*snapshot, error)
	window(wid uint32) (layout.LiveWindow, []uint64, error)
	spaceList() ([]skylight.Display, error)
}

type liveReader struct{}

func (liveReader) gather() (*snapshot, error)                             { return gather() }
func (liveReader) window(wid uint32) (layout.LiveWindow, []uint64, error) { return readWindow(wid) }
func (liveReader) spaceList() ([]skylight.Display, error)                 { return skylight.ManagedDisplaySpaces() }

// screen keeps Current from the window server's events and publishes it by
// value after every change. It owns the per-window request set, kept at
// every placeable window it knows, and tracks the overview state so that
// reconciliation can correct it from the shield-window marker when two
// consecutive reconciles disagree with it: a sample a minute apart must not
// override the Dock's sub-second events.
type screen struct {
	b                        *bus.Bus
	sub                      *bus.Subscription
	r                        reader
	onMain                   func(func())
	send                     func([]uint32) error
	pic                      *snapshot
	set                      watched
	overview                 bool
	markerDisagreements      int // reconciles whose marker disagreed with overview, with no overview event between
	sendFailing, overflowing bool
}

// newScreen subscribes at once; run takes the first gather.
func newScreen(b *bus.Bus, r reader, onMain func(func())) *screen {
	return &screen{b: b, r: r, onMain: onMain, send: skylight.RequestWindowNotifications,
		sub: b.Subscribe("screen", event.WindowAttached{}, event.WindowDetached{}, event.WindowMoved{}, event.WindowResized{},
			event.WindowDestroyed{}, event.SpaceCreated{}, event.SpaceDestroyed{}, event.SpaceSwitched{}, event.DisplayReconfigured{},
			event.OverviewShown{}, event.OverviewEnded{}, Tick{}, SpaceQuiet{}, DisplayQuiet{})}
}

func (s *screen) run(ctx context.Context) {
	defer s.sub.Close()
	s.reconcile("startup", false)
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-s.sub.C():
			if !ok {
				return
			}
			s.handle(e)
		}
	}
}

func (s *screen) handle(e event.Event) {
	if s.pic == nil { // not started: the startup gather failed
		switch e.(type) {
		case Tick:
			s.reconcile("startup", false)
		case SpaceQuiet, DisplayQuiet, event.OverviewEnded: // nothing to reconcile yet
			s.b.Publish(Reconciled{Stamp: event.Now(), Cause: event.Name(e)})
		}
		return
	}
	switch e := e.(type) {
	case Tick:
		s.reconcile(event.Name(e), true)
	case event.SpaceCreated, event.SpaceDestroyed, event.SpaceSwitched, event.DisplayReconfigured:
		s.readSpaces()
	case SpaceQuiet, DisplayQuiet:
		s.reconcile(event.Name(e), true)
	case event.WindowAttached:
		s.readOne(e.Window)
	case event.WindowMoved:
		s.readOne(e.Window)
	case event.WindowResized:
		s.readOne(e.Window)
	case event.WindowDetached:
		if s.pic.winSpace[e.Window] == e.Space {
			s.remove(e.Window, true)
		}
	case event.WindowDestroyed:
		s.remove(e.Window, false)
	case event.OverviewShown: // the Dock speaking breaks a run of disagreeing markers
		s.overview = true
		s.markerDisagreements = 0
	case event.OverviewEnded:
		s.overview = false
		s.markerDisagreements = 0
		s.reconcile(event.Name(e), true)
	}
}

// readOne lists one window and places it in Current; one the server no
// longer lists, or that is not placeable, is removed.
func (s *screen) readOne(wid uint32) {
	var w layout.LiveWindow
	var spaces []uint64
	var err error
	s.onMain(func() { w, spaces, err = s.r.window(wid) })
	if err != nil {
		s.remove(wid, !errors.Is(err, skylight.ErrNotListed))
		return
	}
	var user []uint64
	fsDisplay := ""
	for _, id := range spaces {
		if _, ok := s.pic.idToKey[id]; ok {
			user = append(user, id)
		} else if d, ok := s.pic.fsSpace[id]; ok {
			fsDisplay = d
		}
	}
	switch {
	case len(user) == 1:
		delete(s.pic.fsWindow, wid)
		s.pic.winSpace[wid] = user[0]
	case len(user) == 0 && fsDisplay != "":
		delete(s.pic.winSpace, wid)
		s.pic.fsWindow[wid] = fsDisplay
	default:
		s.remove(wid, true)
		return
	}
	replaced := false
	for i := range s.pic.windows {
		if s.pic.windows[i].ID == wid {
			s.pic.windows[i] = w
			replaced = true
		}
	}
	if !replaced {
		s.pic.windows = append(s.pic.windows, w)
		listed := false
		for _, a := range s.pic.all {
			listed = listed || a.ID == wid
		}
		if !listed {
			s.pic.all = append(s.pic.all, layout.ListedWindow{ID: wid, OwnerName: w.OwnerName, Title: w.Title})
		}
		s.request()
	}
	s.publish(WindowChanged{Stamp: event.Now(), Window: wid, Space: s.pic.winSpace[wid], Frame: w.Frame})
}

// remove drops a window from Current. A window the window server still
// lists stays in all, the whole window list; one it no longer lists leaves
// all even if Current never held it.
func (s *screen) remove(wid uint32, listed bool) {
	_, known := s.pic.winSpace[wid]
	_, fs := s.pic.fsWindow[wid]
	inCurrent := known || fs
	if !listed {
		for i := range s.pic.all {
			if s.pic.all[i].ID == wid {
				s.pic.all = append(s.pic.all[:i], s.pic.all[i+1:]...)
				break
			}
		}
	}
	if !inCurrent {
		return
	}
	delete(s.pic.winSpace, wid)
	delete(s.pic.fsWindow, wid)
	for i := range s.pic.windows {
		if s.pic.windows[i].ID == wid {
			s.pic.windows = append(s.pic.windows[:i], s.pic.windows[i+1:]...)
			break
		}
	}
	s.request()
	s.publish(WindowChanged{Stamp: event.Now(), Window: wid, Gone: true})
}

// readSpaces rebuilds the space list in Current and publishes it.
func (s *screen) readSpaces() {
	var displays []skylight.Display
	var err error
	s.onMain(func() { displays, err = s.r.spaceList() })
	if err != nil {
		log.Printf("screen: reading spaces failed: %v", err)
		return
	}
	s.pic.setSpaces(displays)
	s.b.Publish(CurrentChanged{Stamp: event.Now(), Current: s.pic.clone()})
}

// publish emits Current, as a fresh copy, and then the change. Bus delivery
// is FIFO per subscriber, so whatever a consumer publishes in reaction to the
// change (the putter's PutEnded) reaches the model after the Current it
// describes.
func (s *screen) publish(c WindowChanged) {
	s.b.Publish(CurrentChanged{Stamp: event.Now(), Current: s.pic.clone()})
	s.b.Publish(c)
}

// reconcile replaces Current with one gather. With report, each difference
// is logged and published, the measure of how faithfully the events keep
// Current; a reconcile whose marker disagrees with the overview state keeps
// Current, and the second consecutive one corrects the state.
// With report, Reconciled follows on every exit, after any CurrentChanged.
func (s *screen) reconcile(why string, report bool) {
	if report {
		defer func() { s.b.Publish(Reconciled{Stamp: event.Now(), Cause: why}) }()
	}
	var fresh *snapshot
	var err error
	s.onMain(func() { fresh, err = s.r.gather() })
	if err != nil {
		log.Printf("screen (%s): gather failed: %v", why, err)
		return
	}
	marker := layout.OverviewOpen(fresh.all)
	if s.pic == nil || !report {
		s.overview = marker
		if s.pic == nil && marker { // started inside the overview: the Dock said so before we listened
			s.b.Publish(event.OverviewShown{Stamp: event.Now()})
		}
	} else {
		if marker != s.overview {
			s.markerDisagreements++
			log.Printf("screen (%s): overview marker %v, state %v (%d)", why, marker, s.overview, s.markerDisagreements)
			if s.markerDisagreements < 2 {
				return
			}
			s.markerDisagreements = 0
			s.overview = marker
			if marker {
				s.b.Publish(event.OverviewShown{Stamp: event.Now()})
			} else { // the event coming back reconciles
				s.b.Publish(event.OverviewEnded{Stamp: event.Now()})
			}
			return
		}
		s.markerDisagreements = 0
		if marker { // the overview shows thumbnail frames: keep Current
			return
		}
		for _, d := range differences(s.pic, fresh) {
			log.Printf("screen (%s): %s", why, d)
			s.b.Publish(Difference{Stamp: event.Now(), Text: d})
		}
	}
	s.pic = fresh
	s.request()
	s.b.Publish(CurrentChanged{Stamp: event.Now(), Current: s.pic.clone()})
}

// differences lists what Current got wrong, one line each.
func differences(have, want *snapshot) []string {
	var out []string
	byID := func(ws []layout.LiveWindow) map[uint32]layout.LiveWindow {
		m := make(map[uint32]layout.LiveWindow, len(ws))
		for _, w := range ws {
			m[w.ID] = w
		}
		return m
	}
	h, w := byID(have.windows), byID(want.windows)
	ids := make([]uint32, 0, len(w))
	for id := range w {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		ww := w[id]
		hw, ok := h[id]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("missed window %d %s | %s", id, ww.OwnerName, ww.Title))
		case have.winSpace[id] != want.winSpace[id]:
			out = append(out, fmt.Sprintf("window %d %s on space %d, had %d", id, ww.OwnerName, want.winSpace[id], have.winSpace[id]))
		case !arrangement.SameFrame(hw.Frame, ww.Frame):
			out = append(out, fmt.Sprintf("window %d %s at %.0f,%.0f %.0fx%.0f, had %.0f,%.0f %.0fx%.0f", id, ww.OwnerName,
				ww.Frame.X, ww.Frame.Y, ww.Frame.W, ww.Frame.H, hw.Frame.X, hw.Frame.Y, hw.Frame.W, hw.Frame.H))
		}
	}
	ids = ids[:0]
	for id := range h {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		hw := h[id]
		if _, ok := w[id]; !ok {
			out = append(out, fmt.Sprintf("kept window %d %s | %s that is gone", id, hw.OwnerName, hw.Title))
		}
	}
	if len(have.spaces) != len(want.spaces) {
		out = append(out, fmt.Sprintf("%d spaces, had %d", len(want.spaces), len(have.spaces)))
	}
	disps := make([]string, 0, len(want.currentOf))
	for d := range want.currentOf {
		disps = append(disps, d)
	}
	sort.Strings(disps)
	for _, d := range disps {
		cur := want.currentOf[d]
		if have.currentOf[d] != cur {
			out = append(out, fmt.Sprintf("display %.8s shows space %d, had %d", d, cur, have.currentOf[d]))
		}
	}
	return out
}

// request gives the window server the request set: every placeable window
// in Current. A failing send and an overflow are logged when they begin.
func (s *screen) request() {
	ids := make([]uint32, 0, len(s.pic.windows))
	for _, w := range s.pic.windows {
		ids = append(ids, w.ID)
	}
	s.set.Set(ids)
	var err error
	s.onMain(func() { err = s.send(s.set.IDs()) })
	if err != nil && !s.sendFailing {
		log.Printf("requesting window notifications: %v", err)
	}
	s.sendFailing = err != nil
	n := s.set.Overflow()
	if n > 0 && !s.overflowing {
		log.Printf("watching %d windows; %d more are left to reconciliation", watchedCap, n)
	}
	s.overflowing = n > 0
}
