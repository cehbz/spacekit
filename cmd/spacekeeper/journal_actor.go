package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/store"
)

// journal writes every event but CurrentChanged to the store, in batches,
// until the bus closes; it is the recording an incident is read from.
type journal struct {
	sub *bus.Subscription
	st  *store.Store
}

func newJournal(b *bus.Bus, st *store.Store) *journal {
	return &journal{sub: b.Subscribe("journal"), st: st}
}

func (j *journal) run() {
	for e := range j.sub.C() {
		batch := []event.Event{e}
	drain:
		for len(batch) < 256 {
			select {
			case e, ok := <-j.sub.C():
				if !ok {
					break drain
				}
				batch = append(batch, e)
			default:
				break drain
			}
		}
		var rows []store.Raw
		for _, e := range batch {
			if _, skip := e.(CurrentChanged); !skip {
				rows = append(rows, rawRow(e))
			}
		}
		if err := j.st.Record(rows); err != nil {
			log.Printf("journal: %v", err)
		}
	}
}

// rawRow is the event as a journal row: its name and its fields as JSON,
// the stamp left out since the row carries the time.
func rawRow(e event.Event) store.Raw {
	payload := "{}"
	if b, err := json.Marshal(e); err == nil {
		payload = string(b)
	} else {
		payload = fmt.Sprintf("%q", fmt.Sprint(e))
	}
	return store.Raw{At: e.When(), Kind: event.Name(e), Payload: payload}
}
