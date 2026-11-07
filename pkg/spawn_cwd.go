// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bborbe/errors"
)

// Vault is one entry of `vault-cli config list --output json`.
type Vault struct {
	Name            string `json:"name"`
	Path            string `json:"path"`
	ClaudeScript    string `json:"claude_script"`
	WorkerLauncher  string `json:"worker_launcher"`
	ManagerLauncher string `json:"manager_launcher"`
}

// VaultLister returns the configured vaults.
//
// An interface rather than a direct exec so a test can supply a fixed list: the
// launcher chain is the part worth testing, and shelling out to vault-cli in a
// unit test would make the test depend on the operator's own config.
type VaultLister interface {
	List(ctx context.Context) ([]Vault, error)
}

// NewVaultCLILister creates a VaultLister that shells `vault-cli config list`.
func NewVaultCLILister() VaultLister {
	return &vaultCLILister{}
}

type vaultCLILister struct{}

// List returns the configured vaults.
//
// An unreadable list is an error, never an empty slice: an empty list would make
// every vault name read as "not configured", which is a wrong answer with a
// plausible shape, and the caller would refuse a spawn the operator expected to
// work.
func (l *vaultCLILister) List(ctx context.Context) ([]Vault, error) {
	output, err := exec.CommandContext(ctx, "vault-cli", "config", "list", "--output", "json").
		Output()
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "vault-cli config list failed")
	}
	var vaults []Vault
	if err := json.Unmarshal(output, &vaults); err != nil {
		return nil, errors.Wrapf(ctx, err, "parse vault-cli config list failed")
	}
	return vaults, nil
}

// WorkerTarget is the directory, vault and launcher a spawn resolved to.
type WorkerTarget struct {
	Cwd      string
	Vault    string
	Launcher string
	// Source names the chain step that supplied the launcher, so a spawn's
	// response can say where it came from rather than only what it is.
	Source string
}

// safeLauncher bounds a launcher value to a plain script name or path.
//
// The value is executed by a shell and task frontmatter is writable by workers,
// so anything outside this alphabet is a command injection surface rather than a
// typo.
var safeLauncher = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// ResolveWorkerTarget resolves cwd/vault to a directory and a launcher.
//
// Every branch refuses rather than falling back. The failure this prevents is
// measured, not hypothetical: on 2026-10-03 a defaulted cwd started 27 workers
// in the supervisor repo on the wrong model in one batch, and nobody noticed
// until the ledger showed the launcher.
func ResolveWorkerTarget(
	ctx context.Context,
	vaults []Vault,
	cwd string,
	vault string,
	serverCwd string,
) (*WorkerTarget, error) {
	if strings.TrimSpace(cwd) == "" && strings.TrimSpace(vault) == "" {
		return nil, errors.Wrapf(
			ctx,
			ErrUnknownVault,
			"spawn_agent requires `cwd` or `vault`. Omitting both used to default the "+
				"worker to this server's own working directory (%s), with whatever "+
				"launcher happened to match — which is how 27 workers started in the "+
				"supervisor repo on the wrong model in one batch (2026-10-03). Pass "+
				"`vault` (the vault name, which resolves both the launcher and the "+
				"directory) or an explicit `cwd`",
			serverCwd,
		)
	}
	named, err := findVault(ctx, vaults, vault)
	if err != nil {
		return nil, err
	}
	if named != nil {
		return resolveNamedVault(ctx, named, cwd)
	}
	return resolveByCwd(ctx, vaults, cwd, vault)
}

// resolveNamedVault resolves a spawn that named its vault.
//
// The cwd is still honoured when supplied, but only as a refinement of the
// vault's own directory: the launcher comes from the vault, so a worker in a
// directory its launcher does not belong to would run on the wrong model.
func resolveNamedVault(
	ctx context.Context,
	vault *Vault,
	cwd string,
) (*WorkerTarget, error) {
	if strings.TrimSpace(vault.Path) == "" {
		return nil, errors.Wrapf(
			ctx,
			ErrUnknownVault,
			"vault %q has no `path` in vault-cli config, so there is no directory to "+
				"run a worker in",
			vault.Name,
		)
	}
	trimmed := strings.TrimSpace(cwd)
	if trimmed != "" && !within(trimmed, vault.Path) {
		return nil, errors.Wrapf(
			ctx,
			ErrUnknownVault,
			"`cwd` %q is not inside vault %q (%s) — pass one or the other, not both, "+
				"since a worker cannot run in a directory its launcher does not belong to",
			trimmed,
			vault.Name,
			vault.Path,
		)
	}
	resolvedCwd := vault.Path
	if trimmed != "" {
		resolvedCwd = trimmed
	}
	launcher, source, err := launcherForVault(ctx, vault)
	if err != nil {
		return nil, err
	}
	return &WorkerTarget{
		Cwd: resolvedCwd, Vault: vault.Name, Launcher: launcher, Source: source,
	}, nil
}

