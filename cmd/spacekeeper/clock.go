package main

import (
	"context"
	"log"
	"time"

	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/store"
)

// runClock publishes a tick every interval, numbered from 1.
func runClock(ctx context.Context, b *bus.Bus, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.Publish(Tick{Stamp: event.Now(), N: n})
		}
	}
}

// pruner trims Target's history and the stream every pruneEvery ticks.
type pruner struct {
	sub *bus.Subscription
	st  *store.Store
}

func newPruner(b *bus.Bus, st *store.Store) *pruner {
	return &pruner{sub: b.Subscribe("pruner", Tick{}), st: st}
}

func (p *pruner) run(ctx context.Context) {
	defer p.sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-p.sub.C():
			if !ok {
				return
			}
			if e.(Tick).N%pruneEvery != 0 {
				continue
			}
			now := time.Now()
			if n, err := p.st.Prune(now.Add(-retention), bootTime()); err != nil {
				log.Printf("prune failed: %v", err)
			} else if n > 0 {
				log.Printf("pruned %d placement version(s) older than %d days", n, int(retention.Hours()/24))
			}
			if n, err := p.st.PruneStream(now.Add(-streamRetention)); err != nil {
				log.Printf("prune stream failed: %v", err)
			} else if n > 0 {
				log.Printf("pruned %d stream event(s) older than %d days", n, int(streamRetention.Hours()/24))
			}
		}
	}
}
