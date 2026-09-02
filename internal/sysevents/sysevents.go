// Package sysevents surfaces the macOS notifications the watcher acts on as
// one Go channel: app launches, display reconfiguration, screen and system
// sleep/wake, and active-space changes. Delivery needs the AppKit event loop
// on the main OS thread (see sysevents.m for why a plain run loop is not
// enough): call Run there; it blocks until Stop. OnMain runs a function on
// that thread, which is where SkyLight and Accessibility work belongs.
package sysevents

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework AppKit -framework CoreGraphics
#include <stdint.h>
void sysevents_start(void);
void sysevents_run(void);
void sysevents_stop(void);
void sysevents_on_main(uintptr_t handle);
*/
import "C"

import (
	"runtime/cgo"
	"time"
)

type Kind int

// Order must match the enum in sysevents.m.
const (
	AppLaunched Kind = iota
	DisplayReconfigured
	ScreensSleep
	ScreensWake
	SystemWillSleep
	SystemWake
	SpaceChanged
)

func (k Kind) String() string {
	switch k {
	case AppLaunched:
		return "app launched"
	case DisplayReconfigured:
		return "display reconfigured"
	case ScreensSleep:
		return "screens sleep"
	case ScreensWake:
		return "screens wake"
	case SystemWillSleep:
		return "system will sleep"
	case SystemWake:
		return "system wake"
	case SpaceChanged:
		return "space changed"
	}
	return "unknown"
}

type Event struct {
	Kind Kind
	At   time.Time
	Name string // launched app's name; empty for other kinds
	// Display and Flags carry CGDisplayRegisterReconfigurationCallback's
	// arguments for DisplayReconfigured events, for logging.
	Display uint32
	Flags   uint32
}

var events = make(chan Event, 64)

//export syseventsEmit
func syseventsEmit(kind C.int, name *C.char, display C.uint32_t, flags C.uint32_t) {
	e := Event{Kind: Kind(kind), At: time.Now(), Name: C.GoString(name),
		Display: uint32(display), Flags: uint32(flags)}
	select {
	case events <- e:
	default: // consumers re-gather everything; dropping an event is fine
	}
}

//export syseventsMainCall
func syseventsMainCall(h C.uintptr_t) {
	handle := cgo.Handle(h)
	handle.Value().(func())()
	handle.Delete()
}

// Events returns the stream of notifications.
func Events() <-chan Event { return events }

// Start registers the observers. Call before Run.
func Start() { C.sysevents_start() }

// Run runs the AppKit event loop until Stop. Must run on the main OS thread.
func Run() { C.sysevents_run() }

// Stop ends Run. Safe from any goroutine.
func Stop() { C.sysevents_stop() }

// OnMain runs fn on the main thread's run loop and waits for it to finish.
// Call it from a goroutine while Run is pumping; calling it from the main
// thread itself would deadlock.
func OnMain(fn func()) {
	done := make(chan struct{})
	h := cgo.NewHandle(func() {
		fn()
		close(done)
	})
	C.sysevents_on_main(C.uintptr_t(h))
	<-done
}
