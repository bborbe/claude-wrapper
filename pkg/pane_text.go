// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"regexp"
	"strings"
)

// ansiEscape matches every escape shape a real `wezterm cli get-text --escapes`
// read carries — CSI with private/intermediate bytes (`\x1b[?25h`), OSC/DCS,
// charset selection (`\x1b(B`) and the remaining two-char escapes. A narrower
// pattern leaves those bytes in the visible text, where they read as non-faint
// characters and turn a genuinely empty composer into "not ready".
//
// Ported verbatim from `plugin-version-census.py` `_SGR`; the reload lever and
// this guard MUST agree on what the visible text is, or one of them types into a
// composer the other refuses.
var ansiEscape = regexp.MustCompile(
	`\x1b\[[0-?]*[ -/]*[@-~]` + // CSI, private + intermediate bytes included
		`|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)` + // OSC … BEL / ST
		`|\x1b[()][A-Za-z0-9]` + // charset selection
		`|\x1b[@-Z\\-_]`, // other two-char escapes
)

// sgrEscape matches an SGR (`…m`) escape and captures its parameter list.
var sgrEscape = regexp.MustCompile(`\x1b\[([0-?]*)[ -/]*m`)

// StripANSI returns the line's visible text, with escape sequences removed.
func StripANSI(line string) string {
	return ansiEscape.ReplaceAllString(line, "")
}

// sgrAttrs returns the parameter list of an SGR escape as ints, `nil` for
// anything that is not an SGR.
//
// An omitted parameter is `0`, not a dropped one: `\x1b[m` is the reset form and
// `\x1b[1;;2m` carries an empty middle parameter. Filtering empties out leaves no
// state change at all, so a bare reset would fail to clear faint and a composer
// that was dimmed and then reset would keep reading as empty.
func sgrAttrs(escape string) []int {
	match := sgrEscape.FindStringSubmatch(escape)
	if match == nil {
		return nil
	}
	body := match[1]
	if body == "" {
		return []int{0}
	}
	parts := strings.Split(body, ";")
	params := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			params = append(params, 0)
			continue
		}
		value := 0
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				value = 0
				break
			}
			value = value*10 + int(ch-'0')
		}
		params = append(params, value)
	}
	return params
}

// VisibleIsDim reports whether every visible character of segment is drawn faint
// (SGR 2), and some is.
//
// Parsed POSITIONALLY, never by membership: a bare `2` is a parameter in
// `38;2;r;g;b` (truecolor) and in `38;5;2` (256-colour index 2) as well as the
// faint attribute, so a membership test reads every truecolor escape as faint —
// and a composer holding the operator's colourised text would then read as empty,
// which is the one thing this gate exists to refuse.
//
// Scoped to the VISIBLE text, not the whole segment: a dim span followed by real
// typed text is not empty. Dim must hold for every visible character.
func VisibleIsDim(segment string) bool {
	dim := false
	sawVisible := false
	position := 0
	for _, match := range ansiEscape.FindAllStringIndex(segment, -1) {
		spanSaw, spanDim := scanVisibleDim(segment[position:match[0]], dim)
		sawVisible = sawVisible || spanSaw
		if spanSaw && !spanDim {
			return false
		}
		dim = applySGR(sgrAttrs(segment[match[0]:match[1]]), dim)
		position = match[1]
	}
	spanSaw, spanDim := scanVisibleDim(segment[position:], dim)
	sawVisible = sawVisible || spanSaw
	if spanSaw && !spanDim {
		return false
	}
	return sawVisible
}

// scanVisibleDim reports whether a plain span holds visible characters, and
// whether every one of them is faint under the current state.
func scanVisibleDim(span string, dim bool) (sawVisible bool, allDim bool) {
	for _, ch := range span {
		if isSpaceRune(ch) {
			continue
		}
		if !dim {
			return true, false
		}
		sawVisible = true
	}
	return sawVisible, true
}

// applySGR advances the faint state through one SGR parameter list.
func applySGR(params []int, dim bool) bool {
	for index := 0; index < len(params); {
		param := params[index]
		switch param {
		case 38, 48: // colour: skip its sub-parameters
			index++
			if index < len(params) && params[index] == 5 {
				index += 2 // 38;5;n
			} else if index < len(params) && params[index] == 2 {
				index += 4 // 38;2;r;g;b
			}
			continue
		case 0: // reset: clears faint along with every other attribute
			dim = false
		case 2:
			dim = true
		case 22:
			dim = false
		}
		index++
	}
	return dim
}

func isSpaceRune(ch rune) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', '\v', '\f', ' ':
		return true
	}
	return false
}

// composerLine returns the LAST line whose visible text starts with the prompt
// glyph.
//
// Position — not the glyph alone — is what identifies the composer: every echoed
// submitted prompt above it carries the same glyph, so a substring test matches
// any echoed prompt in scrollback and passes whatever the pane is doing.
func composerLine(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if strings.HasPrefix(StripANSI(lines[index]), promptGlyph) {
			return lines[index], true
		}
	}
	return "", false
}

// PaneIsReady reports whether the composer is EMPTY, including when it holds only
// the TUI's dim placeholder.
//
// A placeholder is not text: Claude Code draws its next-prompt suggestion faint
// (SGR 2), and a plain `get-text` cannot tell it from typed input. The reload
// lever measured that a send into such a composer lands on a long-idle pane, so
// it is ready; text somebody typed is not dim and is refused.
func PaneIsReady(text string) bool {
	line, ok := composerLine(text)
	if !ok {
		return false
	}
	plain := StripANSI(line)
	if strings.TrimSpace(strings.TrimPrefix(plain, promptGlyph)) == "" {
		return true
	}
	index := strings.Index(line, promptGlyph)
	if index < 0 {
		return false
	}
	return VisibleIsDim(line[index+len(promptGlyph):])
}
