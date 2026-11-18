// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import "regexp"

// nukeShutdownReason is the denial text of deny-nuke-shutdown.sh, kept
// byte-identical. It names approve-run.py as forbidden, unlike the prod-deploy
// guard which prescribes it — both are operator rulings of 2026-11-14 and the
// contradiction is deliberate until the operator reconciles them.
const nukeShutdownReason = "Refused: nuke K3s shutdown / VM power-down is operator-only " +
	"(operator ruling 2026-11-14). No AI session may run it — the operator runs it by hand " +
	"in their own shell. Do not route it through approve-run.py either; hand the exact " +
	"command to the operator as a `you run:` line."

var (
	// nukeShutdownScripts matches the two shutdown scripts in execution position.
	nukeShutdownScripts = regexp.MustCompile(
		`(^|[;&|][[:space:]]*|\([[:space:]]*)(sudo[[:space:]]+)?` +
			`([^[:space:];&|]*/)?(remote-k3s-shutdown-nuke|virsh-shutdown-all)\.sh` +
			`([[:space:];&|)]|$)`,
	)
	// nukeShutdownSshScript matches either script inside an ssh payload.
	nukeShutdownSshScript = regexp.MustCompile(
		`ssh[^;&|]*['"][^;&|]*(remote-k3s-shutdown-nuke|virsh-shutdown-all)\.sh`,
	)
	// nukeShutdownVirsh matches the VM power verbs, at command position and
	// inside an ssh payload — the usual spelling is `ssh <nuke host> "sudo virsh
	// destroy <vm>"`.
	nukeShutdownVirsh = regexp.MustCompile(
		`(^|[;&|][[:space:]]*|\([[:space:]]*)(sudo[[:space:]]+)?` +
			`virsh[[:space:]]+(destroy|shutdown|suspend)\b`,
	)
	nukeShutdownSshVirsh = regexp.MustCompile(
		`ssh[^;&|]*virsh[[:space:]]+(destroy|shutdown|suspend)\b`,
	)
	// nukeShutdownService matches stopping the K3s services themselves.
	nukeShutdownService = regexp.MustCompile(
		`(^|[;&|][[:space:]]*|\([[:space:]]*)(sudo[[:space:]]+)?` +
			`systemctl[[:space:]]+(stop|disable|mask)[[:space:]]+(k3s|k3s-agent)\b`,
	)
	nukeShutdownSshService = regexp.MustCompile(
		`ssh[^;&|]*systemctl[[:space:]]+(stop|disable|mask)[[:space:]]+(k3s|k3s-agent)\b`,
	)
	// nukeShutdownHostPower matches a host power verb aimed at a nuke host over
	// ssh. fire, hell and sun stay allowed.
	nukeShutdownHostPower = regexp.MustCompile(
		`ssh[^;&|]*(nuke|192\.168\.178\.3[0-9])[^;&|]*['"]?[[:space:];]*` +
			`([^;&|]*[[:space:];])?(sudo[[:space:]]+)?` +
			`(shutdown|poweroff|halt|reboot)([[:space:];&|'"]|$)`,
	)
)

// NukeShutdownGuard replaces deny-nuke-shutdown.sh.
//
// It denies nuke power-down and K3s quiesce commands. Scope is nuke only:
// fire, hell and sun power-offs stay allowed, and read-only inspection is
// untouched.
type NukeShutdownGuard struct{}

// NewNukeShutdownGuard returns the nuke-shutdown guard.
func NewNukeShutdownGuard() *NukeShutdownGuard {
	return &NukeShutdownGuard{}
}

// Name returns the script this guard replaces.
func (g *NukeShutdownGuard) Name() string { return "deny-nuke-shutdown.sh" }

// Applies reports whether this is a Bash call.
func (g *NukeShutdownGuard) Applies(input Input) bool { return input.IsBash() }

// Evaluate denies a nuke shutdown, VM power-down or K3s quiesce.
func (g *NukeShutdownGuard) Evaluate(input Input) Decision {
	raw, _ := input.BashCommand()
	if raw == "" {
		return Decision{}
	}
	// Prose arguments may NAME the scripts; that is not an invocation.
	command := stripProse(raw)
	for _, pattern := range nukeShutdownPatterns {
		if pattern.MatchString(command) {
			return DenyDecision(nukeShutdownReason)
		}
	}
	return Decision{}
}

// nukeShutdownPatterns is the ordered set the guard tests against.
var nukeShutdownPatterns = []*regexp.Regexp{
	nukeShutdownScripts,
	nukeShutdownSshScript,
	nukeShutdownVirsh,
	nukeShutdownSshVirsh,
	nukeShutdownService,
	nukeShutdownSshService,
	nukeShutdownHostPower,
}
