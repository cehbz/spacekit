package bus

import (
	"sort"
	"time"
)

// Debounce coalesces runs of events by key: Note extends the key's run by
// gap, and Due pops the keys whose run has ended. Note reports the leading
// edge, for consumers that act at a run's start as well as its end. It
// keeps no goroutine: the owner selects on Next's deadline.
type Debounce struct {
	gap  time.Duration
	runs map[any]time.Time // key -> deadline
}

func NewDebounce(gap time.Duration) *Debounce {
	return &Debounce{gap: gap, runs: make(map[any]time.Time)}
}

func (d *Debounce) Note(key any, now time.Time) (leading bool) {
	_, open := d.runs[key]
	d.runs[key] = now.Add(d.gap)
	return !open
}

// Next is the earliest deadline among open runs.
func (d *Debounce) Next() (time.Time, bool) {
	var next time.Time
	for _, at := range d.runs {
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	return next, !next.IsZero()
}

// Due removes and returns the keys whose deadline is at or before now,
// earliest first.
func (d *Debounce) Due(now time.Time) []any {
	type run struct {
		key any
		at  time.Time
	}
	var due []run
	for k, at := range d.runs {
		if !at.After(now) {
			due = append(due, run{k, at})
			delete(d.runs, k)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	keys := make([]any, len(due))
	for i, r := range due {
		keys[i] = r.key
	}
	return keys
}

func (d *Debounce) Open() bool { return len(d.runs) > 0 }

func (d *Debounce) Clear() { d.runs = make(map[any]time.Time) }
