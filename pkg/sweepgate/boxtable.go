// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"strings"
	"unicode"

	"golang.org/x/text/width"
)

// FrameHeader is the box's header row.
var FrameHeader = []string{"Topic / Goal / Task", "Session", "Status", "Phase", "Met"}

// HeaderWidths is the box's column widths.
//
// ⚠️ Status is 34, not 30, not 19. The HELD overlay renders
// `⏸️ HELD — <reason> · <age>`; at width 30 the reason survives whole and the
// age — the thing this cell exists to add — is what gets truncated away. Do not
// "restore" a narrower value.
var HeaderWidths = []int{42, 10, 34, 9, 17}

// OSC 8 hyperlink delimiters. The visible text stays unchanged; the escape
// occupies zero terminal cells.
const (
	osc8Open = "\x1b]8;;"
	osc8ST   = "\x1b\\"
)

// RenderBox draws the table the vendored `box-table.py` draws.
//
// It is a pure renderer: no subprocess, no shell. The five-part structure is the
// byte contract — top rule, header, middle rule, one line per row, bottom rule —
// and every cell is padded by display width, not rune count, so the two-cell
// bucket glyphs keep the right border straight.
func RenderBox(header []string, rows [][]string, widths []int) string {
	return renderBoxWithURLs(header, rows, widths, nil)
}

// renderBoxWithURLs is RenderBox plus a per-cell link resolver.
//
// A nil resolver means "no links", which is what the public RenderBox owes: the
// frame's jump links are resolved by the caller that has a context and a runner.
func renderBoxWithURLs(
	header []string, rows [][]string, widths []int, urlFor func(string) string,
) string {
	out := make([]string, 0, len(rows)+4)
	out = append(out, rule("┌", "┬", "┐", widths))
	out = append(out, line(header, widths, nil))
	out = append(out, rule("├", "┼", "┤", widths))
	for _, row := range rows {
		out = append(out, line(row, widths, urlFor))
	}
	out = append(out, rule("└", "┴", "┘", widths))
	return strings.Join(out, "\n")
}

// line renders one `│ … │` row.
func line(vals []string, widths []int, urlFor func(string) string) string {
	cells := make([]string, 0, len(vals))
	for i, value := range vals {
		w := 0
		if i < len(widths) {
			w = widths[i]
		}
		url := ""
		if urlFor != nil {
			url = urlFor(value)
		}
		cells = append(cells, FormatCell(value, w, url))
	}
	return "│ " + strings.Join(cells, " │ ") + " │"
}

// rule renders one horizontal border segment.
func rule(left, mid, right string, widths []int) string {
	segments := make([]string, 0, len(widths))
	for _, w := range widths {
		segments = append(segments, strings.Repeat("─", w+2))
	}
	return left + strings.Join(segments, mid) + right
}

// FormatCell strips any existing OSC 8, truncates to w reserving one cell for
// `…`, pads to w, then wraps the VISIBLE text in a hyperlink when url is set.
//
// The padding stays outside the escape, so the zero-width sequence never enters
// a width sum and a click on the trailing padding does nothing.
func FormatCell(s string, w int, url string) string {
	s = StripLinks(s)
	if DisplayWidth(s) > w {
		truncated := ""
		for _, r := range s {
			if DisplayWidth(truncated)+CellWidth(r) > w-1 {
				break
			}
			truncated += string(r)
		}
		s = truncated + "…"
	}
	pad := ""
	if remaining := w - DisplayWidth(s); remaining > 0 {
		pad = strings.Repeat(" ", remaining)
	}
	if url == "" {
		return s + pad
	}
	return osc8Open + url + osc8ST + s + osc8Open + osc8ST + pad
}

// DisplayWidth is the terminal-cell width of s. OSC 8 escapes cost nothing.
func DisplayWidth(s string) int {
	total := 0
	for _, r := range StripLinks(s) {
		total += CellWidth(r)
	}
	return total
}

// CellWidth is the terminal-cell width of one rune.
//
// VS16 and a combining mark count 0; an East-Asian Wide or Fullwidth rune counts
// 2; everything else counts 1. VS16 deliberately does NOT widen its base — the
// glyphs `⏸️` and `⚠️` paint in one cell, measured.
func CellWidth(r rune) int {
	if r == '\uFE0F' {
		return 0
	}
	if unicode.Is(unicode.Mn, r) {
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	default:
		return 1
	}
}
