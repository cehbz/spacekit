// Package skylight wraps the private SkyLight read-side calls and the
// SIP-enabled bridged window-move operation. All symbols are resolved at
// runtime (dlopen/objc runtime) so a removed API degrades to an error
// instead of a crash or link failure.
package skylight

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation -framework AppKit -framework CoreGraphics -framework CoreFoundation -framework ApplicationServices
#include <CoreFoundation/CoreFoundation.h>
#include <CoreGraphics/CoreGraphics.h>
#include <dlfcn.h>
#include <stdlib.h>

// Screen Recording access (CoreGraphics, 10.15+). kCGWindowName is only
// populated for the caller's CGWindowListCopyWindowInfo when this is granted.
extern bool CGPreflightScreenCaptureAccess(void);
extern bool CGRequestScreenCaptureAccess(void);

typedef int (*sk_conn_fn)(void);
typedef unsigned long long (*sk_active_fn)(int);
typedef CFArrayRef (*sk_copy_displays_fn)(int);
typedef CFArrayRef (*sk_spaces_for_windows_fn)(int, int, CFArrayRef);

static void *sk_handle(void) {
	static void *h = NULL;
	if (!h) h = dlopen("/System/Library/PrivateFrameworks/SkyLight.framework/SkyLight", RTLD_LAZY);
	return h;
}

static void *sk_sym(const char *sls, const char *cgs) {
	void *h = sk_handle();
	if (!h) return NULL;
	void *f = dlsym(h, sls);
	return f ? f : dlsym(h, cgs);
}

static int sk_cid(void) {
	static sk_conn_fn f = NULL;
	if (!f) f = (sk_conn_fn)sk_sym("SLSMainConnectionID", "CGSMainConnectionID");
	return f ? f() : 0;
}

static unsigned long long sk_active_space(void) {
	static sk_active_fn f = NULL;
	if (!f) f = (sk_active_fn)sk_sym("SLSGetActiveSpace", "CGSGetActiveSpace");
	return f ? f(sk_cid()) : 0;
}

// CF objects cross into Go as XML plist bytes; howett.net/plist decodes them.
static CFDataRef sk_plist(CFTypeRef obj) {
	if (!obj) return NULL;
	return CFPropertyListCreateData(NULL, obj, kCFPropertyListXMLFormat_v1_0, 0, NULL);
}

static CFDataRef sk_copy_managed_display_spaces(void) {
	static sk_copy_displays_fn f = NULL;
	if (!f) f = (sk_copy_displays_fn)sk_sym("SLSCopyManagedDisplaySpaces", "CGSCopyManagedDisplaySpaces");
	if (!f) return NULL;
	CFArrayRef arr = f(sk_cid());
	if (!arr) return NULL;
	CFDataRef d = sk_plist(arr);
	CFRelease(arr);
	return d;
}

static CFDataRef sk_spaces_for_window(uint32_t wid) {
	static sk_spaces_for_windows_fn f = NULL;
	if (!f) f = (sk_spaces_for_windows_fn)sk_sym("SLSCopySpacesForWindows", "CGSCopySpacesForWindows");
	if (!f) return NULL;
	CFNumberRef n = CFNumberCreate(NULL, kCFNumberSInt32Type, &wid);
	CFArrayRef wids = CFArrayCreate(NULL, (const void **)&n, 1, &kCFTypeArrayCallBacks);
	// 0x7 = current | other | user-created spaces (CGSInternal CGSSpace.h)
	CFArrayRef arr = f(sk_cid(), 0x7, wids);
	CFRelease(n);
	CFRelease(wids);
	if (!arr) return NULL;
	CFDataRef d = sk_plist(arr);
	CFRelease(arr);
	return d;
}

static CFDataRef sk_window_list(void) {
	CFArrayRef arr = CGWindowListCopyWindowInfo(kCGWindowListOptionAll, kCGNullWindowID);
	if (!arr) return NULL;
	CFDataRef d = sk_plist(arr);
	CFRelease(arr);
	return d;
}

// Implemented in move.m.
int sk_move_windows_to_space(const uint32_t *wids, int count, unsigned long long sid);
char *sk_bundle_id_for_pid(int pid);
// Implemented in ax.m.
int sk_window_ax_frame(int pid, uint32_t wid, double *x, double *y, double *w, double *h);
int sk_set_window_frame(int pid, uint32_t wid, double x, double y, double w, double h);
int sk_set_fullscreen(int pid, uint32_t wid, int on);
CFDataRef sk_window_titles(int pid);
// Implemented in spacecreate.m.
int sk_add_spaces(const int *counts, int n, int *added);
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"

	"howett.net/plist"
)

