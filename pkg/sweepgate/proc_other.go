// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !unix

package sweepgate

import (
	"os"
)

// processAlive reports whether pid names a live process.
//
// Off unix the signal-0 probe is unavailable; a successful FindProcess is the
// best available reading.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process != nil
}
