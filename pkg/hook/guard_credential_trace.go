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

// The denial texts of deny-credential-trace.sh, kept byte-identical. The trace
// arm substitutes the resolved script path into the text.
//
// Identifiers avoid a `cred`/`secret` prefix: gosec G101 reads such a name as a
// hardcoded credential and flags the denial text as a leaked value.
const (
	spliceDenial = "Refused: a credential lookup spliced into a command line via $(…). " +
		"Any shell error that echoes the expanded argument (failed glob, command not found) " +
		"prints the secret into the transcript. Pipe it instead " +
		"(`teamvault-cli … | shasum -a 256`), or write it to a mode-600 temp file and pass " +
		"that file (`--set-file`)."
	traceDenialPrefix = "Refused: a shell trace (`-x`) on "
	traceDenialSuffix = " — that script reads a credential, and a trace prints every " +
		"assignment (`+ PASSWORD=…`) in the clear, into the transcript, permanently. " +
		"Diagnose without tracing: run it untraced and read `exit=$?`, then follow the " +
		"script's own control flow; if you need visibility, add temporary logging that " +
		"names variables without printing their values."
)

// lookupPattern is the alternation naming every supported credential lookup.
const lookupPattern = `teamvault-cli[[:space:]]+(password|username)|--teamvault-key|` +
	`op[[:space:]]+read|security[[:space:]]+find-generic-password|` +
	`gcloud[[:space:]]+secrets[[:space:]]+versions[[:space:]]+access|` +
	`vault[[:space:]]+kv[[:space:]]+get`

var (
	// splicePattern matches a lookup substituted into argv.
	splicePattern = regexp.MustCompile(`\$\([[:space:]]*(` + lookupPattern + `)`)
	// lookupPatternRe matches a lookup anywhere, used against a script's body.
	lookupPatternRe = regexp.MustCompile(lookupPattern)
	// traceFlags matches a shell `-x` flag cluster, or `set -x`.
	traceFlags = regexp.MustCompile(
		`(^|[[:space:]])(ba|z)?sh[[:space:]]+-[a-zA-Z]*x|(^|[[:space:]])set[[:space:]]+-[a-zA-Z]*x`,
	)
	// scriptPath matches a `.sh` file the command names.
	scriptPath = regexp.MustCompile(`[^[:space:]"']+\.sh`)
)

// CredentialTraceGuard replaces deny-credential-trace.sh.
//
// `bash -x` prints every assignment, so tracing a script that fetches a
// credential publishes that credential into a durable transcript. The guard is
// narrow on purpose: it fires only when the command both traces AND names a
// script that reads a secret, or splices a lookup into argv.
type CredentialTraceGuard struct{}

// NewCredentialTraceGuard returns the credential-trace guard.
func NewCredentialTraceGuard() *CredentialTraceGuard {
	return &CredentialTraceGuard{}
}

// Name returns the script this guard replaces.
func (g *CredentialTraceGuard) Name() string { return "deny-credential-trace.sh" }

// Applies reports whether this is a Bash call.
func (g *CredentialTraceGuard) Applies(input Input) bool { return input.IsBash() }

// Evaluate denies a spliced lookup, or a trace of a script that reads one.
func (g *CredentialTraceGuard) Evaluate(input Input) Decision {
	command, _ := input.BashCommand()
	if command == "" {
		return Decision{}
	}
	if splicePattern.MatchString(command) {
		return DenyDecision(spliceDenial)
	}
	if !traceFlags.MatchString(command) {
		return Decision{}
	}
	for _, script := range scriptPath.FindAllString(command, -1) {
		path := expandHome(script)
		body, err := os.ReadFile(path) //#nosec G304 -- the path is a script the command names
		if err != nil {
			continue
		}
		if lookupPatternRe.MatchString(string(body)) {
			return DenyDecision(traceDenialPrefix + path + traceDenialSuffix)
		}
	}
	return Decision{}
}

// expandHome resolves a leading ~ against HOME.
func expandHome(path string) string {
	if path == "~" {
		return os.Getenv("HOME")
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(path, "~/"))
	}
	return path
}
