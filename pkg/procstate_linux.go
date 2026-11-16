// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package pkg

import (
	"os"
	"strconv"
	"strings"
)

// procState returns the single-letter state from /proc/<pid>/stat — "R", "S",
// "Z" and so on.
//
// The comm field is parenthesised and may itself contain spaces and parens, so
// the state is the first field after the LAST ')'. Everything before that is
// skipped rather than split, which is why this does not use Fields on the whole
// line.
func procState(pid int) (string, bool) {
	content, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	s := string(content)
	idx := strings.LastIndex(s, ")")
	if idx < 0 || idx+2 >= len(s) {
		return "", false
	}
	return string(s[idx+2]), true
}
