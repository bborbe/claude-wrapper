// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"fmt"
	"strings"
)

// maxCompoundSegments bounds how many subcommands a compound may carry. A single
// command is the rules' own job, and a long chain is not something to compose.
const maxCompoundSegments = 8

// compoundAllowReason is the allow reason the script emits.
const compoundAllowReason = "allow-compound-bash: all %d subcommands match an existing " +
	"allow rule"

// CompoundBashGuard replaces allow-compound-bash.py.
//
// It allows a compound command when every subcommand matches an existing `Bash(...)`
// allow rule, or a shape guard, and nothing in the text can hide another command.
// It composes rules; it never invents a permission outside the shape guards, and a
// deny or ask rule always wins.
type CompoundBashGuard struct {
	loadRules func(cwd string) (CompoundRules, error)
}

// NewCompoundBashGuard returns the compound-composition guard.
func NewCompoundBashGuard() *CompoundBashGuard {
	return &CompoundBashGuard{loadRules: LoadRules}
}

// NewCompoundBashGuardWithRules returns the guard over a stubbed rule loader.
func NewCompoundBashGuardWithRules(
	load func(cwd string) (CompoundRules, error),
) *CompoundBashGuard {
	return &CompoundBashGuard{loadRules: load}
}

// Name returns the script this guard replaces.
func (g *CompoundBashGuard) Name() string { return "allow-compound-bash.py" }

// Applies reports whether this is a PreToolUse Bash call.
func (g *CompoundBashGuard) Applies(input Input) bool {
	if input.HookEventName != EventPreToolUse || !input.IsBash() {
		return false
	}
	command, _ := input.BashCommand()
	return strings.TrimSpace(command) != ""
}

// Evaluate allows a compound whose every subcommand is already permitted.
func (g *CompoundBashGuard) Evaluate(input Input) Decision {
	command, _ := input.BashCommand()
	if !strings.ContainsAny(command, ";&|") {
		return Decision{}
	}
	if UnsafeSyntax(command) {
		return Decision{}
	}
	segments, ok := SplitCompound(command)
	if !ok || len(segments) < 2 || len(segments) > maxCompoundSegments {
		return Decision{}
	}
	rules, err := g.loadRules(input.Cwd)
	if err != nil {
		return Decision{}
	}
	roots := DefaultGitRoots()
	if compoundTextAllows(segments, rules, input.Cwd, roots) {
		return AllowDecision(fmt.Sprintf(compoundAllowReason, len(segments)))
	}
	if CompoundTreeAllows(command, rules, input.Cwd, roots) {
		return AllowDecision(fmt.Sprintf(compoundAllowReason, len(segments)))
	}
	return Decision{}
}

// staticWords reports whether every word is a plain literal. The shape guards were
// written against the syntax-tree path, which only ever hands them literal words;
// feeding them a raw `Fields` split lets `git log $X` or a quoted operand through.
func staticWords(words []string) bool {
	for _, word := range words {
		if strings.ContainsAny(word, "$`'\"\\") {
			return false
		}
	}
	return true
}

// compoundTextAllows reports whether every segment is permitted on its own.
func compoundTextAllows(
	segments []string,
	rules CompoundRules,
	cwd string,
	roots []string,
) bool {
	for _, segment := range segments {
		words := strings.Fields(segment)
		if len(words) == 0 {
			return false
		}
		if matchesAnyPattern(segment, rules.Gated) {
			return false
		}
		if compoundShapeAllows(words, cwd, roots) && staticWords(words) {
			continue
		}
		if wrapperProgram(words[0]) {
			return false
		}
		if !matchesAnyPattern(segment, rules.Allow) {
			return false
		}
	}
	return true
}
