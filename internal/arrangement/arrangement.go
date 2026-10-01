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
