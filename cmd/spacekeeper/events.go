package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/layout"
)

// The agent's own events, derived from the window server's and the
// system's by its processes. Every one embeds event.Stamp.

// WindowQuiet: no event has named this window for the window gap.
type WindowQuiet struct {
	event.Stamp
	Window uint32
}

// DisplayBurstBegan and DisplayQuiet bracket a run of display
// reconfigurations; the model decides every window when it ends.
type DisplayBurstBegan struct{ event.Stamp }
type DisplayQuiet struct{ event.Stamp }

// CurrentChanged carries a fresh copy of Current after every change. It is
// not journaled; WindowChanged carries the delta.
type CurrentChanged struct {
	event.Stamp
	Current *snapshot
}

// WindowChanged is one window's new state in Current, or Gone.
type WindowChanged struct {
	event.Stamp
	Window uint32
	Space  uint64
	Frame  layout.Rect
	Gone   bool
}

// Difference is one thing validation found Current had wrong.
type Difference struct {
	event.Stamp
	Text string
}

// TargetChanged is one change to Target: a decide's decisions.
type TargetChanged struct {
	event.Stamp
	Cause     string
	Decisions []arrangement.Decision
}

// PutWanted asks for dirty windows to be put where they should be, with the
// Current and spaces the decide ran on.
type PutWanted struct {
	event.Stamp
	Arr       int64
	Current   *snapshot
	Spaces    []layout.SavedSpace
	Decisions []arrangement.Decision
}

type PutStarted struct {
	event.Stamp
	Windows int
}

// PutStep is one step of a put as it ended.
type PutStep struct {
	Name        string
	Took        time.Duration
	Confirmed   int
	Unconfirmed []uint32
}

type PutEnded struct {
	event.Stamp
	Steps     []PutStep
	Cancelled bool
}

func (p PutEnded) String() string {
	var parts []string
	for _, s := range p.Steps {
		part := fmt.Sprintf("%s %d confirmed in %s", s.Name, s.Confirmed, s.Took.Round(time.Millisecond))
		if len(s.Unconfirmed) > 0 {
			part += fmt.Sprintf(", %d unconfirmed", len(s.Unconfirmed))
		}
		parts = append(parts, part)
	}
	line := "put: " + strings.Join(parts, "; ")
	if p.Cancelled {
		line += " (cancelled)"
	}
	return line
}

// Tick is the clock, numbered from 1.
type Tick struct {
	event.Stamp
	N int
}
