package main

import (
	"testing"

	"github.com/cehbz/spacekit/internal/skylight"
)

func TestPlaceableWindow(t *testing.T) {
	ok := skylight.WindowInfo{Layer: 0, Alpha: 1, Bounds: skylight.WindowBounds{Width: 50, Height: 50}}
	if !placeableWindow(ok, 0, "com.google.Chrome") {
		t.Fatal("a visible 50x50 layer-0 window with no parent is placeable")
	}
	for name, w := range map[string]skylight.WindowInfo{
		"layer":  {Layer: 20, Alpha: 1, Bounds: skylight.WindowBounds{Width: 500, Height: 500}},
		"alpha":  {Layer: 0, Alpha: 0, Bounds: skylight.WindowBounds{Width: 500, Height: 500}},
		"narrow": {Layer: 0, Alpha: 1, Bounds: skylight.WindowBounds{Width: 49, Height: 500}},
		"short":  {Layer: 0, Alpha: 1, Bounds: skylight.WindowBounds{Width: 500, Height: 49}},
	} {
		if placeableWindow(w, 0, "com.google.Chrome") {
			t.Errorf("%s: should not be placeable", name)
		}
	}
	if placeableWindow(ok, 389, "com.google.Chrome") {
		t.Error("a window attached to a parent (a sheet, a save panel, a bubble) is not placeable")
	}
	if placeableWindow(ok, 0, "com.apple.WindowManager") {
		t.Error("the WindowManager process's windows (overlays, tiling handles) are not placeable")
	}
}
