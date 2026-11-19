// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package pkg

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// procStartTime returns the wall-clock start time of pid.
//
// A pid alone cannot identify a process: the kernel reuses it, so kill(pid, 0)
// answers true for a process that is not the child a record names. Field 22 of
// /proc/<pid>/stat is the process's start time in clock ticks since boot; btime
// from /proc/stat turns that into a wall-clock instant.
func procStartTime(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	content, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, false
	}
	s := string(content)
	// The comm field is parenthesised and may itself contain spaces and parens,
	// so the fields are split after the LAST ')'. fields[0] is state (field 3),
	// which makes starttime (field 22) fields[19].
	idx := strings.LastIndex(s, ")")
	if idx < 0 || idx+2 >= len(s) {
		return time.Time{}, false
	}
	fields := strings.Fields(s[idx+2:])
	if len(fields) < 20 {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	btime, ok := bootTime()
	if !ok {
		return time.Time{}, false
	}
	const userHZ = 100 // USER_HZ for /proc on Linux
	secs := btime + ticks/userHZ
	nsecs := (ticks % userHZ) * (uint64(time.Second) / userHZ)
	//nolint:gosec // /proc start times are Unix seconds and fit int64
	return time.Unix(int64(secs), int64(nsecs)).UTC(), true
}

// bootTime reads btime from /proc/stat, the boot instant in Unix seconds.
func bootTime() (uint64, bool) {
	content, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}
