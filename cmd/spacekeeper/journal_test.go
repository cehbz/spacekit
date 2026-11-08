package main

import (
	"testing"
	"time"

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
