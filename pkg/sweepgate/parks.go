// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// stuckSeconds is the runbook's `~30 min` stuck threshold.
	stuckSeconds = 30 * 60
	// liveWindow is who-needs-me.py's LIVE_WINDOW: a transcript written inside
	// five minutes proves the session is working.
	liveWindow = 5 * 60
	// stuckMarkerSeconds is the drive leg's open-tool-marker cap. An older open
	// marker is the stuck path's signal, not proof of work.
	stuckMarkerSeconds = 20 * 60
	// defaultParkSeconds is the fixed park threshold when no cadence was armed.
	defaultParkSeconds = stuckSeconds
)

// OpenBoxCount turns the `n/m` checkbox reading into the number of open boxes.
//
// `—` (no boxes) has no `/` and reads as zero open boxes, not a parse failure.
func OpenBoxCount(met string) int {
	if !strings.Contains(met, "/") {
		return 0
	}
	done, total, found := strings.Cut(met, "/")
	if !found {
		return 0
	}
	doneCount, err := strconv.Atoi(strings.TrimSpace(done))
	if err != nil {
		return 0
	}
	totalCount, err := strconv.Atoi(strings.TrimSpace(total))
	if err != nil {
		return 0
	}
	return totalCount - doneCount
}

// IdleStuck is the idle branch of the stuck rule.
//
// Four gates, all required: not parked, `phase: execution`, at least one open
// box, and the task file unchanged for at least 30 minutes.
func IdleStuck(t Task, now time.Time) bool {
	if t.Liveness == LivenessParked {
		return false
	}
	if t.Phase != "execution" {
		return false
	}
	if OpenBoxCount(t.Met) < 1 {
		return false
	}
	if t.Mtime.IsZero() {
		return false
	}
	return now.Sub(t.Mtime) >= stuckSeconds*time.Second
}

// ApplyStuck sets Task.Stuck and returns the busy-since map to persist.
//
// The busy branch observes the clock across ticks: a continuously busy live
// session whose ProgressHash has not moved for 30 minutes is stuck. A Progress
// write resets the clock. A non-busy row takes the idle branch. The branches are
// disjoint.
func ApplyStuck(
	tasks []Task,
	registry map[string]RegistryRecord,
	prev map[string]BusyStamp,
	now time.Time,
) map[string]BusyStamp {
	newBusy := make(map[string]BusyStamp, len(tasks))
	nowSeconds := float64(now.UnixNano()) / float64(time.Second)
	for i := range tasks {
		task := &tasks[i]
		busy := false
		if task.Session != "" {
			if rec, ok := registry[task.Session]; ok {
				busy = rec.Alive && rec.Status == "busy"
			}
		}
		if !busy {
			task.Stuck = IdleStuck(*task, now)
			continue
		}
		stamp, ok := prev[task.Name]
		if ok && stamp.Progress == task.ProgressHash {
			since := stamp.Since
			if since == 0 {
				since = nowSeconds
			}
			task.Stuck = nowSeconds-since >= stuckSeconds
			newBusy[task.Name] = BusyStamp{Since: since, Progress: task.ProgressHash}
			continue
		}
		task.Stuck = false
		newBusy[task.Name] = BusyStamp{Since: nowSeconds, Progress: task.ProgressHash}
	}
	return newBusy
}

// BlockerStatuses returns every `25 Tasks/*.md` file's status, keyed by name.
//
// Vault-wide, not tracked-set-wide: a blocker may sit outside this topic's set,
// and resolving only within the set would read a genuine blocker as missing.
func BlockerStatuses(ctx context.Context, vault string) (map[string]string, error) {
	out := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(vault, "25 Tasks"))
	if err != nil {
		return out, nil
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(vault, "25 Tasks", entry.Name()))
		if err != nil {
			continue
		}
		fm, ok := SplitFrontmatter(string(raw))
		status := ""
		if ok {
			status = FMScalar(fm, "status")
		}
		out[strings.TrimSuffix(entry.Name(), ".md")] = status
	}
	return out, nil
}

// ApplyBlockedUpstream sets BlockedByUnmet and BlockedUpstream.
//
// A row is blocked while AT LEAST ONE named blocker is not completed. A missing,
// unreadable, aborted or status-less blocker counts as unmet. An empty
// `blocked_by` satisfies the clause vacuously, so it never makes a row blocked.
func ApplyBlockedUpstream(tasks []Task, statuses map[string]string) {
	for i := range tasks {
		unmet := make([]string, 0)
		for _, name := range tasks[i].BlockedBy {
			if statuses[name] != "completed" {
				unmet = append(unmet, name)
			}
		}
		tasks[i].BlockedByUnmet = unmet
		tasks[i].BlockedUpstream = len(unmet) > 0
	}
}

