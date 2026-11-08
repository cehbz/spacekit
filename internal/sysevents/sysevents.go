// Package sysevents surfaces the macOS notifications the watcher acts on as
// one Go channel: app launches, display reconfiguration, screen and system
// sleep/wake, active-space changes, logout/power-off, session unlock, and
// the window server's own window and space notifications.
// Delivery needs the AppKit event loop on the main OS thread (see
// sysevents.m for why a plain run loop is not enough): call Run there; it
// blocks until Stop. OnMain runs a function on that thread, which is where
// SkyLight and Accessibility work belongs.
package sysevents

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework AppKit -framework CoreGraphics
#include <stdint.h>
int sysevents_start(void);
void sysevents_run(void);
void sysevents_stop(void);
void sysevents_on_main(uintptr_t handle);
*/
import "C"

import (
	"errors"
	"fmt"
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
	WillPowerOff // logout, restart, or shutdown requested
	SessionUnlocked
	// Window-server connection notifications. The first five are global;
	// the rest arrive only for windows passed to skylight.RequestWindowNotifications.
	WindowAttached  // to a space (Event.Space, Event.Window)
	WindowDetached  // from a space (Event.Space, Event.Window)
	SpaceCreated    // Event.Space
	SpaceDestroyed  // Event.Space
	SpaceSwitched   // no payload
	WindowMoved     // Event.Window
	WindowResized   // Event.Window
	WindowDestroyed // Event.Window
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
	case WillPowerOff:
		return "will power off"
	case SessionUnlocked:
		return "session unlock"
	case WindowAttached:
		return "window attached"
	case WindowDetached:
		return "window detached"
	case SpaceCreated:
		return "space created"
	case SpaceDestroyed:
		return "space destroyed"
	case SpaceSwitched:
		return "space switched"
	case WindowMoved:
		return "window moved"
	case WindowResized:
		return "window resized"
	case WindowDestroyed:
		return "window destroyed"
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
	// Window and Space carry the window-server notification's payload:
	// the window id and the space id, where the kind has them.
	Window uint32
	Space  uint64
}

var events = make(chan Event, 64)

//export syseventsEmit
func syseventsEmit(kind C.int, name *C.char, display C.uint32_t, flags C.uint32_t, window C.uint32_t, space C.uint64_t) {
	e := Event{Kind: Kind(kind), At: time.Now(), Name: C.GoString(name),
		Display: uint32(display), Flags: uint32(flags), Window: uint32(window), Space: uint64(space)}
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

// Start registers the observers. Call before Run. The error reports
// window-server notifications that could not be registered; the NSWorkspace
// observers are in place either way.
func Start() error { return startErr(int(C.sysevents_start())) }

// startErr turns sysevents_start's result into an error: -1 when the
// SkyLight symbols are missing, otherwise the number of notification types
// whose registration failed.
func startErr(n int) error {
	switch {
	case n < 0:
		return errors.New("window-server notifications unavailable: SkyLight symbols missing")
	case n > 0:
		return fmt.Errorf("window-server notifications: %d type(s) failed to register", n)
	}
	return nil
}

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
