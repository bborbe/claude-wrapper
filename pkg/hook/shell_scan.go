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
	// shellHeredoc matches a heredoc opener on a line.
	shellHeredoc = regexp.MustCompile(`<<-?[ \t]*[^ \t;|&]+`)
	// shellInterpreter names the commands whose heredoc body really executes, so
	// the body must stay in the scan.
	shellInterpreter = map[string]bool{
		"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true,
		"python": true, "python3": true, "perl": true, "ruby": true, "node": true,
		"eval": true, "source": true,
	}
	// shellQuoteSpans strips quoted spans, double quotes first: a double-quoted
	// argument is the OUTER span, so removing it takes the inner quotes with it
	// and the pairing cannot be shifted from outside.
	shellDoubleQuoted = regexp.MustCompile(`(?s)"[^"]*"`)
	shellSingleQuoted = regexp.MustCompile(`(?s)'[^']*'`)
	shellBackticked   = regexp.MustCompile("(?s)`[^`]*`")
	// shellSegmentSplit splits a command into the segments a shell executes.
	shellSegmentSplit = regexp.MustCompile(`[;|&]`)
	// shellCdLine matches a segment that is a `cd`.
	shellCdLine = regexp.MustCompile(`^[[:space:]]*cd([[:space:]]|$)`)
	// shellGitDashC matches the `git -C <dir>` operand.
	shellGitDashC = regexp.MustCompile(`^[[:space:]]*git[[:space:]]+-C[[:space:]]+([^[:space:]]+)`)
	// shellGitPush anchors `git push` at the START of a segment. A `git push`
	// later in a segment is an argument, not a command — that anchor is what
	// stopped a task title containing "git push" from being read as a push.
	shellGitPush = regexp.MustCompile(
		`^[[:space:]]*git([[:space:]]+-C[[:space:]]+[^[:space:]]+)?[[:space:]]+push([[:space:]]|$)`,
	)
	// shellGitPushPrefix strips the command word and its optional -C operand.
	shellGitPushPrefix = regexp.MustCompile(
		`^[[:space:]]*git([[:space:]]+-C[[:space:]]+[^[:space:]]+)?[[:space:]]+push`,
	)
	// shellRedirect drops redirections before the arguments are parsed.
	shellRedirect = regexp.MustCompile(`&>>?[[:space:]]*[^[:space:]]*|[0-9]?>>?[[:space:]]*[^[:space:]]*|[0-9]?<[[:space:]]*[^[:space:]]*`)
	// shellBranchSubstitution matches the two "current branch" idioms.
	shellBranchSubstitution = regexp.MustCompile(
		`"(\$\(git branch --show-current\)|\$\(git rev-parse --abbrev-ref HEAD\))"|` +
			`\$\(git branch --show-current\)|\$\(git rev-parse --abbrev-ref HEAD\)`,
	)
)

// scanText returns the command with heredoc bodies and quoted spans removed.
//
// A heredoc body is stdin, not segments, so a body quoting a push command must
// not be read as one — except when the heredoc feeds a shell interpreter, where
// the body really is executed.
func scanText(command string) string {
	return stripQuotedSpans(stripHeredocBodies(command))
}

// stripHeredocBodies drops heredoc bodies that are fed to a non-interpreter.
func stripHeredocBodies(command string) string {
	lines := strings.Split(command, "\n")
	var kept []string
	delimiter := ""
	for _, line := range lines {
		if delimiter != "" {
			if line == delimiter {
				delimiter = ""
			}
			continue
		}
		kept = append(kept, line)
		delimiter = heredocDelimiter(line)
	}
	return strings.Join(kept, "\n")
}

// heredocDelimiter returns the delimiter when line opens a heredoc whose body is
// data rather than code.
func heredocDelimiter(line string) string {
	match := shellHeredoc.FindString(line)
	if match == "" {
		return ""
	}
	lead := strings.TrimSpace(strings.SplitN(line, " ", 2)[0])
	lead = filepath.Base(lead)
	if shellInterpreter[lead] {
		return ""
	}
	delimiter := strings.TrimPrefix(match, "<<")
	delimiter = strings.TrimPrefix(delimiter, "-")
	delimiter = strings.TrimSpace(delimiter)
	delimiter = strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(delimiter)
	return delimiter
}

// stripQuotedSpans removes double-quoted, single-quoted and backticked spans.
func stripQuotedSpans(command string) string {
	command = shellDoubleQuoted.ReplaceAllString(command, "")
	command = shellSingleQuoted.ReplaceAllString(command, "")
	return shellBackticked.ReplaceAllString(command, "")
}

// segments splits a scanned command into shell segments.
func segments(command string) []string {
	return shellSegmentSplit.Split(command, -1)
}

// cdTarget resolves a `cd` segment's operand against repo, and reports whether
// the segment was a cd at all. An unresolvable operand returns the empty string,
// which reads as "unknown" and fails closed.
func cdTarget(segment string, repo string) (string, bool) {
	if !shellCdLine.MatchString(segment) {
		return repo, false
	}
	operand := strings.TrimSpace(shellCdLine.ReplaceAllString(segment, ""))
	switch {
	case strings.HasPrefix(operand, "/"):
		return operand, true
	case operand == "", strings.HasPrefix(operand, "-"):
		return "", true
	case strings.Contains(operand, "$") || strings.Contains(operand, "`"):
		return "", true
	case operand == "~":
		return os.Getenv("HOME"), true
	case strings.HasPrefix(operand, "~/"):
		return filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(operand, "~/")), true
	case strings.HasPrefix(operand, "~"):
		return "", true
	case repo == "":
		return "", true
	default:
		return repo + "/" + operand, true
	}
}

// gitDashCTarget resolves the `-C` operand of a segment, and reports whether the
// segment carried one.
func gitDashCTarget(segment string, repo string) (string, bool) {
	match := shellGitDashC.FindStringSubmatch(segment)
	if match == nil {
		return repo, false
	}
	operand := match[1]
	switch {
	case strings.HasPrefix(operand, "/"):
		return operand, true
	case operand == "~":
		return os.Getenv("HOME"), true
	case strings.HasPrefix(operand, "~/"):
		return filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(operand, "~/")), true
	default:
		return "", true
	}
}
