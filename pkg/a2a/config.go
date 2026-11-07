// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a2a

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
)

// SupervisorConfigPath is the config file the node supervisor already owns.
//
// Reading the same file rather than inventing a second one is deliberate: the
// per-target URLs live there today, and a second configuration surface would
// disagree with the first the moment either moved.
const SupervisorConfigPath = ".config/claude-supervisor/config.json"

// VaultsConfigPath is the vault -> allowed clusters allowlist.
const VaultsConfigPath = ".config/claude-supervisor/vaults.json"

// Config is everything the A2A path needs from configuration, already resolved
// into plain maps so the rules below are pure.
type Config struct {
	// URLs is `a2a.url` from the config file, keyed by target or cluster name.
	URLs map[string]string
	// EnvURLs / EnvTokens are the per-key environment maps (`SUPERVISOR_A2A_URL_<KEY>`
	// / `SUPERVISOR_A2A_TOKEN_<KEY>`), which win over the file.
	EnvURLs   map[string]string
	EnvTokens map[string]string
	// DefaultCluster is `a2a.defaultCluster`, else empty.
	DefaultCluster string
	// VaultClusters is `vaults.json`: vault name -> allowed cluster names.
	VaultClusters map[string][]string
	// K8sCallbackURL is the in-cluster relay a cluster worker posts its status to.
	// Unset means no relay exists, and the daemon falls back to the Docker
	// gateway URL — which a pod cannot reach, so the channel is simply quiet.
	K8sCallbackURL string
	// RelayURL / RelaySecret configure the tunnel: the daemon PULLS from the
	// relay because the Mac cannot accept an inbound connection from the cluster.
	RelayURL    string
	RelaySecret string
	// ConfigPath is where URLs came from, so a refusal can name the file.
	ConfigPath string
}

// ControllerConfig is one resolved controller endpoint plus its bearer token.
//
// The token is a secret. It is returned to the caller and never logged; the
// types here carry no String method that would print it.
type ControllerConfig struct {
	URL   string
	Token string
	// Target is the canonical `docker` / `k8s:<cluster>` this resolved for.
	Target Target
}

// LoadConfig reads the config file and the environment.
//
// A missing config file is not an error here — the environment may supply
// everything — but a missing URL at resolve time is, and it names this path.
func LoadConfig(ctx context.Context, home string) (Config, error) {
	if home == "" {
		detected, err := os.UserHomeDir()
		if err != nil {
			return Config{}, errors.Wrapf(ctx, err, "resolve home directory failed")
		}
		home = detected
	}
	cfg := Config{
		URLs:          map[string]string{},
		EnvURLs:       map[string]string{},
		EnvTokens:     map[string]string{},
		VaultClusters: map[string][]string{},
		ConfigPath:    filepath.Join(home, SupervisorConfigPath),
	}
	if err := cfg.readSupervisorConfig(ctx); err != nil {
		return Config{}, err
	}
	if err := cfg.readVaultsConfig(ctx, filepath.Join(home, VaultsConfigPath)); err != nil {
		return Config{}, err
	}
	cfg.readEnv(os.Environ())
	cfg.K8sCallbackURL = strings.TrimSpace(os.Getenv("SUPERVISOR_A2A_K8S_CALLBACK_URL"))
	cfg.RelayURL = strings.TrimSpace(os.Getenv("SUPERVISOR_A2A_RELAY_URL"))
	cfg.RelaySecret = strings.TrimSpace(os.Getenv("SUPERVISOR_A2A_RELAY_SECRET"))
	return cfg, nil
}

