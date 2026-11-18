// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// compoundAssignPrefix matches the leading `NAME=literal;` run.
	compoundAssignPrefix = regexp.MustCompile(`^((?:\s*[A-Za-z_][A-Za-z0-9_]*=[^;]*?\s*;\s*)+)`)
	compoundAssignPair   = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)=([^;]*?)\s*;`)
	// compoundUnsafeValue disqualifies a value: whitespace, quotes, a substitution,
	// a backslash, or a separator/redirect character.
	compoundUnsafeValue = regexp.MustCompile(`[\s;|&<>(){}$` + "`" + `'"\\]`)
	// compoundVarWord matches `$NAME` and `${NAME}` in the body.
	compoundVarWord = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)
	// compoundBashRule matches one `Bash(...)` permission rule.
	compoundBashRule = regexp.MustCompile(`(?s)^Bash\((.*)\)$`)
)

// CompoundRules is the merged rule set the guard composes over.
type CompoundRules struct {
	// Allow are the `Bash(...)` allow patterns.
	Allow []string
	// Gated are the `Bash(...)` ask and deny patterns. A subcommand matching one is
	// never composed, whatever an allow rule says.
	Gated []string
}

// SplitCompound splits a command on `&&`, `||`, `|&`, `;`, `|` and newline,
// ignoring separators inside quotes. ok is false when a quote is left open, because
// the split cannot then be trusted.
//
// Unlike the cd-git guard's splitter this one does not treat a backslash as an
// escape inside quotes — it mirrors allow-compound-bash.py, where a quoted span is
// taken literally and an escaped character outside quotes is copied through.
func SplitCompound(command string) ([]string, bool) {
	var segments []string
	var current strings.Builder
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]
		if quote != 0 {
			current.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '"' || c == '\'':
			quote = c
			current.WriteByte(c)
		case c == '\\' && i+1 < len(command):
			current.WriteByte(c)
			current.WriteByte(command[i+1])
			i++
		case strings.HasPrefix(command[i:], "&&"), strings.HasPrefix(command[i:], "||"),
			strings.HasPrefix(command[i:], "|&"):
			segments = appendCompoundSegment(segments, current.String())
			current.Reset()
			i++
		case c == ';' || c == '|' || c == '\n':
			segments = appendCompoundSegment(segments, current.String())
			current.Reset()
		default:
			current.WriteByte(c)
		}
	}
	if quote != 0 {
		return nil, false
	}
	return appendCompoundSegment(segments, current.String()), true
}

// appendCompoundSegment adds a non-empty trimmed segment.
func appendCompoundSegment(segments []string, segment string) []string {
	if trimmed := strings.TrimSpace(segment); trimmed != "" {
		segments = append(segments, trimmed)
	}
	return segments
}

// UnsafeSyntax reports whether the text can hide a command the guard would not see:
// a substitution anywhere, or an unquoted `(){}<>!` or a bare `&`. `2>&1` is a
// descriptor duplication, not a file write, so it is blanked before the scan.
func UnsafeSyntax(command string) bool {
	if strings.Contains(command, "`") || strings.Contains(command, "$(") {
		return true
	}
	scan := strings.ReplaceAll(command, "2>&1", "    ")
	var quote byte
	for i := 0; i < len(scan); i++ {
		c := scan[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '\\':
			i++
		case strings.ContainsRune("(){}<>!", rune(c)):
			return true
		case strings.HasPrefix(scan[i:], "&&"), strings.HasPrefix(scan[i:], "|&"):
			i++
		case c == '&':
			return true
		}
	}
	return false
}

// SubstituteLiterals rewrites a compound whose leading assignments are plain
// literals, so every `$NAME` word carries its value. It reports false when the
// command is outside scope: no leading assignment, a value that is not one bare
// word, or nothing that would change.
//
// The single-bare-word rule is load-bearing. The shell word-splits an unquoted
// expansion, so `W="/x push"; git -C $W log` runs `git -C /x push log`, while a
// naive rewrite reads it as one operand with `log` as the subcommand.
func SubstituteLiterals(command string) (string, bool) {
	match := compoundAssignPrefix.FindString(command)
	if match == "" {
		return "", false
	}
	env := map[string]string{}
	for _, pair := range compoundAssignPair.FindAllStringSubmatch(match, -1) {
		value := strings.TrimSpace(pair[2])
		if value == "" || compoundUnsafeValue.MatchString(value) {
			return "", false
		}
		env[pair[1]] = value
	}
	if len(env) == 0 {
		return "", false
	}
	rewritten, changed := substituteVarWords(command[len(match):], env)
	if !changed {
		return "", false
	}
	return match + rewritten, true
}

// substituteVarWords replaces `$NAME` and `${NAME}` with their literal value,
// leaving single-quoted spans alone because the shell does not expand there.
func substituteVarWords(rest string, env map[string]string) (string, bool) {
	var out strings.Builder
	var quote byte
	changed := false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if quote == '\'' {
			out.WriteByte(c)
			if c == '\'' {
				quote = 0
			}
			continue
		}
		switch c {
		case '\\':
			if i+1 < len(rest) {
				out.WriteString(rest[i : i+2])
				i++
			}
		case '\'':
			quote = '\''
			out.WriteByte(c)
		case '"':
			quote = toggleQuote(quote)
			out.WriteByte(c)
		case '$':
			value, width, ok := lookupVarWord(rest[i:], env)
			if !ok {
				out.WriteByte(c)
				continue
			}
			out.WriteString(value)
			i += width - 1
			changed = true
		default:
			out.WriteByte(c)
		}
	}
	return out.String(), changed
}

