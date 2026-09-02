// Package settle tracks a burst of related events (display reconfiguration
// callbacks, space switches) and reports when the burst has been quiet for
// long enough to act on.
package settle

import "time"

type Window struct {
	quiet       time.Duration
	start, last time.Time
}

func New(quiet time.Duration) *Window { return &Window{quiet: quiet} }

// Note records an event at now. It returns true when this event opened a
// new burst; later events only extend the deadline.
func (w *Window) Note(now time.Time) bool {
	opened := w.start.IsZero()
	if opened {
		w.start = now
	}
	w.last = now
	return opened
}

// Resume reopens a burst that began at start, with the deadline counted from
// now. Used after a restart, when the burst's start was recovered from disk.
func (w *Window) Resume(start, now time.Time) {
	w.start, w.last = start, now
}

func (w *Window) Open() bool { return !w.start.IsZero() }

// Start is the first event of the open burst; zero when closed.
func (w *Window) Start() time.Time { return w.start }

// Deadline is when the open burst counts as settled: the last event plus
// the quiet period. Zero when closed.
func (w *Window) Deadline() time.Time {
	if w.last.IsZero() {
		return time.Time{}
	}
	return w.last.Add(w.quiet)
}

func (w *Window) Close() { w.start, w.last = time.Time{}, time.Time{} }
