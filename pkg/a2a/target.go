// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package a2a carries the A2A spawn targets: the two planes that create a worker
// on an a2a-worker-controller and talk to it afterwards, plus the push channel the
// worker reports back on.
//
// It is a port of claude-supervisor's `server/a2a-*.mjs`, and the wire rules here
// are the ones those modules measured rather than guessed — every one of them
// produced a request that looked correct and was not. Where a rule is surprising,
// the comment says what it repaired.
package a2a

import (
	"context"
	"regexp"

	"github.com/bborbe/errors"
)

// DefaultCluster is the cluster a bare `k8s` resolves to when neither the
// environment nor the config file names one.
//
// It is `nukedev` because that is what the single `k8s` target meant until
// 2026-10-20 — the one controller that existed — so a row spawned before the
// per-cluster split keeps working with no config edit.
const DefaultCluster = "nukedev"

// Kind is the A2A target kind.
type Kind string

const (
	// KindDocker runs one container per task on this Mac.
	KindDocker Kind = "docker"
	// KindK8s runs one pod per task on a cluster's controller.
	KindK8s Kind = "k8s"
)

// Target is a parsed A2A spawn target.
type Target struct {
	Kind    Kind
	Cluster string
}

// String is the canonical target a spawn records: `docker`, or `k8s:<cluster>`.
//
// A bare `k8s` is never stored — the spawn path resolves the default cluster
// first — so a roster row always names where its worker runs.
func (t Target) String() string {
	if t.Kind == KindDocker {
		return string(KindDocker)
	}
	return string(KindK8s) + ":" + t.Cluster
}

// IsZero reports whether the target names nothing.
func (t Target) IsZero() bool { return t.Kind == "" }

// clusterName is the shape a cluster name must have: lowercase, digits and
// hyphens, starting alphanumeric.
//
// It is deliberately NOT normalised. The value is also the config key and the
// roster status, so an uppercase or underscored value REFUSES rather than being
// folded — a silently rewritten target is a worker created somewhere the caller
// did not name.
var clusterName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ParseTarget parses `docker`, `k8s`, or `k8s:<cluster>`.
//
// Anything else refuses, and the refusal names every accepted form rather than a
// default: an unknown target must never fall back to `local`, because a worker
// created somewhere the caller did not ask for is otherwise discovered only by
// noticing it.
func ParseTarget(ctx context.Context, raw string) (Target, error) {
	switch {
	case raw == "":
		return Target{}, errors.Errorf(
			ctx,
			"target is empty, which is not an A2A spawn target — expected "+
				`"docker", "k8s", or "k8s:<cluster>" (e.g. "k8s:nukedev")`,
		)
	case raw == string(KindDocker):
		return Target{Kind: KindDocker}, nil
	case raw == string(KindK8s):
		return Target{Kind: KindK8s}, nil
	case len(raw) > len("k8s:") && raw[:len("k8s:")] == "k8s:":
		cluster := raw[len("k8s:"):]
		if !clusterName.MatchString(cluster) {
			return Target{}, errors.Errorf(
				ctx,
				"%q does not name a cluster — after \"k8s:\" give a lowercase cluster "+
					"name (letters, digits and hyphens, starting with a letter or digit, "+
					`e.g. "k8s:nukedev"). The value is refused rather than normalised, `+
					"because it is also the config key and the roster status",
				raw,
			)
		}
		return Target{Kind: KindK8s, Cluster: cluster}, nil
	case raw == "cluster":
		return Target{}, errors.Errorf(
			ctx,
			"target %q was RETIRED on 2026-10-15 — it named the shared long-lived "+
				"`claude-interactive` pod in nuke dev. `k8s` is the route to a worker in "+
				"that cluster now; `cluster` is refused like any other unknown target",
			raw,
		)
	default:
		return Target{}, errors.Errorf(
			ctx,
			"%q is not an A2A spawn target — expected \"docker\", \"k8s\", or "+
				`"k8s:<cluster>" (e.g. "k8s:nukedev")`,
			raw,
		)
	}
}

