// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// cdOperand finds a `cd <operand>` and carries its leading boundary, so
	// `abcd /tmp` is not read as a `cd`.
	cdOperand = regexp.MustCompile(`(^|[[:space:];&|("'])cd[[:space:]]+[^;&|]+`)
	// cdOperandPrefix strips everything up to and including the `cd`.
	cdOperandPrefix = regexp.MustCompile(`^[[:space:];&|("']?cd[[:space:]]+`)
	// dirOperand finds a `-C <dir>` or `--directory <dir>`.
	dirOperand = regexp.MustCompile(
		`(^|[[:space:];&|("'])(-C|--directory)[[:space:]=]*[^[:space:];&|]+`,
	)
	dirOperandPrefix = regexp.MustCompile(`^[[:space:];&|("']?(--directory|-C)[[:space:]=]*`)
)

// targetDir returns the directory make will read a Makefile from: the command's
// own `cd` operand (or the session cwd), then its `-C` operand if it has one.
//
// It reads the prose-stripped command, so a `cd` quoted inside a commit message
// does not misdirect the lookup while a `cd "…"` operand and a `bash -c 'cd …'`
// payload both survive.
func targetDir(src string, cwd string) string {
	base := cwd
	if matches := cdOperand.FindAllString(src, -1); len(matches) > 0 {
		base = applyOperand(cdOperandPrefix, matches[len(matches)-1], base)
	}
	if matches := dirOperand.FindAllString(src, -1); len(matches) > 0 {
		base = applyOperand(dirOperandPrefix, matches[len(matches)-1], base)
	}
	return base
}

// applyOperand strips the match down to its operand and resolves it against
// base.
func applyOperand(prefix *regexp.Regexp, match string, base string) string {
	operand := strings.TrimSpace(prefix.ReplaceAllString(match, ""))
	operand = strings.Trim(operand, `"'`)
	if operand == "" {
		return base
	}
	return resolveOperand(operand, base)
}

// resolveOperand applies an absolute or ~ operand over base, and appends a
// relative one.
func resolveOperand(operand string, base string) string {
	home := os.Getenv("HOME")
	switch {
	case operand == "~":
		return home
	case strings.HasPrefix(operand, "~/"):
		return filepath.Join(home, strings.TrimPrefix(operand, "~/"))
	case strings.HasPrefix(operand, "/"):
		return operand
	default:
		return base + "/" + operand
	}
}
