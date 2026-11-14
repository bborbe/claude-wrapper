// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"regexp"
	"strings"
)

// The guard's verdict vocabulary. Each string is the exact token
// `pane-wake-guard.py` emits, so a log line read from either implementation
// means the same thing.
const (
	// VerdictWake is an idle pane at an empty composer.
	VerdictWake = "wake"
	// VerdictWakeSuggestion is an idle pane whose composer holds only the dim
	// next-prompt suggestion.
	VerdictWakeSuggestion = "wake:suggestion"
	// VerdictSkipSpinner is a pane mid-turn.
	VerdictSkipSpinner = "skip:spinner"
	// VerdictSkipOperatorAsk is a pane whose last `👤 You:` closer asks the operator.
	VerdictSkipOperatorAsk = "skip:operator-ask"
	// VerdictSkipUnclassified is a pane that read back empty.
	VerdictSkipUnclassified = "skip:unclassified"
	// VerdictSkipModalSelect is a selection modal (`Enter to select`).
	VerdictSkipModalSelect = "skip:modal-select"
	// VerdictSkipModalCancel is a permission/question modal (`Esc to cancel`).
	VerdictSkipModalCancel = "skip:modal-cancel"
	// VerdictSkipOptionList is a numbered option list (`❯ 1.`).
	VerdictSkipOptionList = "skip:option-list"
	// VerdictSkipComposerNotEmpty is a composer holding typed (non-dim) text.
	VerdictSkipComposerNotEmpty = "skip:composer-not-empty"
	// VerdictSkipNoComposer is a pane with no `❯` prompt, so idleness is unproven.
	VerdictSkipNoComposer = "skip:no-composer"
	// VerdictSkipMachineWait is a pane whose last turn declared a machine wait.
	VerdictSkipMachineWait = "skip:machine-wait"
	// VerdictSkipSleeping is a subject that is date-gated asleep.
	VerdictSkipSleeping = "skip:sleeping"
	// VerdictSkipStateUnreadable is a subject that resolved but whose state could
	// not be read — the one place this guard fails closed.
	VerdictSkipStateUnreadable = "skip:state-unreadable"
	// VerdictErrorUnreadable is a pane that could not be read at all.
	VerdictErrorUnreadable = "error:unreadable"
)

// SubjectStateSleeping is `manager-liveness.py --state`'s asleep value.
const SubjectStateSleeping = "SLEEPING"

// SubjectStateUnreadable is "a subject resolved but its state could not be read".
// Distinct from an empty string, which means no subject resolved at all.
const SubjectStateUnreadable = "UNREADABLE"

// promptLead is the set of characters stripped from a line's left before a
// marker is matched: whitespace, NBSP and box-drawing verticals.
const promptLead = " \t │┃║|"

var (
	// spinnerLine matches a Claude Code spinner line: a glyph, a gerund ending in
	// an ellipsis, then `(<n>s`.
	spinnerLine = regexp.MustCompile(`^\S\s+\w[\w ]*…\s*\(\d+[smh]`)
	// optionRow matches a numbered option list's first row.
	optionRow = regexp.MustCompile(`^` + promptGlyph + `\s*1\.`)
)

// Guard marker strings, named so a spec asserts on the same literal the
// implementation reads.
const (
	spinnerContains  = "esc to interrupt"
	modalSelect      = "Enter to select"
	modalCancel      = "Esc to cancel"
	machineWaitGlyph = "\U0001f7e1 WAITING"
	machineWaitEnds  = "⏰ Ends:"
	operatorAsk      = "\U0001f464 You:"
)

// PaneGuardVerdict returns the verdict for one captured screen.
//
// `text` is nil when the pane could not be read; that is the error branch and
// never a guess. `subjectState` is `manager-liveness.py --state`'s first field,
// passed in so this function stays pure: `SLEEPING` refuses the wake before the
// screen is considered, `UNREADABLE` refuses it as the one fail-closed case, and
// an empty string means no subject resolved so the screen decides.
//
// `allowMachineWait` carves out a sweep-gate wake, whose resting state IS a
// machine wait. An operator ask still blocks, because that check runs after.
func PaneGuardVerdict(
	text *string,
	allowMachineWait bool,
	subjectState string,
) string {
	if verdict, decided := subjectVerdict(subjectState); decided {
		return verdict
	}
	if text == nil {
		return VerdictErrorUnreadable
	}
	if strings.TrimSpace(*text) == "" {
		return VerdictSkipUnclassified
	}
	lines := strippedLines(*text)
	if spinnerPresent(lines) {
		return VerdictSkipSpinner
	}
	if verdict := modalVerdict(lines); verdict != "" {
		return verdict
	}
	verdict, suggestion := composerVerdict(*text)
	if verdict != "" {
		return verdict
	}
	if !allowMachineWait && containsMachineWait(lines) {
		return VerdictSkipMachineWait
	}
	if asksOperator(lines) {
		return VerdictSkipOperatorAsk
	}
	if suggestion {
		return VerdictWakeSuggestion
	}
	return VerdictWake
}

