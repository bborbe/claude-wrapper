// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"regexp"
	"strings"
)

// Owner is the subject that owns a row: a goal, a topic, or the fleet sentinel.
//
// The sentinel has no page, so it is represented by an empty Kind rather than by
// a name that could collide with a real subject called "Fleet Manager".
type Owner struct {
	// Kind is `goal`, `topic`, or empty for the fleet sentinel.
	Kind string
	// Name is the subject's page name, empty for the sentinel.
	Name string
}

// IsFleet reports whether this is the fleet sentinel.
func (o Owner) IsFleet() bool { return o.Kind == "" }

// FleetOwnerRef is the sentinel as an Owner value.
var FleetOwnerRef = Owner{}

// OwnerContext is everything owner resolution reads.
//
// The two probes are deliberately separate functions because they answer
// different questions: SubjectLive is "is a manager for this subject live by
// REGISTRY NAME" — the signal that SELECTS an owner — while SubjectLiveness is
// "is the session this subject's page records alive", which is the `--check`
// probe and only ever reports on the owner once it is chosen.
//
// Every probe returns a POINTER for the same reason the branch inputs do: nil is
// "could not answer", and folding it into false would let an unreadable registry
// prove that no manager is live, which is the duplicate-spawn hazard the whole
// branch partition exists to avoid.
type OwnerContext struct {
	// TopicOf maps a goal's name to the topic that declares it.
	TopicOf map[string]string
	// SubjectLive answers "is a manager for this subject live?" by registry name.
	// nil = unanswerable.
	SubjectLive func(kind, name string) *bool
	// SubjectLiveness answers "is the session this subject's page records alive?".
	// nil = unanswerable.
	SubjectLiveness func(kind, name string) *bool
	// FleetLive is the fleet sentinel's own liveness. nil = unanswerable.
	FleetLive *bool
}

var (
	wikilinkNameRe  = regexp.MustCompile(`\[\[([^\]|#]+)\]\]`)
	managedByLineRe = regexp.MustCompile(`(?m)^managed_by:[ \t]*(.*)$`)
)

// ResolveOwner picks the subject that owns a row — the ladder, in order.
//
// A goal's declaring topic is NOT a rung for a worker: it exists so an unmanaged
// GOAL can have its manager started, and it never parents the worker to a topic
// manager. A task's own topic IS a rung (a task named directly in a topic's
// `## Goals` with no `goals:` of its own), which is what stops a goal-less task
// falling straight to the root.
//
// Selection is by REGISTRY liveness, never by the recorded session, so the owner
// is still chosen when the recorded session id has rotated.
func ResolveOwner(kind, name, block string, ctx OwnerContext) Owner {
	switch kind {
	case "topic":
		if keepsAsOwner(ctx.SubjectLive("topic", name)) {
			return Owner{Kind: "topic", Name: name}
		}
		return FleetOwnerRef
	case "goal":
		if keepsAsOwner(ctx.SubjectLive("goal", name)) {
			return Owner{Kind: "goal", Name: name}
		}
		if topic, ok := ctx.TopicOf[name]; ok && keepsAsOwner(ctx.SubjectLive("topic", topic)) {
			return Owner{Kind: "topic", Name: topic}
		}
		return FleetOwnerRef
	}
	// A task: its `managed_by` first, then each of its `goals:` in order.
	for _, subject := range append(managedByOf(block), goalsOf(block)...) {
		if subject == "" {
			continue
		}
		if keepsAsOwner(ctx.SubjectLive("goal", subject)) {
			return Owner{Kind: "goal", Name: subject}
		}
	}
	if topic, ok := ctx.TopicOf[name]; ok && keepsAsOwner(ctx.SubjectLive("topic", topic)) {
		return Owner{Kind: "topic", Name: topic}
	}
	return FleetOwnerRef
}

// OwnerLiveness returns the owner's own liveness and the row's chain liveness.
//
// `ownerLive` is the `--check` answer for the OWNER's recorded manager session.
// `chainLive` is what `open-manager` keys on: does ANY live manager cover the
// row's subject — the goal's own manager, or its declaring topic's? Only a
// PROVEN false opens a manager; nil does not.
func OwnerLiveness(owner Owner, kind, name string, ctx OwnerContext) (ownerLive, chainLive *bool) {
	if !owner.IsFleet() {
		// A resolved subject IS a live manager by construction — it was selected
		// because its registry entry is live — so the chain is proven true here.
		return ctx.SubjectLiveness(owner.Kind, owner.Name), boolRef(true)
	}
	switch kind {
	case "topic":
		return ctx.FleetLive, ctx.SubjectLive("topic", name)
	case "goal":
		chain := ctx.SubjectLive("goal", name)
		if chain != nil && !*chain {
			if topic, ok := ctx.TopicOf[name]; ok {
				chain = ctx.SubjectLive("topic", topic)
			} else {
				chain = boolRef(false)
			}
		}
		return ctx.FleetLive, chain
	}
	// A task's chain is not resolved from its own page: the fleet sentinel owns it,
	// and the row-level branch already decided whether to hold it.
	return ctx.FleetLive, nil
}

// keepsAsOwner reports whether a subject-liveness answer leaves the subject as
// the owner: true or unanswerable both do, and only a PROVEN false demotes it.
func keepsAsOwner(live *bool) bool {
	return live == nil || *live
}

// goalsOf returns the wikilink targets under the page's `goals:` key.
//
// The value may be inline (`goals: [[A]]`) or a list on following lines, and the
// block ends at the next line that starts a new key — a line beginning with a word
// character. Blank and indented lines belong to the value, which is why this walks
// lines rather than matching one regex: Go's RE2 has no lookahead, and the
// alternative — capture to the end and cut afterwards — would need the same walk.
func goalsOf(block string) []string {
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "goals:") {
			continue
		}
		var names []string
		collect := func(text string) {
			for _, m := range wikilinkNameRe.FindAllStringSubmatch(text, -1) {
				names = append(names, strings.TrimSpace(m[1]))
			}
		}
		collect(line)
		for _, next := range lines[i+1:] {
			if next != "" && startsWord(next) {
				break
			}
			collect(next)
		}
		return names
	}
	return nil
}

// startsWord reports whether the line begins a new frontmatter key: its first
// character is a word character. An indented line, a blank line and a `- item`
// line all continue the previous value.
func startsWord(line string) bool {
	c := line[0]
	return c == '_' ||
		(c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z')
}

// managedByOf returns the page's `managed_by` value with list brackets and quotes
// stripped.
func managedByOf(block string) []string {
	match := managedByLineRe.FindStringSubmatch(block)
	if match == nil {
		return nil
	}
	value := strings.Trim(strings.TrimSpace(match[1]), "[]'\"")
	if value == "" {
		return nil
	}
	return []string{value}
}

// boolRef returns a pointer to b, for the tri-state probe answers.
func boolRef(b bool) *bool { return &b }
