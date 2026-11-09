package main

import (
	"fmt"
	"time"

	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/skylight"
)

type sample struct {
	wid          uint32
	app          string
	list, bounds layout.Rect
}

// frames is a diagnostic: it samples every placeable window's drawn rect
// (the window list) and its bounds (SLSGetWindowBounds) every -every for
// -for, and prints a line for each window whose two values differ or whose
// either value moved since the previous sample. Run it while switching
// spaces with a trackpad swipe and while opening Mission Control: a source
// that moves then is not the frame.
func framesCmd(dur, every time.Duration) error {
	var prev []sample
	start := time.Now()
	for time.Since(start) < dur {
		cur, err := sampleFrames()
		if err != nil {
			return err
		}
		for _, line := range framesReport(prev, cur) {
			fmt.Printf("+%5.1fs %s\n", time.Since(start).Seconds(), line)
		}
		prev = cur
		time.Sleep(every)
	}
	return nil
}

func sampleFrames() ([]sample, error) {
	wins, err := skylight.WindowList()
	if err != nil {
		return nil, err
	}
	var out []sample
	for _, w := range wins {
		if !placeableWindow(w) {
			continue
		}
		s := sample{wid: w.Number, app: w.OwnerName, list: layout.Rect{X: w.Bounds.X, Y: w.Bounds.Y, W: w.Bounds.Width, H: w.Bounds.Height}}
		x, y, bw, bh, err := skylight.WindowFrame(w.Number)
		if err != nil {
			return nil, err
		}
		s.bounds = layout.Rect{X: x, Y: y, W: bw, H: bh}
		out = append(out, s)
	}
	return out, nil
}

func framesReport(prev, cur []sample) []string {
	was := make(map[uint32]sample, len(prev))
	for _, s := range prev {
		was[s.wid] = s
	}
	rect := func(r layout.Rect) string { return fmt.Sprintf("%.0f,%.0f %.0fx%.0f", r.X, r.Y, r.W, r.H) }
	motion := func(now, before layout.Rect, had bool) string {
		if had && now != before {
			return " (was " + rect(before) + ")"
		}
		return " (steady)"
	}
	var out []string
	for _, s := range cur {
		p, had := was[s.wid]
		diverge := s.list != s.bounds
		moved := had && (s.list != p.list || s.bounds != p.bounds)
		if !diverge && !moved {
			continue
		}
		line := fmt.Sprintf("%d %s: list %s%s; bounds %s%s", s.wid, s.app, rect(s.list), motion(s.list, p.list, had), rect(s.bounds), motion(s.bounds, p.bounds, had))
		if diverge {
			line += "; LIST != BOUNDS"
		}
		out = append(out, line)
	}
	return out
}
