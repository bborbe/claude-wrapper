// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin

package pkg

import (
	"time"

	"golang.org/x/sys/unix"
)

// procStartTime returns the wall-clock start time of pid.
//
// A pid alone cannot identify a process: the kernel reuses it, so kill(pid, 0)
// answers true for a process that is not the child a record names. The pid plus
// its start time does identify one process instance, which is what lets a stale
// holder be told from a live one.
func procStartTime(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil {
		return time.Time{}, false
	}
	tv := kp.Proc.P_starttime
	if tv.Sec == 0 && tv.Usec == 0 {
		return time.Time{}, false
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1000).UTC(), true
}
