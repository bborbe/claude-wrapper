// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// parkedCloserTailBytes bounds one closer read.
//
// A closer lives at the end of a session's event stream by definition, so the
// read is a bounded tail: a 9 MB event file costs the same as a small one. This
// window is deliberately separate from transcriptTailBytes — the two bound
// different reads and must be able to change independently.
const parkedCloserTailBytes = 256 * 1024

// ParkedClass is the closed set of observables a closer can park on.
type ParkedClass string

const (
	// ParkedOnCard is a wait on an attention card being answered.
	ParkedOnCard ParkedClass = "parked-on-card"
	// ParkedOnPR is a wait on a pull request reaching a state.
	ParkedOnPR ParkedClass = "parked-on-pr"
	// ParkedOnHost is a wait on a host answering on a port.
	ParkedOnHost ParkedClass = "parked-on-host"
	// ParkedUntil is a wait on a date, or on anything this build cannot probe.
	ParkedUntil ParkedClass = "parked-until"
)

// PullRequestRef is the pull request a closer names, split into separate argv
// elements so no shell is ever involved.
type PullRequestRef struct {
	Owner  string
	Repo   string
	Number int
}

// ParkedDeclaration is one classified `⏰ Ends:` declaration.
type ParkedDeclaration struct {
	// Class is the wait's class and is also the skip reason the tick records.
	Class ParkedClass
	// Text is the declaration with the marker and surrounding space removed.
	Text string
	// PR is set for parked-on-pr.
	PR PullRequestRef
	// Host and Port are set for parked-on-host; Port is 22 when the
	// declaration names none.
	Host string
	Port int
	// Deadline is the clock a parked-until declaration names. It is nil when
	// the declaration names something this build cannot probe.
	Deadline *time.Time
}