// Space is one Mission Control space as reported by SLSCopyManagedDisplaySpaces.
type Space struct {
	ManagedSpaceID uint64 `plist:"ManagedSpaceID"`
	ID64           uint64 `plist:"id64"`
	UUID           string `plist:"uuid"`
	Type           int    `plist:"type"`
}

// ID prefers ManagedSpaceID and falls back to id64 (WindowKit pattern).
func (s Space) ID() uint64 {
	if s.ManagedSpaceID != 0 {
		return s.ManagedSpaceID
	}
	return s.ID64
}

// UserSpace reports whether this is a regular desktop (type 0), as opposed
// to a fullscreen/tiled space (type 4) which cannot be a move target.
func (s Space) UserSpace() bool { return s.Type == 0 }

// Display is one monitor with its ordered spaces.
type Display struct {
	UUID         string  `plist:"Display Identifier"`
	CurrentSpace Space   `plist:"Current Space"`
	Spaces       []Space `plist:"Spaces"`
}

// WindowBounds matches the kCGWindowBounds dictionary.
type WindowBounds struct {
	X      float64 `plist:"X"`
	Y      float64 `plist:"Y"`
	Width  float64 `plist:"Width"`
	Height float64 `plist:"Height"`
}

// WindowInfo is one entry from CGWindowListCopyWindowInfo. Name is empty
// without the Screen Recording permission.
type WindowInfo struct {
	Number    uint32       `plist:"kCGWindowNumber"`
	OwnerPID  int          `plist:"kCGWindowOwnerPID"`
	OwnerName string       `plist:"kCGWindowOwnerName"`
	Name      string       `plist:"kCGWindowName"`
	Layer     int          `plist:"kCGWindowLayer"`
	Alpha     float64      `plist:"kCGWindowAlpha"`
	Bounds    WindowBounds `plist:"kCGWindowBounds"`
}

func decodeData(d C.CFDataRef, what string, v any) error {
	if d == 0 {
		return fmt.Errorf("%s unavailable (symbol missing or call returned NULL)", what)
	}
	defer C.CFRelease(C.CFTypeRef(d))
	b := C.GoBytes(unsafe.Pointer(C.CFDataGetBytePtr(d)), C.int(C.CFDataGetLength(d)))
	if _, err := plist.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decoding %s: %w", what, err)
	}
	return nil
}

// ActiveSpaceID returns the current space of the active display, 0 if unknown.
func ActiveSpaceID() uint64 {
	return uint64(C.sk_active_space())
}

// ManagedDisplaySpaces returns every display with its ordered spaces.
func ManagedDisplaySpaces() ([]Display, error) {
	var displays []Display
	if err := decodeData(C.sk_copy_managed_display_spaces(), "SLSCopyManagedDisplaySpaces", &displays); err != nil {
		return nil, err
	}
	return displays, nil
}

