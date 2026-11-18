// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The denial texts of ask-git-push-gate.sh. The default suffix is appended only
// when the push itself needs a human decision; an under-specified command gets an
// agent-clearable remedy instead, because telling the agent to ask misdirects it
// into a round-trip it does not need.
const (
	pushGateSuffix = "The operator must approve this push explicitly. Hand it over as " +
		"`python3 ~/.claude/scripts/approve-run.py --cmd '<the same git push>' --cwd <repo>` " +
		"— that posts an approval card and runs the push once they answer `run it`. " +
		"Retrying cannot clear this: the gate evaluates the command, not the conversation."
	pushGateRefspecRemedy = "Re-run with an explicit refspec — `git push origin <branch>`. " +
		"No operator approval is needed for this."
	pushGateRenameRemedy = "Rename the branch to a conforming shape — one of `fix/` `feat/` " +
		"`docs/` `refactor/` `test/` `chore/` `hotfix/`, a Seibert ticket branch " +
		"`PROJECT-12345-description`, or `dev`/`staging` — then re-run. " +
		"No operator approval is needed for this."
	pushGateAllowReason = "Feature-branch push cleared by ask-git-push-gate.sh: the refspec " +
		"matched an allowed branch shape (a feature prefix, a Seibert ticket branch, or " +
		"dev/staging). master/main, force forms, ref deletions, tags and unknown branch " +
		"shapes remain gated."

	pushGateFeaturePrefixes = `fix/|feat/|docs/|refactor/|test/|chore/|hotfix/|dark-factory/|` +
		`sentry-fix/`
	pushGateEnvBranches   = `dev$|staging$`
	pushGateTicketBranch  = `[A-Z][A-Z0-9]*-[0-9]+-`
	pushGateAllowedShapes = pushGateFeaturePrefixes + `|` + pushGateTicketBranch + `|` +
		pushGateEnvBranches
)

var (
	pushGateEnvMatch   = regexp.MustCompile(`^(` + pushGateEnvBranches + `)$`)
	pushGateShapeMatch = regexp.MustCompile(`^(` + pushGateAllowedShapes + `)`)
	pushGateTagRef     = regexp.MustCompile(`^refs/tags/|^v[0-9]`)
	pushGateTempRemote = regexp.MustCompile(`^/tmp/|^/private/tmp/|^/var/folders/|^/private/var/folders/`)
	pushGateForceFlags = map[string]bool{
		"--force": true, "-f": true, "--force-with-lease": true, "--force-if-includes": true,
		"--mirror": true,
	}
	pushGateDeleteFlags = map[string]bool{"--delete": true, "-d": true}
	pushGateTagFlags    = map[string]bool{"--tags": true, "--follow-tags": true}
)

// PushGateGuard replaces ask-git-push-gate.sh.
//
// It gates `git push` on what it actually does rather than on the bare fact that
// it is a push: master/main, force forms, ref deletions, tags, undetermined
// targets and unknown branch shapes are refused, and a feature-shaped refspec is
// explicitly allowed so the permission layer does not prompt on it.
type PushGateGuard struct{}

// NewPushGateGuard returns the push-gate guard.
func NewPushGateGuard() *PushGateGuard {
	return &PushGateGuard{}
}

// Name returns the script this guard replaces.
func (g *PushGateGuard) Name() string { return "ask-git-push-gate.sh" }

// Applies reports whether this is a Bash call naming a push.
func (g *PushGateGuard) Applies(input Input) bool {
	if !input.IsBash() {
		return false
	}
	command, _ := input.BashCommand()
	return strings.Contains(command, "push")
}

// Evaluate denies a gated push, or allows a feature-shaped one.
func (g *PushGateGuard) Evaluate(input Input) Decision {
	command, _ := input.BashCommand()
	if command == "" {
		return Decision{}
	}
	// Resolve the "current branch" idiom before quotes are stripped: the strip
	// would reduce `git push origin "$(git branch --show-current)"` to
	// `git push origin` and gate it as "no refspec".
	if branch := currentBranch(input.Cwd); branch != "" {
		command = shellBranchSubstitution.ReplaceAllString(command, branch)
	}
	repo := input.Cwd
	allowed := false
	for _, segment := range segments(scanText(command)) {
		repo, _ = cdTarget(segment, repo)
		repo, _ = gitDashCTarget(segment, repo)
		if !shellGitPush.MatchString(segment) {
			continue
		}
		decision, ok := g.evaluatePush(segment, repo)
		if decision.Deny {
			return decision
		}
		if ok {
			allowed = true
		}
	}
	if allowed && WholeCallAllowable(command) {
		return AllowDecision(pushGateAllowReason)
	}
	return Decision{}
}

// evaluatePush parses one `git push` segment and applies the gates in order.
func (g *PushGateGuard) evaluatePush(segment string, repo string) (Decision, bool) {
	args := strings.Fields(shellRedirect.ReplaceAllString(
		shellGitPushPrefix.ReplaceAllString(segment, ""), " ",
	))
	if len(args) == 0 {
		return pushGateDeny(
			"git push with no arguments — the target branch cannot be determined from the command.",
			pushGateRefspecRemedy,
		), false
	}
	flags := parsePushGateFlags(args)
	if flags.unknown {
		// An option this guard does not recognise could be a force form (`-fu`), an
		// abbreviation (`--force-w`, `--del`) or a program runner (`--receive-pack=`).
		// No decision is safer than an Allow that covers it.
		return Decision{}, false
	}
	if decision, denied := flags.decision(); denied {
		return decision, false
	}
	positional := flags.positional
	if len(positional) < 2 {
		return pushGateDeny(
			"git push without a refspec — the target branch cannot be determined from the command.",
			pushGateRefspecRemedy,
		), false
	}
	root, _ := repoRoot(repo)
	allowed := false
	for _, spec := range positional[1:] {
		decision, ok := g.evaluateRefspec(spec, positional[0], root)
		if decision.Deny {
			return decision, false
		}
		if ok {
			allowed = true
		}
	}
	return Decision{}, allowed
}

