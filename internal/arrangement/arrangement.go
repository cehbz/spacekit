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

// Placement is where a window belongs, in one of two forms: a space and a
// frame, or fullscreen on a display. Space is a space key: the space UUID, or
// display:<uuid>/<index> for the untitled desktop. Fullscreen is the display
// UUID of the fullscreen form; Space and Frame are empty in it.
type Placement struct {
	Space      string
	Frame      layout.Rect
	Fullscreen string
}

// FullscreenOn is the fullscreen form on a display.
func FullscreenOn(display string) Placement { return Placement{Fullscreen: display} }

// InPlace reports whether q is where p is: both fullscreen on the same
// display, or both on the same space and within Tolerance on every edge.
func (p Placement) InPlace(q Placement) bool {
	if p.Fullscreen != "" || q.Fullscreen != "" {
		return p.Fullscreen == q.Fullscreen
	}
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

// Look is one decide's input: the windows in Current to decide, and which of
// them changed for a system cause (a display change, a reappearance after a
// reboot or relaunch, a put). A window not in System changed for the user.
type Look struct {
	Windows []Seen
	System  map[uint32]bool
	Retry   bool // a fresh system cause: a window whose put was already attempted is put again rather than reported failed
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
	Move, Resize bool      // Put: what to do at this decide; a fullscreen target is one Move
}

// Decide compares each seen window with its record. A window with no record
// or no target is adopted where it is. A window at its target is clean (a
// decision only when it was dirty). A dirty window off target that no put has
// tried yet was marked by an explicit instruction (owe, undo, a reappearance)
// and is put whatever the cause. A window off target changed for a system
// cause (in look.System) is dirty and put, unless a put was already tried,
// which is a failed put: the target stays and nothing more is ordered until
// the next cause; look.Retry is that next cause, and the window is put again.
// A window off target with no system cause moved for the user: the target
// follows it. Recorded windows the look did not see are left alone.
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
		case r.Dirty && r.Progress == Untried:
			out = append(out, put(r, s))
		case !look.System[s.Binding]:
			out = append(out, Decision{Kind: Adopt, Window: r.Window, Seen: s})
		case r.Dirty && r.Progress == Attempted && !look.Retry:
			out = append(out, Decision{Kind: Failed, Window: r.Window, Seen: s, Want: r.Placement})
		case r.Dirty:
			out = append(out, put(r, s))
		default:
			out = append(out, Decision{Kind: Dirty, Window: r.Window, Seen: s}, put(r, s))
		}
	}
	return out
}

// put orders a dirty window to its target: a move, a resize or both, or for
// a fullscreen target one move, which the putter carries out as staging the
// window to the display's shown space and setting AXFullScreen.
func put(r Recorded, s Seen) Decision {
	d := Decision{Kind: Put, Window: r.Window, Seen: s, Want: r.Placement}
	if r.Fullscreen != "" {
		d.Move = true
		return d
	}
	d.Move, d.Resize = s.Space != r.Space, !SameFrame(r.Frame, s.Frame)
	return d
}

// Progress is what a put leaves for the next decide: Attempted once the
// window was moved or resized. ok is false when nothing was done.
func (d Decision) Progress() (p Progress, ok bool) {
	if d.Kind == Put && (d.Move || d.Resize) {
		return Attempted, true
	}
	return Untried, false
}
