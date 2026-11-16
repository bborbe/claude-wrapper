// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !linux

package pkg

// procState is Linux-only. Elsewhere there is no /proc and a zombie is not
// observable this way; darwin gets the exit as a kqueue event instead.
func procState(int) (string, bool) { return "", false }
