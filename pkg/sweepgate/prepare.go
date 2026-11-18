// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"time"

	"github.com/bborbe/errors"
)

// Prepared is one subject, fully enriched, ready to digest, render or inspect.
type Prepared struct {
	Snapshot    Snapshot
	Tasks       []Task
	Holds       map[string]Hold
	Busy        map[string]BusyStamp
	Digest      string
	ParkBasis   string
	ParkSeconds int
}

// Prepare runs the one pipeline both the tick and the invariant consume.
//
// The order is load-bearing: the digest is built from what the frame renders, so
// a flag resolved after the digest would move the table without moving the change
// signal. ApplyBlockedUpstream runs after ApplyStuck exactly as the reference does,
// with IsWaiting doing the suppression.
func Prepare(ctx context.Context, cfg Config) (Prepared, error) {
	now := cfg.Now.Now().Time()
	branch, members, err := resolveSubjectBounded(ctx, cfg)
	if err != nil {
		return Prepared{}, err
	}
	tasks, err := resolveTrackedBounded(ctx, cfg, members)
	if err != nil {
		return Prepared{}, err
	}
	registry, err := ReadRegistry(ctx, cfg.RegistryDir)
	if err != nil {
		return Prepared{}, errors.Wrap(ctx, err, "read registry")
	}
	feed, err := ReadFeed(ctx, cfg, now)
	if err != nil {
		return Prepared{}, errors.Wrap(ctx, err, "read feed")
	}
	// Read once and carry it: every label reader downstream needs the store and
	// none of them may re-read the file.
	holds, err := ReadHolds(ctx, cfg.HoldsPath)
	if err != nil {
		return Prepared{}, errors.Wrap(ctx, err, "read holds")
	}
	EnrichLiveness(ctx, cfg, tasks, registry, feed, nil)
	parkSeconds, parkBasis, _ := ParkThreshold(cfg, branch, cfg.Subject, now)
	ApplyUnregisteredParks(ctx, cfg, tasks, feed, registry, now, parkSeconds)
	ApplyExternalWaits(tasks, feed)
	ApplyDeclaredWaits(tasks)

	statePath, err := StatePath(ctx, cfg, cfg.Subject)
	if err != nil {
		return Prepared{}, errors.Wrapf(ctx, err, "resolve state path for %s", cfg.Subject)
	}
	state, err := LoadState(ctx, statePath)
	if err != nil {
		return Prepared{}, errors.Wrapf(ctx, err, "load state for %s", cfg.Subject)
	}
	busy := ApplyStuck(tasks, registry, state.BusySince, now)

	statuses, err := blockerStatusesBounded(ctx, cfg)
	if err != nil {
		return Prepared{}, err
	}
	ApplyBlockedUpstream(tasks, statuses)

	return Prepared{
		Snapshot:    Snapshot{Branch: branch, Members: members, Tasks: tasks},
		Tasks:       tasks,
		Holds:       holds,
		Busy:        busy,
		Digest:      Digest(tasks),
		ParkBasis:   parkBasis,
		ParkSeconds: parkSeconds,
	}, nil
}

// readTimeout returns the configured bound, falling back to the package default.
func readTimeout(cfg Config) time.Duration {
	if cfg.ReadTimeout > 0 {
		return cfg.ReadTimeout
	}
	return DefaultReadTimeout
}

// resolveSubjectBounded resolves the subject under the read bound.
func resolveSubjectBounded(ctx context.Context, cfg Config) (string, []string, error) {
	timeout := readTimeout(cfg)
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	branch, members, err := ResolveSubject(readCtx, cfg.Vault, cfg.Subject)
	if err != nil {
		return "", nil, errors.Wrapf(ctx, err,
			"resolve subject %q failed (read timeout %s)", cfg.Subject, timeout)
	}
	return branch, members, nil
}

// resolveTrackedBounded resolves the tracked set under the read bound.
func resolveTrackedBounded(ctx context.Context, cfg Config, members []string) ([]Task, error) {
	timeout := readTimeout(cfg)
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tasks, err := ResolveTracked(readCtx, cfg.Vault, members)
	if err != nil {
		return nil, errors.Wrapf(ctx, err,
			"resolve tracked set failed (read timeout %s)", timeout)
	}
	return tasks, nil
}

// blockerStatusesBounded reads the vault-wide blocker statuses under the read bound.
func blockerStatusesBounded(ctx context.Context, cfg Config) (map[string]string, error) {
	timeout := readTimeout(cfg)
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	statuses, err := BlockerStatuses(readCtx, cfg.Vault)
	if err != nil {
		return nil, errors.Wrapf(ctx, err,
			"read blocker statuses failed (read timeout %s)", timeout)
	}
	return statuses, nil
}
