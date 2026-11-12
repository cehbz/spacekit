package skylight

/*
#cgo CFLAGS: -x objective-c
#import <AppKit/AppKit.h>
#include <CoreGraphics/CoreGraphics.h>
#include <stdlib.h>

// sk_display_frames fills *frame and *visible with the frame and visible
// frame of the AppKit screen whose display UUID is uuid, and *primary with
// the primary screen's height, all in AppKit coordinates (origin at the
// primary screen's bottom-left, y up). Returns 0, or -1 when no screen has
// that UUID.
static int sk_display_frames(const char *uuid, CGRect *frame, CGRect *visible, double *primary) {
	int rc = -1;
	@autoreleasepool {
		NSArray<NSScreen *> *screens = [NSScreen screens];
		if (screens.count == 0) return -1;
		*primary = screens[0].frame.size.height;
		CFStringRef want = CFStringCreateWithCString(NULL, uuid, kCFStringEncodingUTF8);
		if (!want) return -1;
		for (NSScreen *s in screens) {
			NSNumber *num = s.deviceDescription[@"NSScreenNumber"];
			CFUUIDRef u = CGDisplayCreateUUIDFromDisplayID((CGDirectDisplayID)num.unsignedIntValue);
			if (!u) continue;
			CFStringRef str = CFUUIDCreateString(NULL, u);
			CFRelease(u);
			if (!str) continue;
			if (CFStringCompare(str, want, kCFCompareCaseInsensitive) == kCFCompareEqualTo) {
				*frame = NSRectToCGRect(s.frame);
				*visible = NSRectToCGRect(s.visibleFrame);
				rc = 0;
			}
			CFRelease(str);
			if (rc == 0) break;
		}
		CFRelease(want);
	}
	return rc;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// ErrNoDisplay reports a display UUID AppKit lists no screen for.
var ErrNoDisplay = errors.New("no screen for display")

// rect is an AppKit rectangle: origin at the primary screen's bottom-left, y up.
type rect struct{ x, y, w, h float64 }

func toRect(r C.CGRect) rect {
	return rect{float64(r.origin.x), float64(r.origin.y), float64(r.size.width), float64(r.size.height)}
}

// serverFrame converts an AppKit rectangle to window-server coordinates:
// origin at the primary display's top-left, y down.
func serverFrame(primaryHeight float64, r rect) (x, y, w, h float64) {
	return r.x, primaryHeight - (r.y + r.h), r.w, r.h
}

// displayFrames reads the frame and visible frame of the display with UUID
// uuid from AppKit, with the primary screen's height for conversion.
func displayFrames(uuid string) (frame, visible rect, primaryHeight float64, err error) {
	cs := C.CString(uuid)
	defer C.free(unsafe.Pointer(cs))
	var f, v C.CGRect
	var ph C.double
	if C.sk_display_frames(cs, &f, &v, &ph) != 0 {
		return rect{}, rect{}, 0, fmt.Errorf("%w %s", ErrNoDisplay, uuid)
	}
	return toRect(f), toRect(v), float64(ph), nil
}

// DisplayVisibleFrame is the area AppKit reports as usable on the display
// with UUID uuid (under the menu bar, beside the Dock), in window-server
// coordinates. ErrNoDisplay when AppKit lists no screen with that UUID.
// Call it on the main thread, as AppKit expects.
func DisplayVisibleFrame(uuid string) (x, y, w, h float64, err error) {
	_, vis, ph, err := displayFrames(uuid)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	x, y, w, h = serverFrame(ph, vis)
	return x, y, w, h, nil
}
