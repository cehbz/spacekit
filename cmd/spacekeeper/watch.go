package main

// Timing and the helpers the processes share; the agent itself is agent.go.

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/layout"
)

// Timing. A display wake produces two hotplug out/in cycles over ~6s and
// WindowServer's own window-return attempts give up ~7s after the first, so
// displaySettle after the last callback lands after macOS has finished.
// Chrome keeps restoring windows for up to 16 s after the last hotplug "in",
// so window changes within displayGrace of the settle are the display
// change's.
const (
	displaySettle = 10 * time.Second
	displayGrace  = 20 * time.Second
	// windowSettle debounces window-server window events: a drag reports a
	// move per frame, with gaps inside a stroke under 300 ms.
	windowSettle = 500 * time.Millisecond
	// putGrace: a put's windows stay the put's this long after it ends; the
	// settler's 500 ms debounce lands a put's last window settle after
	// PutEnded, and a step waits up to stepTimeout for confirmation.
	putGrace = 2 * time.Second
	// retention is how long history is kept.
	retention = 90 * 24 * time.Hour
	// pruneEvery is the number of ticks between prunes (one-minute ticks).
	pruneEvery      = 1440
	streamRetention = 7 * 24 * time.Hour // how long the raw event stream is kept
)

// stepTimeout bounds each awaited step of a put.
const stepTimeout = 2 * time.Second

type watchOptions struct {
	interval time.Duration
}

// bindings are the window-server ids of the windows a look saw.
func bindings(seen []arrangement.Seen) []uint32 {
	out := make([]uint32, len(seen))
	for i, s := range seen {
		out[i] = s.Binding
	}
	return out
}

// displaySet is the arrangement key for the displays present: their UUIDs,
// sorted and comma-joined.
func displaySet(spaces []layout.SavedSpace) string {
	seen := map[string]bool{}
	var ids []string
	for _, sp := range spaces {
		if !seen[sp.DisplayUUID] {
			seen[sp.DisplayUUID] = true
			ids = append(ids, sp.DisplayUUID)
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// seenWindows are the snapshot's windows a decide sees: each on a user
// desktop at its frame, and each titled window fullscreen on a display. An
// untitled fullscreen window (a transition's transient, Chrome's toolbar
// strip) could never be bound, so it is not seen and never recorded; nor is
// a window in transit between spaces.
func seenWindows(s *snapshot) []arrangement.Seen {
	out := make([]arrangement.Seen, 0, len(s.windows))
	for _, l := range s.windows {
		var p arrangement.Placement
		if sp, placed := s.winSpace[l.ID]; placed {
			p = arrangement.Placement{Space: s.idToKey[sp], Frame: l.Frame}
		} else if d, fs := s.fsWindow[l.ID]; fs && l.Title != "" {
			p = arrangement.FullscreenOn(d)
		} else {
			continue
		}
		out = append(out, arrangement.Seen{
			Binding: l.ID, PID: l.OwnerPID, Bundle: l.BundleID, App: l.OwnerName, Title: l.Title, Placement: p,
		})
	}
	return out
}

// placementText is a placement as the logs and inspect show it: the space
// key's first eight characters and the frame, or fullscreen@ and the
// display's first eight.
func placementText(p arrangement.Placement) string {
	if p.Fullscreen != "" {
		return fmt.Sprintf("fullscreen@%.8s", p.Fullscreen)
	}
	return fmt.Sprintf("%.8s %.0f,%.0f %.0fx%.0f", p.Space, p.Frame.X, p.Frame.Y, p.Frame.W, p.Frame.H)
}

// logDecisions writes one line per decide that did something, and one line
// per failed put.
func logDecisions(cause string, ds []arrangement.Decision) {
	var adopted, dirty, put, clean, failed int
	name := func(s arrangement.Seen) string {
		t := s.Title
		if len(t) > 24 {
			t = t[:24]
		}
		return s.App + " | " + t
	}
	var touched []string
	named := map[uint32]bool{}
	touch := func(s arrangement.Seen) {
		if !named[s.Binding] {
			named[s.Binding] = true
			touched = append(touched, name(s))
		}
	}
	for _, d := range ds {
		switch d.Kind {
		case arrangement.Adopt:
			adopted++
		case arrangement.Dirty:
			dirty++
			touch(d.Seen)
		case arrangement.Clean:
			clean++
		case arrangement.Put:
			if _, ok := d.Progress(); ok {
				put++
				touch(d.Seen)
			}
		case arrangement.Failed:
			failed++
			log.Printf("put failed: %s at %s; target %s", name(d.Seen), placementText(d.Seen.Placement), placementText(d.Want))
		}
	}
	if adopted+dirty+put+clean+failed == 0 {
		return
	}
	line := fmt.Sprintf("decide (%s): adopted %d, dirty %d, put %d, clean %d, failed %d", cause, adopted, dirty, put, clean, failed)
	if len(touched) > 0 {
		line += ": " + strings.Join(touched, "; ")
	}
	log.Print(line)
}
