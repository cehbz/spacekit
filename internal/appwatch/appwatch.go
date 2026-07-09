// Package appwatch surfaces NSWorkspace app-launch notifications as a Go
// channel, so a restore can reconcile as apps finish launching instead of
// guessing a settle delay. Delivery needs a run loop: call Run on the main
// OS thread; it blocks until Stop.
package appwatch

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

extern void appwatchLaunched(char *name);

static void appwatch_start(void) {
	[[[NSWorkspace sharedWorkspace] notificationCenter]
		addObserverForName:NSWorkspaceDidLaunchApplicationNotification
		            object:nil
		             queue:nil
		        usingBlock:^(NSNotification *note) {
		NSRunningApplication *app = note.userInfo[NSWorkspaceApplicationKey];
		const char *name = app.localizedName.UTF8String;
		appwatchLaunched((char *)(name ? name : ""));
	}];
}

static void appwatch_run(void)  { CFRunLoopRun(); }
static void appwatch_stop(void) { CFRunLoopStop(CFRunLoopGetMain()); }
*/
import "C"

var events = make(chan string, 16)

//export appwatchLaunched
func appwatchLaunched(name *C.char) {
	select {
	case events <- C.GoString(name):
	default: // reconcile passes re-gather everything; dropping an event is fine
	}
}

// Events returns the stream of launched-app names.
func Events() <-chan string { return events }

// Start registers the observer. Call before Run.
func Start() { C.appwatch_start() }

// Run pumps the current thread's run loop until Stop. Must run on the main
// OS thread or NSWorkspace notifications are not delivered.
func Run() { C.appwatch_run() }

// Stop ends Run. Safe from any goroutine.
func Stop() { C.appwatch_stop() }
