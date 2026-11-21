package main

import (
	"os"
	"testing"
)

// pending skips a model-level test until the task that implements it removes
// the call. RUN_PENDING=1 in the environment runs pending tests, to show them
// failing.
func pending(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_PENDING") == "" {
		t.Skip("pending: its task has not run")
	}
}