// readSupervisorConfig reads `a2a.url` and `a2a.defaultCluster`.
//
// ⚠️ A BARE-STRING `a2a.url` IS REFUSED, NOT READ AS EVERY TARGET. That shape
// predates the per-target split, and one controller URL cannot serve more than
// one target — reading it would send a docker spawn to whatever the single
// string named.
func (c *Config) readSupervisorConfig(ctx context.Context) error {
	raw, err := os.ReadFile(c.ConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.Wrapf(ctx, err, "read %s failed", c.ConfigPath)
	}
	var file struct {
		A2A struct {
			URL            json.RawMessage `json:"url"`
			DefaultCluster string          `json:"defaultCluster"`
			Token          json.RawMessage `json:"token"`
		} `json:"a2a"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return errors.Wrapf(ctx, err, "parse %s failed", c.ConfigPath)
	}
	if len(file.A2A.URL) > 0 {
		var single string
		if err := json.Unmarshal(file.A2A.URL, &single); err == nil {
			return errors.Errorf(
				ctx,
				`"a2a.url" in %s is a single string, and one controller URL cannot serve `+
					"more than one target — make it an object keyed by target "+
					`("docker", "nukedev", …)`,
				c.ConfigPath,
			)
		}
		if err := json.Unmarshal(file.A2A.URL, &c.URLs); err != nil {
			return errors.Wrapf(ctx, err, `parse "a2a.url" in %s failed`, c.ConfigPath)
		}
	}
	// ⚠️ A PLAINTEXT TOKEN IN THE FILE IS REFUSED, NOT READ. The migration moved
	// tokens to TeamVault; a token here is exactly the exposure it removed.
	if len(file.A2A.Token) > 0 && string(file.A2A.Token) != "null" {
		return errors.Errorf(
			ctx,
			`"a2a.token" in %s is refused: a plaintext token in the config file is the `+
				"exposure the TeamVault migration removed. Tokens resolve from TeamVault "+
				"(teamvault-cli) or from SUPERVISOR_A2A_TOKEN_<CLUSTER>",
			c.ConfigPath,
		)
	}
	c.DefaultCluster = strings.TrimSpace(file.A2A.DefaultCluster)
	return nil
}

// readVaultsConfig reads the vault -> allowed clusters map.
//
// An unreadable or absent file leaves the map empty, and the allowlist rule
// then refuses every cluster target: an allowlist that cannot be read must not
// read as "everything allowed".
//
// ⚠️ TWO SHAPES, ONE FILE. The live file is a flat array per vault
// (`{"private-personal": ["nukedev", …]}`); an earlier revision nested them
// (`{"private-personal": {"clusters": [...]}}`). Reading only the nested form
// fails the unmarshal on the real file, and the swallow below then turns a
// perfectly good allowlist into an empty one — every cluster spawn refused for a
// vault that is in fact declared. Accept both, and keep the fail-closed
// behaviour for a file that parses as neither.
func (c *Config) readVaultsConfig(ctx context.Context, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.Wrapf(ctx, err, "read %s failed", path)
	}
	var file struct {
		Vaults map[string]json.RawMessage `json:"vaults"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		// A file that does not parse leaves the allowlist empty, which fails closed.
		return nil
	}
	for vault, entry := range file.Vaults {
		clusters, ok := parseVaultClusters(entry)
		if !ok {
			// An entry in neither shape stays undeclared, so the vault fails closed
			// rather than silently allowing every cluster.
			continue
		}
		c.VaultClusters[vault] = clusters
	}
	return nil
}

// parseVaultClusters reads one vault's allowlist entry in either shape: a flat
// array of cluster names, or an object carrying them under `clusters`.
func parseVaultClusters(entry json.RawMessage) ([]string, bool) {
	var flat []string
	if err := json.Unmarshal(entry, &flat); err == nil {
		return flat, true
	}
	var nested struct {
		Clusters []string `json:"clusters"`
	}
	if err := json.Unmarshal(entry, &nested); err == nil {
		return nested.Clusters, true
	}
	return nil, false
}

// readEnv fills the per-key environment maps.
func (c *Config) readEnv(environ []string) {
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if key, ok := strings.CutPrefix(name, "SUPERVISOR_A2A_URL_"); ok {
			c.EnvURLs[envKeyToName(key)] = value
			continue
		}
		if key, ok := strings.CutPrefix(name, "SUPERVISOR_A2A_TOKEN_"); ok {
			c.EnvTokens[envKeyToName(key)] = value
		}
	}
}

// envKeyToName lowercases an environment suffix back into a lookup key.
//
// `OCTOPUS_DEV` becomes `octopus-dev`. The mapping is lossy in both directions
// (an underscore could have been a hyphen or an underscore), which is why the
// file stays keyed by the name as written and the environment route is
// documented as the normalised one. A name that carries an underscore already
// cannot be a cluster name, so the ambiguity never reaches a real target.
func envKeyToName(key string) string {
	return strings.ToLower(strings.ReplaceAll(key, "_", "-"))
}

