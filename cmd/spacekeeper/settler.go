package main

import (
	"context"
	"time"

	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
)

// settler compresses the streams into the events the model judges on: one
// WindowQuiet per window per run of the screen's WindowChanged about it,
// SpaceBurstBegan and SpaceQuiet around a run of space events,
// DisplayBurstBegan and DisplayQuiet around a run of reconfigurations.
// Window runs are keyed on the screen's output, not the window server's
// events: delivery is FIFO per subscriber and the screen publishes
// CurrentChanged before WindowChanged, so every WindowQuiet reaches the model
// after the Current carrying that window's last state. Window changes inside
// an open space or display run add nothing, and a run beginning drops open
// window runs: the run's trailing event covers them.
type settler struct {
	b                         *bus.Bus
	sub                       *bus.Subscription
	windows, spaces, displays *bus.Debounce
}

var one = struct{}{} // the key of a run that is not per window

func newSettler(b *bus.Bus, windowGap, spaceGap, displayGap time.Duration) *settler {
	return &settler{b: b,
		sub: b.Subscribe("settler", WindowChanged{},
			event.SpaceCreated{}, event.SpaceDestroyed{}, event.SpaceSwitched{}, event.DisplayReconfigured{}),
		windows: bus.NewDebounce(windowGap), spaces: bus.NewDebounce(spaceGap), displays: bus.NewDebounce(displayGap)}
}

func (s *settler) run(ctx context.Context) {
	defer s.sub.Close()
	for {
		var windowC, spaceC, displayC <-chan time.Time
		if next, ok := s.windows.Next(); ok {
			windowC = time.After(time.Until(next))
		}
		if next, ok := s.spaces.Next(); ok {
			spaceC = time.After(time.Until(next))
		}
		if next, ok := s.displays.Next(); ok {
			displayC = time.After(time.Until(next))
		}
		select {
		case <-ctx.Done():
			return
		case e, ok := <-s.sub.C():
			if !ok {
				return
			}
			s.note(e)
		case now := <-windowC:
			for _, k := range s.windows.Due(now) {
				s.b.Publish(WindowQuiet{Stamp: event.Now(), Window: k.(uint32)})
			}
		case now := <-spaceC:
			if len(s.spaces.Due(now)) > 0 {
				s.b.Publish(SpaceQuiet{Stamp: event.Now()})
			}
		case now := <-displayC:
			if len(s.displays.Due(now)) > 0 {
				s.b.Publish(DisplayQuiet{Stamp: event.Now()})
			}
		}
	}
}

func (s *settler) note(e event.Event) {
	at := e.When()
	switch e := e.(type) {
	case WindowChanged: // a Gone change too: the model's judgement of a gone window decides nothing
		s.window(e.Window, at)
	case event.SpaceCreated, event.SpaceDestroyed, event.SpaceSwitched:
		s.windows.Clear()
		if s.spaces.Note(one, at) {
			s.b.Publish(SpaceBurstBegan{Stamp: event.Now()})
		}
	case event.DisplayReconfigured:
		s.windows.Clear()
		if s.displays.Note(one, at) {
			s.b.Publish(DisplayBurstBegan{Stamp: event.Now()})
		}
	}
}

func (s *settler) window(wid uint32, at time.Time) {
	if s.spaces.Open() || s.displays.Open() {
		return
	}
	s.windows.Note(wid, at)
}
