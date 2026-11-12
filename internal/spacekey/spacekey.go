// Package spacekey posts the space-switch shortcut, Ctrl+Left or
// Ctrl+Right, the way Mission Control accepts it from a process. Posting
// needs Accessibility. The shortcut switches the display under the pointer,
// one space along that display's order, with the native slide.
package spacekey

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <stdlib.h>
#include "post.h"

// spacekey_pointer sets *p to the pointer's position in global
// coordinates. Returns 0, -1 when the position could not be read.
static int spacekey_pointer(CGPoint *p) {
	CGEventRef e = CGEventCreate(NULL);
	if (!e) return -1;
	*p = CGEventGetLocation(e);
	CFRelease(e);
	return 0;
}

// spacekey_centre sets *centre to the centre of the active display whose
// UUID is uuid. Returns 0, -1 when no active display has that UUID, -2 when
// the active displays could not be listed.
static int spacekey_centre(const char *uuid, CGPoint *centre) {
	CGDirectDisplayID ids[32];
	uint32_t n = 0;
	if (CGGetActiveDisplayList(32, ids, &n) != kCGErrorSuccess) return -2;
	CFStringRef want = CFStringCreateWithCString(NULL, uuid, kCFStringEncodingUTF8);
	if (!want) return -1;
	int rc = -1;
	for (uint32_t i = 0; i < n && rc != 0; i++) {
		CFUUIDRef u = CGDisplayCreateUUIDFromDisplayID(ids[i]);
		if (!u) continue;
		CFStringRef s = CFUUIDCreateString(NULL, u);
		CFRelease(u);
		if (!s) continue;
		if (CFStringCompare(s, want, kCFCompareCaseInsensitive) == kCFCompareEqualTo) {
			CGRect b = CGDisplayBounds(ids[i]);
			*centre = CGPointMake(CGRectGetMidX(b), CGRectGetMidY(b));
			rc = 0;
		}
		CFRelease(s);
	}
	CFRelease(want);
	return rc;
}
*/
import "C"

import (
	"fmt"
	"time"
	"unsafe"
)

// pointerHold is how long the pointer stays on the display after the post:
// Mission Control reads the pointer about 45 ms after it at idle; 100 ms is
// the margin.
const pointerHold = 100 * time.Millisecond

// Step posts Ctrl+Right when right, else Ctrl+Left.
func Step(right bool) error {
	r := C.int(0)
	if right {
		r = 1
	}
	if rc := C.spacekey_post(r); rc != 0 {
		return fmt.Errorf("posting ctrl+arrow failed (rc=%d)", int(rc))
	}
	return nil
}

// StepOn posts the shortcut as Step does with the pointer moved to the
// centre of the display whose UUID is display, held there for pointerHold,
// then put back. Nothing is posted unless the pointer reached display.
func StepOn(display string, right bool) (err error) {
	cs := C.CString(display)
	defer C.free(unsafe.Pointer(cs))
	var centre C.CGPoint
	switch C.spacekey_centre(cs, &centre) {
	case 0:
	case -2:
		return fmt.Errorf("listing active displays failed")
	default:
		return fmt.Errorf("no active display %s", display)
	}
	var saved C.CGPoint
	if C.spacekey_pointer(&saved) != 0 {
		return fmt.Errorf("reading the pointer position failed")
	}
	if rc := C.CGWarpMouseCursorPosition(centre); rc != C.kCGErrorSuccess {
		return fmt.Errorf("moving the pointer to display %s failed (CGError %d)", display, int(rc))
	}
	defer func() {
		if rc := C.CGWarpMouseCursorPosition(saved); rc != C.kCGErrorSuccess && err == nil {
			err = fmt.Errorf("moving the pointer back failed (CGError %d)", int(rc))
		}
	}()
	if err := Step(right); err != nil {
		return err
	}
	time.Sleep(pointerHold)
	return nil
}
