package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/cehbz/spacekit/internal/settle"
)

// A look put off again and again while the overview is showing is logged
// once, when it is first put off; the retried look logs itself when it runs.
func TestPutOffLogsOnce(t *testing.T) {
	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(out); log.SetFlags(flags) }()

	w := &watcher{spaces: settle.New(spaceQuiet)}
	for i := 0; i < 3; i++ {
		w.putOff("interval", "the overview is showing")
	}
	if w.retry != "interval" || !w.spaces.Open() {
		t.Fatalf("retry %q, spaces open %v; want the look queued", w.retry, w.spaces.Open())
	}
	if n := strings.Count(buf.String(), "put off"); n != 1 {
		t.Fatalf("logged %d put-off lines, want 1:\n%s", n, buf.String())
	}
}
