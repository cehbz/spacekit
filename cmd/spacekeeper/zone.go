package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// zoneWriter is a log output that prefixes each line with the time in the
// zone the link names, reloaded whenever the link's target changes.
type zoneWriter struct {
	out    io.Writer
	link   string // a symlink to a TZif file, as /etc/localtime
	target string // the link's target when loc was loaded
	loc    *time.Location
	now    func() time.Time
}

func newZoneWriter(out io.Writer, link string) *zoneWriter {
	return &zoneWriter{out: out, link: link, loc: time.Local, now: time.Now}
}

func (z *zoneWriter) Write(p []byte) (int, error) {
	stamp := z.now().In(z.zone()).Format("2006/01/02 15:04:05 ")
	if _, err := io.WriteString(z.out, stamp); err != nil {
		return 0, err
	}
	return z.out.Write(p)
}

// zone is the location the link names, or time.Local when the link or its
// target is unreadable.
func (z *zoneWriter) zone() *time.Location {
	target, err := os.Readlink(z.link)
	if err != nil {
		z.target, z.loc = "", time.Local
		return z.loc
	}
	if target == z.target {
		return z.loc
	}
	path := target
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(z.link), path)
	}
	z.target, z.loc = target, time.Local
	if data, err := os.ReadFile(path); err == nil {
		if loc, err := time.LoadLocationFromTZData(zoneName(path), data); err == nil {
			z.loc = loc
		}
	}
	return z.loc
}

// zoneName is the zone's name from its file's path under a zoneinfo
// directory, as Asia/Bangkok, or the path itself.
func zoneName(path string) string {
	const dir = "/zoneinfo/"
	if i := strings.LastIndex(path, dir); i >= 0 {
		return path[i+len(dir):]
	}
	return path
}
