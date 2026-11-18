// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// CompoundTreeAllows parses the command and reports whether every command node
// matches an allow rule on its own and nothing in the tree can hide a command.
//
// It is deliberately conservative: a node type, a redirect, an assignment or an
// expansion this walker does not explicitly model is refused. That direction is
// the only safe one for a tree pass — a shape it fails to model must fall back to
// the normal permission prompt, never to an allow the script would not have given.
func CompoundTreeAllows(
	command string,
	rules CompoundRules,
	cwd string,
	roots []string,
) bool {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).
		Parse(strings.NewReader(command), "")
	if err != nil {
		return false
	}
	walker := &compoundWalker{rules: rules, cwd: cwd, roots: roots}
	if err := walker.walk(file); err != nil {
		return false
	}
	return walker.calls > 0
}

// compoundWalker accumulates the verdict while walking one parsed file.
type compoundWalker struct {
	rules CompoundRules
	cwd   string
	roots []string
	calls int
}

// walk visits the file's statements.
func (w *compoundWalker) walk(file *syntax.File) error {
	for _, stmt := range file.Stmts {
		if err := w.stmt(stmt); err != nil {
			return err
		}
	}
	return nil
}

// stmt visits one statement.
func (w *compoundWalker) stmt(stmt *syntax.Stmt) error {
	if stmt == nil {
		return nil
	}
	if stmt.Background || stmt.Coprocess || stmt.Negated {
		return errCompoundUnmodelled
	}
	if err := w.redirects(stmt.Redirs); err != nil {
		return err
	}
	return w.command(stmt.Cmd)
}

// command visits one command node.
func (w *compoundWalker) command(cmd syntax.Command) error {
	switch node := cmd.(type) {
	case *syntax.CallExpr:
		return w.call(node)
	case *syntax.Block:
		return w.stmts(node.Stmts)
	case *syntax.Subshell:
		return w.stmts(node.Stmts)
	case *syntax.BinaryCmd:
		if err := w.stmt(node.X); err != nil {
			return err
		}
		return w.stmt(node.Y)
	case *syntax.IfClause:
		// Every branch is a command the shell runs, so all three must be walked.
		// Walking only Cond let `if git status; then rm -rf /x; fi` through with the
		// body unchecked.
		if err := w.stmts(node.Cond); err != nil {
			return err
		}
		if err := w.stmts(node.Then); err != nil {
			return err
		}
		if node.Else != nil {
			return w.command(node.Else)
		}
		return nil
	case *syntax.ForClause:
		// A word-list `for` is bounded; a C-style loop is not.
		if _, ok := node.Loop.(*syntax.WordIter); !ok {
			return errCompoundUnmodelled
		}
		return w.stmts(node.Do)
	case *syntax.WhileClause:
		// No bound on the iteration count: `while true; do <allowlisted>; done`
		// would spin the Bash tool.
		return errCompoundUnmodelled
	default:
		return errCompoundUnmodelled
	}
}

// stmts visits a statement list.
func (w *compoundWalker) stmts(stmts []*syntax.Stmt) error {
	for _, stmt := range stmts {
		if err := w.stmt(stmt); err != nil {
			return err
		}
	}
	return nil
}

// call checks one simple command against the rule set.
func (w *compoundWalker) call(call *syntax.CallExpr) error {
	if len(call.Assigns) > 0 {
		// `VAR=value cmd` defeats prefix matching, and an assignment can change
		// what a later command reads.
		return errCompoundUnmodelled
	}
	if len(call.Args) == 0 {
		return errCompoundUnmodelled
	}
	words := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		value, ok := literalWord(arg)
		if !ok {
			return errCompoundUnmodelled
		}
		words = append(words, value)
	}
	segment := strings.Join(words, " ")
	if matchesAnyPattern(segment, w.rules.Gated) {
		return errCompoundUnmodelled
	}
	if compoundShapeAllows(words, w.cwd, w.roots) {
		w.calls++
		return nil
	}
	if wrapperProgram(words[0]) {
		return errCompoundUnmodelled
	}
	if !matchesAnyPattern(segment, w.rules.Allow) {
		return errCompoundUnmodelled
	}
	w.calls++
	return nil
}

// redirects refuses every redirect this walker does not explicitly model.
func (w *compoundWalker) redirects(redirs []*syntax.Redirect) error {
	for _, redir := range redirs {
		switch redir.Op {
		case syntax.DplOut, syntax.DplIn:
			// `2>&1` and friends duplicate a descriptor rather than write a file.
			if redir.Word == nil {
				return errCompoundUnmodelled
			}
			if target, ok := literalWord(redir.Word); !ok || !isDupTarget(target) {
				return errCompoundUnmodelled
			}
		case syntax.RdrOut, syntax.AppOut, syntax.RdrIn:
			if redir.Word == nil {
				return errCompoundUnmodelled
			}
			if target, ok := literalWord(redir.Word); !ok || target != "/dev/null" {
				return errCompoundUnmodelled
			}
		default:
			return errCompoundUnmodelled
		}
	}
	return nil
}

// errCompoundUnmodelled marks a tree shape the walker refuses.
var errCompoundUnmodelled = &compoundRefusalError{}

// compoundRefusalError is the sentinel error for an unmodelled shape.
type compoundRefusalError struct{}

// Error implements error.
func (*compoundRefusalError) Error() string { return "compound tree shape refused" }

// literalWord renders a word only when it carries no expansion.
func literalWord(word *syntax.Word) (string, bool) {
	if word == nil || len(word.Parts) != 1 {
		return "", false
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	if !ok {
		return "", false
	}
	return lit.Value, true
}

// isDupTarget reports whether a descriptor target is a digit or `-`.
func isDupTarget(target string) bool {
	if target == "-" {
		return true
	}
	return allDigits(target)
}

// compoundShapeAllows reports whether a shape guard approves this call without an
// allow rule. The caller checks the gated rules first, so a deny or ask still wins.
func compoundShapeAllows(words []string, cwd string, roots []string) bool {
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "git":
		return GitShapeAllows(words[1:], cwd, roots) ||
			GitMergeAllows(words[1:], DefaultMergeRoots())
	case "sed":
		return SedFilterAllows(words[1:], cwd, roots)
	case "gh":
		return GhMergeAllows(words[1:]) || GhCreateAllows(words[1:]) ||
			GhReadAllows(words[1:])
	}
	return false
}

// matchesAnyPattern reports whether segment matches any pattern.
func matchesAnyPattern(segment string, patterns []string) bool {
	for _, pattern := range patterns {
		if RuleMatches(segment, pattern) {
			return true
		}
	}
	return false
}

// wrapperProgram reports whether the program runs arbitrary code.
func wrapperProgram(program string) bool {
	switch program {
	case "git", "env", "xargs", "eval", "exec", "sudo", "ssh",
		"sh", "bash", "zsh", "fish", "node", "deno", "npx", "bunx",
		"python", "python3", "npm", "yarn", "pnpm", "bun":
		return true
	default:
		return false
	}
}
