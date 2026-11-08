// Package event defines the agent's events: what the window server says,
// what the system says, and the stamp every event carries. Events derived
// by the agent's own processes are defined beside those processes; all of
// them satisfy Event.
package event

import (
	"reflect"
	"strings"
	"time"
	"unicode"
)

// Event is anything that happened at a time.
type Event interface{ When() time.Time }

// Stamp is the time an event happened; embed it in every event type. It
// stays out of JSON: a journal row carries the time itself.
type Stamp struct {
	At time.Time `json:"-"`
}

func (s Stamp) When() time.Time { return s.At }

func Now() Stamp { return Stamp{At: time.Now()} }

// Name is the event's type name as lower-case words: WindowMoved is
// "window moved". It keys journal rows and names log lines.
func Name(e Event) string {
	n := reflect.TypeOf(e).Name()
	var b strings.Builder
	for i, r := range n {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// What the window server says. Window ids are the server's; space ids are
// the server's 64-bit managed space ids.
type (
	WindowAttached struct {
		Stamp
		Window uint32
		Space  uint64
	}
	WindowDetached struct {
		Stamp
		Window uint32
		Space  uint64
	}
	WindowMoved struct {
		Stamp
		Window uint32
	}
	WindowResized struct {
		Stamp
		Window uint32
	}
	WindowDestroyed struct {
		Stamp
		Window uint32
	}
	SpaceCreated struct {
		Stamp
		Space uint64
	}
	SpaceDestroyed struct {
		Stamp
		Space uint64
	}
	// SpaceSwitched is the window server's 1329/1401 and NSWorkspace's
	// active-space notification alike.
	SpaceSwitched struct{ Stamp }
)

// What the system says.
type (
	DisplayReconfigured struct {
		Stamp
		Display, Flags uint32
	}
	ScreensSlept    struct{ Stamp }
	ScreensWoke     struct{ Stamp }
	SystemSleeping  struct{ Stamp }
	SystemWoke      struct{ Stamp }
	SessionLocked   struct{ Stamp }
	SessionUnlocked struct{ Stamp }
	PoweringOff     struct{ Stamp } // logout, restart or shutdown requested
	AppLaunched     struct {
		Stamp
		Name string
	}
	OverviewShown struct{ Stamp } // Mission Control or App Exposé
	OverviewEnded struct{ Stamp }
)
