// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin

package pkg

import (
	stderrors "errors"
	"log"
	"syscall"
)

// waitForProcessExit blocks until pid is no longer running.
//
// ⚠️ **An adopted child has no parent to `Wait` on.** After a restart the child is
// reparented to pid 1, so its exit status is not collectable here — the only fact
// available is that the pid stops existing. kqueue's `EVFILT_PROC` with
// `NOTE_EXIT` reports that transition as an event instead of polling for it: the
// watcher sleeps until the kernel wakes it, so an idle session costs nothing, and
// the exit is observed when it happens rather than up to one poll interval later.
//
// ⚠️ **The event can already have fired before the registration.** A child that
// exited between adoption and this call leaves no pid to watch, and `kevent`
// answers `ESRCH`/`ENOENT` — which is the same fact this function exists to
// return, so it is success, not a failure to report.
func waitForProcessExit(pid int) {
	if !processAlive(pid) {
		return
	}
	kq, err := syscall.Kqueue()
	if err != nil {
		log.Printf("WARNING: kqueue for pid %d failed, polling instead: %v", pid, err)
		pollForProcessExit(pid)
		return
	}
	defer func() { _ = syscall.Close(kq) }()

	changes := []syscall.Kevent_t{{
		//nolint:gosec // pid is checked positive above, so the conversion is exact
		Ident:  uint64(pid),
		Filter: syscall.EVFILT_PROC,
		Flags:  syscall.EV_ADD | syscall.EV_ONESHOT,
		Fflags: syscall.NOTE_EXIT,
	}}
	events := make([]syscall.Kevent_t, 1)
	for {
		// A nil timeout blocks: there is nothing to do until the process leaves.
		n, err := syscall.Kevent(kq, changes, events, nil)
		if err != nil {
			if stderrors.Is(err, syscall.EINTR) {
				continue
			}
			if stderrors.Is(err, syscall.ESRCH) || stderrors.Is(err, syscall.ENOENT) {
				return
			}
			log.Printf("WARNING: kqueue wait for pid %d failed, polling instead: %v", pid, err)
			pollForProcessExit(pid)
			return
		}
		if n > 0 {
			return
		}
	}
}
