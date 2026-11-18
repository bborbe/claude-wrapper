// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bborbe/errors"
)

// Snapshot is one subject resolved to its tracked set.
type Snapshot struct {
	Branch  string
	Members []string
	Tasks   []Task
}

// Snapshotter resolves a subject into its tracked set.
type Snapshotter interface {
	Snapshot(ctx context.Context, subject string) (Snapshot, error)
}

// NewVaultSnapshotter composes subject resolution and tracked-set resolution.
func NewVaultSnapshotter(cfg Config) Snapshotter {
	return &vaultSnapshotter{cfg: cfg}
}

type vaultSnapshotter struct {
	cfg Config
}

// Snapshot resolves one subject against the configured vault.
func (s *vaultSnapshotter) Snapshot(ctx context.Context, subject string) (Snapshot, error) {
	branch, members, err := ResolveSubject(ctx, s.cfg.Vault, subject)
	if err != nil {
		return Snapshot{}, err
	}
	tasks, err := ResolveTracked(ctx, s.cfg.Vault, members)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Branch: branch, Members: members, Tasks: tasks}, nil
}

// ResolveSubject resolves a subject to its branch and members.
//
// A goal resolves by folder alone. A topic additionally requires `page_type:
// topic`. Both-match and no-match are refusals, never a silent preference.
func ResolveSubject(ctx context.Context, vault, subject string) (
	branch string, members []string, err error,
) {
	goalPath := filepath.Join(vault, "24 Goals", subject+".md")
	topicPath := filepath.Join(vault, "23 Topics", subject+".md")
	isGoal := fileExists(goalPath)
	isTopic := fileExists(topicPath) && pageTypeOf(topicPath) == "topic"
	if isGoal && isTopic {
		return "", nil, errors.Errorf(ctx,
			"ambiguous subject: both %s and %s resolve", goalPath, topicPath)
	}
	if isGoal {
		return "goal", []string{subject}, nil
	}
	if isTopic {
		members, err := DeclaredMembers(topicPath)
		if err != nil {
			return "", nil, err
		}
		return "topic", members, nil
	}
	return "", nil, errors.Errorf(ctx,
		"no subject page for %q in 24 Goals/ or 23 Topics/", subject)
}

// ResolveTracked resolves the members to the tracked task set.
//
// A member is a goal (admits tasks whose `goals:` name it) or a task name declared
// directly. Results are sorted by name. An unreadable `25 Tasks/` yields an empty
// set with a nil error.
func ResolveTracked(ctx context.Context, vault string, members []string) ([]Task, error) {
	tasksDir := filepath.Join(vault, "25 Tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return []Task{}, nil
	}
	memberSet := make(map[string]bool, len(members))
	for _, member := range members {
		memberSet[member] = true
	}
	tracked := make([]Task, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		task, ok, err := ReadTask(ctx, filepath.Join(tasksDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if memberSet[task.Name] || intersects(memberSet, task.Goals) {
			tracked = append(tracked, task)
		}
	}
	sort.Slice(tracked, func(i, j int) bool { return tracked[i].Name < tracked[j].Name })
	return tracked, nil
}

// pageTypeOf returns the page's `page_type:` frontmatter value, or "".
func pageTypeOf(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	fm, ok := SplitFrontmatter(string(raw))
	if !ok {
		return ""
	}
	return FMScalar(fm, "page_type")
}

// fileExists reports whether a path exists, treating any error as absent.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// intersects reports whether any value is in the set.
func intersects(set map[string]bool, values []string) bool {
	for _, value := range values {
		if set[value] {
			return true
		}
	}
	return false
}
