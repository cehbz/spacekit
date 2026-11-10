package main

import (
	"strings"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/store"
)

func TestExplainLinesInterleaveDecisionsAndRawEventsByTime(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 17, 54, 12, 428_000_000, time.Local)
	steps := []store.Step{
		{At: t0.Add(544 * time.Millisecond), Change: 1382, Cause: "window settled", Kind: "put"},
		{At: t0.Add(1080 * time.Millisecond), Change: 1385, Cause: "window settled", Kind: "adopted",
			Opened: &arrangement.Placement{Space: "67A5E710-8641-4BAC-B07D-1DA2BEE18754", Frame: layout.Rect{X: 0, Y: 30, W: 1279, H: 1410}}},
	}
	raws := []store.Raw{
		{At: t0, Kind: "window attached", Payload: `{"Window":9432,"Space":4}`},
		{At: t0.Add(9 * time.Millisecond), Kind: "window resized", Payload: `{"Window":9432}`}, // duplicated by the change below: left out
		{At: t0.Add(10 * time.Millisecond), Kind: "window changed", Payload: `{"Window":9432,"Space":4,"Frame":{"X":81,"Y":447,"W":1118,"H":576},"Gone":false}`},
		{At: t0.Add(545 * time.Millisecond), Kind: "target changed", Payload: `{"Decisions":[{"Kind":3,"Window":77,"Seen":{"Binding":9432}}]}`}, // duplicated by the decision line: left out
		{At: t0.Add(549 * time.Millisecond), Kind: "window detached", Payload: `{"Window":9432,"Space":4}`},
		{At: t0.Add(549 * time.Millisecond), Kind: "window attached", Payload: `{"Window":359,"Space":6}`}, // another window: left out
		{At: t0.Add(560 * time.Millisecond), Kind: "put ended", Payload: `{"Steps":[{"Name":"home","Took":18879666,"Confirmed":1,"Unconfirmed":null}],"Cancelled":false}`},
	}
	got := explainLines(steps, raws, 9432)
	want := []string{
		"17:54:12.428  window attached      space 4",
		"17:54:12.438  window changed       space 4 at 81,447 1118x576",
		"17:54:12.972  put                  #1382 window settled",
		"17:54:12.977  window detached      space 4",
		"17:54:13.508  adopted              #1385 window settled → 67A5E710 0,30 1279x1410",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestExplainLinesKeepRawEventsNamingTheWindowAsABinding(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 17, 54, 12, 0, time.Local)
	raws := []store.Raw{
		{At: t0, Kind: "put wanted", Payload: `{"Arr":1,"Decisions":[{"Kind":3,"Window":77,"Seen":{"Binding":9432,"Title":"Save"}}]}`},
		{At: t0, Kind: "put wanted", Payload: `{"Arr":1,"Decisions":[{"Kind":3,"Window":78,"Seen":{"Binding":94321,"Title":"x"}}]}`},
	}
	got := explainLines(nil, raws, 9432)
	if len(got) != 1 || !strings.HasPrefix(got[0], "17:54:12.000  put wanted") {
		t.Fatalf("lines = %q, want the one put naming binding 9432", got)
	}
}

func TestShortDisplaySetAbbreviatesEachDisplay(t *testing.T) {
	got := shortDisplaySet("37D8832A-2D66-02CA-B9F7-8F30A301B230,D1962D28-DB51-432D-9526-A52C3D1D542D")
	if got != "37D8832A+D1962D28" {
		t.Fatalf("got %q", got)
	}
	if got := shortDisplaySet("37D8832A-2D66-02CA-B9F7-8F30A301B230"); got != "37D8832A" {
		t.Fatalf("got %q", got)
	}
}

func TestMatchWindowsByAppOrTitleIgnoringCase(t *testing.T) {
	ws := []layout.LiveWindow{
		{ID: 359, OwnerName: "Google Chrome", Title: ""}, // a hidden space: the terminal sees no title
		{ID: 208, OwnerName: "PocketSmith", Title: "Summary | Dashboard"},
		{ID: 222, OwnerName: "iTerm", Title: "Charless-MacBook-Air"},
	}
	titles := map[uint32]string{359: "Inbox (33) - haynes@edge.ceh.bz - Gmail"}
	if got := matchWindows(ws, titles, "gmail"); len(got) != 1 || got[0].ID != 359 {
		t.Fatalf("gmail, by the recorded title: %+v", got)
	}
	if got := matchWindows(ws, titles, "pocket"); len(got) != 1 || got[0].ID != 208 {
		t.Fatalf("pocket: %+v", got)
	}
	if got := matchWindows(ws, titles, "a"); len(got) != 3 {
		t.Fatalf("a matches every window here: %+v", got)
	}
	if got := matchWindows(ws, titles, "zzz"); got != nil {
		t.Fatalf("zzz: %+v", got)
	}
}
