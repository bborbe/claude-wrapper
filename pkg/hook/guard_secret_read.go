// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"regexp"
	"strings"
)

// The denial texts of deny-k8s-secret-read.sh, kept byte-identical: the
// Kubernetes rules quote them, including the hash-comparison remedy.
//
// The identifiers avoid a "secret" prefix on purpose — gosec G101 reads such a
// name as a hardcoded credential and flags the denial text as a leaked value.
const (
	redactionRaw  = "Refused: kubectl --raw on secrets/configmaps prints values."
	redactionEdit = "Refused: kubectl edit on a Secret/ConfigMap/Config opens every value. " +
		"Read one non-secret field with -o jsonpath, or compare hashes " +
		"(base64 -d | shasum -a 256)."
	redactionJsonpath = "Refused: jsonpath into .data/.stringData/.spec.env/annotations prints " +
		"secret values. Compare hashes instead: ... | base64 -d | shasum -a 256."
	redactionOutput = "Refused: printing a Secret/ConfigMap/Config with -o yaml/json leaks values " +
		"(and last-applied-configuration embeds the whole object). Use -o jsonpath for one " +
		"non-secret field, \"-o json | jq '<path> | keys'\" as the last stage for key names, " +
		"or compare hashes. The jq form must END at | keys or | keys[] with nothing after the " +
		"closing quote — a trailing redirect, || fallback, or extra stage is refused. " +
		"kubectl describe secret <name> also lists key names."
)

var (
	// redactionKind matches the resource kinds whose values must not print.
	redactionKind = `(secrets?|configmaps?|cm|configs?)(\.[a-z0-9.-]+)?(/[^[:space:]]+)?`

	// redactionRawPath matches --raw on a secrets or configmaps API path.
	redactionRawPath = regexp.MustCompile(
		`(?i)kubectl[a-z]*[^;&|]*--raw[= ][^[:space:]]*/(secrets|configmaps)`,
	)
	// redactionGetOrEdit matches a get or edit of one of those kinds.
	redactionGetOrEdit = regexp.MustCompile(
		`(?i)kubectl[a-z]*[^;&|]*[[:space:]](get|edit)[[:space:]]+([^;&|]*[[:space:]])?` +
			redactionKind + `([[:space:]]|$)`,
	)
	redactionEditVerb = regexp.MustCompile(`(?i)kubectl[a-z]*[^;&|]*[[:space:]]edit[[:space:]]`)
	// redactionJsonpathFlag matches a jsonpath output selector.
	redactionJsonpathFlag = regexp.MustCompile(`(-o|--output)[[:space:]=]*jsonpath`)
	// redactionValuePath matches a jsonpath that reaches a value field.
	redactionValuePath = regexp.MustCompile(`\.(data|stringData|env)\b|\.spec\.env|annotations`)
	// redactionOutputFormat matches an output format that prints whole objects.
	redactionOutputFormat = regexp.MustCompile(
		`(-o|--output)[[:space:]=]*(yaml|json|go-template|custom-columns)([^a-z-]|$)`,
	)
	// redactionJqKeys matches the one form that is allowed: jq as the last
	// pipeline stage, ending at `| keys` with nothing after the closing quote.
	redactionJqKeys = regexp.MustCompile(
		`\|[[:space:]]*jq([[:space:]]+-[a-zA-Z]+)*[[:space:]]+'[^']*\|[[:space:]]*keys(\[\])?` +
			`[[:space:]]*'[[:space:]]*$`,
	)
)

// SecretReadGuard replaces deny-k8s-secret-read.sh.
//
// It denies kubectl reads that print Secret, ConfigMap or Config values. The
// last-applied-configuration annotation embeds the whole object as plaintext, so
// a ConfigMap or a Config CR leaks credentials exactly as a Secret does.
type SecretReadGuard struct{}

// NewSecretReadGuard returns the secret-read guard.
func NewSecretReadGuard() *SecretReadGuard {
	return &SecretReadGuard{}
}

// Name returns the script this guard replaces.
func (g *SecretReadGuard) Name() string { return "deny-k8s-secret-read.sh" }

// Applies reports whether this is a Bash call naming kubectl.
func (g *SecretReadGuard) Applies(input Input) bool {
	if !input.IsBash() {
		return false
	}
	command, _ := input.BashCommand()
	return strings.Contains(command, "kubectl")
}

// Evaluate denies a read that would print a secret value.
func (g *SecretReadGuard) Evaluate(input Input) Decision {
	command, _ := input.BashCommand()
	if redactionRawPath.MatchString(command) {
		return DenyDecision(redactionRaw)
	}
	if !redactionGetOrEdit.MatchString(command) {
		return Decision{}
	}
	if redactionEditVerb.MatchString(command) {
		return DenyDecision(redactionEdit)
	}
	if redactionJsonpathFlag.MatchString(command) && redactionValuePath.MatchString(command) {
		return DenyDecision(redactionJsonpath)
	}
	if !redactionOutputFormat.MatchString(command) {
		return Decision{}
	}
	// Key names only: jq is the last stage and its program ends in `| keys`.
	if redactionJqKeys.MatchString(command) {
		return Decision{}
	}
	return DenyDecision(redactionOutput)
}
