package main

import (
	"fmt"

	"github.com/cehbz/spacekit/internal/layout"
)

// reconciler drives repeated matching passes of one saved layout against the
// live session. Each saved window is acted on at most once across passes, so
// convergence never fights the user's own rearranging (see applyPass).
type reconciler struct {
	layout             layout.Layout
	handled            map[int]bool
	frames, fullscreen bool
}

func newReconciler(l layout.Layout, frames, fullscreen bool) *reconciler {
	return &reconciler{layout: l, handled: make(map[int]bool), frames: frames, fullscreen: fullscreen}
}

// pass gathers the live session, optionally recreates missing desktops, and
// acts on windows matched for the first time. It reports whether every saved
// window has now been handled.
func (r *reconciler) pass(create bool) (bool, passStats, error) {
	s, err := gather()
	if err != nil {
		return false, passStats{}, err
	}
	if create {
		if s, err = createMissingSpaces(r.layout, s, false); err != nil {
			return false, passStats{}, err
		}
	}
	return r.passOn(s)
}

// passOn is pass over an already gathered snapshot.
func (r *reconciler) passOn(s *snapshot) (bool, passStats, error) {
	st, err := applyPass(r.layout, s, r.handled, r.frames, r.fullscreen)
	if err != nil {
		return false, st, err
	}
	return r.done(), st, nil
}

func (r *reconciler) done() bool { return len(r.handled) == len(r.layout.Windows) }

func (r *reconciler) progress() string {
	return fmt.Sprintf("%d/%d", len(r.handled), len(r.layout.Windows))
}
