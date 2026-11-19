// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"regexp"
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
	walker := &compoundWalker{rules: rules, cwd: cwd, roots: roots, src: command}
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
	src   string
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
		iter, ok := node.Loop.(*syntax.WordIter)
		if !ok {
			return errCompoundUnmodelled
		}
		if err := w.loopWords(iter); err != nil {
			return err
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
	if len(call.Args) == 0 {
		if len(call.Assigns) == 0 {
			return errCompoundUnmodelled
		}
		// A bare assignment sets a fresh local; its value is walked.
		return w.assigns(call.Assigns, false)
	}
	if err := w.assigns(call.Assigns, true); err != nil {
		return err
	}
	if w.calls >= maxTreeCalls {
		return errCompoundUnmodelled
	}
	program, ok := literalWord(call.Args[0])
	if !ok || program == "" || strings.ContainsAny(program, "*?[") {
		return errCompoundUnmodelled
	}
	words, bare, static := staticCallWords(call.Args)
	if static {
		done, err := w.staticShape(program, words, bare)
		if done || err != nil {
			return err
		}
	}
	if w.refusedProgram(program, words, static, call.Args) {
		return errCompoundUnmodelled
	}
	segment := strings.Join(words, " ")
	if !static {
		dynamic, err := w.dynamicSegment(call.Args)
		if err != nil {
			return err
		}
		segment = dynamic
	}
	if !matchesAnyPattern(segment, w.rules.Allow) {
		return errCompoundUnmodelled
	}
	w.calls++
	return nil
}

// staticShape decides a fully literal call a gated rule, a shape guard or `cd`
// settles; done is false when the allow rules must decide.
func (w *compoundWalker) staticShape(program string, words []string, bare []string) (bool, error) {
	if matchesAnyPattern(strings.Join(words, " "), w.rules.Gated) {
		return true, errCompoundUnmodelled
	}
	if unexpandedWords(bare) && shapeAllows(words, w.cwd, w.roots) {
		w.calls++
		return true, nil
	}
	if program == "cd" {
		return true, w.cd(words)
	}
	return false, nil
}

// refusedProgram reports whether no allow rule may approve this program: git, sed
// and gh are approved by their shape guard or not at all — a broad rule like
// `Bash(sed -n:*)` would otherwise pass `sed -n -i` — except a read-only gh verb,
// whose rule cannot reach a write; wrappers, shell-state builtins and `printf -v`
// run or change more than the rule names.
func (w *compoundWalker) refusedProgram(
	program string, words []string, static bool, args []*syntax.Word,
) bool {
	if singleShapeHeads[program] && (!static || !GhRuleReadable(words)) {
		return true
	}
	if wrapperProgram(program) || printSetsVariable(args) {
		return true
	}
	if static {
		return shellStateBuiltin(words)
	}
	return shellStateBuiltins[program]
}

// dynamicSegment walks the expansions of a call that is not fully literal and
// returns its source text, the form an allow rule is matched against.
func (w *compoundWalker) dynamicSegment(args []*syntax.Word) (string, error) {
	for _, arg := range args[1:] {
		if err := w.dynamicWord(arg); err != nil {
			return "", err
		}
	}
	segment := w.sourceText(args)
	if segment == "" || matchesAnyPattern(segment, w.rules.Gated) {
		return "", errCompoundUnmodelled
	}
	return segment, nil
}

// loopWords checks a word-list loop: `for PATH in …` assigns the loop variable, so
// the name must be a fresh local, and every listed word is walked.
func (w *compoundWalker) loopWords(iter *syntax.WordIter) error {
	if !localName(iter.Name.Value) {
		return errCompoundUnmodelled
	}
	for _, item := range iter.Items {
		if err := w.dynamicWord(item); err != nil {
			return err
		}
	}
	return nil
}

// staticCallWords renders every argument as a literal, and the unquoted ones the
// shell can still glob. static is false when any word carries an expansion.
func staticCallWords(args []*syntax.Word) (words []string, bare []string, static bool) {
	for _, arg := range args {
		value, ok := literalWord(arg)
		if !ok {
			return nil, nil, false
		}
		words = append(words, value)
		if _, unquoted := arg.Parts[0].(*syntax.Lit); unquoted {
			// Only an unquoted word is subject to glob and tilde expansion.
			bare = append(bare, value)
		}
	}
	return words, bare, true
}