// ParkThreshold reads the armed cadence and returns 2x its interval.
//
// The basis is ALWAYS returned and the caller always prints it: a silent default
// is the defect this exists to remove. Without a cadence record the threshold is
// the fixed 1800 seconds and armed is false.
func ParkThreshold(
	cfg Config,
	branch, subject string,
	now time.Time,
) (seconds int, basis string, armed bool) {
	slug, err := Slug(context.Background(), subject)
	if err != nil {
		return defaultParkSeconds,
			"no cadence record for " + subject + "; fixed 1800s", false
	}
	path := filepath.Join(cfg.StateDir, branch+"--"+slug+".cadence")
	raw, err := os.ReadFile(path)
	if err == nil {
		if interval, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil &&
			interval > 0 {
			return 2 * interval,
				"2 x cadence " + strconv.Itoa(interval) + "s (armed) from " + path, true
		}
	}
	return defaultParkSeconds, "no cadence record at " + path + "; fixed 1800s", false
}

// ParkedOnUnregisteredGate reports a live worker parked on a gate the feed never
// registered. All eight conditions are required.
func ParkedOnUnregisteredGate(
	t Task,
	now time.Time,
	parkSeconds int,
	registry map[string]RegistryRecord,
) bool {
	if t.Status == "completed" || t.Status == "aborted" {
		return false
	}
	if t.Phase != "execution" {
		return false
	}
	if OpenBoxCount(t.Met) < 1 {
		return false
	}
	if t.Mtime.IsZero() {
		return false
	}
	if now.Sub(t.Mtime) < time.Duration(parkSeconds)*time.Second {
		return false // working — the file moved inside the threshold
	}
	if t.FeedGate {
		return false // a registered gate — the feed row is the report
	}
	if t.Liveness != LivenessLive {
		return false // no live owner — orphan or stuck, not a park
	}
	if t.InFlight {
		return false // working — the session is mid-tool-call
	}
	return true
}

// ApplyUnregisteredParks sets FeedGate and UnregisteredPark on every row.
//
// It reads t.Session only, never the whole id set, so the feed-derived flags agree
// about whose record they read.
func ApplyUnregisteredParks(
	ctx context.Context,
	cfg Config,
	tasks []Task,
	feed map[string]FeedRecord,
	registry map[string]RegistryRecord,
	now time.Time,
	parkSeconds int,
) {
	for i := range tasks {
		sid := tasks[i].Session
		tasks[i].FeedGate = sid != "" && IsOpenGate(feed[sid])
		tasks[i].InFlight = SessionInFlight(ctx, cfg, sid, registry, now)
		tasks[i].UnregisteredPark = ParkedOnUnregisteredGate(
			tasks[i], now, parkSeconds, registry)
	}
}

// ApplyExternalWaits sets ExternalWait from the row's current owner.
func ApplyExternalWaits(tasks []Task, feed map[string]FeedRecord) {
	for i := range tasks {
		sid := tasks[i].Session
		tasks[i].ExternalWait = sid != "" && IsExternalWait(feed[sid])
	}
}

// ApplyDeclaredWaits sets DeclaredWaitActive from the row's own declared_wait.
//
// A present-but-empty or null-spelled value is not a declaration. The flag is
// RENDER-ONLY and is not a Digest input.
func ApplyDeclaredWaits(tasks []Task) {
	for i := range tasks {
		raw := strings.TrimSpace(tasks[i].DeclaredWait)
		tasks[i].DeclaredWaitActive = raw != "" && !yamlNulls[raw]
	}
}

// SessionInFlight reports whether a session is mid-tool-call.
//
// Three limbs in order: the registry's status, the session's open tool marker, and
// the transcript mtime. False means "not proven in flight", never "dead".
func SessionInFlight(
	ctx context.Context,
	cfg Config,
	sid string,
	registry map[string]RegistryRecord,
	now time.Time,
) bool {
	if sid == "" {
		return false
	}
	if rec, ok := registry[sid]; ok {
		if rec.Status == "shell" {
			return true
		}
		if rec.Status == "waiting" {
			return false
		}
	}
	if markerInFlight(filepath.Join(cfg.ToolMarkerDir, sid+".tool.json"), now) {
		return true
	}
	return transcriptInFlight(cfg.TranscriptDir, sid, now)
}

// markerInFlight reads one `<sid>.tool.json` marker.
func markerInFlight(path string, now time.Time) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var marker struct {
		State string  `json:"state"`
		TS    float64 `json:"ts"`
	}
	if err := json.Unmarshal(raw, &marker); err != nil {
		return false
	}
	if marker.State != "open" {
		return false
	}
	age := now.Sub(time.Unix(int64(marker.TS), 0))
	return age < stuckMarkerSeconds*time.Second
}

// transcriptInFlight checks both transcript shapes against the live window.
func transcriptInFlight(projectsDir, sid string, now time.Time) bool {
	candidates := []string{filepath.Join(projectsDir, sid+".jsonl")}
	if nested, err := filepath.Glob(filepath.Join(projectsDir, "*", sid+".jsonl")); err == nil {
		candidates = append(candidates, nested...)
	}
	best := time.Duration(-1)
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		age := now.Sub(info.ModTime())
		if best < 0 || age < best {
			best = age
		}
	}
	if best < 0 {
		return false
	}
	return best < liveWindow*time.Second
}
