// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// heartbeatTTLSeconds mirrors the supervisor server's HEARTBEAT_TTL_MS.
	heartbeatTTLSeconds = 60
	// storeTimeout bounds the attention-store HTTP read. It is short: the store
	// is the primary source but an unreachable one must fall through quickly.
	storeTimeout = 3 * time.Second
)

// kindFromMechanism maps a store mechanism to the feed's `kind`.
var kindFromMechanism = map[string]string{
	"permission": "permission",
	"message":    "question",
}

// entityRe matches the `&nbsp;` family of entities the closer panel emits.
var entityRe = regexp.MustCompile(`(?i)&(?:nbsp|#160|#xa0);`)

// RegistryRecord is one session registry entry.
type RegistryRecord struct {
	PID    int
	Status string
	Name   string
	Alive  bool
}

// ReadRegistry reads `<dir>/*.json` into a session-id keyed map.
//
// A malformed record reads as no record, never as a fatal error: an unreadable
// record must never stop a tick.
func ReadRegistry(ctx context.Context, dir string) (map[string]RegistryRecord, error) {
	out := map[string]RegistryRecord{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, nil
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var record struct {
			SessionID string `json:"sessionId"`
			PID       int    `json:"pid"`
			Status    string `json:"status"`
			Name      string `json:"name"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			continue
		}
		if record.SessionID == "" {
			continue
		}
		out[record.SessionID] = RegistryRecord{
			PID:    record.PID,
			Status: record.Status,
			Name:   record.Name,
			Alive:  record.PID > 0 && processAlive(record.PID),
		}
	}
	return out, nil
}

// HeartbeatLive reports whether a session's heartbeat stamp is fresh.
//
// The verdict is the stamp's AGE against the TTL, never the file's existence: a
// killed server never clears its stamps, so existence would report a dead worker
// live. A missing file is (false, true); any other stat error is (false, false),
// which means "could not read", never "not live".
func HeartbeatLive(ctx context.Context, dir, sessionID string, now time.Time) (bool, bool) {
	info, err := os.Stat(filepath.Join(dir, sessionID+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, true
		}
		return false, false
	}
	return now.Sub(info.ModTime()) < heartbeatTTLSeconds*time.Second, true
}

// FeedRecord is one attention-feed record, from any of the three sources.
type FeedRecord struct {
	SessionID string  `json:"session_id"`
	ItemID    string  `json:"item_id"`
	Kind      string  `json:"kind"`
	State     string  `json:"state"`
	Event     string  `json:"event"`
	Detail    string  `json:"detail"`
	Mechanism string  `json:"mechanism"`
	Replayed  bool    `json:"replayed"`
	TS        float64 `json:"ts"`
}

// storeItem is one item the attention store returns.
type storeItem struct {
	ItemID          string  `json:"item_id"`
	DedupKey        string  `json:"dedup_key"`
	ProducerID      string  `json:"producer_id"`
	AnswerMechanism string  `json:"answer_mechanism"`
	Payload         string  `json:"payload"`
	State           string  `json:"state"`
	CreatedAt       float64 `json:"created_at"`
}

// eventLine is one `<sid>.events.jsonl` line.
type eventLine struct {
	Type      string  `json:"type"`
	ItemID    string  `json:"item_id"`
	SessionID string  `json:"session_id"`
	Kind      string  `json:"kind"`
	Event     string  `json:"event"`
	Detail    string  `json:"detail"`
	TS        float64 `json:"ts"`
}

// ReadFeed reads the feed from the reference's three sources, in its order.
//
// The attention store is primary and WINS when it answers; `<sid>.events.jsonl` is
// the fallback, every record stamped Replayed; `<sid>.needs.json` is merged LAST
// and only for sessions the primary did not cover. An unreachable store is not an
// error: it falls through to the event log.
func ReadFeed(ctx context.Context, cfg Config, now time.Time) (map[string]FeedRecord, error) {
	events := logItems(cfg.FeedDir)
	byItem := make(map[string]FeedRecord, len(events))
	for _, rec := range events {
		byItem[rec.ItemID] = rec
	}
	out := map[string]FeedRecord{}
	covered := map[string]bool{}

	items, ok := storeItems(ctx, cfg.StoreURL)
	if ok {
		for _, item := range items {
			rec, keep := normalizeStoreItem(item, byItem)
			if !keep {
				continue
			}
			keepRecord(out, rec)
			covered[rec.SessionID] = true
		}
		for _, rec := range events {
			if rec.Kind == "idle" {
				keepRecord(out, rec)
			}
		}
	} else {
		for _, rec := range events {
			rec.Replayed = true
			keepRecord(out, rec)
			covered[rec.SessionID] = true
		}
	}

	mergeLegacyNeeds(cfg.FeedDir, out, covered)
	return out, nil
}

// storeItems reads the attention store. Only a 200 is a read; every failure is
// "unreachable", which the caller turns into the event-log fallback.
func storeItems(ctx context.Context, storeURL string) ([]storeItem, bool) {
	if storeURL == "" {
		return nil, false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(storeURL, "/")+"/api/1.0/attention", nil)
	if err != nil {
		return nil, false
	}
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: storeTimeout}
	resp, err := client.Do(request)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var items []storeItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, false
	}
	return items, true
}

// normalizeStoreItem turns one store item into the record shape is_open_gate
// reads. It returns false for an item whose mechanism has no kind.
func normalizeStoreItem(item storeItem, events map[string]FeedRecord) (FeedRecord, bool) {
	kind, ok := kindFromMechanism[item.AnswerMechanism]
	if !ok {
		return FeedRecord{}, false
	}
	itemID := item.DedupKey
	if itemID == "" {
		itemID = item.ItemID
	}
	rec := FeedRecord{
		ItemID:    itemID,
		SessionID: item.ProducerID,
		Kind:      kind,
		Detail:    item.Payload,
		State:     "answered",
		Mechanism: item.AnswerMechanism,
		TS:        item.CreatedAt,
	}
	if item.State == "open" {
		rec.State = "open"
	}
	if event, ok := events[itemID]; ok {
		rec.Event = event.Event
	}
	return rec, true
}

// logItems folds every `<sid>.events.jsonl` into its currently-open items.
//
// An item is open when its item_id has an `open` line and no matching `close`.
// A malformed line is skipped, and only bytes up to the last newline are consumed.
func logItems(feedDir string) []FeedRecord {
	entries, err := os.ReadDir(feedDir)
	if err != nil {
		return []FeedRecord{}
	}
	out := make([]FeedRecord, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".events.jsonl") {
			continue
		}
		out = append(out, foldEventLog(filepath.Join(feedDir, entry.Name()))...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ItemID < out[j].ItemID })
	return out
}

// foldEventLog folds one append-only event log into its open items.
func foldEventLog(path string) []FeedRecord {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	cut := strings.LastIndexByte(string(raw), '\n')
	if cut < 0 {
		return nil
	}
	opened := map[string]FeedRecord{}
	closed := map[string]bool{}
	for _, line := range strings.Split(string(raw[:cut+1]), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event eventLine
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.ItemID == "" {
			continue
		}
		if event.Type == "open" {
			opened[event.ItemID] = FeedRecord{
				SessionID: event.SessionID,
				ItemID:    event.ItemID,
				Kind:      event.Kind,
				Event:     event.Event,
				Detail:    event.Detail,
				TS:        event.TS,
			}
			continue
		}
		closed[event.ItemID] = true
	}
	ids := make([]string, 0, len(opened))
	for id := range opened {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]FeedRecord, 0, len(ids))
	for _, id := range ids {
		rec := opened[id]
		rec.State = "open"
		if closed[id] {
			rec.State = "answered"
		}
		out = append(out, rec)
	}
	return out
}

// mergeLegacyNeeds merges the legacy `<sid>.needs.json` snapshot for sessions the
// primary source did not cover.
func mergeLegacyNeeds(feedDir string, out map[string]FeedRecord, covered map[string]bool) {
	entries, err := os.ReadDir(feedDir)
	if err != nil {
		return
	}
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
		keepRecord(out, rec)
	}
}

// keepRecord keeps one record per session, preferring a raised gate.
func keepRecord(out map[string]FeedRecord, rec FeedRecord) {
	if rec.SessionID == "" {
		return
	}
	prev, ok := out[rec.SessionID]
	if !ok || (!IsOpenGate(prev) && IsOpenGate(rec)) {
		out[rec.SessionID] = rec
	}
}

// IsOpenGate reports whether a record is a raised gate the operator has not
// answered. `state: open` alone is not a park: the dominant idle record is not a
// gate.
func IsOpenGate(rec FeedRecord) bool {
	if rec.State == "answered" {
		return false
	}
	if rec.Kind != "permission" && rec.Kind != "question" {
		return false
	}
	// A rendered closer panel is not a parked gate.
	if rec.Event == "Stop" {
		return false
	}
	return !strings.HasPrefix(normalizeDetail(rec.Detail), "later (on ")
}

// normalizeDetail resolves the `&nbsp;` family and collapses whitespace.
func normalizeDetail(detail string) string {
	return strings.Join(strings.Fields(entityRe.ReplaceAllString(detail, " ")), " ")
}

// IsExternalWait reports a stopped worker that declared a machine-watched wait.
func IsExternalWait(rec FeedRecord) bool {
	if rec.Event != "Stop" {
		return false
	}
	return strings.Contains(rec.Detail, "⏰ Ends:")
}

// HeartbeatFunc supplies one session's heartbeat verdict. A nil func means the
// caller has no heartbeat store to read.
type HeartbeatFunc func(sessionID string) (live bool, readable bool)

// LivenessOf returns live / parked / none for one session id.
//
// The registry answers for interactive sessions; the heartbeat answers for the
// headless workers the registry cannot see.
func LivenessOf(
	sid string,
	registry map[string]RegistryRecord,
	feed map[string]FeedRecord,
	heartbeat HeartbeatFunc,
) string {
	if sid == "" {
		return LivenessNone
	}
	if rec, ok := registry[sid]; ok && rec.Alive {
		if IsOpenGate(feed[sid]) || rec.Status == "waiting" {
			return LivenessParked
		}
		return LivenessLive
	}
	live := false
	if heartbeat != nil {
		live, _ = heartbeat(sid)
	}
	if !live {
		return LivenessNone
	}
	if IsOpenGate(feed[sid]) {
		return LivenessParked
	}
	return LivenessLive
}

// LivenessOfSet returns the row's verdict over its whole id set.
//
// Precedence is live > parked > none, so a dead `claude_session_id` cannot mask a
// live `metrics_sessions` id.
func LivenessOfSet(
	ids []string,
	registry map[string]RegistryRecord,
	feed map[string]FeedRecord,
	heartbeat HeartbeatFunc,
) string {
	sawParked := false
	for _, sid := range ids {
		switch LivenessOf(sid, registry, feed, heartbeat) {
		case LivenessLive:
			return LivenessLive
		case LivenessParked:
			sawParked = true
		}
	}
	if sawParked {
		return LivenessParked
	}
	return LivenessNone
}

// EnrichLiveness sets Task.Liveness from the whole id set.
func EnrichLiveness(
	ctx context.Context,
	cfg Config,
	tasks []Task,
	registry map[string]RegistryRecord,
	feed map[string]FeedRecord,
	heartbeat HeartbeatFunc,
) {
	if heartbeat == nil {
		now := cfg.Now.Now().Time()
		heartbeat = func(sid string) (bool, bool) {
			return HeartbeatLive(ctx, cfg.HeartbeatDir, sid, now)
		}
	}
	for i := range tasks {
		ids := tasks[i].SessionIDs
		if len(ids) == 0 && tasks[i].Session != "" {
			ids = []string{tasks[i].Session}
		}
		tasks[i].Liveness = LivenessOfSet(ids, registry, feed, heartbeat)
	}
}