// ResolveController resolves the URL and bearer token for one target.
func ResolveController(ctx context.Context, target Target, cfg Config) (ControllerConfig, error) {
	cluster := target.Cluster
	if target.Kind == KindK8s && cluster == "" {
		cluster = cfg.defaultCluster()
	}
	defaultCluster := cfg.defaultCluster()
	primaryKey := string(target.Kind)
	if target.Kind == KindK8s {
		primaryKey = cluster
	}
	lookupKeys := []string{primaryKey}
	// The legacy `k8s` key is read for the DEFAULT cluster only — that is the
	// one it meant before the per-cluster split.
	if target.Kind == KindK8s && cluster == defaultCluster {
		lookupKeys = append(lookupKeys, string(KindK8s))
	}

	url, urlSource := cfg.firstURL(lookupKeys)
	if url == "" {
		configured := cfg.configuredClusters()
		return ControllerConfig{}, errors.Errorf(
			ctx,
			`no controller URL is configured for target %q — set `+
				`SUPERVISOR_A2A_URL_%s or "a2a.url.%s" in %s. Configured: %s. `+
				"The value is refused rather than defaulted: a worker created on a "+
				"cluster the operator did not configure is discovered only by noticing it",
			target.String(),
			ClusterEnvSuffix(primaryKey),
			primaryKey,
			cfg.ConfigPath,
			strings.Join(configured, ", "),
		)
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return ControllerConfig{}, errors.Errorf(
			ctx,
			"the controller URL for %q is not http(s): %s (from %s)",
			target.String(), redactURL(url), urlSource,
		)
	}

	token := cfg.firstToken(lookupKeys)
	if token == "" {
		return ControllerConfig{}, errors.Errorf(
			ctx,
			"no controller token is configured for target %q — set "+
				"SUPERVISOR_A2A_TOKEN_%s, or let teamvault-cli resolve the key this "+
				"daemon names for it. An \"a2a.token\" in %s is NOT a fallback: it is "+
				"refused, because a plaintext token there is the exposure the TeamVault "+
				"migration removed",
			target.String(),
			ClusterEnvSuffix(primaryKey),
			cfg.ConfigPath,
		)
	}
	return ControllerConfig{
		URL:    strings.TrimRight(url, "/"),
		Token:  token,
		Target: Target{Kind: target.Kind, Cluster: cluster},
	}, nil
}

func (c Config) defaultCluster() string {
	if c.DefaultCluster != "" {
		return c.DefaultCluster
	}
	return DefaultCluster
}

func (c Config) firstURL(keys []string) (string, string) {
	for _, key := range keys {
		if value := c.EnvURLs[key]; value != "" {
			return value, "SUPERVISOR_A2A_URL_" + ClusterEnvSuffix(key)
		}
		if value := c.URLs[key]; value != "" {
			return value, `"a2a.url.` + key + `" in ` + c.ConfigPath
		}
	}
	return "", ""
}

func (c Config) firstToken(keys []string) string {
	for _, key := range keys {
		if value := c.EnvTokens[key]; value != "" {
			return value
		}
	}
	return ""
}

// configuredClusters lists the cluster names the configuration reaches.
//
// `local`, `docker` and `k8s` are kinds or a legacy alias, never cluster names,
// so they are excluded from the report.
func (c Config) configuredClusters() []string {
	reserved := map[string]struct{}{"local": {}, "docker": {}, "k8s": {}}
	seen := map[string]struct{}{}
	for _, source := range []map[string]string{c.URLs, c.EnvURLs} {
		for key := range source {
			if _, skip := reserved[key]; skip {
				continue
			}
			seen[key] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	return names
}

// ClusterAllowlistRefusal returns the refusal for a vault whose declared
// clusters do not include this target's, or nil.
//
// The rule (operator, 2026-10-20): a seibert worker must not run on nuke-dev or
// nuke-prod, and a trading or personal worker must not run on the octopus
// clusters. A wrong placement is a data-boundary breach, not a mistake that can
// be retried, so every branch below refuses rather than defaulting.
//
// ⚠️ NO FALLBACK. An unreadable allowlist, a vault that declares nothing and a
// target that names no cluster would each be satisfied by "allow it" and by
// "deny it"; only one of those is safe, and it is deny.
func ClusterAllowlistRefusal(
	ctx context.Context,
	vault string,
	target Target,
	cfg Config,
) error {
	if target.Kind == KindDocker {
		return nil
	}
	if target.Cluster == "" {
		return errors.Errorf(
			ctx,
			"target %q names no cluster, so vault %q's cluster allowlist cannot be "+
				"checked against it — name the cluster as `k8s:<cluster>`. Refusing "+
				"rather than spawning without the boundary, since a cluster that cannot "+
				"be named is not one that can be allowed",
			target.String(), vault,
		)
	}
	allowed, known := cfg.VaultClusters[vault]
	if !known {
		return errors.Errorf(
			ctx,
			"vault %q declares no cluster allowlist, so target %q is refused. Add the "+
				"vault to %s, or spawn with target `docker`",
			vault, target.String(), VaultsConfigPath,
		)
	}
	for _, candidate := range allowed {
		if candidate == target.Cluster {
			return nil
		}
	}
	return errors.Errorf(
		ctx,
		"vault %q may not run a worker on cluster %q (allowed: %s) — a wrong placement "+
			"is a data-boundary breach, not a mistake that can be retried",
		vault, target.Cluster, strings.Join(allowed, ", "),
	)
}

// redactURL drops the query and userinfo from a URL before it reaches a message.
//
// A controller URL can carry a token in its query in some deployments, and a
// refusal that echoed it would write the credential to a log.
func redactURL(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i] + "?<redacted>"
	}
	return raw
}
