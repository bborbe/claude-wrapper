// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

// tmpWriteTools are the file-writing tools this guard may approve.
var tmpWriteTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true}

// TmpWriteGuard allows a Write or Edit whose target resolves inside /tmp.
//
// A scratch file under /tmp is the documented place for a PR body, a probe script
// or a log, and under `defaultMode: auto` each such write parked a headless worker.
// The target must be an absolute path with no `..` whose longest existing prefix
// resolves inside /tmp, so a symlink under /tmp that points elsewhere is refused.
// Every other write passes through to the permission prompt.
type TmpWriteGuard struct{}

// NewTmpWriteGuard returns the /tmp write guard.
func NewTmpWriteGuard() *TmpWriteGuard { return &TmpWriteGuard{} }

// Name names the guard.
func (g *TmpWriteGuard) Name() string { return "tmp-write" }

// MayAllow grants this guard allow authority; see Registry.Evaluate.
func (g *TmpWriteGuard) MayAllow() bool { return true }

// Applies reports whether this is a PreToolUse call of a file-writing tool.
func (g *TmpWriteGuard) Applies(input Input) bool {
	return input.HookEventName == EventPreToolUse && tmpWriteTools[input.ToolName]
}

// Evaluate allows the write when its target resolves inside /tmp.
func (g *TmpWriteGuard) Evaluate(input Input) Decision {
	path, ok := input.FilePath()
	if !ok || !TmpPath(path) {
		return Decision{}
	}
	return AllowDecision("tmp-write: the file resolves inside /tmp")
}
