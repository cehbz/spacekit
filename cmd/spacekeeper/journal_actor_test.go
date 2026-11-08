package main

import (
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
)

func TestJournalDrainsOnClose(t *testing.T) {
	st := tempStore(t)
	b := bus.New()
	j := newJournal(b, st)
	done := make(chan struct{})
	go func() { j.run(); close(done) }()
	b.Publish(event.WindowMoved{Stamp: event.Now(), Window: 7})
	b.Publish(WindowQuiet{Stamp: event.Now(), Window: 7})
	b.Publish(CurrentChanged{Stamp: event.Now(), Current: testPicture()}) // not journaled
	b.Publish(Tick{Stamp: event.Now(), N: 1})
	b.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("journal did not finish after Close")
	}
	rows, err := st.Stream(0, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Kind != "window moved" || rows[1].Kind != "window quiet" || rows[1].Payload != `{"Window":7}` || rows[2].Kind != "tick" {
		t.Fatalf("journal = %+v", rows)
	}
}
