// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"fmt"
	"strings"
)

// typoWrong and typoRight are the mistyped and correct home directory. The typo
// appears whenever a model drops a character from the user name, and a path
// that does not exist fails much later than the typo is made.
const (
	typoWrong = "/Users/bborge"
	typoRight = "/Users/bborbe"
)

// TypoGuard replaces fix-bborge.sh.
//
// It denies any tool argument that carries the mistyped home directory and
// names the corrected path, so the retry is one edit rather than a search for
// why the path is empty.
type TypoGuard struct{}

// NewTypoGuard returns the typo guard.
func NewTypoGuard() *TypoGuard {
	return &TypoGuard{}
}

// Name returns the script this guard replaces.
func (g *TypoGuard) Name() string { return "fix-bborge.sh" }

// Applies reports whether this input can carry a path or command.
func (g *TypoGuard) Applies(input Input) bool {
	switch input.ToolName {
	case "Read", "Edit", "Write", "NotebookEdit", "Bash", "Glob", "Grep":
		return true
	default:
		return false
	}
}

// Evaluate denies a call whose argument carries the typo.
func (g *TypoGuard) Evaluate(input Input) Decision {
	switch input.ToolName {
	case "Bash":
		command, _ := input.BashCommand()
		if !strings.Contains(command, typoWrong) {
			return Decision{}
		}
		return DenyDecision(
			"Typo '/Users/bborge' in command. Use '/Users/bborbe' (your home directory) and retry.",
		)
	case "Glob", "Grep":
		pattern, path := input.searchArgs()
		if !strings.Contains(pattern, typoWrong) && !strings.Contains(path, typoWrong) {
			return Decision{}
		}
		return DenyDecision(fmt.Sprintf(
			"Typo '/Users/bborge' in %s args. Use '/Users/bborbe' and retry.", input.ToolName,
		))
	default:
		path, ok := input.FilePath()
		if !ok || !strings.Contains(path, typoWrong) {
			return Decision{}
		}
		fixed := strings.ReplaceAll(path, typoWrong, typoRight)
		return DenyDecision(fmt.Sprintf(
			"Typo in file_path: '%s'. Correct path: '%s'. Retry with the corrected path.", path, fixed,
		))
	}
}
