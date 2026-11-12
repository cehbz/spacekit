// The space-switch shortcut, posted so that Mission Control acts on it.
// Mission Control ignores synthetic shortcuts posted to the session tap from
// a session-state source. Building the event from the HID system state and
// posting to the HID tap is the documented bypass (MacGesture fix). Physical
// arrow keys carry the secondary-fn flag, so the synthetic ones do too.
// Included by package spacekey and by cmd/spaceswitch, whose event-tap
// callback posts from C.
#ifndef SPACEKEY_POST_H
#define SPACEKEY_POST_H

#include <ApplicationServices/ApplicationServices.h>

// spacekey_post posts Ctrl+Right when right is non-zero, else Ctrl+Left.
// Returns 0, -1 when no event source, -2 when an event could not be made.
static int spacekey_post(int right) {
	const CGEventFlags flags = kCGEventFlagMaskControl | kCGEventFlagMaskSecondaryFn;
	CGEventSourceRef src = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
	if (!src) return -1;
	CGKeyCode key = right ? 124 : 123;
	CGEventRef down = CGEventCreateKeyboardEvent(src, key, true);
	CGEventRef up = CGEventCreateKeyboardEvent(src, key, false);
	int rc = 0;
	if (down && up) {
		CGEventSetFlags(down, flags);
		CGEventSetFlags(up, flags);
		CGEventPost(kCGHIDEventTap, down);
		CGEventPost(kCGHIDEventTap, up);
	} else {
		rc = -2;
	}
	if (down) CFRelease(down);
	if (up) CFRelease(up);
	CFRelease(src);
	return rc;
}

#endif
