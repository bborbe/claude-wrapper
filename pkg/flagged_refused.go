// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"regexp"
	"slices"
	"strings"
)

// OpeningActors are the only `flag_set_by` values that admit a row.
//
// `legacy` is the marker the 270 pre-field rows were converted to on 2026-09-30, so an
// absent field can mean REFUSE without silently dropping the operator's own historic
// sets. An absent `flag_set_by` therefore admits nothing.
var OpeningActors = []string{"operator", "legacy"}

// yamlTrue is the TASK truthiness set — the unquoted scalars a YAML 1.1 loader resolves
// to boolean true, `y`/`Y` included. `commands/open.md` reads `flag` off a parsed page as
// a YAML boolean, so a quoted scalar is a STRING and FlagSet rejects it exactly as that
// reader does.
var yamlTrue = []string{"true", "True", "TRUE", "yes", "Yes", "YES", "on", "On", "ON", "y", "Y"}

// flagTruthy is the GOAL/TOPIC truthiness set — read textually and tolerantly, because
// those pages are hand-edited by several writers and historically accepted `1`.
var flagTruthy = []string{"true", "yes", "on", "1"}

var (
	// `(?m)` is what makes `^` mean start-of-line in a whole-frontmatter block.
	flagLine      = regexp.MustCompile(`(?m)^flag:[ \t]*(.*)$`)
	flagSetByLine = regexp.MustCompile(`(?m)^flag_set_by:[ \t]*(.*)$`)
	// inlineNote is a YAML inline comment. `flag: true  # note` loads as a real `true`
	// in the vault's own parser, so reading it as a refusal here would let `task work-on`
	// advance a row this predicate does not count as approved.
	inlineNote = regexp.MustCompile(`\s+#.*$`)
)

// FlagSet reports whether the page's `flag:` is set, read under its KIND's truthiness
// rule.
//
// The two kinds genuinely differ: a task flag must be a YAML boolean (a quoted scalar is
// a string), while a goal or topic flag is read tolerantly and also accepts `1`. Folding
// them into one rule would either refuse a legitimate goal flag or admit a quoted task
// flag that the consumer's own reader does not see.
func FlagSet(block, kind string) bool {
	match := flagLine.FindStringSubmatch(block)
	if match == nil {
		return false
	}
	value := strings.TrimSpace(inlineNote.ReplaceAllString(match[1], ""))
	if kind == "task" {
		// The strict branch cannot strip quotes: `flag: "true"` is a string, not True.
		if strings.HasPrefix(value, `"`) || strings.HasPrefix(value, "'") {
			return false
		}
		return slices.Contains(yamlTrue, value)
	}
	return slices.Contains(flagTruthy, strings.ToLower(strings.Trim(value, `"'`)))
}

// FlagAdmits reports whether the page's `flag:` opens it — the one decision, for every
// kind and tier.
func FlagAdmits(block, kind string) bool {
	return FlagSet(block, kind) && slices.Contains(OpeningActors, flagSetByOf(block))
}

// Refusals returns the provenance refusal a row carries: `agent-set-flag` when the flag
// is SET but its provenance does not admit it, and nothing otherwise.
//
// Identical at every tier. Before the 2026-11-10 operator ruling this returned nothing
// for a goal or topic, so an agent-set flag on a goal opened a manager while the
// byte-identical write on a task was refused; that split is gone, and the reason is the
// same for all three subjects.
//
// An UNFLAGGED row returns nothing here — "not flagged" is the eligibility gate's
// reason, not a provenance refusal, and emitting it twice would put a spurious token on
// every unflagged row. That is why this asks only whether the flag is SET and then
// whether it ADMITS, rather than folding both questions into one.
func Refusals(kind, block string) []string {
	if !FlagSet(block, kind) {
		return []string{}
	}
	if FlagAdmits(block, kind) {
		return []string{}
	}
	return []string{RefusalAgentSetFlag}
}

// flagSetByOf reads `flag_set_by` with quotes stripped — the empty string when the key
// is absent, which admits nothing.
func flagSetByOf(block string) string {
	match := flagSetByLine.FindStringSubmatch(block)
	if match == nil {
		return ""
	}
	value := strings.TrimSpace(inlineNote.ReplaceAllString(match[1], ""))
	return strings.Trim(value, `"'`)
}
