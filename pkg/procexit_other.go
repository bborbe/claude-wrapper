// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !darwin

package pkg

// waitForProcessExit blocks until pid is no longer running.
//
// Polling, because kqueue is BSD/macOS only. The wrapper runs under launchd, so
// this exists to keep the package building and testing off macOS rather than to
// serve a real deployment; darwin takes the event-driven path.
func waitForProcessExit(pid int) {
	pollForProcessExit(pid)
}
