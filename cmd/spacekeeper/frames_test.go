package main

import (
	"testing"

	"github.com/cehbz/spacekit/internal/layout"
)

func TestFramesReportNamesDivergenceAndMotion(t *testing.T) {
	a := layout.Rect{X: 0, Y: 30, W: 1280, H: 1410}
	b := layout.Rect{X: -2624, Y: 30, W: 1280, H: 1410}
	prev := []sample{{wid: 1, app: "Chrome", list: a, bounds: a}}
	cur := []sample{{wid: 1, app: "Chrome", list: b, bounds: a}, {wid: 2, app: "iTerm", list: a, bounds: a}}
	got := framesReport(prev, cur)
	want := []string{
		"1 Chrome: list -2624,30 1280x1410 (was 0,30 1280x1410); bounds 0,30 1280x1410 (steady); LIST != BOUNDS",
	}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("report = %q, want %q", got, want)
	}
	if got := framesReport(cur, cur); len(got) != 1 || got[0] != "1 Chrome: list -2624,30 1280x1410 (steady); bounds 0,30 1280x1410 (steady); LIST != BOUNDS" {
		t.Fatalf("steady divergence report = %q", got)
	}
	if got := framesReport(prev, prev); len(got) != 0 {
		t.Fatalf("nothing changed, nothing diverges: %q", got)
	}
}