// SpacesForWindow returns the space IDs a window belongs to (normally one;
// several for sticky/all-spaces windows).
func SpacesForWindow(wid uint32) ([]uint64, error) {
	var ids []uint64
	if err := decodeData(C.sk_spaces_for_window(C.uint32_t(wid)), "SLSCopySpacesForWindows", &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// WindowList returns all windows in the session, including other spaces.
func WindowList() ([]WindowInfo, error) {
	var wins []WindowInfo
	if err := decodeData(C.sk_window_list(), "CGWindowListCopyWindowInfo", &wins); err != nil {
		return nil, err
	}
	return wins, nil
}

// ScreenRecordingGranted reports whether this binary has Screen Recording
// access, which CGWindowListCopyWindowInfo needs to return window titles.
func ScreenRecordingGranted() bool { return bool(C.CGPreflightScreenCaptureAccess()) }

// RequestScreenRecording registers the binary with TCC and prompts if access
// is undetermined. Granting takes effect on the next process launch. Calling
// this is how a CLI gets correctly listed; hand-adding it in System Settings
// often does not bind.
func RequestScreenRecording() bool { return bool(C.CGRequestScreenCaptureAccess()) }

// WindowTitlesForPID returns an app's window titles keyed by CGWindowID, read
// via Accessibility (no Screen Recording needed). Empty if AX is unavailable.
func WindowTitlesForPID(pid int) map[uint32]string {
	d := C.sk_window_titles(C.int(pid))
	out := map[uint32]string{}
	var entries []struct {
		ID    uint64 `plist:"id"`
		Title string `plist:"title"`
	}
	if decodeData(d, "AXTitle list", &entries) != nil {
		return out
	}
	for _, e := range entries {
		out[uint32(e.ID)] = e.Title
	}
	return out
}

// BundleIDForPID resolves a process's bundle identifier, "" if none.
func BundleIDForPID(pid int) string {
	cs := C.sk_bundle_id_for_pid(C.int(pid))
	if cs == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(cs))
	return C.GoString(cs)
}

// AddSpaces creates desktops by driving Mission Control's accessibility tree:
// counts[i] desktops are added to the i-th display (display order matches
// ManagedDisplaySpaces). This animates Mission Control in and out — it is the
// only no-SIP way to create a Dock-managed space. Returns the number of
// desktops actually added alongside any error.
func AddSpaces(counts []int) (int, error) {
	total := 0
	for _, c := range counts {
		if c > 0 {
			total += c
		}
	}
	if total == 0 {
		return 0, nil
	}
	c := make([]C.int, len(counts))
	for i, v := range counts {
		if v < 0 {
			v = 0
		}
		c[i] = C.int(v)
	}
	var added C.int
	rc := C.sk_add_spaces(&c[0], C.int(len(counts)), &added)
	switch rc {
	case 0:
		return int(added), nil
	case 1:
		return int(added), errors.New("could not reach Mission Control (Accessibility not granted, or no Dock)")
	case 2:
		return int(added), errors.New("a display's add-desktop button was not found (Mission Control AX tree changed?)")
	default:
		return int(added), fmt.Errorf("add-spaces failed (rc=%d)", int(rc))
	}
}

// SetWindowFrame moves and resizes a window via the Accessibility API
// (CGWindow bounds are read-only). Requires the process to be AX-trusted.
func SetWindowFrame(pid int, wid uint32, x, y, w, h float64) error {
	rc := C.sk_set_window_frame(C.int(pid), C.uint32_t(wid),
		C.double(x), C.double(y), C.double(w), C.double(h))
	switch rc {
	case 0:
		return nil
	case 1:
		return errors.New("Accessibility not permitted or app has no AX windows")
	case 2:
		return errors.New("window not found in its app's AX window list")
	case 3:
		return errors.New("app refused the position/size change")
	default:
		return fmt.Errorf("set-frame failed (rc=%d)", int(rc))
	}
}

// WindowAXFrame reads a window's frame as its app reports it through
// Accessibility. ok is false when the app does not list the window (windows
// on an inactive space, or an app without AX). Compared with the CGWindow
// bounds this reveals a window whose app believes it is on another display.
func WindowAXFrame(pid int, wid uint32) (x, y, w, h float64, ok bool) {
	var cx, cy, cw, ch C.double
	rc := C.sk_window_ax_frame(C.int(pid), C.uint32_t(wid), &cx, &cy, &cw, &ch)
	return float64(cx), float64(cy), float64(cw), float64(ch), rc == 0
}

// FullscreenResult reports the outcome of a SetFullscreen call.
type FullscreenResult int

const (
	FullscreenChanged      FullscreenResult = iota // transitioned into the requested state
	FullscreenNotPermitted                         // AX not granted, or app has no windows
	FullscreenWindowGone                           // window not found in the app
	FullscreenUnsupported                          // app exposes no settable AXFullScreen
	FullscreenAlready                              // already in the requested state
)

// SetFullscreen toggles native fullscreen on a window via AXFullScreen. Going
// fullscreen creates a dedicated space; this is how fullscreen windows are
// restored. Requires the process to be AX-trusted.
func SetFullscreen(pid int, wid uint32, on bool) FullscreenResult {
	v := 0
	if on {
		v = 1
	}
	return FullscreenResult(C.sk_set_fullscreen(C.int(pid), C.uint32_t(wid), C.int(v)))
}

// MoveWindowsToSpace moves windows to a space via
// SLSBridgedMoveWindowsToManagedSpaceOperation (SIP-enabled since Tahoe 26.4).
// The operation is asynchronous; callers should verify afterwards.
func MoveWindowsToSpace(wids []uint32, sid uint64) error {
	if len(wids) == 0 {
		return nil
	}
	rc := C.sk_move_windows_to_space((*C.uint32_t)(unsafe.Pointer(&wids[0])), C.int(len(wids)), C.ulonglong(sid))
	switch rc {
	case 0:
		return nil
	case 1:
		return errors.New("SLSBridgedMoveWindowsToManagedSpaceOperation class not found (API removed in this macOS?)")
	case 2:
		return errors.New("bridged-move operation does not respond to performWithWMBridgeDelegate (selector changed?)")
	case 3:
		return errors.New("bridged-move operation init failed")
	default:
		return fmt.Errorf("bridged-move failed (rc=%d)", int(rc))
	}
}
