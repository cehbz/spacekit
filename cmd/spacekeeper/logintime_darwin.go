package main

import "time"

// loginTime returns when the current login session began: the time of the
// utmpx console entry. Implementation to follow; until then it returns the
// boot time.
func loginTime() time.Time { return bootTime() }
