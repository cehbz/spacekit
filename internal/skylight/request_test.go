package skylight

import (
	"errors"
	"testing"
)

// Runs against this machine's window server, as the other skylight calls'
// callers do: an empty set is a valid request, not an error.
func TestRequestWindowNotificationsEmptyIsNotAnError(t *testing.T) {
	if err := RequestWindowNotifications(nil); err != nil {
		t.Fatalf("RequestWindowNotifications(nil) = %v, want nil", err)
	}
}

func TestWindowInfoForUnknownWindow(t *testing.T) {
	_, err := WindowInfoFor(0xFFFFFFF0)
	if !errors.Is(err, ErrNotListed) {
		t.Fatalf("err = %v, want ErrNotListed", err)
	}
}
