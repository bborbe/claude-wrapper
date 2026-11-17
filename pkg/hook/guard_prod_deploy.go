// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import "regexp"

// The denial texts of deny-prod-deploy.sh. Five of them share one tail, which is
// the hand-over instruction naming approve-run.py; it is quoted by other rules
// and must stay byte-identical, em dash included.
const (
	prodDeployTail = "Prod deploys are operator-gated — hand it over as " +
		"`python3 ~/.claude/scripts/approve-run.py --cmd '<the same command>' --cwd <dir>`: " +
		"that posts an approval card and runs the command once they answer `run it`. " +
		"Never ask the operator to type it."

	prodDeployProdBranch = "Refused: production deploy (BRANCH=prod make buca/apply/deploy). " +
		prodDeployTail
	prodDeployProdWorktree = "Refused: make buca/apply/deploy inside a *-prod worktree. " +
		prodDeployTail
	prodDeployCwdProdPrefix = "Refused: make buca/apply/deploy with the session cwd in a *-prod worktree ("
	prodDeployMasterPrefix  = "Refused: production deploy (BRANCH=master maps to prod in "
	prodDeployInstallPrefix = "Refused: production deploy " +
		"(BRANCH=master make install/upgrade maps to prod in "

	prodDeployDevPrefix = "Refused: dev deploy whose target repo does not map BRANCH=dev " +
		"onto a dev stage ("
	prodDeployDevSuffix = "). A dev label alone is not evidence of a dev deploy — hand it over as " +
		"`python3 ~/.claude/scripts/approve-run.py --cmd '<the same command>' --cwd <dir>` " +
		"so the operator approves a card instead of typing it."

	prodDeployKubectl = "Refused: destructive kubectl verb on a production target. Only " +
		"read-only verbs and rollout restart/undo/scale are allowed on prod; the operator " +
		"runs anything else via `! <command>`."
)

var (
	// prodDeployApproveRun is the sanctioned hand-over. Its --cmd payload
	// necessarily contains the deploy text, so scanning it here would refuse the
	// very hand-over the refusal message prescribes.
	prodDeployApproveRun = regexp.MustCompile(
		`(^|[[:space:];&|(])(python3[[:space:]]+)?[^[:space:];&|]*approve-run\.py([[:space:]]|$)`,
	)
	// prodDeployProseSingle and prodDeployProseDouble blank the value of the
	// free-text arguments, so a commit message that MENTIONS a prod deploy is not
	// read as one. `bash -c '…'` payloads are not stripped and still inspected.
	prodDeployProseSingle = regexp.MustCompile(
		`(-m|--message|--body|--title|--notes|--context|--payload)([= ]+)'[^']*'`,
	)
	prodDeployProseDouble = regexp.MustCompile(
		`(-m|--message|--body|--title|--notes|--context|--payload)([= ]+)"[^"]*"`,
	)
	// prodDeployMakePrefix matches `make`, any flags, and any single-target
	// prefix argument, up to the target itself.
	prodDeployMakePrefix = `make([[:space:]]+-[^[:space:]]+` +
		`([[:space:]]+[^-[:space:]][^[:space:]]*)?)*[[:space:]]+`
	prodDeployMakeTarget  = regexp.MustCompile(prodDeployMakePrefix + `(buca|apply|deploy)\b`)
	prodDeployMakeApply   = regexp.MustCompile(prodDeployMakePrefix + `apply\b`)
	prodDeployMakeInstall = regexp.MustCompile(prodDeployMakePrefix + `install\b`)
	prodDeployMakeUpgrade = regexp.MustCompile(prodDeployMakePrefix + `upgrade\b`)
	// prodDeployAnyCd matches a `cd` with any leading boundary, used to decide
	// whether the session cwd is what selects the worktree.
	prodDeployAnyCd = regexp.MustCompile(`(^|[[:space:];&|])cd[[:space:]]`)

	prodDeployProdBranchRe   = regexp.MustCompile(`(^|[[:space:];&|"'])BRANCH=["']?prod`)
	prodDeployProdWorktreeRe = regexp.MustCompile(`cd[[:space:]]+[^;&|]*-prod(/|[[:space:];&|]|$)`)
	prodDeployCwdProdRe      = regexp.MustCompile(`-prod(/|$)`)
	prodDeployDevBranchRe    = regexp.MustCompile(
		`(^|[[:space:];&|"'])BRANCH=["']?dev(elop)?([[:space:];&|"']|$)`,
	)
	prodDeployDevBranchName  = regexp.MustCompile(`BRANCH=["']?(dev|develop)`)
	prodDeployMasterBranchRe = regexp.MustCompile(
		`(^|[[:space:];&|"'])BRANCH=["']?master([[:space:];&|"']|$)`,
	)

	prodDeployKubeVerb = regexp.MustCompile(
		`(^|[[:space:];&|(])kubectl[a-z]*([[:space:]]+-[^[:space:]]+` +
			`([[:space:]]+[^-[:space:]][^[:space:]]*)?)*[[:space:]]+` +
			`(apply|create|delete|patch|edit|replace|exec|port-forward|cp|annotate|label|` +
			`set|taint|drain|cordon|run|attach|debug|proxy|autoscale)\b`,
	)
	prodDeployKubeTarget = regexp.MustCompile(
		`(^|[[:space:];&|(])(kubectlnukeprod|kubectlprod|kubectlsmallprod)\b` +
			`|kubectl[^;&|]*--context[= ][^[:space:]]*prod` +
			`|kubectl[^;&|]*(-n[[:space:]=]*|--namespace[[:space:]=]+)prod\b`,
	)
)

