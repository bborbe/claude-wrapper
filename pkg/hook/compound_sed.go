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
	// sedFlags are the only options the filter shape accepts.
	sedFlags = map[string]bool{
		"-n": true, "-E": true, "-r": true, "-En": true, "-nE": true, "-rn": true, "-nr": true,
	}
	// sedAddress is one line address: a number, `$`, or a /regex/.
	sedAddress = `(\d+|\$|/[^/]*/)`
	// sedPrint matches a p or q selector, optionally with an address or a range.
	sedPrint = regexp.MustCompile(
		`^(` + sedAddress + `(,(` + sedAddress + `|\+\d+))?)?[pq]$|^\d+q$`,
	)
)

// SedFilterAllows reports whether `sed <args>` is used purely as a filter: only the
// -n/-E/-r flags, one static `s///`, `p` or `q` script, and file operands under
// roots or in /tmp.
func SedFilterAllows(values []string, cwd string, roots []string) bool {
	index, script := 0, ""
	for index < len(values) && strings.HasPrefix(values[index], "-") && values[index] != "-" {
		value := values[index]
		if value == "-e" && index+1 < len(values) && script == "" {
			script, index = values[index+1], index+2
			continue
		}
		if !sedFlags[value] {
			return false
		}
		index++
	}
	if script == "" {
		if index >= len(values) {
			return false
		}
		script, index = values[index], index+1
	}
	if !sedScriptAllowed(script) {
		return false
	}
	for _, operand := range values[index:] {
		if strings.HasPrefix(operand, "-") || !readableOperand(operand, cwd, roots) {
			return false
		}
	}
	return true
}

// sedScriptAllowed reports whether every `;`-separated part of the script is a
// substitution or a print/quit selector.
func sedScriptAllowed(script string) bool {
	parts := strings.FieldsFunc(script, func(r rune) bool { return r == ';' || r == '\n' })
	checked := 0
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		checked++
		if !isSedSubstitution(trimmed) && !sedPrint.MatchString(trimmed) {
			return false
		}
	}
	return checked > 0
}

// isSedSubstitution reports whether text is one `s<delim>from<delim>to<delim>[flags]`
// expression. It is hand-written because the original pattern uses a backreference
// and a negative lookahead, neither of which RE2 supports.
func isSedSubstitution(text string) bool {
	if len(text) < 4 || text[0] != 's' {
		return false
	}
	delimiter := text[1]
	if delimiter == '\\' || delimiter == '\n' {
		return false
	}
	rest := text[2:]
	for range 2 {
		consumed, ok := scanToDelimiter(rest, delimiter)
		if !ok {
			return false
		}
		rest = rest[consumed+1:]
	}
	for _, flag := range rest {
		if !strings.ContainsRune("gIip0123456789", flag) {
			return false
		}
	}
	return true
}

// scanToDelimiter returns the offset of the next unescaped delimiter.
func scanToDelimiter(text string, delimiter byte) (int, bool) {
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++
		case delimiter:
			return i, true
		}
	}
	return 0, false
}

// readableOperand reports whether a sed file operand stays inside roots or in /tmp.
func readableOperand(path string, cwd string, roots []string) bool {
	if strings.HasPrefix(path, "~") && !strings.HasPrefix(path, "~/") {
		return false
	}
	absolute := strings.HasPrefix(path, "/") || strings.HasPrefix(path, "~/")
	if !absolute && cwd == "" {
		return false
	}
	full := path
	if !absolute {
		full = filepath.Join(cwd, path)
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			full = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		real = full
	}
	if strings.HasPrefix(real, "/tmp/") || strings.HasPrefix(real, "/private/tmp/") {
		return true
	}
	return UnderRoots(full, roots)
}
