// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// OpenCards returns EVERY open gate on the attention feed.
//
// It deliberately does not use ReadFeed: that reader collapses the feed to one
// record per session, so a subject holding three open cards would count as one and
// a subject whose card is not its session's preferred item would count as zero.
// The sources, order and fallback rules are identical to ReadFeed; the only
// difference is that nothing is collapsed.
func OpenCards(ctx context.Context, cfg Config) ([]FeedRecord, error) {
	events := map[string]FeedRecord{}
	for _, rec := range logItems(cfg.FeedDir) {
		if rec.ItemID == "" {
			continue
		}
		events[rec.ItemID] = rec
	}
	ordered := sortedEvents(events)
	out := make([]FeedRecord, 0, len(ordered))
	covered := map[string]bool{}

	items, ok := storeItems(ctx, cfg.StoreURL)
	if ok {
		for _, item := range items {
			rec, keep := normalizeStoreItem(item, events)
			if !keep {
				continue
			}
			out = append(out, rec)
			covered[rec.SessionID] = true
		}
		for _, rec := range ordered {
			if rec.Kind == "idle" {
				out = append(out, rec)
			}
		}
	} else {
		for _, rec := range ordered {
			rec.Replayed = true
			out = append(out, rec)
			covered[rec.SessionID] = true
		}
	}

	out = append(out, legacyNeedCards(cfg.FeedDir, covered)...)
	return openGateRecords(out), nil
}

// sortedEvents returns the event-log records in a stable item-id order.
func sortedEvents(events map[string]FeedRecord) []FeedRecord {
	ids := make([]string, 0, len(events))
	for id := range events {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]FeedRecord, 0, len(ids))
	for _, id := range ids {
		out = append(out, events[id])
	}
	return out
}

// legacyNeedCards appends `<sid>.needs.json` for sessions the primary did not
// cover, giving each the identity `needs:<sid>`.
//
// The prefix keeps the synthetic id from colliding with a real item id and from
// matching a subject slug in CardsForSubject's `:`-segment test.
func legacyNeedCards(feedDir string, covered map[string]bool) []FeedRecord {
	entries, err := os.ReadDir(feedDir)
	if err != nil {
		return nil
	}
	out := make([]FeedRecord, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".needs.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(feedDir, entry.Name()))
		if err != nil {
			continue
		}
		var rec FeedRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue
		}
		if rec.SessionID == "" || covered[rec.SessionID] {
			continue
		}
		rec.ItemID = "needs:" + rec.SessionID
		out = append(out, rec)
		covered[rec.SessionID] = true
	}
	return out
}

// openGateRecords keeps only records IsOpenGate accepts.
func openGateRecords(records []FeedRecord) []FeedRecord {
	out := make([]FeedRecord, 0, len(records))
	for _, rec := range records {
		if IsOpenGate(rec) {
			out = append(out, rec)
		}
	}
	return out
}

// ManagerSubject returns the subject a producer session manages.
//
// Absent, unreadable or malformed -> ("", false), never a guess: a wrong
// attribution either suppresses a card that is owed or raises one that is not.
func ManagerSubject(ctx context.Context, cfg Config, producerSessionID string) (string, bool) {
	if !safePathComponent(producerSessionID) {
		return "", false
	}
	raw, err := os.ReadFile(filepath.Join(cfg.WorkerManagerDir, producerSessionID+".json"))
	if err != nil {
		return "", false
	}
	var record struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return "", false
	}
	if record.Subject == "" {
		return "", false
	}
	return record.Subject, true
}

// CardsForSubject returns the cards attributable to a subject, by both signals.
//
// A card is attributable when its producer session is registered as this
// subject's manager, or when its ItemID carries the subject's slug as one of its
// `:`-separated segments. Either signal alone fails one of the pinned cases.
func CardsForSubject(
	ctx context.Context,
	cfg Config,
	subject string,
	cards []FeedRecord,
) []FeedRecord {
	want, err := Slug(ctx, subject)
	if err != nil {
		want = ""
	}
	out := make([]FeedRecord, 0)
	for _, rec := range cards {
		if managed, ok := ManagerSubject(ctx, cfg, rec.SessionID); ok && managed == subject {
			out = append(out, rec)
			continue
		}
		if want != "" && containsSegment(rec.ItemID, want) {
			out = append(out, rec)
		}
	}
	return out
}

// containsSegment reports whether slug is one `:`-separated segment of itemID.
func containsSegment(itemID, slug string) bool {
	for _, segment := range strings.Split(itemID, ":") {
		if segment == slug {
			return true
		}
	}
	return false
}

// SubjectHasManager reports whether any manager registration names this subject.
//
// Declared ownership counts; the registration need not belong to a live session.
// An absent or unreadable registry is false: emit only when an owner exists.
func SubjectHasManager(ctx context.Context, cfg Config, subject string) bool {
	want := strings.TrimSpace(subject)
	if want == "" {
		return false
	}
	entries, err := os.ReadDir(cfg.WorkerManagerDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		sessionID := strings.TrimSuffix(entry.Name(), ".json")
		if managed, ok := ManagerSubject(ctx, cfg, sessionID); ok && managed == want {
			return true
		}
	}
	return false
}

// CachedVerdicts reads the audit verdict cached for each of a subject's rows.
//
// An absent or unreadable cache yields an empty map and a nil error: a missing
// cache must never turn every row into "blocked on the operator".
func CachedVerdicts(
	ctx context.Context,
	cfg Config,
	subject string,
) (map[string]string, error) {
	out := map[string]string{}
	slug, err := Slug(ctx, subject)
	if err != nil {
		return out, nil
	}
	raw, err := os.ReadFile(filepath.Join(cfg.PredispatchDir, slug+".verdicts.json"))
	if err != nil {
		return out, nil
	}
	var cache map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cache); err != nil {
		return out, nil
	}
	for name, entry := range cache {
		var value struct {
			Verdict string `json:"verdict"`
		}
		if err := json.Unmarshal(entry, &value); err != nil {
			continue
		}
		out[name] = value.Verdict
	}
	return out, nil
}

// safePathComponent refuses a file-name component that could escape its directory.
func safePathComponent(value string) bool {
	if value == "" || strings.Contains(value, "..") {
		return false
	}
	return !strings.ContainsAny(value, `/\`)
}
