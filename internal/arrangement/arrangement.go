// Package arrangement holds the rules of the arrangement model: what a look
// at the live session means for the recorded intent. It has no storage and
// no system calls; the store and the agent sit on either side of it.
package arrangement

import (
	"math"

	"github.com/cehbz/spacekit/internal/layout"
)

// Tolerance is the hysteresis band, in points per edge, inside which a frame
// counts as in place: a difference inside it is neither adopted nor repaired.
const Tolerance = 3.0

// Placement is where a window belongs: a space and a frame. Space is a space
// key: the space UUID, or display:<uuid>/<index> for the untitled desktop.
type Placement struct {
	Space string
	Frame layout.Rect
}

// InPlace reports whether q is on the same space as p and within Tolerance of
// it on every edge.
func (p Placement) InPlace(q Placement) bool {
	return p.Space == q.Space && sameFrame(p.Frame, q.Frame)
}

func sameFrame(a, b layout.Rect) bool {
	near := func(x, y float64) bool { return math.Abs(x-y) <= Tolerance }
	return near(a.X, b.X) && near(a.Y, b.Y) && near(a.X+a.W, b.X+b.W) && near(a.Y+a.H, b.Y+b.H)
}

// Progress records how far the last repair of an owed window got, for the
// next look to judge.
type Progress int

const (
	Untried   Progress = iota
	Moved              // moved to its space; its frame could not be reached yet
	Attempted          // everything it needed has been attempted
)

// Recorded is a window as the store knows it in the current arrangement.
type Recorded struct {
	Window  int64  // store identity
	Binding uint32 // window-server id in this boot
	Placed  bool   // has a placement in the current arrangement
	Placement
	Owed     bool
	Progress Progress
}

// Seen is a window as a look found it.
type Seen struct {
	Binding            uint32
	PID                int
	Bundle, App, Title string
	Placement
}

// Look is one observation of the live session, taken when no disturbance is
// in progress. Visible holds the keys of the spaces shown at this look or the
// previous one: the only spaces whose windows the user can have touched.
type Look struct {
	Windows []Seen
	Visible map[string]bool
}

type Kind int

const (
	Adopt   Kind = iota // the arrangement follows the screen
	Owe                 // a change nobody can have made by hand: put it back
	Release             // an owed window is back in place
	GiveUp              // a repair was attempted and did not take: adopt where it is
	Repair              // an owed window is out of place: act where possible
)

// Decision is what a look means for one window.
type Decision struct {
	Kind         Kind
	Window       int64 // 0: a window never seen before
	Seen         Seen
	Want         Placement // Repair: where it belongs
	Move, Resize bool      // Repair: what to do at this look
}

// Decide compares each seen window with its record. A window with no record
// or no placement is adopted where it is. A settled window that differs from
// its placement is adopted when its old or new space was visible, and owed
// otherwise. An owed window is released once seen in place, repaired where
// that is possible now, and given up on when a completed attempt did not
// take. Recorded windows the look did not see are left alone.
func Decide(recorded []Recorded, look Look) []Decision {
	byBinding := make(map[uint32]Recorded, len(recorded))
	for _, r := range recorded {
		byBinding[r.Binding] = r
	}
	var out []Decision
	for _, s := range look.Windows {
		r, known := byBinding[s.Binding]
		switch {
		case !known:
			out = append(out, Decision{Kind: Adopt, Seen: s})
		case !r.Placed:
			out = append(out, Decision{Kind: Adopt, Window: r.Window, Seen: s})
		case r.Owed:
			out = append(out, owed(r, s, look.Visible))
		case r.Placement.InPlace(s.Placement):
		case look.Visible[r.Space] || look.Visible[s.Space]:
			out = append(out, Decision{Kind: Adopt, Window: r.Window, Seen: s})
		default:
			out = append(out, Decision{Kind: Owe, Window: r.Window, Seen: s}, owed(r, s, look.Visible))
		}
	}
	return out
}

func owed(r Recorded, s Seen, visible map[string]bool) Decision {
	if r.Placement.InPlace(s.Placement) {
		return Decision{Kind: Release, Window: r.Window, Seen: s}
	}
	wrongSpace := s.Space != r.Space
	if r.Progress == Attempted || (r.Progress == Moved && wrongSpace) {
		return Decision{Kind: GiveUp, Window: r.Window, Seen: s}
	}
	return Decision{
		Kind: Repair, Window: r.Window, Seen: s, Want: r.Placement,
		Move:   wrongSpace,
		Resize: visible[r.Space] && !sameFrame(r.Frame, s.Frame),
	}
}

// Progress is what a repair leaves for the next look: Attempted when the
// frame was written or only the space was wrong, Moved when the window was
// moved and its frame is still to do. ok is false when nothing was done.
func (d Decision) Progress() (p Progress, ok bool) {
	switch {
	case d.Kind != Repair:
		return Untried, false
	case d.Resize, d.Move && sameFrame(d.Want.Frame, d.Seen.Frame):
		return Attempted, true
	case d.Move:
		return Moved, true
	}
	return Untried, false
}
