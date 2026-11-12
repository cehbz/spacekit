package main

/*
#cgo CFLAGS: -I${SRCDIR}/../../internal/spacekey
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <stdio.h>

// spacekey_post: the shared poster, also behind spacekey.Step.
#include "post.h"

static int ax_trusted(int prompt) {
	if (!prompt) return AXIsProcessTrusted();
	CFStringRef keys[] = { kAXTrustedCheckOptionPrompt };
	CFBooleanRef vals[] = { kCFBooleanTrue };
	CFDictionaryRef opts = CFDictionaryCreate(NULL, (const void **)keys, (const void **)vals, 1,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	Boolean ok = AXIsProcessTrustedWithOptions(opts);
	CFRelease(opts);
	return ok;
}

// Daemon: an active session event tap on scroll-wheel events. A tilt click is
// a discrete horizontal-only delta (M720: exactly one event per click,
// h = ±1). Continuous deltas (trackpads, Magic Mouse) and shift+wheel
// (synthesized into horizontal scroll downstream of HID) pass through.
// The callback stays in C and posts directly: tap callbacks that run slow get
// auto-disabled by the system, so no cgo round-trip per event.
static int g_invert = 0;
static CFMachPortRef g_tap = NULL;

static CGEventRef tilt_callback(CGEventTapProxy proxy, CGEventType type, CGEventRef event, void *info) {
	if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
		if (g_tap) CGEventTapEnable(g_tap, true);
		fprintf(stderr, "spaceswitch: tap re-enabled (disable type=%d)\n", (int)type);
		return event;
	}
	if (type != kCGEventScrollWheel) return event;

	int64_t continuous = CGEventGetIntegerValueField(event, kCGScrollWheelEventIsContinuous);
	int64_t v = CGEventGetIntegerValueField(event, kCGScrollWheelEventDeltaAxis1);
	int64_t h = CGEventGetIntegerValueField(event, kCGScrollWheelEventDeltaAxis2);
	if (continuous || h == 0 || v != 0) return event;
	if (CGEventGetFlags(event) & kCGEventFlagMaskShift) return event;

	// CGEvent scroll deltas are positive for up/left, so h > 0 is tilt-left.
	int right = (h < 0) ? !g_invert : g_invert;
	spacekey_post(right);
	return NULL; // consume the tilt so it doesn't also scroll horizontally
}

static int run_daemon(int invert) {
	g_invert = invert;
	g_tap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap, kCGEventTapOptionDefault,
		CGEventMaskBit(kCGEventScrollWheel), tilt_callback, NULL);
	if (!g_tap) return -1;
	CFRunLoopSourceRef src = CFMachPortCreateRunLoopSource(NULL, g_tap, 0);
	CFRunLoopAddSource(CFRunLoopGetCurrent(), src, kCFRunLoopCommonModes);
	CGEventTapEnable(g_tap, true);
	CFRunLoopRun();
	return 0;
}
*/
import "C"

import "fmt"

func axTrusted(prompt bool) bool {
	p := C.int(0)
	if prompt {
		p = 1
	}
	return C.ax_trusted(p) != 0
}

// runDaemon blocks in CFRunLoopRun and only returns on setup failure.
func runDaemon(invert bool) error {
	i := C.int(0)
	if invert {
		i = 1
	}
	if rc := C.run_daemon(i); rc != 0 {
		return fmt.Errorf("event tap creation failed — is Accessibility granted to this binary?")
	}
	return nil
}