// IsTarget reports whether raw names an A2A target of any kind.
func IsTarget(ctx context.Context, raw string) bool {
	_, err := ParseTarget(ctx, raw)
	return err == nil
}

// ClusterEnvSuffix is the environment-variable suffix for a cluster name:
// uppercased, every non-alphanumeric replaced by `_`.
//
// `octopus-dev` becomes `OCTOPUS_DEV`, because an environment variable name
// cannot carry a hyphen. This is the only lossy mapping on this path, which is
// why the config file stays keyed by the cluster name as written.
func ClusterEnvSuffix(cluster string) string {
	out := make([]rune, 0, len(cluster))
	for _, r := range cluster {
		switch {
		case r >= 'a' && r <= 'z':
			out = append(out, r-'a'+'A')
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// SpawnArguments is every argument a spawn call may carry, reduced to the ones
// an A2A target has an opinion about.
type SpawnArguments struct {
	Target      string
	Task        string
	Resume      bool
	Policy      string
	Interactive *bool
	WindowID    string
	Role        string
	Env         map[string]string
	Shipping    bool
}

// RefuseSpawnArguments returns the refusal for an argument an A2A target cannot
// honour, or nil.
//
// REFUSED RATHER THAN DROPPED. The controller creates this worker, so this
// daemon never runs it and never holds a process for it. Anything that names a
// LOCAL process, a local window or a local policy is therefore not something
// this path can honour, and a key accepted and quietly ignored is how a caller
// ends up believing a worker was resumed when a fresh session was opened.
//
// `env` stays refused and is the only one that does: a raw map is an ungoverned
// surface, and nothing here can tell a harmless variable from a credential. The
// sanctioned routes are `mounts` and `secrets`, which the controller validates
// or resolves itself, so no value travels from here.
func RefuseSpawnArguments(ctx context.Context, args SpawnArguments) error {
	target := args.Target
	if len(args.Env) > 0 {
		return errors.Errorf(
			ctx,
			"env is not supported with target %q: the controller creates this worker "+
				"and this daemon never runs it, so there is no local process here to set "+
				"an environment on. To give the worker a FILE or a CREDENTIAL, declare "+
				"`mounts` (a host path the controller checks against its allowlist) or "+
				"`secrets` (a TeamVault key the controller resolves itself) instead",
			target,
		)
	}
	if args.Resume {
		return errors.Errorf(
			ctx,
			"resume is not supported with target %q: this path mints a NEW session id, "+
				"so it cannot continue an existing conversation. To send a further turn to "+
				"a worker that already exists, call `send_agent_message` on that worker",
			target,
		)
	}
	if args.Policy != "" {
		return errors.Errorf(
			ctx,
			"policy is not supported with target %q: the policy layer answers permission "+
				"requests raised on THIS daemon, and a worker the controller created raises "+
				"its gates against its own attention route",
			target,
		)
	}
	if args.Interactive != nil {
		return errors.Errorf(
			ctx,
			"interactive is not supported with target %q: it chooses between a local tab "+
				"and a local headless worker, and this target creates neither",
			target,
		)
	}
	if args.WindowID != "" {
		return errors.Errorf(
			ctx,
			"window_id is not supported with target %q: it names a local WezTerm window, "+
				"and this target opens no tab on this machine",
			target,
		)
	}
	if args.Role != "" {
		return errors.Errorf(
			ctx,
			"role is not supported with target %q: it resolves a colour and a window from "+
				"the LOCAL role map, and this target opens no local tab for either to apply to",
			target,
		)
	}
	if args.Shipping {
		return errors.Errorf(
			ctx,
			"shipping is not supported with target %q: it pre-allows `git add` / `commit` / "+
				"`push` for a worker this daemon supervises, and a worker the controller "+
				"created is supervised there",
			target,
		)
	}
	if args.Task == "" {
		return errors.Errorf(
			ctx,
			"target %q requires `task`: it names the vault task the worker is opened for, "+
				"and it is the name the controller creates one worker for",
			target,
		)
	}
	return nil
}
