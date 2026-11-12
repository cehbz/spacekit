package main

import (
	"bytes"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/layout"
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

// A decide sees each window on a user space and each titled window
// fullscreen on a display. An untitled window on a fullscreen space (a
// transition's transient, Chrome's toolbar strip) and a window in transit
// are not seen, so never recorded.
func TestSeenWindowsIncludeTitledFullscreenWindows(t *testing.T) {
	p := testPicture()
	p.fsSpace[300] = "D1"
	p.windows = append(p.windows,
		layout.LiveWindow{ID: 3, OwnerName: "TIDAL", Title: "TIDAL", Frame: layout.Rect{X: -1470, Y: 283, W: 1470, H: 923}},
		layout.LiveWindow{ID: 4, OwnerName: "Google Chrome", Frame: layout.Rect{W: 2560, H: 124}},
		layout.LiveWindow{ID: 5, OwnerName: "C", Title: "five", Frame: layout.Rect{W: 300, H: 300}})
	p.fsWindow[3], p.fsWindow[4] = "D1", "D1" // 5 is in transit: on no space
	got := map[uint32]arrangement.Placement{}
	for _, s := range seenWindows(p) {
		got[s.Binding] = s.Placement
	}
	want := map[uint32]arrangement.Placement{
		1: {Space: "s1", Frame: layout.Rect{W: 100, H: 100}},
		3: arrangement.FullscreenOn("D1"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("seen %+v, want %+v", got, want)
	}
}

// The failed-put line names a fullscreen target as fullscreen on its display.
func TestLogDecisionsNamesAFullscreenTarget(t *testing.T) {
	var buf bytes.Buffer
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(os.Stderr); log.SetFlags(flags) })
	logDecisions("put ended", []arrangement.Decision{{Kind: arrangement.Failed, Window: 7,
		Seen: arrangement.Seen{Binding: 13, App: "TIDAL", Title: "TIDAL",
			Placement: arrangement.Placement{Space: "972AEF80-1F7C-4B2B-9C58-8C3C7E5A6F10", Frame: layout.Rect{X: -1191, Y: 430, W: 1470, H: 923}}},
		Want: arrangement.FullscreenOn("37D8832A-2D66-02CA-B9F7-8F30A301B230")}})
	want := "put failed: TIDAL | TIDAL at 972AEF80 -1191,430 1470x923; target fullscreen@37D8832A\n"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("log:\n%s\nwant a line %q", buf.String(), want)
	}
}
