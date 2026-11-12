package main

import (
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/store"
)

func TestSpanLines(t *testing.T) {
	at := time.Date(2026, 11, 8, 15, 50, 39, 0, time.UTC)
	rows := []store.Raw{
		{At: at, Kind: "display burst began"},
		{At: at.Add(11 * time.Second), Kind: "display quiet"},
		{At: at.Add(time.Minute), Kind: "display burst began"},
	}
	got := spanLines(rows)
	if len(got) != 2 || got[0] != "2026-11-08 15:50:39  display disturbance, 11s" || got[1] != "2026-11-08 15:51:39  display disturbance, still open" {
		t.Fatalf("spanLines = %q", got)
	}
}

// log -v shows a change into fullscreen as fullscreen@ and the display.
func TestEventLineShowsAFullscreenTarget(t *testing.T) {
	from := arrangement.Placement{Space: "972AEF80-1F7C-4B2B-9C58-8C3C7E5A6F10", Frame: layout.Rect{X: -1191, Y: 430, W: 1470, H: 923}}
	to := arrangement.FullscreenOn("37D8832A-2D66-02CA-B9F7-8F30A301B230")
	got := eventLine(store.Event{Kind: "adopted", App: "TIDAL", Title: "TIDAL", From: &from, To: &to})
	if want := "adopted   TIDAL | TIDAL  972AEF80 -1191,430 1470x923 -> fullscreen@37D8832A"; got != want {
		t.Fatalf("event line\n%q\nwant\n%q", got, want)
	}
}
