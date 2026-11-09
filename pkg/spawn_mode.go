// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"slices"
	"strings"

	"github.com/bborbe/errors"
)

// The two ways a local worker opens.
const (
	// SpawnModeInteractive opens a WezTerm tab under the vault's cc-* launcher.
	SpawnModeInteractive = "interactive"
	// SpawnModeHeadless opens an in-process worker whose prompts park for the
	// manager.
	SpawnModeHeadless = "headless"
)

// SpawnModeEnv is the environment variable that sets the fleet's spawn mode.
const SpawnModeEnv = "SUPERVISOR_SPAWN_MODE"

// The chain step that decided a spawn's mode, as reported in `mode_source`.
const (
	SpawnModeSourceArgument = "argument"
	SpawnModeSourceEnv      = "env"
	SpawnModeSourceConfig   = "config"
	SpawnModeSourceDefault  = "default"
)

// SpawnModes is every valid string mode, in the order a refusal lists them.
func SpawnModes() []string {
	return []string{SpawnModeInteractive, SpawnModeHeadless}
}

// SpawnModeSources is what the operator configured, before anything is chosen.
type SpawnModeSources struct {
	// Env is SUPERVISOR_SPAWN_MODE; empty means unset.
	Env string
	// Config is the raw `spawn.mode` from the config file; empty or `null` means
	// unset. Kept raw so a non-string value is refused rather than read as unset.
	Config json.RawMessage
	// ConfigPath names the file in a refusal.
	ConfigPath string
}

// SpawnModeResolution is the mode a spawn runs under and the step that decided it.
type SpawnModeResolution struct {
	Mode   string
	Source string
}

// ResolveSpawnMode ports the node supervisor's `resolveSpawnMode`
// (`server/spawn-mode.mjs`).
//
// Precedence, highest first: the per-call `interactive` argument,
// SUPERVISOR_SPAWN_MODE, `spawn.mode` in the config file, the built-in
// `interactive` default. The source is returned rather than inferred by the
// caller, because "why is this worker headless" is otherwise unanswerable from
// the outside.
//
// Every string source is validated BEFORE the argument is consulted, as the node
// does: a typo in the config file must not stay invisible merely because this
// particular call passed `interactive` explicitly. An invalid value refuses the
// spawn rather than falling back, since a worker opened the wrong way is
// discovered only by noticing it.
func ResolveSpawnMode(
	ctx context.Context,
	interactive *bool,
	sources SpawnModeSources,
) (SpawnModeResolution, error) {
	candidates, err := spawnModeCandidates(ctx, sources)
	if err != nil {
		return SpawnModeResolution{}, err
	}
	if interactive != nil {
		mode := SpawnModeHeadless
		if *interactive {
			mode = SpawnModeInteractive
		}
		return SpawnModeResolution{Mode: mode, Source: SpawnModeSourceArgument}, nil
	}
	if len(candidates) > 0 {
		return candidates[0], nil
	}
	// Interactive, and not arbitrarily: a worker must come up with the same
	// tooling a normal session has, and a tab is the shape that guarantees it.
	return SpawnModeResolution{Mode: SpawnModeInteractive, Source: SpawnModeSourceDefault}, nil
}

// spawnModeCandidates returns every configured string source in precedence
// order, refusing the first one that is not a known mode.
func spawnModeCandidates(
	ctx context.Context,
	sources SpawnModeSources,
) ([]SpawnModeResolution, error) {
	var candidates []SpawnModeResolution
	if sources.Env != "" {
		if !isSpawnMode(sources.Env) {
			return nil, invalidSpawnMode(ctx, SpawnModeEnv, strconvQuote(sources.Env))
		}
		candidates = append(candidates, SpawnModeResolution{
			Mode:   sources.Env,
			Source: SpawnModeSourceEnv,
		})
	}
	raw := strings.TrimSpace(string(sources.Config))
	if raw == "" || raw == "null" {
		return candidates, nil
	}
	where := `"spawn.mode" in ` + sources.ConfigPath
	var value string
	if err := json.Unmarshal(sources.Config, &value); err != nil {
		return nil, invalidSpawnMode(ctx, where, raw)
	}
	if value == "" {
		return candidates, nil
	}
	if !isSpawnMode(value) {
		return nil, invalidSpawnMode(ctx, where, raw)
	}
	return append(candidates, SpawnModeResolution{
		Mode:   value,
		Source: SpawnModeSourceConfig,
	}), nil
}

func isSpawnMode(value string) bool {
	return slices.Contains(SpawnModes(), value)
}

func invalidSpawnMode(ctx context.Context, where string, shown string) error {
	return errors.Errorf(
		ctx,
		"%s is %s, which is not a spawn mode — refusing to spawn rather than guessing, "+
			"since a worker opened the wrong way is discovered only by noticing it. "+
			"Valid values: %s",
		where,
		shown,
		strings.Join(SpawnModes(), ", "),
	)
}

func strconvQuote(value string) string {
	quoted, _ := json.Marshal(value)
	return string(quoted)
}

// SpawnModeSourcesReader reads the operator's spawn-mode configuration.
type SpawnModeSourcesReader interface {
	Read(ctx context.Context) (SpawnModeSources, error)
}

// NewSpawnModeSourcesReader reads SUPERVISOR_SPAWN_MODE and `spawn.mode` from
// the node supervisor's own config file, located by SupervisorConfigPath
// (SUPERVISOR_CONFIG, else `$XDG_CONFIG_HOME` or `~/.config`, then
// `claude-supervisor/config.json`).
//
// An empty home resolves to the user's home directory. A nil getenv reads the
// process environment.
func NewSpawnModeSourcesReader(home string, getenv func(string) string) SpawnModeSourcesReader {
	if getenv == nil {
		getenv = os.Getenv
	}
	return &spawnModeSourcesReader{home: home, getenv: getenv}
}

type spawnModeSourcesReader struct {
	home   string
	getenv func(string) string
}

// Read is called on every spawn rather than once at boot, so an edit to the
// config file takes effect on the next spawn without a daemon restart.
//
// A missing file is no config. An unreadable or unparseable one is logged and
// read as no config — the node does the same (`falling back to the built-in spawn
// default`) — while a parseable file with a bad `spawn.mode` VALUE refuses in
// ResolveSpawnMode.
func (r *spawnModeSourcesReader) Read(ctx context.Context) (SpawnModeSources, error) {
	// One resolver for the config path, shared with the spawn caps, so the mode
	// and the caps can never be read from two different files.
	configPath, err := SupervisorConfigPath(ctx, r.home, r.getenv)
	if err != nil {
		return SpawnModeSources{}, err
	}
	sources := SpawnModeSources{
		Env:        strings.TrimSpace(r.getenv(SpawnModeEnv)),
		ConfigPath: configPath,
	}
	raw, err := os.ReadFile(sources.ConfigPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf(
				"WARNING: read %s failed (%v) — falling back past spawn.mode",
				sources.ConfigPath,
				err,
			)
		}
		return sources, nil
	}
	var file struct {
		Spawn struct {
			Mode json.RawMessage `json:"mode"`
		} `json:"spawn"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		log.Printf(
			"WARNING: parse %s failed (%v) — falling back past spawn.mode",
			sources.ConfigPath,
			err,
		)
		return sources, nil
	}
	sources.Config = file.Spawn.Mode
	return sources, nil
}
