// Package arrangement holds the rules of the arrangement model: what a look
// at the live session means for the recorded intent. It has no storage and
// no system calls; the store and the agent sit on either side of it.
package arrangement

import (
	"math"

	"github.com/cehbz/spacekit/internal/layout"
)

// Tolerance is the hysteresis band, in points per edge, inside which a frame
// counts as in place: a difference inside it is neither adopted nor put back.
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
	return p.Space == q.Space && SameFrame(p.Frame, q.Frame)
}

// SameFrame reports whether two frames are within Tolerance on every edge.
func SameFrame(a, b layout.Rect) bool {
	near := func(x, y float64) bool { return math.Abs(x-y) <= Tolerance }
	return near(a.X, b.X) && near(a.Y, b.Y) && near(a.X+a.W, b.X+b.W) && near(a.Y+a.H, b.Y+b.H)
}

// Progress records whether a put of a dirty window has been attempted, for
// the next decide.
type Progress int

const (
	Untried   Progress = 0
	Attempted Progress = 2 // the window was moved, resized, or both
)

// Recorded is a window as the store knows it in the current arrangement.
type Recorded struct {
	Window  int64  // store identity
	Binding uint32 // window-server id in this boot
	Placed  bool   // has a placement in the current arrangement
	Placement
	Dirty    bool
	Progress Progress
}

// Seen is a window as a look found it.
type Seen struct {
	Binding            uint32
	PID                int
	Bundle, App, Title string
	Placement
}

// Look is one decide's input: the windows in Current to judge, and which of
// them changed for a system cause (a display change, a reappearance after a
// reboot or relaunch, a put). A window not in System changed for the user.
type Look struct {
	Windows []Seen
	System  map[uint32]bool
}

type Kind int

const (
	Adopt  Kind = iota // the target follows the window (no system cause, or no target yet)
	Dirty              // a system cause moved it: the window is to be put back
	Clean              // a dirty window is at its target
	Put                // a dirty window is off target: act where possible
	Failed             // a put did not take; the target stays, nothing more is tried until the next cause
)

// Decision is what a look means for one window.
type Decision struct {
	Kind         Kind
	Window       int64 // 0: a window never seen before
	Seen         Seen
	Want         Placement // Put, Failed: the target
	Move, Resize bool      // Put: what to do at this decide
}

// Decide compares each seen window with its record. A window with no record
// or no target is adopted where it is. A window at its target is clean (a
// decision only when it was dirty). A window off target changed for a system
// cause (in look.System) is dirty and put, unless a put was already tried,
// which is a failed put: the target stays and nothing more is ordered until
// the next cause. A window off target with no system cause moved for the
// user: the target follows it. Recorded windows the look did not see are left
// alone.
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
		case r.Placement.InPlace(s.Placement):
			if r.Dirty {
				out = append(out, Decision{Kind: Clean, Window: r.Window, Seen: s})
			}
		case !look.System[s.Binding]:
			out = append(out, Decision{Kind: Adopt, Window: r.Window, Seen: s})
		case r.Dirty && r.Progress == Attempted:
			out = append(out, Decision{Kind: Failed, Window: r.Window, Seen: s, Want: r.Placement})
		case r.Dirty:
			out = append(out, put(r, s))
		default:
			out = append(out, Decision{Kind: Dirty, Window: r.Window, Seen: s}, put(r, s))
		}
	}
	return out
}

func put(r Recorded, s Seen) Decision {
	return Decision{
		Kind: Put, Window: r.Window, Seen: s, Want: r.Placement,
		Move:   s.Space != r.Space,
		Resize: !SameFrame(r.Frame, s.Frame),
	}
}

// Progress is what a put leaves for the next decide: Attempted once the
// window was moved or resized. ok is false when nothing was done.
func (d Decision) Progress() (p Progress, ok bool) {
	if d.Kind == Put && (d.Move || d.Resize) {
		return Attempted, true
	}
	return Untried, false
}
