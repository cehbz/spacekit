package sysevents

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestSessionUnlockedString(t *testing.T) {
	if got, want := SessionUnlocked.String(), "session unlock"; got != want {
		t.Errorf("SessionUnlocked.String() = %q, want %q", got, want)
	}
}

// The Go constants and the enum in sysevents.m are the same list in the same
// order; this test is the one place the duplication is checked.
func TestKindsMatchObjCEnum(t *testing.T) {
	want := []string{
		"kAppLaunched", "kDisplayReconfigured", "kScreensSleep", "kScreensWake",
		"kSystemWillSleep", "kSystemWake", "kSpaceChanged", "kWillPowerOff", "kSessionUnlocked",
		"kWindowAttached", "kWindowDetached", "kSpaceCreated", "kSpaceDestroyed", "kSpaceSwitched",
		"kWindowMoved", "kWindowResized", "kWindowShown", "kWindowHidden", "kWindowDestroyed",
	}
	if got := int(WindowDestroyed) + 1; got != len(want) {
		t.Fatalf("Go declares %d kinds, the test lists %d", got, len(want))
	}
	for i := range want {
		if Kind(i).String() == "unknown" {
			t.Errorf("Kind(%d) has no String", i)
		}
	}
	src, err := os.ReadFile("sysevents.m")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)enum \{(.*?)\};`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no enum in sysevents.m")
	}
	var got []string
	for _, f := range strings.Split(string(m[1]), ",") {
		if f = strings.TrimSpace(f); f != "" {
			got = append(got, f)
		}
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("sysevents.m enum:\n%v\nwant:\n%v", got, want)
	}
}

func TestWindowKindStrings(t *testing.T) {
	cases := map[Kind]string{
		WindowAttached: "window attached", WindowDetached: "window detached",
		SpaceCreated: "space created", SpaceDestroyed: "space destroyed", SpaceSwitched: "space switched",
		WindowMoved: "window moved", WindowResized: "window resized",
		WindowShown: "window shown", WindowHidden: "window hidden", WindowDestroyed: "window destroyed",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", int(k), got, want)
		}
	}
}

func TestStartErr(t *testing.T) {
	if err := startErr(0); err != nil {
		t.Fatalf("startErr(0) = %v, want nil", err)
	}
	if err := startErr(-1); err == nil || !strings.Contains(err.Error(), "SkyLight") {
		t.Fatalf("startErr(-1) = %v, want an error naming SkyLight", err)
	}
	if err := startErr(3); err == nil || !strings.Contains(err.Error(), "3") {
		t.Fatalf("startErr(3) = %v, want an error naming the count", err)
	}
}
