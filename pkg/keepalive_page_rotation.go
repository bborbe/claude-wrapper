// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "sort"

// KeepalivePageRef is one page the lister enumerated: its vault, page name, and
// the recorded session id (the claim and act need it; a ref without it cannot be
// acted on).
type KeepalivePageRef struct {
	Vault     string
	Name      string
	SessionID string
}

// RotatePages selects up to perVaultCap pages PER VAULT from `all`, starting just
// after each vault's cursor and WRAPPING at the end, and returns the next cursor
// per vault.
//
// ⚠️ **A fixed prefix starves later pages.** Taking the first N every tick would
// re-examine the same N and never reach the rest of the population, so the tick
// would keep a fraction of the fleet alive and report clean forever. This rotates:
// each tick resumes after the last page it examined, so the whole population is
// covered over successive ticks.
//
// Semantics (pinned):
//   - per vault, `all` is sorted by page NAME and the scan starts at the first name
//     STRICTLY GREATER than the cursor (a cursor naming a now-gone page resumes at
//     the next name, not the start);
//   - the scan WRAPS to the start when it runs past the end;
//   - the next cursor is the LAST page SELECTED for that vault (a skipped page —
//     no session, not in_progress — is filtered OUT before this call, so it never
//     occupies a slot; the caller decides which refs are candidates);
//   - a vault with no cursor starts at its first name.
//
// `perVaultCap <= 0` selects nothing (returns the cursor unchanged) rather than
// meaning "unlimited" — an accidental zero must not examine the whole fleet.
func RotatePages(
	cursor map[string]string,
	all []KeepalivePageRef,
	perVaultCap int,
) ([]KeepalivePageRef, map[string]string) {
	next := make(map[string]string, len(cursor))
	for vault, name := range cursor {
		next[vault] = name
	}
	if perVaultCap <= 0 {
		return nil, next
	}
	byVault := map[string][]KeepalivePageRef{}
	for _, ref := range all {
		byVault[ref.Vault] = append(byVault[ref.Vault], ref)
	}
	selected := make([]KeepalivePageRef, 0, len(byVault)*perVaultCap)
	for vault, refs := range byVault {
		sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
		start := rotationStart(refs, cursor[vault])
		lastSelected := ""
		for i := 0; i < perVaultCap && i < len(refs); i++ {
			ref := refs[(start+i)%len(refs)]
			selected = append(selected, ref)
			lastSelected = ref.Name
		}
		if lastSelected != "" {
			next[vault] = lastSelected
		}
	}
	// Order the vaults deterministically so the log is reproducible, but keep the
	// WITHIN-vault rotation order intact — a stable sort by vault alone preserves the
	// d,a,b wrap order; sorting by name would undo the rotation.
	sort.SliceStable(selected, func(i, j int) bool {
		return selected[i].Vault < selected[j].Vault
	})
	return selected, next
}

// rotationStart returns the index in the sorted `refs` at which to begin, given
// the cursor: the first ref whose name is STRICTLY GREATER than it, wrapping to 0
// when the cursor is empty or no name is greater.
func rotationStart(refs []KeepalivePageRef, cursor string) int {
	if cursor == "" {
		return 0
	}
	for i, ref := range refs {
		if ref.Name > cursor {
			return i
		}
	}
	return 0
}
