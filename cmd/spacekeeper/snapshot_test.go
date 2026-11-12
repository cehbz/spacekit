package main

import (
	"errors"
	"testing"

	"github.com/cehbz/spacekit/internal/layout"
)

var errAny = errors.New("no screen")

func TestSnapshotVisibleFrameOfDisplay(t *testing.T) {
	vis := layout.Rect{X: 0, Y: 30, W: 2560, H: 1410}
	p := &snapshot{displays: []layout.CurrentDisplay{{UUID: "D1", Visible: vis}, {UUID: "D2"}}}
	for _, c := range []struct {
		display string
		want    layout.Rect
		ok      bool
	}{
		{"D1", vis, true},
		{"D2", layout.Rect{}, false}, // listed, no visible frame
		{"D9", layout.Rect{}, false}, // unknown
	} {
		got, ok := p.visible(c.display)
		if got != c.want || ok != c.ok {
			t.Errorf("visible(%s) = %v, %v; want %v, %v", c.display, got, ok, c.want, c.ok)
		}
	}
}

func TestCloneKeepsVisibleFrames(t *testing.T) {
	vis := layout.Rect{X: -1470, Y: 283, W: 1470, H: 923}
	p := testPicture()
	p.displays[0].Visible = vis
	if got, ok := p.clone().visible("D1"); !ok || got != vis {
		t.Fatalf("clone().visible(D1) = %v, %v; want %v, true", got, ok, vis)
	}
}

func TestFillVisibleAsksTheReaderPerDisplay(t *testing.T) {
	vis := layout.Rect{X: 0, Y: 30, W: 2560, H: 1410}
	p := testPicture()
	p.fillVisible(func(uuid string) (layout.Rect, error) {
		if uuid == "D1" {
			return vis, nil
		}
		return layout.Rect{}, errAny
	})
	if got, ok := p.visible("D1"); !ok || got != vis {
		t.Fatalf("visible(D1) = %v, %v; want %v, true", got, ok, vis)
	}
}
