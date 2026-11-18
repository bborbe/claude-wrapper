// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !darwin && !linux

package pkg

import "time"

// procStartTime is unavailable on this platform. An unknown start time never
// proves reuse, so callers keep holding the session — fail-closed, because
// releasing a live child is the corruption the resume guard exists to prevent.
func procStartTime(int) (time.Time, bool) { return time.Time{}, false }