// resolveByCwd resolves a spawn that named only a directory.
//
// The directory must still fall inside a configured vault, because the launcher
// comes from the vault and not from the directory.
func resolveByCwd(
	ctx context.Context,
	vaults []Vault,
	cwd string,
	vault string,
) (*WorkerTarget, error) {
	trimmed := strings.TrimSpace(cwd)
	if trimmed == "" {
		return nil, errors.Wrapf(ctx, ErrUnknownVault, "vault %q is not configured", vault)
	}
	// The MOST SPECIFIC vault wins, not the first one listed: configured paths can
	// nest (an Obsidian root and a vault inside it), and taking the first match
	// would run a worker under the parent's launcher.
	best := -1
	for i := range vaults {
		if vaults[i].Path == "" || !within(trimmed, vaults[i].Path) {
			continue
		}
		if best == -1 || len(vaults[i].Path) > len(vaults[best].Path) {
			best = i
		}
	}
	if best >= 0 {
		launcher, source, err := launcherForVault(ctx, &vaults[best])
		if err != nil {
			return nil, err
		}
		return &WorkerTarget{
			Cwd: trimmed, Vault: vaults[best].Name, Launcher: launcher, Source: source,
		}, nil
	}
	return nil, errors.Wrapf(
		ctx,
		ErrUnknownVault,
		"cwd %q is outside every configured vault, so no launcher could be resolved "+
			"for it — refusing to spawn rather than falling back to the bare `claude` "+
			"binary. Known vaults: %s. Pass `vault` instead when the worker belongs to "+
			"a vault rather than to a specific directory",
		trimmed,
		vaultNames(vaults),
	)
}

// findVault returns the named vault, or nil when the name is empty.
//
// An unknown NAME is an error; an empty name is not, because the cwd branch may
// still resolve.
func findVault(ctx context.Context, vaults []Vault, vault string) (*Vault, error) {
	name := strings.TrimSpace(vault)
	if name == "" {
		return nil, nil
	}
	for i := range vaults {
		if vaults[i].Name == name {
			return &vaults[i], nil
		}
	}
	return nil, errors.Wrapf(
		ctx,
		ErrUnknownVault,
		"vault %q is not configured — refusing to spawn rather than falling back to "+
			"another vault's launcher. Known vaults: %s",
		name,
		vaultNames(vaults),
	)
}

// launcherForVault picks the launcher and names the chain step that supplied it.
//
// worker_launcher before claude_script: the first is the operator's explicit
// choice of what workers run as, the second is the vault's general launcher.
func launcherForVault(ctx context.Context, vault *Vault) (string, string, error) {
	candidates := []struct{ value, source, key string }{
		{vault.WorkerLauncher, "worker-default", "worker_launcher"},
		{vault.ClaudeScript, "vault", "claude_script"},
	}
	for _, candidate := range candidates {
		value := strings.TrimSpace(candidate.value)
		if value == "" {
			continue
		}
		if err := validateLauncher(ctx, value, candidate.key, vault.Name); err != nil {
			return "", "", err
		}
		return value, candidate.source, nil
	}
	return "", "", errors.Wrapf(
		ctx,
		ErrNoLauncher,
		"vault %q has no `claude_script` in vault-cli config, so there is no launcher "+
			"to start a worker with — refusing rather than falling back to the bare "+
			"`claude` binary, which routes around the router env, the MCP config and "+
			"the model selection that every cc-* script carries",
		vault.Name,
	)
}

// validateLauncher refuses a launcher value that is not a plain script name/path.
func validateLauncher(ctx context.Context, value, key, vaultName string) error {
	if !safeLauncher.MatchString(value) || hasDotDotSegment(value) {
		return errors.Wrapf(
			ctx,
			ErrNoLauncher,
			"launcher %q in vault %q `%s` is not a plain script name or path — refusing "+
				"to spawn, since the value is executed by a shell and frontmatter is "+
				"writable by workers. Use letters, digits, `.`, `_`, `-` and `/` only",
			value,
			vaultName,
			key,
		)
	}
	return nil
}

