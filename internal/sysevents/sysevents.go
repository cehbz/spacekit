// Package sysevents turns the macOS notifications the agent acts on into
// typed events handed to one sink, on the main thread: NSWorkspace's app
// launches, sleep and wake, space changes and power-off; loginwindow's lock
// and unlock; display reconfiguration; the window server's own window and
// space notifications; and the Dock's overview notifications. Delivery
// needs the AppKit event loop on the main OS thread (see sysevents.m): call
// Run there; it blocks until Stop. OnMain runs a function on that thread,
// which is where SkyLight and Accessibility work belongs.
package sysevents

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework AppKit -framework CoreGraphics -framework ApplicationServices
#include <stdint.h>
int sysevents_start(void);
int sysevents_observe_dock(void);
void sysevents_run(void);
void sysevents_stop(void);
void sysevents_on_main(uintptr_t handle);
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime/cgo"
	"sync"

	"github.com/cehbz/spacekit/internal/event"
)

// The ObjC side reports a kind number; order must match the enum in
// sysevents.m, which TestKindsMatchObjCEnum checks against kindNames.
const (
	kAppLaunched = iota
	kDisplayReconfigured
	kScreensSleep
	kScreensWake
	kSystemWillSleep
	kSystemWake
	kSpaceChanged
	kWillPowerOff
	kSessionUnlocked
	kSessionLocked
	kWindowAttached
	kWindowDetached
	kSpaceCreated
	kSpaceDestroyed
	kSpaceSwitched
	kWindowMoved
	kWindowResized
	kWindowDestroyed
	kOverviewShown
	kOverviewEnded
)

var kindNames = []string{
	"kAppLaunched", "kDisplayReconfigured", "kScreensSleep", "kScreensWake",
	"kSystemWillSleep", "kSystemWake", "kSpaceChanged", "kWillPowerOff", "kSessionUnlocked", "kSessionLocked",
	"kWindowAttached", "kWindowDetached", "kSpaceCreated", "kSpaceDestroyed", "kSpaceSwitched",
	"kWindowMoved", "kWindowResized", "kWindowDestroyed", "kOverviewShown", "kOverviewEnded",
}

// typed builds the event for a kind and its payload; nil for an unknown kind.
func typed(kind int, s event.Stamp, name string, display, flags, window uint32, space uint64) event.Event {
	switch kind {
	case kAppLaunched:
		return event.AppLaunched{Stamp: s, Name: name}
	case kDisplayReconfigured:
		return event.DisplayReconfigured{Stamp: s, Display: display, Flags: flags}
	case kScreensSleep:
		return event.ScreensSlept{Stamp: s}
	case kScreensWake:
		return event.ScreensWoke{Stamp: s}
	case kSystemWillSleep:
		return event.SystemSleeping{Stamp: s}
	case kSystemWake:
		return event.SystemWoke{Stamp: s}
	case kSpaceChanged, kSpaceSwitched:
		return event.SpaceSwitched{Stamp: s}
	case kWillPowerOff:
		return event.PoweringOff{Stamp: s}
	case kSessionUnlocked:
		return event.SessionUnlocked{Stamp: s}
	case kSessionLocked:
		return event.SessionLocked{Stamp: s}
	case kWindowAttached:
		return event.WindowAttached{Stamp: s, Window: window, Space: space}
	case kWindowDetached:
		return event.WindowDetached{Stamp: s, Window: window, Space: space}
	case kSpaceCreated:
		return event.SpaceCreated{Stamp: s, Space: space}
	case kSpaceDestroyed:
		return event.SpaceDestroyed{Stamp: s, Space: space}
	case kWindowMoved:
		return event.WindowMoved{Stamp: s, Window: window}
	case kWindowResized:
		return event.WindowResized{Stamp: s, Window: window}
	case kWindowDestroyed:
		return event.WindowDestroyed{Stamp: s, Window: window}
	case kOverviewShown:
		return event.OverviewShown{Stamp: s}
	case kOverviewEnded:
		return event.OverviewEnded{Stamp: s}
	}
	return nil
}

var (
	sinkMu sync.Mutex
	sink   func(event.Event)
)

func setSink(fn func(event.Event)) {
	sinkMu.Lock()
	sink = fn
	sinkMu.Unlock()
}

func emit(e event.Event) {
	sinkMu.Lock()
	fn := sink
	sinkMu.Unlock()
	if fn != nil {
		fn(e)
	}
}

//export syseventsEmit
func syseventsEmit(kind C.int, name *C.char, display C.uint32_t, flags C.uint32_t, window C.uint32_t, space C.uint64_t) {
	if e := typed(int(kind), event.Now(), C.GoString(name), uint32(display), uint32(flags), uint32(window), uint64(space)); e != nil {
		emit(e)
	}
}

//export syseventsMainCall
func syseventsMainCall(h C.uintptr_t) {
	handle := cgo.Handle(h)
	handle.Value().(func())()
	handle.Delete()
}

// Start installs sink and registers the observers. Call before Run. The
// error reports window-server notifications that could not be registered;
// the NSWorkspace observers are in place either way. sink runs on the main
// thread and must not block.
func Start(sink func(event.Event)) error {
	setSink(sink)
	return startErr(int(C.sysevents_start()))
}

func startErr(n int) error {
	switch {
	case n < 0:
		return errors.New("window-server notifications unavailable: SkyLight symbols missing")
	case n > 0:
		return fmt.Errorf("window-server notifications: %d type(s) failed to register", n)
	}
	return nil
}

// ObserveDock registers for the Dock's Mission Control and App Exposé
// Accessibility notifications. Call after Start, on the main thread: before
// Run from the main goroutine (which runtime.LockOSThread pins there), or
// through OnMain once Run is pumping. The notifications arrive once Run is
// pumping. The observer follows a Dock relaunch.
func ObserveDock() error { return observeDockErr(int(C.sysevents_observe_dock())) }

func observeDockErr(n int) error {
	switch {
	case n < 0:
		return errors.New("overview notifications unavailable: no Dock AX observer")
	case n > 0:
		return fmt.Errorf("overview notifications: %d failed to register", n)
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
