package sysevents

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/event"
)

// The Go kind list and the enum in sysevents.m are the same list in the same
// order; this test is the one place the duplication is checked.
func TestKindsMatchObjCEnum(t *testing.T) {
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
	if strings.Join(got, " ") != strings.Join(kindNames, " ") {
		t.Fatalf("sysevents.m enum:\n%v\nwant:\n%v", got, kindNames)
	}
	for i := range kindNames {
		if typed(i, event.Stamp{}, "", 0, 0, 0, 0) == nil {
			t.Errorf("kind %s (%d) produces no event", kindNames[i], i)
		}
	}
}

func TestTypedPayloads(t *testing.T) {
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	s := event.Stamp{At: at}
	cases := []struct {
		kind int
		want event.Event
	}{
		{kAppLaunched, event.AppLaunched{Stamp: s, Name: "Finder"}},
		{kDisplayReconfigured, event.DisplayReconfigured{Stamp: s, Display: 1, Flags: 2}},
		{kSpaceChanged, event.SpaceSwitched{Stamp: s}},
		{kSpaceSwitched, event.SpaceSwitched{Stamp: s}},
		{kWindowAttached, event.WindowAttached{Stamp: s, Window: 7, Space: 9}},
		{kWindowMoved, event.WindowMoved{Stamp: s, Window: 7}},
		{kSessionLocked, event.SessionLocked{Stamp: s}},
		{kOverviewEnded, event.OverviewEnded{Stamp: s}},
	}
	for _, c := range cases {
		got := typed(c.kind, s, "Finder", 1, 2, 7, 9)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("typed(%d) = %#v, want %#v", c.kind, got, c.want)
		}
	}
}

func TestEmitReachesSink(t *testing.T) {
	var got []event.Event
	setSink(func(e event.Event) { got = append(got, e) })
	defer setSink(nil)
	emit(event.WindowMoved{Stamp: event.Now(), Window: 7})
	if len(got) != 1 || got[0].(event.WindowMoved).Window != 7 {
		t.Fatalf("sink got %+v", got)
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

func TestObserveDockErr(t *testing.T) {
	if err := observeDockErr(0); err != nil {
		t.Fatalf("0 = %v, want nil", err)
	}
	if err := observeDockErr(-1); err == nil || !strings.Contains(err.Error(), "Dock") {
		t.Fatalf("-1 = %v, want an error naming the Dock", err)
	}
	if err := observeDockErr(2); err == nil || !strings.Contains(err.Error(), "2") {
		t.Fatalf("2 = %v, want the count", err)
	}
}
