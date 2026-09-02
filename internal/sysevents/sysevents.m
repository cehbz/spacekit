#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <CoreGraphics/CoreGraphics.h>
#include <stdint.h>

extern void syseventsEmit(int kind, char *name, uint32_t display, uint32_t flags);
extern void syseventsMainCall(uintptr_t handle);

// Must match the Kind constants in sysevents.go.
enum {
	kAppLaunched,
	kDisplayReconfigured,
	kScreensSleep,
	kScreensWake,
	kSystemWillSleep,
	kSystemWake,
	kSpaceChanged,
};

static void observe(NSNotificationName name, int kind) {
	[[[NSWorkspace sharedWorkspace] notificationCenter]
		addObserverForName:name
		            object:nil
		             queue:nil
		        usingBlock:^(NSNotification *note) {
		const char *app = "";
		if (kind == kAppLaunched) {
			NSRunningApplication *a = note.userInfo[NSWorkspaceApplicationKey];
			if (a.localizedName) app = a.localizedName.UTF8String;
		}
		syseventsEmit(kind, (char *)app, 0, 0);
	}];
}

static void displayChanged(CGDirectDisplayID display, CGDisplayChangeSummaryFlags flags, void *userInfo) {
	syseventsEmit(kDisplayReconfigured, "", display, (uint32_t)flags);
}

void sysevents_start(void) {
	observe(NSWorkspaceDidLaunchApplicationNotification, kAppLaunched);
	observe(NSWorkspaceScreensDidSleepNotification, kScreensSleep);
	observe(NSWorkspaceScreensDidWakeNotification, kScreensWake);
	observe(NSWorkspaceWillSleepNotification, kSystemWillSleep);
	observe(NSWorkspaceDidWakeNotification, kSystemWake);
	observe(NSWorkspaceActiveSpaceDidChangeNotification, kSpaceChanged);
	CGDisplayRegisterReconfigurationCallback(displayChanged, NULL);
}

void sysevents_run(void)  { CFRunLoopRun(); }
void sysevents_stop(void) { CFRunLoopStop(CFRunLoopGetMain()); }

// Runs the Go function behind handle on the main run loop.
void sysevents_on_main(uintptr_t handle) {
	CFRunLoopPerformBlock(CFRunLoopGetMain(), kCFRunLoopCommonModes, ^{
		syseventsMainCall(handle);
	});
	CFRunLoopWakeUp(CFRunLoopGetMain());
}
