package main

import (
	"context"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/bus"
)

// The wired agent: Current reaches the model and a startup decide
// runs, with every process running and stopping on cancel.
func TestAgentStartsAndStops(t *testing.T) {
	b := bus.New()
	st := tempStore(t)
	out := b.Subscribe("t", TargetChanged{})
	ctx, cancel := context.WithCancel(context.Background())
	r := &fakeReader{pic: testPicture()}
	wait := runAgent(ctx, b, st, watchOptions{interval: time.Hour},
		agentDeps{reader: r, orders: &fakeOrders{calls: make(chan string, 10)}, onMain: func(fn func()) { fn() }, send: func([]uint32) error { return nil }})
	decisionsOf(t, out, 1)
	cancel()
	done := make(chan struct{})
	go func() { wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("processes did not stop")
	}
}
