// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import "regexp"

// sleepReason is the denial text of deny-sleep-commands.sh, kept byte-identical
// because it names the incident that produced the rule and the one case the
// rule deliberately leaves alone.
const sleepReason = "Blocked: this command can suspend the operator's Mac.\n\n" +
	"A session may not put the machine to sleep, wake it in a loop, shut it down or\n" +
	"restart it. On 2026-11-14 a session did exactly that to satisfy a task criterion\n" +
	"and the operator had to kill every Claude process to get the machine back.\n\n" +
	"If the work genuinely needs a sleep window, ask the operator to close the lid or\n" +
	"run it themselves. Shell `sleep N` is fine and is not blocked by this hook."

var (
	// sleepPmset matches pmset sleepnow and displaysleepnow, with any battery
	// flag in between.
	sleepPmset = regexp.MustCompile(
		`(?i)pmset[[:space:]]+(-[a-z]+[[:space:]]+)*(sleepnow|displaysleepnow)\b`,
	)
	// sleepOsascript matches osascript driving System Events, Finder or
	// loginwindow to sleep, shut down, restart or log out.
	sleepOsascript = regexp.MustCompile(`(?i)osascript`)
	sleepVerbs     = regexp.MustCompile(
		`(?i)(to|tell)[[:space:]]+(application[[:space:]]+)?[^|;]*` +
			`(sleep|shut[[:space:]]+down|restart|log[[:space:]]+out)`,
	)
	// sleepSystemsetup matches the power-management socket.
	sleepSystemsetup = regexp.MustCompile(`(?i)systemsetup[[:space:]]+-sleepnow`)
)

// SleepGuard replaces deny-sleep-commands.sh.
//
// Only SYSTEM sleep is denied. A shell `sleep 8` is an ordinary poll loop and is
// untouched: denying it would break every watcher on this machine.
type SleepGuard struct{}

// NewSleepGuard returns the machine-sleep guard.
func NewSleepGuard() *SleepGuard {
	return &SleepGuard{}
}

// Name returns the script this guard replaces.
func (g *SleepGuard) Name() string { return "deny-sleep-commands.sh" }

// Applies reports whether this input is a Bash call.
func (g *SleepGuard) Applies(input Input) bool { return input.IsBash() }

// Evaluate denies a command that can suspend, sleep, shut down or restart the
// machine.
func (g *SleepGuard) Evaluate(input Input) Decision {
	command, _ := input.BashCommand()
	if command == "" {
		return Decision{}
	}
	if sleepPmset.MatchString(command) ||
		sleepSystemsetup.MatchString(command) ||
		(sleepOsascript.MatchString(command) && sleepVerbs.MatchString(command)) {
		return DenyDecision(sleepReason)
	}
	return Decision{}
}
