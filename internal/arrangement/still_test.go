package arrangement

import (
	"testing"

	"github.com/cehbz/spacekit/internal/layout"
)

// A look taken while spaces slide sees every window of a space shifted
// sideways by the same amount (observed: x=2327 for windows recorded at
// x=0). Two samples a moment apart differ then, and must not be a look.
func TestStill(t *testing.T) {
	at := func(x float64) []Seen {
		return []Seen{
			saw(1, "S5", layout.Rect{X: x, Y: 30, W: 2560, H: 1410}),
			saw(2, "S5", layout.Rect{X: x, Y: 30, W: 1280, H: 1410}),
		}
	}
	if !Still(at(0), at(0)) {
		t.Fatal("identical samples are still")
	}
	if Still(at(2327), at(1100)) {
		t.Fatal("frames that moved between samples are not still")
	}
	if Still(at(0), at(0)[:1]) {
		t.Fatal("a window that vanished between samples is not still")
	}
	moved := at(0)
	moved[1].Space = "S4"
	if Still(at(0), moved) {
		t.Fatal("a window that changed space between samples is not still")
	}
	if !Still(nil, nil) {
		t.Fatal("an empty session is still")
	}
}
