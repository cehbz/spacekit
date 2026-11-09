package main

import (
	"context"
	"time"

	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
)

// settler compresses the streams into the events the model decides on: one
// WindowQuiet per window per run of the screen's WindowChanged about it,
// and DisplayBurstBegan and DisplayQuiet around a run of reconfigurations.
// Window runs are keyed on the screen's output, not the window server's
// events: delivery is FIFO per subscriber and the screen publishes
// CurrentChanged before WindowChanged, so every WindowQuiet reaches the model
// after the Current carrying that window's last state. Window changes inside
// an open display run add nothing, and a run beginning drops open window
// runs: the run's trailing event covers them.
type settler struct {
	b                 *bus.Bus
	sub               *bus.Subscription
	windows, displays *bus.Debounce
}

var one = struct{}{} // the key of a run that is not per window

func newSettler(b *bus.Bus, windowGap, displayGap time.Duration) *settler {
	return &settler{b: b,
		sub:     b.Subscribe("settler", WindowChanged{}, event.DisplayReconfigured{}),
		windows: bus.NewDebounce(windowGap), displays: bus.NewDebounce(displayGap)}
}

func (s *settler) run(ctx context.Context) {
	defer s.sub.Close()
	for {
		var windowC, displayC <-chan time.Time
		if next, ok := s.windows.Next(); ok {
			windowC = time.After(time.Until(next))
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
	case WindowChanged: // a Gone change too: deciding a gone window changes nothing
		s.window(e.Window, at)
	case event.DisplayReconfigured:
		s.windows.Clear()
		if s.displays.Note(one, at) {
			s.b.Publish(DisplayBurstBegan{Stamp: event.Now()})
		}
	}
}

func (s *settler) window(wid uint32, at time.Time) {
	if s.displays.Open() {
		return
	}
	s.windows.Note(wid, at)
}
