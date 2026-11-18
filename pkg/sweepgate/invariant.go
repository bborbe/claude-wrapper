// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/bborbe/errors"
)

// operatorBuckets are the bucket KEYS whose next move is the operator's.
var operatorBuckets = map[string]bool{"waiting": true, "approval": true}

// operatorVerdicts are the cached audit verdicts that mean "the operator's move".
var operatorVerdicts = map[string]bool{"needs-you": true, "below-bar": true}

// recordedAtFormat is the local-time stamp the state and settled records carry.
const recordedAtFormat = "2006-01-02T15:04:05-07:00"

// invariantCard is the card payload the invariant posts.
type invariantCard struct {
	DedupKey  string   `json:"dedup_key"`
	Payload   string   `json:"payload"`
	Context   string   `json:"context"`
	Options   []string `json:"options"`
	Recommend string   `json:"recommend"`
}

// nonTerminalRows returns every row whose bucket key is not `done`.
func nonTerminalRows(now time.Time, holds map[string]Hold, tasks []Task) []Task {
	rows := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if _, key := Bucket(now, holds, task); key != "done" {
			rows = append(rows, task)
		}
	}
	return rows
}

// SubjectBlockedOnOperator reports whether EVERY non-terminal row's next move is
// the operator's.
//
// Three ways a row qualifies: its bucket key is waiting/approval, it carries the
// HELD overlay (a label change that keeps its ordinary key), or its cached audit
// verdict is needs-you/below-bar. Any row that is none of those makes the subject
// not operator-blocked — the control that stops this firing on every subject with
// nothing in flight.
func SubjectBlockedOnOperator(
	ctx context.Context,
	cfg Config,
	prepared Prepared,
	subject string,
) bool {
	now := cfg.Now.Now().Time()
	rows := nonTerminalRows(now, prepared.Holds, prepared.Tasks)
	if len(rows) == 0 {
		return false
	}
	verdicts, _ := CachedVerdicts(ctx, cfg, subject)
	for _, task := range rows {
		_, key := Bucket(now, prepared.Holds, task)
		if operatorBuckets[key] {
			continue
		}
		if _, held := HeldLabel(now, prepared.Holds, task.Session); held {
			continue
		}
		if operatorVerdicts[verdicts[task.Name]] {
			continue
		}
		return false
	}
	return true
}

// RowSetKey is the identity of the ask: every non-terminal row and how it reads.
//
// It is order-free, so a reorder is not a change, while a row joining, leaving or
// changing bucket/held/verdict IS one. Set-change, never a tick count.
func RowSetKey(ctx context.Context, cfg Config, prepared Prepared, subject string) string {
	now := cfg.Now.Now().Time()
	verdicts, _ := CachedVerdicts(ctx, cfg, subject)
	rows := make([][]any, 0, len(prepared.Tasks))
	for _, task := range nonTerminalRows(now, prepared.Holds, prepared.Tasks) {
		_, key := Bucket(now, prepared.Holds, task)
		_, held := HeldLabel(now, prepared.Holds, task.Session)
		rows = append(rows, []any{task.Name, key, held, verdicts[task.Name]})
	}
	sort.Slice(rows, func(i, j int) bool { return rowLess(rows[i], rows[j]) })
	blob, err := json.Marshal(rows)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])[:16]
}

// rowLess orders two row tuples element-wise, the way Python sorts lists.
func rowLess(left, right []any) bool {
	for index := 0; index < len(left) && index < len(right); index++ {
		if cmp := compareRowValue(left[index], right[index]); cmp != 0 {
			return cmp < 0
		}
	}
	return len(left) < len(right)
}

// compareRowValue compares two values of a row tuple.
func compareRowValue(left, right any) int {
	leftText, leftIsText := left.(string)
	rightText, rightIsText := right.(string)
	if leftIsText && rightIsText {
		switch {
		case leftText < rightText:
			return -1
		case leftText > rightText:
			return 1
		default:
			return 0
		}
	}
	leftHeld, leftIsHeld := left.(bool)
	rightHeld, rightIsHeld := right.(bool)
	if leftIsHeld && rightIsHeld {
		switch {
		case !leftHeld && rightHeld:
			return -1
		case leftHeld && !rightHeld:
			return 1
		default:
			return 0
		}
	}
	return 0
}

// InvariantPayload builds the card payload when the invariant fires.
//
// The conjunction is, in order: no attributable open card, an owner exists, every
// non-terminal row is the operator's move, and the settled ask's row set has
// changed. stderr is a required parameter — the function never writes to the
// process's stderr itself.
func InvariantPayload(
	ctx context.Context,
	cfg Config,
	prepared Prepared,
	stderr io.Writer,
) ([]byte, bool, error) {
	cards, err := OpenCards(ctx, cfg)
	if err != nil {
		return nil, false, errors.Wrap(ctx, err, "read open cards")
	}
	if len(CardsForSubject(ctx, cfg, cfg.Subject, cards)) > 0 {
		return nil, false, nil
	}
	if !SubjectHasManager(ctx, cfg, cfg.Subject) {
		fmt.Fprintf(stderr,
			"skip %s: resolves no owner — no manager registration names it\n", cfg.Subject)
		return nil, false, nil
	}
	if !SubjectBlockedOnOperator(ctx, cfg, prepared, cfg.Subject) {
		return nil, false, nil
	}
	rowKey := RowSetKey(ctx, cfg, prepared, cfg.Subject)
	settledPath, err := SettledPath(ctx, cfg, cfg.Subject)
	if err != nil {
		return nil, false, errors.Wrapf(ctx, err, "resolve settled path for %s", cfg.Subject)
	}
	settled, err := LoadSettled(ctx, settledPath)
	if err != nil {
		return nil, false, errors.Wrapf(ctx, err, "load settled record for %s", cfg.Subject)
	}
	if settled.RowKey == rowKey {
		fmt.Fprintf(stderr,
			"suppress %s: settled ask unchanged — already answered\n", cfg.Subject)
		return nil, false, nil
	}
	payload, err := marshalInvariantCard(ctx, cfg, prepared)
	if err != nil {
		return nil, false, err
	}
	if err := SaveSettled(ctx, settledPath, Settled{
		Subject:    cfg.Subject,
		RowKey:     rowKey,
		RecordedAt: cfg.Now.Now().Time().Format(recordedAtFormat),
	}); err != nil {
		return nil, false, errors.Wrapf(ctx, err, "save settled record for %s", cfg.Subject)
	}
	return payload, true, nil
}

// marshalInvariantCard builds and encodes the card payload.
func marshalInvariantCard(
	ctx context.Context,
	cfg Config,
	prepared Prepared,
) ([]byte, error) {
	slug, err := Slug(ctx, cfg.Subject)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "slug subject %s", cfg.Subject)
	}
	now := cfg.Now.Now().Time()
	count := len(nonTerminalRows(now, prepared.Holds, prepared.Tasks))
	card := invariantCard{
		DedupKey: "open-card-invariant:" + slug,
		Payload:  cfg.Subject + " is blocked on you, and the board carries no card for it.",
		Context: fmt.Sprintf(
			"All %d non-terminal row(s) of %s are waiting on the operator, and the "+
				"attention board holds no open card attributable to it — so the "+
				"subject reports itself operator-blocked with nothing for the operator "+
				"to answer.",
			count, cfg.Subject,
		),
		Options: []string{
			"Look at the blocked rows now",
			"Leave it — I know about this",
		},
		Recommend: "Look at the blocked rows now",
	}
	payload, err := json.Marshal(card)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "marshal invariant card for %s", cfg.Subject)
	}
	return payload, nil
}
