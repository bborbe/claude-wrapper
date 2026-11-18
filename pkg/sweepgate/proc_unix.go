// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package sweepgate

import "syscall"

// processAlive reports whether pid names a live process.
//
// Signal 0 performs the permission and existence checks without sending a signal.
// EPERM means the process exists but is not ours, which is still alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return err == syscall.EPERM
}