// cd allows `cd <dir under the roots>` — the documented `cd /abs && <cmd>` form.
// Every later command still matches its own rule; only the directory moves. The
// walker forgets the cwd from here on, so a shape guard refuses a relative operand
// it can no longer resolve: `cd /tmp && sed -n p ../x` reads outside the roots
// otherwise, and a `cd` in a subshell or one that fails leaves the real cwd unknown.
func (w *compoundWalker) cd(words []string) error {
	if len(words) != 2 || !UnderRoots(words[1], w.roots) {
		return errCompoundUnmodelled
	}
	w.cwd = ""
	w.calls++
	return nil
}

// printSetsVariable reports whether a printf/print call takes `-v NAME`, which
// assigns a variable, or a dynamic word where it reads its options.
func printSetsVariable(args []*syntax.Word) bool {
	program, _ := literalWord(args[0])
	if program != "printf" && program != "print" {
		return false
	}
	for _, arg := range args[1:] {
		value, ok := literalWord(arg)
		if !ok {
			return true
		}
		if value == "--" || !strings.HasPrefix(value, "-") {
			return false
		}
		if printVarFlag.MatchString(value) {
			return true
		}
	}
	return false
}

// printVarFlag matches a bundled printf/print option set that carries `-v`.
var printVarFlag = regexp.MustCompile(`^-[A-Za-z]*v`)

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

// literalWord renders a word only when it carries no expansion. A word may be
// built from several literal parts (`--format="%h %s"`); a quoted part may not
// open the word with `~`, which the shell leaves unexpanded while UnderRoots would
// expand it.
func literalWord(word *syntax.Word) (string, bool) {
	if word == nil || len(word.Parts) == 0 {
		return "", false
	}
	var out strings.Builder
	for i, part := range word.Parts {
		value, ok := literalPart(part, i == 0)
		if !ok {
			return "", false
		}
		out.WriteString(value)
	}
	return out.String(), true
}

// literalPart renders one part of a literal word.
func literalPart(part syntax.WordPart, first bool) (string, bool) {
	switch part := part.(type) {
	case *syntax.Lit:
		// The parser keeps an escape in Value (`\-o`) while the shell strips it, so
		// a word carrying one is not the literal the shape guards would judge. An
		// escaped space (`65\ Runbooks`) is the one escape rendered as the shell does.
		value := strings.ReplaceAll(part.Value, "\\ ", " ")
		if strings.Contains(value, "\\") {
			return "", false
		}
		return value, true
	case *syntax.DblQuoted:
		// `"/x/y"` with no expansion inside is the same text as `/x/y`.
		if len(part.Parts) == 0 {
			return "", true
		}
		if len(part.Parts) != 1 {
			return "", false
		}
		lit, ok := part.Parts[0].(*syntax.Lit)
		if !ok || strings.ContainsAny(lit.Value, "\\$`") || (first && strings.HasPrefix(lit.Value, "~")) {
			return "", false
		}
		return lit.Value, true
	case *syntax.SglQuoted:
		// `'150,158p'` never expands: the shell passes the inside verbatim.
		if part.Dollar || (first && strings.HasPrefix(part.Value, "~")) {
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
	return unexpandedWords(words) && shapeAllows(words, cwd, roots)
}

// shapeAllows is compoundShapeAllows without the expansion check, for a caller that
// knows which words the shell can still glob.
func shapeAllows(words []string, cwd string, roots []string) bool {
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "git":
		return GitShapeAllows(words[1:], cwd, roots) ||
			GitMergeAllows(words[1:], DefaultMergeRoots()) ||
			GitWriteAllows(words[1:], DefaultMergeRoots())
	case "sed":
		return SedFilterAllows(words[1:], cwd, roots)
	case "base64":
		return Base64DecodeAllows(words[1:])
	case "gh":
		return GhMergeAllows(words[1:]) || GhCreateAllows(words[1:]) ||
			GhReadAllows(words[1:]) || GhAPIAllows(words[1:])
	}
	return false
}

// unexpandedWords reports whether no word is subject to glob or tilde expansion,
// which turns a planted file name like `--output=x` into an option. A leading
// `~/` is the one tilde form allowed: UnderRoots resolves it itself.
func unexpandedWords(words []string) bool {
	for _, word := range words {
		// A glob expands only to names that start with the word's own first
		// character, so a word that starts with a plain character other than `-`
		// can never become an option. `[-]x` and `*` can.
		if strings.ContainsAny(word, "*?[") &&
			(word == "" || strings.ContainsAny(word[:1], "-*?[")) {
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
