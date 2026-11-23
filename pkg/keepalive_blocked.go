// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
)

// NewVaultAPIBlockers answers a task's UNRESOLVED `blocked_by` entries from
// vault-api, using the same rule the flagged tick applies.
//
// ⚠️ **The rule is not restated here.** `openBlockersOf` is the single home of
// "is this blocker still open", and a second copy in the keep-alive leg would
// drift from it — the tick would skip a row the keep-alive resumed, or the other
// way round. A blocker counts as resolved only when the index reports it
// `completed`; "cannot verify it is done" stays blocked.
//
// ⚠️ **A row the index does not carry answers no blockers.** It cannot be proven
// gated, and the resume act reports `missing-row` if the page is genuinely gone.
// Answering "blocked" there would suppress a row on a read failure, which is the
// opposite of the fail-safe the tick applies.
func NewVaultAPIBlockers(
	client VaultAPIClient,
) func(ctx context.Context, task string) ([]string, error) {
	return func(ctx context.Context, task string) ([]string, error) {
		if client == nil {
			return nil, errors.New(ctx, "blocker read has no vault-api client")
		}
		items, err := client.ListAll(ctx, "task")
		if err != nil {
			return nil, errors.Wrap(ctx, err, "list tasks failed")
		}
		want := strings.ToLower(strings.TrimSpace(task))
		statuses := make(map[string]string, len(items))
		var vault string
		var blockedBy []string
		found := false
		for _, item := range items {
			statuses[vaultSubjectKey(item.Vault, "task", strings.ToLower(item.Title))] = item.Status
			if !found && strings.ToLower(item.Title) == want {
				vault = item.Vault
				blockedBy = item.BlockedBy
				found = true
			}
		}
		if !found {
			return nil, nil
		}
		open := openBlockersOf(blockedBy, vault, "task", statuses)
		names := make([]string, 0, len(open))
		for _, blocker := range open {
			names = append(names, wikilinkTitle(blocker))
		}
		return names, nil
	}
}
