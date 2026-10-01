package arrangement

import (
	"testing"

	"github.com/cehbz/spacekit/internal/layout"
)

var (
	full   = layout.Rect{X: 0, Y: 30, W: 1280, H: 1410}
	shrunk = layout.Rect{X: 0, Y: 517, W: 735, H: 923}
)

func TestInPlaceHysteresis(t *testing.T) {
	p := Placement{Space: "S1", Frame: full}
	cases := []struct {
		name string
		q    Placement
		want bool
	}{
		{"identical", Placement{"S1", full}, true},
		{"three points off on every edge", Placement{"S1", layout.Rect{X: 3, Y: 27, W: 1280, H: 1410}}, true},
		{"width one point narrower", Placement{"S1", layout.Rect{X: 0, Y: 30, W: 1279, H: 1410}}, true},
		{"four points off", Placement{"S1", layout.Rect{X: 4, Y: 30, W: 1280, H: 1410}}, false},
		{"far edge four points off", Placement{"S1", layout.Rect{X: 0, Y: 30, W: 1284, H: 1410}}, false},
		{"shrunk", Placement{"S1", shrunk}, false},
		{"other space, same frame", Placement{"S2", full}, false},
	}
	for _, c := range cases {
		if got := p.InPlace(c.q); got != c.want {
			t.Errorf("%s: InPlace = %v, want %v", c.name, got, c.want)
		}
	}
}
