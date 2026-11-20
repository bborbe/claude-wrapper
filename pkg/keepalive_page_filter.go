// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// KeepalivePage is a raw enumerated page with the frontmatter fields the lister
// needs: its vault, page name, the recorded `claude_session_id`, and its `status`.
type KeepalivePage struct {
	Vault     string
	Name      string
	SessionID string
	Status    string
}

// The two page kinds a lister enumerates.
const (
	// KeepaliveKindTopic is the topic lister (row 1) — topic pages have no status
	// vocabulary, so only the session requirement applies.
	KeepaliveKindTopic = "topic"
	// KeepaliveKindTask is the task lister (row 6) — a task page must also be
	// `status: in_progress` to be a live worker.
	KeepaliveKindTask = "task"
)

// FilterCandidates keeps the ELIGIBLE pages for one kind, in input order, ready for
// RotatePages.
//
// ⚠️ **Filter to the LIVE population.** A page with NO `claude_session_id` is
// dropped — there is nothing to resume and no `id=<sid>` to log. A TASK page
// (kind `task`) must ALSO be `status: in_progress`: a completed or aborted page
// keeps its session id but is not a worker to keep alive, and would otherwise
// consume the per-tick cap and starve the live pages behind it. A TOPIC page
// (kind `topic`) has no status vocabulary, so only the session requirement applies.
func FilterCandidates(pages []KeepalivePage, kind string) []KeepalivePageRef {
	refs := make([]KeepalivePageRef, 0, len(pages))
	for _, page := range pages {
		if page.SessionID == "" {
			continue
		}
		if kind == KeepaliveKindTask && page.Status != "in_progress" {
			continue
		}
		refs = append(refs, KeepalivePageRef{Vault: page.Vault, Name: page.Name, SessionID: page.SessionID})
	}
	return refs
}
