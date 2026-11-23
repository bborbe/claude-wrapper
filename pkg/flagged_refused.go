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

// UnrecordedActors are the `flag_set_by` values that mean "nobody recorded who set
// this flag". `unknown` is what `vault-cli task set … flag true` stamps when the
// write declares no actor (`--by` defaults to `unknown`); `none` and the empty
// string are the same absence from other writers.
//
// This is a THIRD state, not an alias for `agent`. Folding it into the agent case
// is the defect this set exists to prevent: a legitimate operator task whose flag
// was re-stamped without an actor read exactly like an agent-set flag and was
// refused forever, with no remedy visible anywhere in the tick line.
var UnrecordedActors = []string{"", "unknown", "none"}

// Provenance is the closed set of provenance states a flagged row may carry.
type Provenance string

// Recognised Provenance values. AvailableProvenances mirrors this set.
const (
	ProvenanceOperator   Provenance = "operator"
	ProvenanceAgent      Provenance = "agent"
	ProvenanceUnrecorded Provenance = "unrecorded"
)

// AvailableProvenances lists every Provenance value the tick reports.
var AvailableProvenances = []Provenance{
	ProvenanceOperator,
	ProvenanceAgent,
	ProvenanceUnrecorded,
}

// String returns the provenance as its wire value.
func (p Provenance) String() string {
	return string(p)
}

// RefusalProvenanceUnrecorded is the refusal token for a flag whose provenance was
// never recorded. It is distinct from RefusalAgentSetFlag so the tick line can say
// "missing" rather than "an agent set this".
const RefusalProvenanceUnrecorded = "flag-provenance-unrecorded"

// ProvenanceClass folds a raw `flag_set_by` into the three states the tick line
// names. `legacy` is an operator row — it was dispositioned by the operator.
func ProvenanceClass(setBy string) Provenance {
	value := strings.TrimSpace(setBy)
	switch {
	case slices.Contains(OpeningActors, value):
		return ProvenanceOperator
	case slices.Contains(UnrecordedActors, value):
		return ProvenanceUnrecorded
	default:
		return ProvenanceAgent
	}
}

// ProvenanceRefusal returns the refusal reason for a non-admitting provenance, or
// the empty string when the flag admits.
//
// The unrecorded case carries the one command that clears it, because a refusal
// with no visible remedy is indistinguishable from a permanent one. Only the
// operator runs that command: an agent re-stamping `--by operator` would be
// writing the operator's approval in their name.
//
// The title is interpolated into that command, so it goes through shellQuote. A
// vault task title is attacker-influenced content — any session may file a task —
// and a title carrying a double quote, `$(…)` or a backtick would otherwise close
// the quoting and run as shell the moment the operator pastes the remedy.
func ProvenanceRefusal(setBy, name string) string {
	switch ProvenanceClass(setBy) {
	case ProvenanceOperator:
		return ""
	case ProvenanceUnrecorded:
		return RefusalProvenanceUnrecorded +
			": no actor recorded; clear it with vault-cli task set " + shellQuote(name) + " flag true --by operator"
	default:
		return RefusalAgentSetFlag
	}
}

// shellQuote (pkg/tab.go) renders the title as a POSIX single-quoted shell word.

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

// Refusals returns the provenance refusal a row carries: `agent-set-flag` when the
// flag is SET and an agent set it, `flag-provenance-unrecorded` when it is SET and
// nobody recorded the actor, and nothing otherwise.
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
	if reason := ProvenanceRefusal(flagSetByOf(block), "<name>"); reason != "" {
		return []string{reason}
	}
	return []string{}
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