// ProdDeployGuard replaces deny-prod-deploy.sh.
//
// It denies production deploys and destructive kubectl verbs on production
// targets. `deny` rather than `ask` because under defaultMode auto a hook's
// `ask` is inert — deny is the only decision a hook can gate with.
type ProdDeployGuard struct{}

// NewProdDeployGuard returns the prod-deploy guard.
func NewProdDeployGuard() *ProdDeployGuard {
	return &ProdDeployGuard{}
}

// Name returns the script this guard replaces.
func (g *ProdDeployGuard) Name() string { return "deny-prod-deploy.sh" }

// Applies reports whether this is a Bash call.
func (g *ProdDeployGuard) Applies(input Input) bool { return input.IsBash() }

// Evaluate denies a production deploy or a destructive prod kubectl verb.
func (g *ProdDeployGuard) Evaluate(input Input) Decision {
	raw, _ := input.BashCommand()
	if raw == "" || prodDeployApproveRun.MatchString(raw) {
		return Decision{}
	}
	command := stripProse(raw)
	if decision := g.evaluateMake(command, input.Cwd); decision.Deny {
		return decision
	}
	if decision := g.evaluateDevBranch(command, input.Cwd); decision.Deny {
		return decision
	}
	if decision := g.evaluateMasterBranch(command, input.Cwd); decision.Deny {
		return decision
	}
	if prodDeployKubeVerb.MatchString(command) && prodDeployKubeTarget.MatchString(command) {
		return DenyDecision(prodDeployKubectl)
	}
	return Decision{}
}

// stripProse blanks the free-text arguments of a command.
func stripProse(command string) string {
	command = prodDeployProseSingle.ReplaceAllString(command, "${1}${2}''")
	return prodDeployProseDouble.ReplaceAllString(command, `${1}${2}""`)
}

// evaluateMake covers the path-based arms: a prod branch label, a *-prod
// worktree operand, and a session cwd inside a *-prod worktree.
func (g *ProdDeployGuard) evaluateMake(command string, cwd string) Decision {
	if !prodDeployMakeTarget.MatchString(command) {
		return Decision{}
	}
	if prodDeployProdBranchRe.MatchString(command) {
		return DenyDecision(prodDeployProdBranch)
	}
	if prodDeployProdWorktreeRe.MatchString(command) {
		return DenyDecision(prodDeployProdWorktree)
	}
	if !prodDeployAnyCd.MatchString(command) && prodDeployCwdProdRe.MatchString(cwd) {
		return DenyDecision(prodDeployCwdProdPrefix + cwd + "). " + prodDeployTail)
	}
	return Decision{}
}

// evaluateDevBranch covers the no-ask carve-out: a `BRANCH=dev` label is no
// evidence of a dev deploy unless the target repo maps it onto a dev stage.
func (g *ProdDeployGuard) evaluateDevBranch(command string, cwd string) Decision {
	if !prodDeployDevBranchRe.MatchString(command) || !prodDeployMakeApply.MatchString(command) {
		return Decision{}
	}
	dir := targetDir(command, cwd)
	branch := "dev"
	if matches := prodDeployDevBranchName.FindAllStringSubmatch(command, -1); len(matches) > 0 {
		branch = matches[len(matches)-1][1]
	}
	if mapsBranchToDev(dir, branch) {
		return Decision{}
	}
	root, ok := deployRoot(dir)
	if !ok {
		root = dir
	}
	return DenyDecision(prodDeployDevPrefix + root + prodDeployDevSuffix)
}

// evaluateMasterBranch covers BRANCH=master, resolved against the target repo:
// master selects prod only in a repo that says so in its own Makefile.env.
func (g *ProdDeployGuard) evaluateMasterBranch(command string, cwd string) Decision {
	if !prodDeployMasterBranchRe.MatchString(command) {
		return Decision{}
	}
	dir := targetDir(command, cwd)
	if !mapsMasterToProd(dir) {
		return Decision{}
	}
	root, _ := deployRoot(dir)
	if prodDeployMakeTarget.MatchString(command) {
		return DenyDecision(prodDeployMasterPrefix + root + "). " + prodDeployTail)
	}
	if prodDeployMakeUpgrade.MatchString(command) ||
		(prodDeployMakeInstall.MatchString(command) && componentDeploys(dir)) {
		return DenyDecision(prodDeployInstallPrefix + root + "). " + prodDeployTail)
	}
	return Decision{}
}
