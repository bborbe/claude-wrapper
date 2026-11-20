// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strconv"
	"strings"

	"github.com/bborbe/errors"
)

// TopiclessGoalRow is one row of `topicless-goal-keepalive.py`'s BLOCK.
//
// The row is `<goal>\t<verdict>\t<command or ->\t<reason>`: the goal's page name,
// the verdict `topic-keepalive.py` gave it, the command the round would run (`-`
// when the verdict licenses no act), and the reason line.
type TopiclessGoalRow struct {
	Goal    string
	Verdict string
	// Command is `-` when the verdict licenses no act, else the shell command the
	// round would run. ⚠️ The `resume` row's command is `/supervisor:open --goal
	// "<goal>"` — it carries NO session id; the session is the goal page's own
	// `claude_session_id`, which the caller reads.
	Command string
	Reason  string
}

// TopiclessGoalBlock is `topicless-goal-keepalive.py`'s parsed BLOCK output.
type TopiclessGoalBlock struct {
	// Selected is the script's `selected: <n>` — the population it examined.
	Selected int
	// Actionable is the script's `actionable: <n>` — the rows whose verdict is
	// `resume` or `restart`.
	Actionable int
	Rows       []TopiclessGoalRow
}

// ParseTopiclessGoalBlock reads the BLOCK `topicless-goal-keepalive.py` prints.
//
// Shape (from the script, not a docstring):
//
//	selected: <n>
//	<goal>\t<verdict>\t<command or ->\t<reason>
//	…
//	actionable: <n>
//
// ⚠️ **The wrapper lines are NOT rows.** `selected:` and `actionable:` are
// summaries; a parser that treats every non-empty line as a row would invent two
// goals named "selected:" and "actionable:". A row is identified by its TABS, not
// by its position.
//
// ⚠️ **`REFUSED:` (exit 2) is a refusal, not an empty selection.** The script
// declines to decide when its goals or topics store is unreadable; that is an
// ERROR the tick must surface, never a quiet `none` over an unread fleet.
func ParseTopiclessGoalBlock(ctx context.Context, output string) (TopiclessGoalBlock, error) {
	block := TopiclessGoalBlock{}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "REFUSED:"):
			return TopiclessGoalBlock{}, errors.Errorf(ctx, "topicless-goal-keepalive refused: %s", trimmed)
		case strings.HasPrefix(trimmed, "selected:"):
			n, err := parseBlockCount(ctx, trimmed, "selected:")
			if err != nil {
				return TopiclessGoalBlock{}, err
			}
			block.Selected = n
		case strings.HasPrefix(trimmed, "actionable:"):
			n, err := parseBlockCount(ctx, trimmed, "actionable:")
			if err != nil {
				return TopiclessGoalBlock{}, err
			}
			block.Actionable = n
		case strings.Contains(line, "\t"):
			row, err := parseTopiclessGoalRow(ctx, line)
			if err != nil {
				return TopiclessGoalBlock{}, err
			}
			block.Rows = append(block.Rows, row)
		default:
			// A non-tabbed, non-summary line is neither a row nor a summary — the
			// script emits none, so it is a shape change worth failing on rather
			// than silently skipping.
			return TopiclessGoalBlock{}, errors.Errorf(ctx, "topicless-goal block has an unexpected line: %q", trimmed)
		}
	}
	return block, nil
}

// parseBlockCount reads the integer after a `<key>: <n>` summary line.
func parseBlockCount(ctx context.Context, line, key string) (int, error) {
	value := strings.TrimSpace(strings.TrimPrefix(line, key))
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "%s value %q is not an integer", key, value)
	}
	return n, nil
}

// parseTopiclessGoalRow splits one tab-separated row into its four fields.
func parseTopiclessGoalRow(ctx context.Context, line string) (TopiclessGoalRow, error) {
	// SplitN with 4 keeps any TAB inside the reason intact (the reason is free
	// text); only the first three TABS separate the fields.
	fields := strings.SplitN(line, "\t", 4)
	if len(fields) != 4 {
		return TopiclessGoalRow{}, errors.Errorf(
			ctx, "topicless-goal row has %d tab-separated fields, want 4: %q", len(fields), line,
		)
	}
	return TopiclessGoalRow{
		Goal:    fields[0],
		Verdict: fields[1],
		Command: fields[2],
		Reason:  fields[3],
	}, nil
}

// FirstActionable returns the first row whose verdict licenses an act
// (`resume` / `restart`) as a decision the topicless procedure can run.
//
// ⚠️ **The decision carries no SessionID** — the BLOCK's `resume` row names the
// goal, not the session. The caller reads the goal page's `claude_session_id` and
// stamps it before the procedure claims and acts; a resume with an empty id is
// refused by the act seam, which is the correct failure for a page that records
// no session.
func (b TopiclessGoalBlock) FirstActionable(vault string) (LapsedManagerDecision, bool) {
	for _, row := range b.Rows {
		if row.Verdict == "resume" || row.Verdict == "restart" {
			return LapsedManagerDecision{
				Verdict: row.Verdict,
				Vault:   vault,
				Name:    row.Goal,
				Branch:  "goal",
			}, true
		}
	}
	return LapsedManagerDecision{}, false
}
