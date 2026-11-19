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
	if w.calls >= maxTreeCalls {
		return errCompoundUnmodelled
	}
	segment := strings.Join(words, " ")
	if matchesAnyPattern(segment, w.rules.Gated) {
		return errCompoundUnmodelled
	}
	if compoundShapeAllows(words, w.cwd, w.roots) {
		w.calls++
		return nil
	}
	if singleShapeHeads[words[0]] {
		// git / sed / gh are approved by their shape guard or not at all: a broad
		// rule like `Bash(sed -n:*)` would otherwise pass `sed -n -i`.
		return errCompoundUnmodelled
	}
	if wrapperProgram(words[0]) || shellStateBuiltin(words) {
		return errCompoundUnmodelled
	}
	if !matchesAnyPattern(segment, w.rules.Allow) {
		return errCompoundUnmodelled
	}
	w.calls++
	return nil
}

// shellStateBuiltins run another command or change shell state a later command in
// the same line inherits, so a prefix rule naming one is no capability limit.
// Mirrors AST_WRAPPERS in the retired allow-compound-bash.py.
var shellStateBuiltins = map[string]bool{
	"trap": true, "source": true, ".": true, "command": true, "builtin": true,
	"timeout": true, "nohup": true, "nice": true, "watch": true, "time": true,
	"doas": true, "parallel": true, "noglob": true, "nocorrect": true, "-": true,
	"hash": true, "unhash": true, "alias": true, "unalias": true, "unset": true,
	"set": true, "setopt": true, "unsetopt": true, "emulate": true, "enable": true,
	"disable": true, "zmodload": true, "autoload": true, "functions": true,
	"typeset": true, "declare": true, "export": true, "local": true, "readonly": true,
	"integer": true, "float": true, "let": true, "read": true, "vared": true,
	"getopts": true, "mapfile": true, "readarray": true, "shift": true, "zstyle": true,
	"bindkey": true, "exec": true, "eval": true,
}

// shellStateBuiltin reports whether the call is one of shellStateBuiltins.
// `command -v|-V <name>…` only looks a name up and never runs it, so it stays
// with the allow rules.
func shellStateBuiltin(words []string) bool {
	if !shellStateBuiltins[words[0]] {
		return false
	}
	if words[0] == "command" && len(words) > 2 && (words[1] == "-v" || words[1] == "-V") {
		for _, word := range words[2:] {
			if strings.HasPrefix(word, "-") {
				return true
			}
		}
		return false
	}
	return true
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
		case syntax.RdrOut, syntax.AppOut, syntax.RdrIn, syntax.RdrAll, syntax.AppAll,
			syntax.RdrClob:
			if err := w.fileRedirect(redir); err != nil {
				return err
			}
		case syntax.Hdoc, syntax.DashHdoc:
			// Data on stdin. The body must be free of expansions: an unquoted
			// delimiter lets `$(…)` inside the body run.
			if redir.Hdoc != nil && !literalParts(redir.Hdoc) {
				return errCompoundUnmodelled
			}
		case syntax.WordHdoc:
			if _, ok := literalWord(redir.Word); !ok {
				return errCompoundUnmodelled
			}
		default:
			return errCompoundUnmodelled
		}
	}
	return nil
}

// fileRedirect allows a file redirect to `/dev/null`, or a write whose resolved
// target stays inside /tmp or under the session roots. A `{fd}>` allocation and an
// input redirect from anything but `/dev/null` are refused.
func (w *compoundWalker) fileRedirect(redir *syntax.Redirect) error {
	if redir.Word == nil {
		return errCompoundUnmodelled
	}
	if redir.N != nil && !allDigits(redir.N.Value) {
		return errCompoundUnmodelled
	}
	target, ok := literalWord(redir.Word)
	if !ok {
		return errCompoundUnmodelled
	}
	if target == "/dev/null" {
		return nil
	}
	if redir.Op == syntax.RdrIn {
		return errCompoundUnmodelled
	}
	if TmpTarget(target) {
		return nil
	}
	// A relative or `~` target would resolve against a cwd the shell may have
	// changed; only an absolute path under the roots is bounded.
	if WriteTargetUnderRoots(target, w.roots) {
		return nil
	}
	return errCompoundUnmodelled
}

// literalParts reports whether a word (a heredoc body) carries no expansion.
func literalParts(word *syntax.Word) bool {
	for _, part := range word.Parts {
		if _, ok := part.(*syntax.Lit); !ok {
			return false
		}
	}
	return true
}

// maxTreeCalls bounds how many simple commands one tree may carry.
const maxTreeCalls = 24

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
	switch part := word.Parts[0].(type) {
	case *syntax.Lit:
		// The parser keeps an escape in Value (`\-o`) while the shell strips it, so
		// a word carrying one is not the literal the shape guards would judge.
		if strings.Contains(part.Value, "\\") {
			return "", false
		}
		return part.Value, true
	case *syntax.DblQuoted:
		// `"/x/y"` with no expansion inside is the same single word as `/x/y`.
		if len(part.Parts) != 1 {
			return "", false
		}
		lit, ok := part.Parts[0].(*syntax.Lit)
		if !ok || strings.ContainsAny(lit.Value, "\\$`") || strings.HasPrefix(lit.Value, "~") {
			return "", false
		}
		return lit.Value, true
	case *syntax.SglQuoted:
		// `'150,158p'` never expands: the shell passes the inside verbatim, and the
		// shape guards still judge that value exactly as they judge a bare word.
		// A quoted `~` is not expanded by the shell, while UnderRoots would expand it.
		if part.Dollar || strings.HasPrefix(part.Value, "~") {
			return "", false
		}
		return part.Value, true
	}
	return "", false
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
	if len(words) == 0 || !unexpandedWords(words) {
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

// unexpandedWords reports whether no word is subject to glob or tilde expansion,
// which turns a planted file name like `--output=x` into an option. A leading
// `~/` is the one tilde form allowed: UnderRoots resolves it itself.
func unexpandedWords(words []string) bool {
	for _, word := range words {
		if strings.ContainsAny(word, "*?[") {
			return false
		}
		if strings.Contains(word, "~") && !strings.HasPrefix(word, "~/") {
			return false
		}
		if strings.Contains(strings.TrimPrefix(word, "~/"), "~") {
			return false
		}
	}
	return true
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

// wrapperProgram reports whether the program runs arbitrary code or changes the
// directory a later command resolves against.
//
// `cd` belongs here: a shape guard resolves a relative operand against the session
// cwd, but the shell resolves it against the directory the `cd` just entered, so
// `cd /tmp && sed -n p ../Users/…/id_rsa` read a file under a root that the shell
// never visited.
func wrapperProgram(program string) bool {
	switch program {
	case "git", "env", "xargs", "eval", "exec", "sudo", "ssh", "cd",
		"sh", "bash", "zsh", "fish", "node", "deno", "npx", "bunx",
		"python", "python3", "npm", "yarn", "pnpm", "bun":
		return true
	default:
		return false
	}
}
