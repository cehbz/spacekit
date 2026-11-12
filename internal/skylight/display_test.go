package skylight

import (
	"errors"
	"testing"
)

func TestServerFrameFlipsAppKitRectangles(t *testing.T) {
	for _, c := range []struct {
		primaryHeight float64
		in            rect
		x, y, w, h    float64
	}{
		{1440, rect{-1470, 234, 1470, 923}, -1470, 283, 1470, 923}, // the MacBook left of a 2560x1440 primary
		{1440, rect{0, 0, 2560, 1410}, 0, 30, 2560, 1410},          // the primary under its 30-point menu bar
	} {
		x, y, w, h := serverFrame(c.primaryHeight, c.in)
		if x != c.x || y != c.y || w != c.w || h != c.h {
			t.Errorf("serverFrame(%v, %v) = %v,%v %vx%v, want %v,%v %vx%v", c.primaryHeight, c.in, x, y, w, h, c.x, c.y, c.w, c.h)
		}
	}
}

// Runs against this machine's displays: each display the window server
// lists has an AppKit screen whose visible frame is non-empty, inside its
// frame and below its top edge (the menu bar).
func TestDisplayVisibleFrameLiesInsideEachDisplay(t *testing.T) {
	displays, err := ManagedDisplaySpaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range displays {
		frame, visible, _, err := displayFrames(d.UUID)
		if err != nil {
			t.Fatalf("displayFrames(%s): %v", d.UUID, err)
		}
		if visible.w <= 0 || visible.h <= 0 {
			t.Errorf("%s: visible frame %v is empty", d.UUID, visible)
		}
		if visible.x < frame.x || visible.y < frame.y || visible.x+visible.w > frame.x+frame.w || visible.y+visible.h > frame.y+frame.h {
			t.Errorf("%s: visible frame %v outside frame %v", d.UUID, visible, frame)
		}
		if visible.y+visible.h >= frame.y+frame.h {
			t.Errorf("%s: visible frame %v reaches the top of frame %v; the menu bar should be excluded", d.UUID, visible, frame)
		}
	}
}

func TestDisplayVisibleFrameUnknownDisplay(t *testing.T) {
	_, _, _, _, err := DisplayVisibleFrame("00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("err = %v, want ErrNoDisplay", err)
	}
}