// subjectVerdict is the two subject states the guard acts on before the screen.
func subjectVerdict(subjectState string) (string, bool) {
	switch subjectState {
	case SubjectStateSleeping:
		return VerdictSkipSleeping, true
	case SubjectStateUnreadable:
		return VerdictSkipStateUnreadable, true
	}
	return "", false
}

// spinnerPresent reports whether any line is a spinner or carries its marker.
func spinnerPresent(lines []string) bool {
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), spinnerContains) ||
			spinnerLine.MatchString(strings.TrimLeft(line, promptLead)) {
			return true
		}
	}
	return false
}

// modalVerdict returns the modal/option-list verdict, or "" when none is present.
func modalVerdict(lines []string) string {
	for _, line := range lines {
		lead := strings.TrimLeft(line, promptLead)
		if strings.HasPrefix(lead, modalSelect) {
			return VerdictSkipModalSelect
		}
		if strings.HasPrefix(lead, modalCancel) {
			return VerdictSkipModalCancel
		}
		if optionRow.MatchString(lead) {
			return VerdictSkipOptionList
		}
	}
	return ""
}

// composerVerdict classifies the composer: a refusal, or "" with the suggestion
// flag set when the pane is wakeable.
func composerVerdict(text string) (verdict string, suggestion bool) {
	rawLines := rawScreenLines(text)
	prompts := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		if strings.HasPrefix(strings.TrimLeft(StripANSI(line), promptLead), promptGlyph) {
			prompts = append(prompts, line)
		}
	}
	if len(prompts) == 0 {
		return VerdictSkipNoComposer, false
	}
	raw := prompts[len(prompts)-1]
	plain := strings.TrimLeft(StripANSI(raw), promptLead)
	composer := strings.TrimPrefix(plain, promptGlyph)
	if strings.Trim(composer, " \t ") == "" {
		return "", false
	}
	index := strings.Index(raw, promptGlyph)
	if index < 0 || !VisibleIsDim(raw[index+len(promptGlyph):]) {
		return VerdictSkipComposerNotEmpty, false
	}
	return "", true
}

// asksOperator reports whether the last `👤 You:` closer asks for something.
func asksOperator(lines []string) bool {
	asks := operatorAsks(lines)
	if len(asks) == 0 {
		return false
	}
	verb := strings.ToLower(strings.TrimSpace(asks[len(asks)-1]))
	return !strings.HasPrefix(verb, "nothing")
}

// strippedLines is the screen with escapes removed and trailing whitespace
// trimmed, one entry per line.
func strippedLines(text string) []string {
	raw := strings.Split(StripANSI(text), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		lines = append(lines, strings.TrimRight(line, " \t\r "))
	}
	return lines
}

// rawScreenLines keeps the escape bytes, trimming only trailing whitespace.
func rawScreenLines(text string) []string {
	raw := strings.Split(text, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		lines = append(lines, strings.TrimRight(line, " \t\r "))
	}
	return lines
}

func containsMachineWait(lines []string) bool {
	for _, line := range lines {
		if strings.Contains(line, machineWaitGlyph) || strings.Contains(line, machineWaitEnds) {
			return true
		}
	}
	return false
}

// operatorAsks returns the text AFTER the ask marker on every line that carries
// it, oldest first.
func operatorAsks(lines []string) []string {
	asks := make([]string, 0, 1)
	for _, line := range lines {
		index := strings.Index(line, operatorAsk)
		if index < 0 {
			continue
		}
		asks = append(asks, line[index+len(operatorAsk):])
	}
	return asks
}

// IsWakeVerdict reports whether a verdict is one the send path may act on.
func IsWakeVerdict(verdict string) bool {
	return strings.HasPrefix(verdict, "wake")
}
