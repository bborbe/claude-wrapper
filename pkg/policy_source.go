// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/bborbe/errors"
)

// bundledPolicyJSON is the shipped default rule set.
//
// It is embedded rather than read from the supervisor plugin directory on
// purpose: this process is the one that decides now, so the defaults it applies
// must be the ones it ships. Reading a sibling plugin's file would make the
// wrapper's decisions depend on another package's version.
//
//go:embed policy.json
var bundledPolicyJSON []byte

// UserPolicyEnv is the environment variable naming the user's overlay file.
//
// The name is the supervisor's, deliberately: the overlay already exists on
// every machine that ran the node server, and an operator must not have to move
// it for the deciding process to move.
const UserPolicyEnv = "SUPERVISOR_POLICY"

// DefaultUserPolicyPath is where the user overlay lives when the env var is
// unset.
const DefaultUserPolicyPath = "~/.config/claude-supervisor/policy.json"

// PolicyFile is the on-disk shape of a rules file.
type PolicyFile struct {
	Version int    `json:"version"`
	Rules   []Rule `json:"rules"`
}

// UserPolicyPath is the overlay path this process would read.
func UserPolicyPath() string {
	if path := os.Getenv(UserPolicyEnv); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "claude-supervisor", "policy.json")
}

// BundledRules returns the shipped default rule set.
func BundledRules(ctx context.Context) ([]Rule, error) {
	rules, err := parsePolicyFile(ctx, bundledPolicyJSON, "bundled policy")
	if err != nil {
		return nil, err
	}
	return rules, nil
}

// LoadPolicyFile reads one rules file from disk.
//
// A file that cannot be read is an ERROR, never "no rules": a typo in a
// per-spawn path must not quietly become an empty overlay, because an empty
// overlay is indistinguishable from a policy the operator never wrote.
func LoadPolicyFile(ctx context.Context, path string) ([]Rule, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read policy file %q failed", path)
	}
	return parsePolicyFile(ctx, content, path)
}

// parsePolicyFile decodes one rules file and normalizes its rules.
func parsePolicyFile(ctx context.Context, content []byte, name string) ([]Rule, error) {
	var file PolicyFile
	if err := json.Unmarshal(content, &file); err != nil {
		return nil, errors.Wrapf(ctx, err, "parse policy file %q failed", name)
	}
	rules := make([]Rule, 0, len(file.Rules))
	for _, rule := range file.Rules {
		// An absent `match` means "any", the same as an explicit `*`. Normalizing
		// at load keeps the matcher free of a second convention and makes the
		// JSON `??` semantics hold for a rule that omits the key.
		if rule.Match == "" {
			rule.Match = "*"
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// ResolvePolicy builds the ordered rule set for one spawn.
//
// Order is spawn overlay, then user overlay, then the bundled defaults — first
// match wins, so the more specific file is the one that decides. A missing user
// or spawn file is not an error (most workers have neither); an unreadable one
// that EXISTS is.
func ResolvePolicy(ctx context.Context, spawnPath string) ([]Rule, error) {
	bundled, err := BundledRules(ctx)
	if err != nil {
		return nil, err
	}
	rules := bundled

	userPath := UserPolicyPath()
	if userPath != "" {
		if _, statErr := os.Stat(userPath); statErr == nil {
			userRules, loadErr := LoadPolicyFile(ctx, userPath)
			if loadErr != nil {
				return nil, loadErr
			}
			rules = OverlayRules(userRules, rules)
		}
	}

	if spawnPath != "" {
		spawnRules, loadErr := LoadPolicyFile(ctx, spawnPath)
		if loadErr != nil {
			return nil, loadErr
		}
		rules = OverlayRules(spawnRules, rules)
	}
	return rules, nil
}
