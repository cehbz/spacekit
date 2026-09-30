#import <Foundation/Foundation.h>
#import <ApplicationServices/ApplicationServices.h>

// Private: maps an AX element to its CGWindowID. Same symbol AeroSpace and
// others rely on; there is no public AXUIElement <-> CGWindowID bridge.
extern AXError _AXUIElementGetWindow(AXUIElementRef element, CGWindowID *outID);

// Window titles keyed by CGWindowID for one app, as a plist array of
// {id, title} dicts. Reads AXTitle, which only needs Accessibility — unlike
// CGWindowListCopyWindowInfo's kCGWindowName, which needs Screen Recording.
CFDataRef sk_window_titles(int pid) {
	AXUIElementRef app = AXUIElementCreateApplication((pid_t)pid);
	if (!app) return NULL;
	CFArrayRef windows = NULL;
	if (AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, (CFTypeRef *)&windows) != kAXErrorSuccess || !windows) {
		CFRelease(app);
		return NULL;
	}
	NSMutableArray *out = [NSMutableArray array];
	for (CFIndex i = 0; i < CFArrayGetCount(windows); i++) {
		AXUIElementRef w = (AXUIElementRef)CFArrayGetValueAtIndex(windows, i);
		CGWindowID wid = 0;
		if (_AXUIElementGetWindow(w, &wid) != kAXErrorSuccess || wid == 0) continue;
		CFTypeRef t = NULL;
		NSString *title = @"";
		if (AXUIElementCopyAttributeValue(w, kAXTitleAttribute, &t) == kAXErrorSuccess && t) {
			title = [NSString stringWithFormat:@"%@", (__bridge id)t];
			CFRelease(t);
		}
		[out addObject:@{@"id": @(wid), @"title": title}];
	}
	CFRelease(windows);
	CFRelease(app);
	CFDataRef d = CFPropertyListCreateData(NULL, (__bridge CFTypeRef)out, kCFPropertyListXMLFormat_v1_0, 0, NULL);
	return d;
}

// Move/resize the window with the given CGWindowID to the frame. The window is
// found by walking the owning app's AX windows and matching CGWindowID, since
// frames can only be set through the Accessibility API (CGWindow bounds are
// read-only). Returns 0 on success, 1 if AX is not permitted / app has no
// windows, 2 if the window id was not found, 3 if setting an attribute failed.
int sk_set_window_frame(int pid, uint32_t wid, double x, double y, double w, double h) {
	AXUIElementRef app = AXUIElementCreateApplication((pid_t)pid);
	if (!app) return 1;

	CFArrayRef windows = NULL;
	AXError err = AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, (CFTypeRef *)&windows);
	if (err != kAXErrorSuccess || !windows) {
		CFRelease(app);
		return 1;
	}

	int rc = 2;
	CFIndex n = CFArrayGetCount(windows);
	for (CFIndex i = 0; i < n; i++) {
		AXUIElementRef win = (AXUIElementRef)CFArrayGetValueAtIndex(windows, i);
		CGWindowID got = 0;
		if (_AXUIElementGetWindow(win, &got) != kAXErrorSuccess || got != wid) continue;

		CGPoint pos = CGPointMake(x, y);
		CGSize size = CGSizeMake(w, h);
		AXValueRef posVal = AXValueCreate(kAXValueCGPointType, &pos);
		AXValueRef sizeVal = AXValueCreate(kAXValueCGSizeType, &size);
		// Position first: a window near the bottom of a display has its
		// height clamped to the remaining screen if the size lands first
		// (observed: y=517 clamped 1410 to 923). Then size, then position
		// again for apps that shift the origin when resizing.
		AXError e0 = AXUIElementSetAttributeValue(win, kAXPositionAttribute, posVal);
		AXError e1 = AXUIElementSetAttributeValue(win, kAXSizeAttribute, sizeVal);
		AXError e2 = AXUIElementSetAttributeValue(win, kAXPositionAttribute, posVal);
		CFRelease(posVal);
		CFRelease(sizeVal);
		rc = (e0 == kAXErrorSuccess && e1 == kAXErrorSuccess && e2 == kAXErrorSuccess) ? 0 : 3;
		break;
	}

	CFRelease(windows);
	CFRelease(app);
	return rc;
}

