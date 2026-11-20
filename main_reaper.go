// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/claude-wrapper/pkg"
)

// newReaperJob builds the one ReaperJob that replaces IdleReaperJob: the four
// reapers as five adapters (idle-close, terminal-task, stood-down-close,
// manager-reap, drive-reap), each calling existing code unchanged.
func newReaperJob(
	ctx context.Context,
	supervisor pkg.Supervisor,
	registry pkg.SessionRegistry,
	heartbeats pkg.HeartbeatReader,
	vaults pkg.VaultLister,
	now libtime.CurrentDateTimeGetter,
) (*pkg.ReaperJob, error) {
	verdicts := pkg.NewKeepaliveVerdicts(registry, supervisor.List, heartbeats.Rows)
	roleFor := func(ctx context.Context, sessionID string) (string, error) {
		sessions, err := supervisor.List(ctx)
		if err != nil {
			return "", err
		}
		for _, session := range sessions {
			if session.ID == sessionID {
				return session.Role, nil
			}
		}
		return "", nil
	}
	configured, err := vaults.List(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "list vaults failed")
	}
	taskDirs := make([]string, 0, len(configured))
	for _, vault := range configured {
		taskDirs = append(taskDirs, filepath.Join(vault.Path, "25 Tasks"))
	}

	idle := &pkg.IdleReaperJob{Supervisor: supervisor, Now: func() time.Time { return now.Now().Time() }}
	reapers := []pkg.Reaper{
		&pkg.IdleCloseReaper{Job: idle},
		&pkg.TerminalTaskReaper{
			Supervisor: supervisor,
			Resolver:   pkg.NewDirAnchoredTaskResolver(taskDirs),
		},
		newStoodDownReaper(supervisor, configured, verdicts),
		pkg.NewManagerReapReaper(verdicts, roleFor, supervisor),
		pkg.NewDriveReapReaper(verdicts, roleFor, supervisor),
	}
	return &pkg.ReaperJob{
		Reapers: reapers,
		Logf:    func(line string) { log.Printf("%s", line) },
	}, nil
}

// newStoodDownReaper builds the stood-down close, re-reading the marker store.
func newStoodDownReaper(
	supervisor pkg.Supervisor,
	vaults []pkg.Vault,
	verdicts func(context.Context) ([]pkg.LivenessVerdict, error),
) pkg.Reaper {
	candidates := func(ctx context.Context) ([]pkg.StoodDownCandidate, error) {
		out, _, runErr := keepaliveScript("manager-liveness.py")(ctx, "--stood-down-past", "60")
		if runErr != nil {
			return nil, runErr
		}
		return pkg.ParseStoodDownCandidates(ctx, out)
	}
	sessionFor := func(ctx context.Context, base string) (string, error) {
		for _, vault := range vaults {
			for _, kind := range []string{pkg.OpenKindGoal, pkg.OpenKindTopic} {
				page := pkg.ReadOpenPage(ctx, vault, kind, base)
				if page.Found && page.ReadErr == nil {
					return page.Fields["claude_session_id"], nil
				}
			}
		}
		return "", nil
	}
	live := func(ctx context.Context, sessionID string) (bool, error) {
		all, err := verdicts(ctx)
		if err != nil {
			return false, err
		}
		for _, verdict := range all {
			if verdict.SessionID == sessionID {
				return verdict.Liveness == pkg.LivenessLive, nil
			}
		}
		return false, nil
	}
	return &pkg.StoodDownCloseReaper{
		Supervisor: supervisor,
		Candidates: candidates,
		SessionFor: sessionFor,
		Live:       live,
	}
}
