package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func pointLink(t *testing.T, link, target string) {
	t.Helper()
	os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestZoneWriterFollowsLink(t *testing.T) {
	link := filepath.Join(t.TempDir(), "localtime")
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	z := newZoneWriter(&out, link)
	z.now = func() time.Time { return at }

	pointLink(t, link, "/var/db/timezone/zoneinfo/Asia/Bangkok")
	z.Write([]byte("one\n"))
	pointLink(t, link, "/var/db/timezone/zoneinfo/Europe/London")
	z.Write([]byte("two\n"))

	want := "2026/01/15 19:00:00 one\n2026/01/15 12:00:00 two\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestZoneWriterUnreadableLink(t *testing.T) {
	link := filepath.Join(t.TempDir(), "absent")
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	z := newZoneWriter(&out, link)
	z.now = func() time.Time { return at }

	z.Write([]byte("line\n"))

	want := at.In(time.Local).Format("2006/01/02 15:04:05 ") + "line\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