// Read the AX position/size of the window with the given CGWindowID. Returns
// 0 on success, 1 if the app has no AX window list, 2 if the window is not in
// it (typically: it is on an inactive space).
int sk_window_ax_frame(int pid, uint32_t wid, double *x, double *y, double *w, double *h) {
	AXUIElementRef app = AXUIElementCreateApplication(pid);
	CFArrayRef windows = NULL;
	if (AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, (CFTypeRef *)&windows) != kAXErrorSuccess || !windows) {
		CFRelease(app);
		return 1;
	}
	int rc = 2;
	CFIndex n = CFArrayGetCount(windows);
	for (CFIndex i = 0; i < n; i++) {
		AXUIElementRef win = (AXUIElementRef)CFArrayGetValueAtIndex(windows, i);
		CGWindowID got = 0;
		if (_AXUIElementGetWindow(win, &got) != kAXErrorSuccess || got != wid) continue;
		AXValueRef pv = NULL, sv = NULL;
		CGPoint p = CGPointZero; CGSize s = CGSizeZero;
		if (AXUIElementCopyAttributeValue(win, kAXPositionAttribute, (CFTypeRef *)&pv) == kAXErrorSuccess && pv) { AXValueGetValue(pv, kAXValueCGPointType, &p); CFRelease(pv); }
		if (AXUIElementCopyAttributeValue(win, kAXSizeAttribute, (CFTypeRef *)&sv) == kAXErrorSuccess && sv) { AXValueGetValue(sv, kAXValueCGSizeType, &s); CFRelease(sv); }
		*x = p.x; *y = p.y; *w = s.width; *h = s.height;
		rc = 0;
		break;
	}
	CFRelease(windows);
	CFRelease(app);
	return rc;
}

// Set (or clear) native fullscreen on the window with the given CGWindowID via
// the AXFullScreen attribute — the same state the green button toggles, which
// creates/removes a dedicated fullscreen space. Returns 0 if changed, 1 if AX
// is not permitted / app has no windows, 2 if the window was not found, 3 if
// the window does not support a settable AXFullScreen, 4 if already in the
// requested state (no-op).
int sk_set_fullscreen(int pid, uint32_t wid, int on) {
	AXUIElementRef app = AXUIElementCreateApplication((pid_t)pid);
	if (!app) return 1;

	CFArrayRef windows = NULL;
	AXError err = AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, (CFTypeRef *)&windows);
	if (err != kAXErrorSuccess || !windows) {
		CFRelease(app);
		return 1;
	}

	int rc = 2;
	CFStringRef kFullScreen = CFSTR("AXFullScreen");
	CFIndex n = CFArrayGetCount(windows);
	for (CFIndex i = 0; i < n; i++) {
		AXUIElementRef win = (AXUIElementRef)CFArrayGetValueAtIndex(windows, i);
		CGWindowID got = 0;
		if (_AXUIElementGetWindow(win, &got) != kAXErrorSuccess || got != wid) continue;

		Boolean settable = false;
		if (AXUIElementIsAttributeSettable(win, kFullScreen, &settable) != kAXErrorSuccess || !settable) {
			rc = 3;
			break;
		}
		CFBooleanRef cur = NULL;
		if (AXUIElementCopyAttributeValue(win, kFullScreen, (CFTypeRef *)&cur) == kAXErrorSuccess && cur) {
			Boolean isOn = CFBooleanGetValue(cur);
			CFRelease(cur);
			if (isOn == (on != 0)) { rc = 4; break; }
		}
		AXError se = AXUIElementSetAttributeValue(win, kFullScreen, on ? kCFBooleanTrue : kCFBooleanFalse);
		rc = (se == kAXErrorSuccess) ? 0 : 3;
		break;
	}

	CFRelease(windows);
	CFRelease(app);
	return rc;
}
