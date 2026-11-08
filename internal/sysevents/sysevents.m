#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <CoreGraphics/CoreGraphics.h>
#import <ApplicationServices/ApplicationServices.h>
#include <stdint.h>
#include <dlfcn.h>
#include <string.h>

extern void syseventsEmit(int kind, char *name, uint32_t display, uint32_t flags, uint32_t window, uint64_t space);
extern void syseventsMainCall(uintptr_t handle);

// Must match kindNames in sysevents.go.
enum {
	kAppLaunched,
	kDisplayReconfigured,
	kScreensSleep,
	kScreensWake,
	kSystemWillSleep,
	kSystemWake,
	kSpaceChanged,
	kWillPowerOff,
	kSessionUnlocked,
	kSessionLocked,
	kWindowAttached,
	kWindowDetached,
	kSpaceCreated,
	kSpaceDestroyed,
	kSpaceSwitched,
	kWindowMoved,
	kWindowResized,
	kWindowDestroyed,
	kOverviewShown,
	kOverviewEnded,
};

static int observeDock(void);

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
			if ([a.bundleIdentifier isEqualToString:@"com.apple.dock"]) {
				dispatch_after(dispatch_time(DISPATCH_TIME_NOW, NSEC_PER_SEC), dispatch_get_main_queue(), ^{ observeDock(); });
			}
		}
		syseventsEmit(kind, (char *)app, 0, 0, 0, 0);
	}];
}

// loginwindow posts com.apple.screenIsLocked and com.apple.screenIsUnlocked.
static void observeLock(void) {
	NSDistributedNotificationCenter *c = [NSDistributedNotificationCenter defaultCenter];
	[c addObserverForName:@"com.apple.screenIsLocked" object:nil queue:nil usingBlock:^(NSNotification *note) {
		syseventsEmit(kSessionLocked, "", 0, 0, 0, 0);
	}];
	[c addObserverForName:@"com.apple.screenIsUnlocked" object:nil queue:nil usingBlock:^(NSNotification *note) {
		syseventsEmit(kSessionUnlocked, "", 0, 0, 0, 0);
	}];
}

static void displayChanged(CGDirectDisplayID display, CGDisplayChangeSummaryFlags flags, void *userInfo) {
	syseventsEmit(kDisplayReconfigured, "", display, (uint32_t)flags, 0, 0);
}

// Window-server connection notifications, delivered on the main run loop.
// Types and payloads were measured on macOS 26 (see the design spec):
// 1325/1326 attach/detach carry a u64 space id then a u32 window id;
// 1327/1328/1329 carry a u64 space id; 1401 carries nothing; 806/807/804
// carry a u32 window id.
typedef void (*sls_notify_cb)(uint32_t type, void *data, size_t len, void *ctx, int cid);
typedef int (*sls_cid_fn)(void);
typedef CGError (*sls_register_fn)(int cid, sls_notify_cb cb, uint32_t type, void *ctx);

static const struct { uint32_t type; int kind; } slsTypes[] = {
	{1325, kWindowAttached}, {1326, kWindowDetached},
	{1327, kSpaceCreated}, {1328, kSpaceDestroyed},
	{1329, kSpaceSwitched}, {1401, kSpaceSwitched},
	{806, kWindowMoved}, {807, kWindowResized},
	{804, kWindowDestroyed},
};

static void slsNotify(uint32_t type, void *data, size_t len, void *ctx, int cid) {
	int kind = -1;
	for (size_t i = 0; i < sizeof slsTypes / sizeof slsTypes[0]; i++) {
		if (slsTypes[i].type == type) { kind = slsTypes[i].kind; break; }
	}
	if (kind < 0) return;
	uint32_t window = 0;
	uint64_t space = 0;
	switch (type) {
	case 1325: case 1326:
		if (len >= 12) { memcpy(&space, data, 8); memcpy(&window, (char *)data + 8, 4); }
		break;
	case 1327: case 1328: case 1329:
		if (len >= 8) memcpy(&space, data, 8);
		break;
	case 806: case 807: case 804:
		if (len >= 4) memcpy(&window, data, 4);
		break;
	}
	syseventsEmit(kind, "", 0, 0, window, space);
}

