package main

/*
#include <pwd.h>
#include <string.h>
#include <unistd.h>
#include <utmpx.h>

// console_login returns the time (unix seconds) of this user's latest utmpx
// USER_PROCESS entry on the console, 0 when there is none. Another user's
// console login under fast user switching is not this agent's session.
static long console_login(void) {
	struct passwd pw, *me = NULL;
	char buf[4096];
	if (getpwuid_r(getuid(), &pw, buf, sizeof buf, &me) != 0 || me == NULL) {
		return 0;
	}
	long best = 0;
	struct utmpx *u;
	setutxent();
	for (u = getutxent(); u; u = getutxent()) {
		if (u->ut_type == USER_PROCESS &&
		    strncmp(u->ut_line, "console", sizeof u->ut_line) == 0 &&
		    strncmp(u->ut_user, me->pw_name, sizeof u->ut_user) == 0 &&
		    u->ut_tv.tv_sec > best) {
			best = u->ut_tv.tv_sec;
		}
	}
	endutxent();
	return best;
}
*/
import "C"

import (
	"sync"
	"time"
)

// loginClock reads when the login session began. Only a time read from utmpx
// is kept: launchd can start the agent before loginwindow writes the console
// entry, so while none has been read each call reads again and answers with
// the fallback, the boot time. The mutex also keeps utmpx's process-global
// cursor to one walker.
type loginClock struct {
	mu       sync.Mutex
	read     func() int64 // the console entry, unix seconds; 0 when none
	fallback func() time.Time
	began    time.Time
}

// start returns when the login session began and whether that came from the
// console entry rather than the fallback.
func (c *loginClock) start() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.began.IsZero() {
		if sec := c.read(); sec != 0 {
			c.began = time.Unix(sec, 0)
		}
	}
	if c.began.IsZero() {
		return c.fallback(), false
	}
	return c.began, true
}

var login = &loginClock{read: func() int64 { return int64(C.console_login()) }, fallback: bootTime}

// loginTime returns when the current login session began: the time of this
// user's latest utmpx console entry (what `last console` prints), or the boot
// time while utmpx has none.
func loginTime() time.Time {
	t, _ := login.start()
	return t
}