// pushGateFlags classifies the push arguments.
type pushGateFlags struct {
	force      bool
	delete     bool
	tags       bool
	unknown    bool
	positional []string
}

// pushGateSafeFlags are the options that carry no target and change nothing.
var pushGateSafeFlags = map[string]bool{
	"-u": true, "--set-upstream": true, "--dry-run": true,
}

// parsePushGateFlags parses the arguments into flags and positionals.
func parsePushGateFlags(args []string) pushGateFlags {
	var flags pushGateFlags
	for _, arg := range args {
		switch {
		case pushGateForceFlags[arg] || strings.HasPrefix(arg, "--force-with-lease="):
			flags.force = true
		case pushGateDeleteFlags[arg]:
			flags.delete = true
		case pushGateTagFlags[arg]:
			flags.tags = true
		case pushGateSafeFlags[arg]:
			// carries no target
		case strings.HasPrefix(arg, "-"):
			// Unrecognised: could be a combined short flag, an abbreviation git
			// resolves, or a program runner. Refuse to compose it.
			flags.unknown = true
		default:
			flags.positional = append(flags.positional, arg)
		}
	}
	return flags
}

// decision returns the denial for a force, delete or tag flag.
func (f pushGateFlags) decision() (Decision, bool) {
	switch {
	case f.force:
		return pushGateDeny(
			"force-push (--force/-f/--force-with-lease/--mirror) is gated.", "",
		), true
	case f.delete:
		return pushGateDeny("deleting a remote ref (--delete/-d) is gated.", ""), true
	case f.tags:
		return pushGateDeny(
			"push of tags (--tags/--follow-tags) is gated — the operator pushes release tags.", "",
		), true
	}
	return Decision{}, false
}

// evaluateRefspec applies the gates to one refspec.
func (g *PushGateGuard) evaluateRefspec(spec string, remote string, root string) (Decision, bool) {
	if strings.HasPrefix(spec, "+") {
		return pushGateDeny("force-push (leading '+' on the refspec) is gated.", ""), false
	}
	dst := spec
	if index := strings.Index(spec, ":"); index >= 0 {
		src := spec[:index]
		dst = spec[index+1:]
		if src == "" || dst == "" {
			return pushGateDeny(
				"deleting a remote ref (':' refspec with an empty source or destination) is gated.",
				"",
			), false
		}
	}
	dst = strings.TrimPrefix(dst, "refs/heads/")
	if pushGateTagRef.MatchString(dst) {
		return pushGateDeny(
			"push of tag '"+dst+"' is gated — the operator pushes release tags.", "",
		), false
	}
	if dst == "master" || dst == "main" {
		if pushGateMasterExempt(root, remote) {
			return Decision{}, false
		}
		return pushGateDeny("push to '"+dst+"' is gated.", ""), false
	}
	if dst == "HEAD" {
		return pushGateDeny(
			"push to 'HEAD' — the target branch cannot be determined from the command.", "",
		), false
	}
	if pushGateEnvMatch.MatchString(dst) {
		return Decision{}, true
	}
	if !pushGateShapeMatch.MatchString(dst) {
		return pushGateDeny(
			"push to '"+dst+"' matches no known branch shape ("+pushGateAllowedShapes+`|`+
				pushGateEnvBranches+").",
			pushGateRenameRemedy,
		), false
	}
	return Decision{}, true
}

// pushGateMasterExempt reports whether an ordinary push to master/main is
// exempted. Every arm requires a POSITIVELY resolved repo root: an unresolvable
// target must never be the permissive case.
func pushGateMasterExempt(root string, remote string) bool {
	if root == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(root, ".maintainer.yaml")); err != nil {
		return true
	}
	if root == filepath.Join(os.Getenv("HOME"), ".claude") {
		return true
	}
	if pushGateAllowlisted(root) {
		return true
	}
	return pushGateTempRemote.MatchString(pushGateRemoteURL(root, remote))
}

// pushGateAllowlisted reports whether the repo root's basename is in the
// operator's allowlist file.
func pushGateAllowlisted(root string) bool {
	path := os.Getenv("PUSH_GATE_ALLOWLIST")
	if path == "" {
		path = filepath.Join(os.Getenv("HOME"), ".claude", "hooks", "push-gate-allow.txt")
	}
	data, err := os.ReadFile(path) //#nosec G304 G703 -- the operator's own allowlist path
	if err != nil {
		return false
	}
	wanted := filepath.Base(root)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == wanted {
			return true
		}
	}
	return false
}

// pushGateRemoteURL resolves a remote name or path to its push URL.
func pushGateRemoteURL(root string, remote string) string {
	url := remote
	if !strings.HasPrefix(remote, "/") && !strings.HasPrefix(remote, "file://") {
		url = remoteURL(root, remote)
	}
	url = strings.TrimPrefix(url, "file://")
	if strings.Contains(url, "..") {
		return ""
	}
	return url
}

// pushGateDeny builds a refusal, with the default suffix when no remedy is given.
func pushGateDeny(reason string, remedy string) Decision {
	if remedy == "" {
		remedy = pushGateSuffix
	}
	return DenyDecision("Refused: " + reason + " " + remedy)
}