// lookupVarWord resolves a `$NAME` or `${NAME}` at the head of rest.
func lookupVarWord(rest string, env map[string]string) (string, int, bool) {
	word := compoundVarWord.FindStringSubmatch(rest)
	if word == nil {
		return "", 0, false
	}
	name := word[1]
	if name == "" {
		name = word[2]
	}
	value, ok := env[name]
	if !ok {
		return "", 0, false
	}
	return value, len(word[0]), true
}

// toggleQuote flips the double-quote state.
func toggleQuote(quote byte) byte {
	if quote == '"' {
		return 0
	}
	return '"'
}

// RuleMatches reports whether one subcommand matches one `Bash(...)` pattern.
//
// A `:*` suffix is a prefix rule, which is what the harness itself matches. A
// pattern carrying a `*` anywhere else is matched with Python's fnmatch semantics,
// not path globbing.
func RuleMatches(segment string, pattern string) bool {
	if prefix, ok := strings.CutSuffix(pattern, ":*"); ok {
		return segment == prefix || strings.HasPrefix(segment, prefix+" ")
	}
	if strings.Contains(pattern, "*") {
		return FnmatchCase(segment, pattern)
	}
	return segment == pattern
}

// FnmatchCase reports whether name matches pattern with Python's fnmatch rules:
// `*` matches any run of characters including `/`, `?` one character, and `[seq]`
// / `[!seq]` a character class. It is deliberately not path.Match, which stops `*`
// at a separator and would refuse rules the harness allows.
func FnmatchCase(name string, pattern string) bool {
	expression := fnmatchToRegexp(pattern)
	compiled, err := regexp.Compile(`(?s)^` + expression + `$`)
	if err != nil {
		return false
	}
	return compiled.MatchString(name)
}

// fnmatchToRegexp translates an fnmatch pattern into a regexp body.
func fnmatchToRegexp(pattern string) string {
	var out strings.Builder
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			out.WriteString(".*")
		case '?':
			out.WriteString(".")
		case '[':
			class, next, ok := fnmatchClass(pattern, i)
			if !ok {
				out.WriteString(`\[`)
				continue
			}
			out.WriteString(class)
			i = next
		default:
			out.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return out.String()
}

// fnmatchClass translates one `[...]` class starting at index i.
func fnmatchClass(pattern string, i int) (string, int, bool) {
	end := strings.IndexByte(pattern[i+1:], ']')
	if end < 0 {
		return "", i, false
	}
	body := pattern[i+1 : i+1+end]
	negated := strings.HasPrefix(body, "!")
	if negated {
		body = strings.TrimPrefix(body, "!")
	}
	escaped := strings.ReplaceAll(body, `\`, `\\`)
	if negated {
		return "[^" + escaped + "]", i + 1 + end, true
	}
	return "[" + escaped + "]", i + 1 + end, true
}

// LoadRules merges the `Bash(...)` rules from the user settings and every ancestor
// project settings file between cwd and the home directory.
//
// An unreadable or wrongly-shaped settings file is an error rather than an empty
// set: its deny and ask rules would be unknowable, and composing without them is
// the one failure direction this guard must not take.
func LoadRules(cwd string) (CompoundRules, error) {
	home := os.Getenv("HOME")
	paths := make([]string, 0, 20)
	paths = append(paths,
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(home, ".claude", "settings.local.json"),
	)
	paths = append(paths, ancestorSettings(cwd, home)...)
	var rules CompoundRules
	// The home settings appear twice when cwd is inside home — once in the fixed
	// list and once from the ancestor walk, which is how the script builds it too.
	// Dedupe so a rule list stays readable; the match is a set membership either way.
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		perms, err := readPermissions(path)
		if err != nil {
			return CompoundRules{}, err
		}
		if perms == nil {
			continue
		}
		rules.Allow = appendBashRules(rules.Allow, perms["allow"])
		rules.Gated = appendBashRules(rules.Gated, perms["ask"])
		rules.Gated = appendBashRules(rules.Gated, perms["deny"])
	}
	return rules, nil
}

// ancestorSettings returns the `.claude/settings*.json` paths from cwd up to home.
func ancestorSettings(cwd string, home string) []string {
	if cwd == "" || home == "" {
		return nil
	}
	abs, err := filepath.Abs(cwd)
	if err != nil || len(abs) < len(home) {
		return nil
	}
	var paths []string
	for dir := abs; dir != "/" && len(dir) >= len(home); dir = filepath.Dir(dir) {
		paths = append(paths,
			filepath.Join(dir, ".claude", "settings.json"),
			filepath.Join(dir, ".claude", "settings.local.json"),
		)
		if dir == home {
			break
		}
	}
	return paths
}

// readPermissions returns one settings file's permissions map, or nil when the file
// is absent.
func readPermissions(path string) (map[string]any, error) {
	data, err := os.ReadFile(path) //#nosec G304 G703 -- a fixed name under a resolved root
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	perms, ok := settings["permissions"].(map[string]any)
	if !ok {
		return nil, nil
	}
	return perms, nil
}

// appendBashRules appends the `Bash(...)` patterns found in one rule list.
func appendBashRules(sink []string, raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return sink
	}
	for _, entry := range list {
		rule, ok := entry.(string)
		if !ok {
			continue
		}
		if match := compoundBashRule.FindStringSubmatch(rule); match != nil {
			sink = append(sink, match[1])
		}
	}
	return sink
}