// Returns -1 when SkyLight or its symbols are missing, otherwise the number
// of types whose registration failed.
static int registerWindowServer(void) {
	void *h = dlopen("/System/Library/PrivateFrameworks/SkyLight.framework/SkyLight", RTLD_LAZY);
	if (!h) return -1;
	sls_cid_fn cidFn = (sls_cid_fn)dlsym(h, "SLSMainConnectionID");
	sls_register_fn reg = (sls_register_fn)dlsym(h, "SLSRegisterConnectionNotifyProc");
	if (!cidFn || !reg) return -1;
	int cid = cidFn();
	int failed = 0;
	for (size_t i = 0; i < sizeof slsTypes / sizeof slsTypes[0]; i++) {
		if (reg(cid, slsNotify, slsTypes[i].type, NULL) != kCGErrorSuccess) failed++;
	}
	return failed;
}

int sysevents_start(void) {
	// NSApplication is needed even though nothing is ever shown: display
	// reconfiguration callbacks, screens sleep/wake, and active-space changes
	// all arrive over the SkyLight display-notification channel, which only
	// the AppKit event loop services. A bare CFRunLoopRun (even after
	// -finishLaunching) never delivers them; measured on macOS 26.5.
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyProhibited];
	observe(NSWorkspaceDidLaunchApplicationNotification, kAppLaunched);
	observe(NSWorkspaceScreensDidSleepNotification, kScreensSleep);
	observe(NSWorkspaceScreensDidWakeNotification, kScreensWake);
	observe(NSWorkspaceWillSleepNotification, kSystemWillSleep);
	observe(NSWorkspaceDidWakeNotification, kSystemWake);
	observe(NSWorkspaceActiveSpaceDidChangeNotification, kSpaceChanged);
	observe(NSWorkspaceWillPowerOffNotification, kWillPowerOff);
	observeLock();
	CGDisplayRegisterReconfigurationCallback(displayChanged, NULL);
	return registerWindowServer();
}

static AXObserverRef dockObserver = NULL;

static void dockNotify(AXObserverRef o, AXUIElementRef el, CFStringRef name, void *ctx) {
	int kind = CFEqual(name, CFSTR("AXExposeExit")) ? kOverviewEnded : kOverviewShown;
	syseventsEmit(kind, "", 0, 0, 0, 0);
}

// Returns -1 with no Dock or no observer, else the count of notifications
// that failed to register. Replaces any earlier observer.
static int observeDock(void) {
	if (dockObserver) {
		CFRunLoopRemoveSource(CFRunLoopGetMain(), AXObserverGetRunLoopSource(dockObserver), kCFRunLoopCommonModes);
		CFRelease(dockObserver);
		dockObserver = NULL;
	}
	NSArray *docks = [NSRunningApplication runningApplicationsWithBundleIdentifier:@"com.apple.dock"];
	if (docks.count == 0) return -1;
	pid_t pid = [docks[0] processIdentifier];
	if (AXObserverCreate(pid, dockNotify, &dockObserver) != kAXErrorSuccess) { dockObserver = NULL; return -1; }
	AXUIElementRef dock = AXUIElementCreateApplication(pid);
	int failed = 0;
	for (NSString *n in @[@"AXExposeShowAllWindows", @"AXExposeShowFrontWindows", @"AXExposeShowDesktop", @"AXExposeExit"]) {
		if (AXObserverAddNotification(dockObserver, dock, (__bridge CFStringRef)n, NULL) != kAXErrorSuccess) failed++;
	}
	CFRelease(dock);
	CFRunLoopAddSource(CFRunLoopGetMain(), AXObserverGetRunLoopSource(dockObserver), kCFRunLoopCommonModes);
	return failed;
}

int sysevents_observe_dock(void) { return observeDock(); }

void sysevents_run(void) { [NSApp run]; }

void sysevents_stop(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp stop:nil];
		// -stop takes effect once the loop sees an event; give it one.
		NSEvent *e = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
		                                location:NSZeroPoint modifierFlags:0 timestamp:0
		                            windowNumber:0 context:nil subtype:0 data1:0 data2:0];
		[NSApp postEvent:e atStart:YES];
	});
}

// Runs the Go function behind handle on the main run loop.
void sysevents_on_main(uintptr_t handle) {
	CFRunLoopPerformBlock(CFRunLoopGetMain(), kCFRunLoopCommonModes, ^{
		syseventsMainCall(handle);
	});
	CFRunLoopWakeUp(CFRunLoopGetMain());
}