var (
	// parkedCardPattern matches any declaration mentioning a card, case
	// insensitively, wherever the word sits in the text.
	parkedCardPattern = regexp.MustCompile(`(?i)\bcard\b`)
	// parkedPRPattern matches one whitespace-delimited owner/repo#number token.
	// It is anchored and starts alphanumeric, so a leading `-` cannot match and
	// no flag can be smuggled into the argv. The number is at least 1.
	parkedPRPattern = regexp.MustCompile(
		`^([A-Za-z0-9][A-Za-z0-9._-]*)/([A-Za-z0-9][A-Za-z0-9._-]*)#([1-9][0-9]*)$`,
	)
	// parkedHostPortPattern matches one name:port token: an alphanumeric name
	// followed by a numeric port. The port range is checked after parsing.
	parkedHostPortPattern = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9.-]*):([0-9]+)$`)
	// parkedIPv4Pattern matches four numeric octets; the 0-255 range is checked
	// after parsing, so `v1.2.3` and `999.1.1.1` both fail.
	parkedIPv4Pattern = regexp.MustCompile(`^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$`)
)

// LastCloser returns the session's last closer text from its event stream, or
// "".
//
// Only the LAST assistant record is considered: a tool-only record there yields
// "", deliberately unlike LastAssistantText, which walks backwards. A missing,
// unreadable or undecodable file yields "" rather than an error — no closer
// means the row keeps the existing clock resume.
func LastCloser(path string) string {
	lastAssistant, ok := lastAssistantRecord(path)
	if !ok || lastAssistant == "" {
		return ""
	}
	return assistantText(lastAssistant)
}

// lastAssistantRecord returns the last newline-terminated assistant record in
// the file's bounded tail, and false when the file cannot be read at all.
//
// The first fragment of a tail window is discarded because it starts mid-line.
// Only newline-terminated lines count; a fragment ending at io.EOF is a partial
// write and is dropped rather than parsed.
func lastAssistantRecord(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return "", false
	}
	offset := int64(0)
	if info.Size() > parkedCloserTailBytes {
		offset = info.Size() - parkedCloserTailBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", false
	}

	reader := bufio.NewReader(file)
	if offset > 0 {
		// The window starts mid-line, so the first fragment is a truncated
		// record. Discard everything through its newline; it is never parsed.
		if _, err := reader.ReadString('\n'); err != nil && err != io.EOF {
			return "", false
		}
	}

	lastAssistant := ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				return "", false
			}
			break
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var record transcriptRecord
		if json.Unmarshal([]byte(trimmed), &record) != nil {
			return "", false
		}
		if record.Type == "assistant" {
			lastAssistant = trimmed
		}
	}
	return lastAssistant, true
}

// assistantText joins the text blocks of one encoded assistant record.
//
// A record with no text block yields "", which is what makes a tool-only last
// record produce no closer.
func assistantText(line string) string {
	var record transcriptRecord
	if json.Unmarshal([]byte(line), &record) != nil {
		return ""
	}
	var body messageBody
	if json.Unmarshal(record.Message, &body) != nil {
		return ""
	}
	texts := make([]string, 0, len(body.Content))
	for _, block := range body.Content {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// ClassifyParked turns a closer's last `⏰ Ends:` declaration into exactly one
// wait class.
//
// The returned bool is false when the closer carries no `⏰ Ends:` line at all —
// a `⏰ Next:` pointer alone is not a park. A declaration that names something
// this build cannot address safely falls through to ParkedUntil with a nil
// Deadline, so nothing out of range ever reaches a command or a dial.
func ClassifyParked(closer string) (ParkedDeclaration, bool) {
	text, ok := parkedDeclarationText(closer)
	if !ok {
		return ParkedDeclaration{}, false
	}
	declaration := ParkedDeclaration{Class: ParkedUntil, Text: text}
	if parkedCardPattern.MatchString(text) {
		declaration.Class = ParkedOnCard
		return declaration, true
	}
	if ref, ok := parkedPRRef(text); ok {
		declaration.Class = ParkedOnPR
		declaration.PR = ref
		return declaration, true
	}
	if host, port, ok := parkedHost(text); ok {
		declaration.Class = ParkedOnHost
		declaration.Host = host
		declaration.Port = port
		return declaration, true
	}
	declaration.Deadline = parkedDeadline(text)
	return declaration, true
}

// parkedDeclarationText returns the trimmed remainder of the last line carrying
// the machine-wait marker, and false when no line carries one.
func parkedDeclarationText(closer string) (string, bool) {
	text := ""
	found := false
	for _, line := range strings.Split(closer, "\n") {
		index := strings.Index(line, machineWaitEnds)
		if index < 0 {
			continue
		}
		text = strings.TrimSpace(line[index+len(machineWaitEnds):])
		found = true
	}
	return text, found
}

// parkedPRRef returns the first owner/repo#number token, if any.
func parkedPRRef(text string) (PullRequestRef, bool) {
	for _, token := range strings.Fields(text) {
		match := parkedPRPattern.FindStringSubmatch(token)
		if match == nil {
			continue
		}
		number, err := strconv.Atoi(match[3])
		if err != nil {
			continue
		}
		return PullRequestRef{Owner: match[1], Repo: match[2], Number: number}, true
	}
	return PullRequestRef{}, false
}

// parkedHost returns the first host token, defaulting the port to 22 when the
// token carries no port. A bare word, a token with more than one colon, and a
// dotted token that is not four valid octets are all rejected.
func parkedHost(text string) (string, int, bool) {
	for _, token := range strings.Fields(text) {
		if match := parkedHostPortPattern.FindStringSubmatch(token); match != nil {
			port, err := strconv.Atoi(match[2])
			if err == nil && port >= 1 && port <= 65535 {
				return match[1], port, true
			}
			continue
		}
		if match := parkedIPv4Pattern.FindStringSubmatch(token); match != nil {
			if octetsValid(match[1:]) {
				return token, 22, true
			}
		}
	}
	return "", 0, false
}

// octetsValid reports whether every IPv4 octet parses and is 0-255.
func octetsValid(octets []string) bool {
	for _, octet := range octets {
		value, err := strconv.Atoi(octet)
		if err != nil || value < 0 || value > 255 {
			return false
		}
	}
	return true
}

// parkedDeadline returns the first token that parses as RFC3339 or as a local
// date, or nil when no token names a clock.
func parkedDeadline(text string) *time.Time {
	for _, token := range strings.Fields(text) {
		if deadline, err := time.Parse(time.RFC3339, token); err == nil {
			return &deadline
		}
		if deadline, err := time.ParseInLocation("2006-01-02", token, time.Local); err == nil {
			return &deadline
		}
	}
	return nil
}
