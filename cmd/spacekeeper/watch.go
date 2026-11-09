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
// displayQuiet after the last callback lands after macOS has finished.
const (
	displayQuiet = 10 * time.Second
	spaceQuiet   = 3 * time.Second
	// displayGrace: window changes this soon after displays settle are the
	// display change's; the window server and Chrome keep moving windows for
	// up to 16 s after the last hotplug.
	displayGrace = 20 * time.Second
	// putGrace: a put's windows stay the put's this long after it ends; the
	// settler's 500 ms debounce lands a put's last window settle after
	// PutEnded, and a step waits up to stepTimeout for confirmation.
	putGrace = 2 * time.Second
	// windowQuiet debounces window-server window events: a drag reports a
	// move per frame, with gaps inside a stroke under 300 ms.
	windowQuiet = 500 * time.Millisecond
	// retention is how long history is kept.
	retention = 90 * 24 * time.Hour
	// pruneEvery is the number of ticks between prunes (one-minute ticks).
	pruneEvery      = 1440
	streamRetention = 7 * 24 * time.Hour // how long the raw event stream is kept
)

// stepTimeout bounds each awaited step of a repair.
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

// seenWindows are the gathered windows on user desktops; fullscreen
// windows are not placed.
func seenWindows(s *snapshot) []arrangement.Seen {
	out := make([]arrangement.Seen, 0, len(s.windows))
	for _, l := range s.windows {
		if _, fs := s.fsWindow[l.ID]; fs {
			continue
		}
		out = append(out, arrangement.Seen{
			Binding: l.ID, PID: l.OwnerPID, Bundle: l.BundleID, App: l.OwnerName, Title: l.Title,
			Placement: arrangement.Placement{Space: s.idToKey[s.winSpace[l.ID]], Frame: l.Frame},
		})
	}
	return out
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
			f, w := d.Seen.Frame, d.Want.Frame
			log.Printf("put failed: %s at %.0f,%.0f %.0fx%.0f on %.8s; target %.0f,%.0f %.0fx%.0f on %.8s",
				name(d.Seen), f.X, f.Y, f.W, f.H, d.Seen.Space, w.X, w.Y, w.W, w.H, d.Want.Space)
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