// hasDotDotSegment reports whether a path climbs out of its directory.
func hasDotDotSegment(value string) bool {
	for segment := range strings.SplitSeq(value, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

// within reports whether path is inside root.
//
// Both sides are resolved through symlinks first, because a string comparison
// refuses a path that reaches the same directory by another name: the Obsidian
// vaults are routinely opened through a symlink to the vault's real directory,
// and `/tmp` is `/private/tmp` on macOS. A path that cannot be resolved (it does
// not exist yet) falls back to its absolute form.
func within(path, root string) bool {
	cleanPath, err := resolvePath(path)
	if err != nil {
		return false
	}
	cleanRoot, err := resolvePath(root)
	if err != nil {
		return false
	}
	if cleanPath == cleanRoot {
		return true
	}
	return strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator))
}

// resolvePath returns path with symlinks resolved, falling back to its absolute
// form when it cannot be resolved.
func resolvePath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return filepath.Abs(resolved)
	}
	return filepath.Abs(path)
}

// vaultNames renders the configured vault names for a refusal message.
func vaultNames(vaults []Vault) string {
	names := make([]string, 0, len(vaults))
	for _, vault := range vaults {
		names = append(names, vault.Name)
	}
	if len(names) == 0 {
		return "(none configured)"
	}
	return strings.Join(names, ", ")
}

// ParseLauncherModel returns the model a launcher script starts its session with,
// or "" when it cannot be read.
//
// Read from the script rather than from vault-cli config, because the model is
// not in that config at all: a cc-* launcher passes it as an argument
// (`--model "${ANTHROPIC_DEFAULT_OPUS_MODEL}"`) with the value in an `export`
// above. A caller asking "did my worker come up on the right model" cannot answer
// that from the vault name, and the vault name is all the config holds.
//
// Empty rather than a guess when either half is missing: a model reported from a
// failed parse would be worse than no model at all, since the field exists
// precisely so a wrong spawn is visible and a plausible wrong value defeats that.
func ParseLauncherModel(scriptText string) string {
	if strings.TrimSpace(scriptText) == "" {
		return ""
	}
	flag := launcherModelFlag.FindStringSubmatch(scriptText)
	if flag == nil {
		return ""
	}
	raw := ""
	for _, group := range flag[1:] {
		if group != "" {
			raw = strings.TrimSpace(group)
			break
		}
	}
	if raw == "" {
		return ""
	}
	reference := launcherModelReference.FindStringSubmatch(raw)
	if reference == nil {
		return raw
	}
	// Two alternatives, so the name sits in whichever group matched: `${VAR}` is
	// group 1, `$VAR` is group 2. Reading only the first silently turns an
	// unbraced reference into the empty name and loses the model.
	name := reference[1]
	if name == "" {
		name = reference[2]
	}
	if name == "" {
		return raw
	}
	assignment := regexp.MustCompile(
		`(?m)^[ \t]*export[ \t]+` + name + `=(?:"([^"]*)"|'([^']*)'|(\S+))`,
	).FindStringSubmatch(scriptText)
	if assignment == nil {
		return ""
	}
	for _, group := range assignment[1:] {
		if group != "" {
			return strings.TrimSpace(group)
		}
	}
	return ""
}

// launcherModelFlag matches `--model` at a whitespace boundary, so a hypothetical
// `--model-name` flag cannot match, in all three quoting shapes.
var launcherModelFlag = regexp.MustCompile(`(?:^|\s)--model[ \t]+(?:"([^"]*)"|'([^']*)'|(\S+))`)

// launcherModelReference matches a whole-value `${VAR}` or `$VAR` expansion.
//
// Anchored on both ends so a literal like `deepseek-v4-flash[1m]` is returned as
// itself rather than being mistaken for a reference.
var launcherModelReference = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$|^\$([A-Za-z_][A-Za-z0-9_]*)$`)

// LauncherModel reads a launcher script and returns its model, or "".
//
// No error return: a missing or unreadable launcher is a MISSING MODEL, not a
// failed spawn — the launcher itself may still run, and the model is only a
// diagnostic for "did this worker come up on what I asked for".
func LauncherModel(launcher string) string {
	path := launcher
	if !strings.Contains(path, "/") {
		resolved, err := exec.LookPath(path)
		if err != nil {
			return ""
		}
		path = resolved
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return ParseLauncherModel(string(raw))
}
