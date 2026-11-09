package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/skylight"
	"github.com/cehbz/spacekit/internal/store"
	"github.com/cehbz/spacekit/internal/sysevents"
)

// agentDeps are the window-server touchpoints the processes share; tests
// substitute every one.
type agentDeps struct {
	reader reader
	orders orders
	onMain func(func())
	send   func([]uint32) error
}

// runAgent constructs every process (each subscribes at once, so the
// screen's first Current finds every subscriber in place), starts them, and
// returns a function that waits for them after ctx is cancelled. The
// journal is not among them: it runs until the bus closes.
func runAgent(ctx context.Context, b *bus.Bus, st *store.Store, o watchOptions, d agentDeps) func() {
	settler := newSettler(b, windowSettle, displaySettle)
	sc := newScreen(b, d.reader, d.onMain)
	sc.send = d.send
	model := newModel(b, st)
	putter := newPutter(b, d.orders, d.onMain)
	pruner := newPruner(b, st)
	var wg sync.WaitGroup
	start := func(f func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f(ctx)
		}()
	}
	for _, run := range []func(context.Context){settler.run, model.run, putter.run, pruner.run, sc.run} {
		start(run)
	}
	start(func(ctx context.Context) { runClock(ctx, b, o.interval) })
	return wg.Wait
}

func watchCmd(o watchOptions) error {
	log.SetFlags(0)
	log.SetOutput(newZoneWriter(log.Writer(), "/etc/localtime"))
	st, err := store.Open(filepath.Join(dataDir(), "spacekeeper.db"))
	if err != nil {
		return err
	}
	b := bus.New()
	j := newJournal(b, st) // subscribed before the first event is published
	journalDone := make(chan struct{})
	go func() {
		j.run()
		close(journalDone)
	}()
	if err := sysevents.Start(b.Publish); err != nil {
		log.Printf("%v; the screen follows the tick only", err)
	}
	if err := sysevents.ObserveDock(); err != nil {
		log.Printf("%v; the overview is seen at validation only", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	wait := runAgent(ctx, b, st, o, agentDeps{reader: liveReader{}, orders: liveOrders{}, onMain: sysevents.OnMain, send: skylight.RequestWindowNotifications})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sig
		log.Printf("%s: stopping", s)
		cancel()
		wait()
		b.Close()
		<-journalDone
		st.Close()
		sysevents.Stop()
	}()
	sysevents.Run()
	return nil
}
