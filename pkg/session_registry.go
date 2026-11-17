// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// RegistryEntry is one live entry of Claude Code's session registry.
//
// The registry is keyed by pid and its file is DELETED when the session exits,
// which is the property that makes absence meaningful here: a pid-keyed file
// that is gone is a session that is gone. A file left behind by a crashed
// session would read as live forever, so every entry is confirmed against its
// pid rather than trusted on the JSON alone.
type RegistryEntry struct {
	PID       int
	SessionID string
	Status    string
	Cwd       string
	// Name is the tab title the session registered under.
	//
	// It is the only join from a spawned tab back to its session id: a tab worker
	// is a separate process and never reports its id to us, so the title we set is
	// what identifies it.
	Name string
}

// SessionRegistry reads Claude Code's own session registry.
type SessionRegistry interface {
	// Status returns the raw registry status for a session — idle, busy, waiting
	// or shell — or nil when the registry does not list it.
	//
	// nil is deliberately distinct from any status value and from "not waiting":
	// an unlisted session is a different fact, and reporting it as a status would
	// be a guess wearing a measurement's clothes.
	Status(ctx context.Context, sessionID string) (*string, error)
	// Cwd returns the working directory recorded for a session, or nil when the
	// registry does not list it.
	Cwd(ctx context.Context, sessionID string) (*string, error)
	// Live returns every registry entry whose pid is still alive.
	//
	// An unreadable directory is an error, never an empty slice: the caller
	// distinguishes "nothing is live" from "the probe could not run".
	Live(ctx context.Context) ([]RegistryEntry, error)
	// Entries returns every parseable entry, live or not — the BULK form of
	// Status and Cwd.
	//
	// It exists because each of those re-reads and re-parses the whole registry
	// directory, so a roster sweep that asked them per row paid O(rows x files)
	// file reads for one list call. Measured 2026-11-17: 126 registry files over
	// 62 live sessions put 13.1 s of a 20.0 s sweep into those repeated reads.
	// One snapshot answers every row.
	//
	// Unreadable is an error, never an empty slice, for the same reason Live's is.
	Entries(ctx context.Context) ([]RegistryEntry, error)
	// Named returns the session ids registered under a tab title.
	//
	// Deliberately NOT pid-checked, unlike Live. Tab naming asks "is this title
	// taken", and a stale file for a dead session still holds the title a live
	// session would collide with; filtering by pid here would hand out a name that
	// is already on disk. Liveness is a different question, asked by Live.
	Named(ctx context.Context, name string) ([]string, error)
	// NamesForSession returns the names a session id is registered under — the
	// INVERSE of Named, and the join a caller needs when it holds an id while the
	// channel it wants takes a name (the peer channel `SendMessage` takes).
	//
	// PID-CHECKED, and the direction is what decides that. A name handed to
	// `SendMessage` must be one a live session answers to: a stale entry left by a
	// crashed session yields an address that fails at the caller, which is the
	// unperformable-remedy defect this join exists to remove. Named reads the other
	// way round, where presence is the safe answer.
	//
	// A LIST rather than one name, because the ambiguity must stay visible: two
	// live entries can carry different names for one id, and picking either would
	// deliver into the wrong conversation. An empty slice means "read, and this id
	// is registered under nothing" — a worker that cannot be addressed — while an
	// error is "the registry could not be read", which is retryable and must not
	// be reported as the worker being gone.
	NamesForSession(ctx context.Context, sessionID string) ([]string, error)
}

// NewSessionRegistry creates a SessionRegistry over dir.
//
// dir defaults to ~/.claude/sessions when empty.
func NewSessionRegistry(dir string) SessionRegistry {
	return &sessionRegistry{dir: dir}
}

type sessionRegistry struct {
	dir string
}

// registryFile is the on-disk shape, narrowed to the fields this package reads.
type registryFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	Cwd       string `json:"cwd"`
	Name      string `json:"name"`
}

func (r *sessionRegistry) directory(ctx context.Context) (string, error) {
	if r.dir != "" {
		return r.dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve home dir failed")
	}
	return filepath.Join(home, ".claude", "sessions"), nil
}

// entries reads every parseable entry, live or not.
//
// A foreign or half-written file is skipped rather than raised: it is not ours
// to fail on, and one bad file must not take every other session's liveness
// down with it.
func (r *sessionRegistry) entries(ctx context.Context) ([]registryFile, error) {
	dir, err := r.directory(ctx)
	if err != nil {
		return nil, err
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read registry dir %q failed", dir)
	}
	result := make([]registryFile, 0, len(names))
	for _, name := range names {
		if !strings.HasSuffix(name.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name.Name()))
		if err != nil {
			continue
		}
		var entry registryFile
		if err := json.Unmarshal(raw, &entry); err != nil {
			continue
		}
		if entry.SessionID == "" || entry.PID <= 0 {
			continue
		}
		result = append(result, entry)
	}
	return result, nil
}

// Status returns the raw registry status, or nil when unlisted.
func (r *sessionRegistry) Status(ctx context.Context, sessionID string) (*string, error) {
	all, err := r.entries(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range all {
		if entry.SessionID == sessionID {
			status := entry.Status
			return &status, nil
		}
	}
	return nil, nil
}

// Cwd returns the registry's working directory for a session, or nil.
func (r *sessionRegistry) Cwd(ctx context.Context, sessionID string) (*string, error) {
	all, err := r.entries(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range all {
		if entry.SessionID == sessionID {
			cwd := entry.Cwd
			return &cwd, nil
		}
	}
	return nil, nil
}

// Live returns every entry whose pid is alive.
func (r *sessionRegistry) Live(ctx context.Context) ([]RegistryEntry, error) {
	all, err := r.entries(ctx)
	if err != nil {
		return nil, err
	}
	live := make([]RegistryEntry, 0, len(all))
	for _, entry := range all {
		if !pidAlive(entry.PID) {
			continue
		}
		live = append(live, RegistryEntry(entry))
	}
	return live, nil
}

// Entries returns every parseable entry, live or not.
func (r *sessionRegistry) Entries(ctx context.Context) ([]RegistryEntry, error) {
	all, err := r.entries(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]RegistryEntry, len(all))
	for i, entry := range all {
		entries[i] = RegistryEntry(entry)
	}
	return entries, nil
}

// Named returns the session ids registered under a tab title.
func (r *sessionRegistry) Named(ctx context.Context, name string) ([]string, error) {
	all, err := r.entries(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, 2)
	for _, entry := range all {
		if entry.Name == name {
			ids = append(ids, entry.SessionID)
		}
	}
	return ids, nil
}

// NamesForSession returns the live names a session id is registered under.
//
// The inverse of Named, and pid-checked for the reason the interface states: the
// result is an ADDRESS a caller will send to, so an entry whose process is gone
// would produce a name that fails at the far end — the very defect this join
// exists to remove.
func (r *sessionRegistry) NamesForSession(ctx context.Context, sessionID string) ([]string, error) {
	if sessionID == "" {
		return []string{}, nil
	}
	all, err := r.entries(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, entry := range all {
		if entry.SessionID != sessionID || entry.Name == "" || !pidAlive(entry.PID) {
			continue
		}
		if _, ok := seen[entry.Name]; ok {
			continue
		}
		seen[entry.Name] = struct{}{}
		names = append(names, entry.Name)
	}
	return names, nil
}

// pidAlive reports whether a pid names a running process.
//
// Signal 0 performs the permission and existence checks without delivering
// anything. EPERM means "there, but not yours", which is a live process every
// bit as much as ESRCH is a dead one — folding it into "dead" would make every
// session owned by another user read as gone.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return strings.Contains(err.Error(), "operation not permitted")
}

// TranscriptState is one session's read-time transcript facts.
//
// Both fields come from ONE read of the same 256 KiB tail window. They were two
// methods until 2026-11-17 and the roster called both per row, so the window was
// opened, seeked and JSON-parsed twice for every session — measured at 6.5 s of a
// 20.0 s sweep over 62 sessions, on top of a registry read inside each call.
type TranscriptState struct {
	// LastMessage is the last assistant text, or nil when the tail carries none.
	LastMessage *string
	// CurrentToolCall is the call in flight, or nil when none is.
	CurrentToolCall *ToolCall
}

// TranscriptReader reads a session's conversation from disk.
//
// The transcript is the only source that answers "which tool is it inside right
// now" for a session this process did not spawn: the child's event stream is
// this process's own, and a foreign worker has none here.
type TranscriptReader interface {
	// State returns the row's last assistant text and call in flight from a
	// SINGLE read of the transcript tail.
	//
	// cwd is the session's working directory as the CALLER resolved it, from one
	// registry snapshot shared by the whole sweep. Taking it as an argument is
	// what keeps the registry read count at one per roster call rather than one
	// per row: resolving it here would put a full registry directory read back
	// inside every transcript read, which is half the cost this signature removes.
	// A nil or empty cwd means the registry did not list the session, so there is
	// no transcript path to derive — nil state, nil error.
	State(ctx context.Context, sessionID string, cwd *string) (*TranscriptState, error)
}

// NewTranscriptReader creates a TranscriptReader.
//
// now is injected rather than read from the clock: the held duration of a tool
// call is a measurement, and a package-level time.Now() makes it untestable and
// puts a second clock in a process that already has one.
func NewTranscriptReader(now libtime.CurrentDateTimeGetter) TranscriptReader {
	return NewTranscriptReaderInDir(now, "")
}

// NewTranscriptReaderInDir creates a TranscriptReader over an explicit projects
// directory, so a test does not have to write into the operator's own
// ~/.claude/projects.
func NewTranscriptReaderInDir(
	now libtime.CurrentDateTimeGetter,
	projectsDir string,
) TranscriptReader {
	return &transcriptReader{now: now, projectsDir: projectsDir}
}

type transcriptReader struct {
	now         libtime.CurrentDateTimeGetter
	projectsDir string
}

// transcriptTailBytes bounds one transcript read.
//
// The last message and the call in flight are both at the end by definition, and
// a roster sweep reads every worker — reading each file whole would turn one
// status call into a read of the fleet's entire history.
const transcriptTailBytes = 256 * 1024

// transcriptPath resolves ~/.claude/projects/<cwd-slug>/<session-id>.jsonl.
//
// The slug is Claude Code's own: the working directory with its separators and
// dots replaced by dashes. The cwd arrives from the caller's registry snapshot;
// a nil or empty one means the registry did not list the session, so the path is
// unknown rather than guessed — an empty path, not an error.
func (t *transcriptReader) transcriptPath(
	ctx context.Context,
	sessionID string,
	cwd *string,
) (string, error) {
	if cwd == nil || *cwd == "" {
		return "", nil
	}
	projectsDir := t.projectsDir
	if projectsDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.Wrapf(ctx, err, "resolve home dir failed")
		}
		projectsDir = filepath.Join(home, ".claude", "projects")
	}
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(*cwd)
	return filepath.Join(projectsDir, slug, sessionID+".jsonl"), nil
}

// tailLines returns the records of the last transcriptTailBytes, oldest first.
//
// The first line of the window is dropped unless the window starts at the file
// beginning: a partial line parses as a truncated record, and a truncated record
// would otherwise be read as a malformed one rather than as "the window cut it".
func (t *transcriptReader) tailLines(
	ctx context.Context,
	sessionID string,
	cwd *string,
) ([]transcriptRecord, error) {
	path, err := t.transcriptPath(ctx, sessionID, cwd)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		// A session with no transcript on disk is a normal state — a tab that has
		// not spoken yet — not a failure.
		return nil, nil
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "stat transcript failed")
	}
	offset := int64(0)
	if info.Size() > transcriptTailBytes {
		offset = info.Size() - transcriptTailBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, errors.Wrapf(ctx, err, "seek transcript failed")
	}
	reader := bufio.NewReader(file)
	if offset > 0 {
		// Discard the partial first line.
		if _, err := reader.ReadString('\n'); err != nil && err != io.EOF {
			return nil, errors.Wrapf(ctx, err, "skip partial transcript line failed")
		}
	}
	records := make([]transcriptRecord, 0, 64)
	for {
		line, err := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			var record transcriptRecord
			if json.Unmarshal([]byte(trimmed), &record) == nil {
				records = append(records, record)
			}
		}
		if err != nil {
			break
		}
	}
	return records, nil
}

// transcriptRecord is the part of one transcript line this package reads.
type transcriptRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

// messageBody is the part of a message this package reads.
type messageBody struct {
	Content []contentBlock `json:"content"`
}

// contentBlock is one block of an assistant message.
type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// ToolUseID is set on a tool_result block and is what closes a tool_use.
	ToolUseID string `json:"tool_use_id"`
}

// State returns the row's last assistant text and call in flight.
//
// ONE backward pass over ONE read of the tail window, and it stops as soon as
// both answers are in hand. The two questions used to be two methods, each
// opening and parsing the same window: the roster called both per row, so every
// session's 256 KiB tail was read and JSON-decoded twice.
//
// "In flight" means the LAST tool_use with no tool_result carrying its id — not
// "any unmatched tool_use". An interrupted call leaves an unmatched tool_use
// behind and the conversation carries on, so a scan that reports any of them
// keeps naming a call the worker abandoned.
func (t *transcriptReader) State(
	ctx context.Context,
	sessionID string,
	cwd *string,
) (*TranscriptState, error) {
	records, err := t.tailLines(ctx, sessionID, cwd)
	if err != nil {
		return nil, err
	}
	state := &TranscriptState{}
	// Walk backwards collecting the newest tool_use and every id that has a
	// result; the first tool_use whose id has no result is the one in flight.
	answered := map[string]struct{}{}
	for i := len(records) - 1; i >= 0; i-- {
		var body messageBody
		if json.Unmarshal(records[i].Message, &body) != nil {
			continue
		}
		if state.CurrentToolCall == nil {
			state.CurrentToolCall = t.callInRecord(body, answered, records[i].Timestamp)
		}
		if state.LastMessage == nil && records[i].Type == "assistant" {
			state.LastMessage = lastTextOf(body)
		}
		if state.CurrentToolCall != nil && state.LastMessage != nil {
			break
		}
	}
	return state, nil
}

// lastTextOf renders one assistant message's text blocks, or nil when it has none.
//
// Extracted so the single pass above stays a pass: the text half is the same
// question the old LastAssistantText asked, and it is easier to read on its own.
func lastTextOf(body messageBody) *string {
	parts := make([]string, 0, len(body.Content))
	for _, block := range body.Content {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	text := strings.Join(parts, "\n")
	if len(text) > 2000 {
		text = text[:2000]
	}
	return &text
}

// callInRecord returns the in-flight call inside one record, recording every
// answered id it sees on the way.
//
// Extracted so the walk above stays a walk: the nesting was the whole of this
// function's complexity, and the rule it encodes — a tool_use is in flight only
// when nothing closes it — is easier to read on its own.
func (t *transcriptReader) callInRecord(
	body messageBody,
	answered map[string]struct{},
	timestamp string,
) *ToolCall {
	for _, block := range body.Content {
		switch block.Type {
		case "tool_result":
			if block.ToolUseID != "" {
				answered[block.ToolUseID] = struct{}{}
			}
		case "tool_use":
			if block.ID == "" {
				continue
			}
			if _, ok := answered[block.ID]; ok {
				continue
			}
			return toolCallOf(block, timestamp, t.now.Now().Time())
		}
	}
	return nil
}

// toolCallOf renders one tool_use as the roster's ToolCall.
//
// The duration is measured from the record's own timestamp, so it does not reset
// when a caller polls again. Name and duration fail independently: a record
// without a timestamp still reports which tool, and the two time fields go null
// together rather than one of them guessing.
func toolCallOf(block contentBlock, timestamp string, now time.Time) *ToolCall {
	call := &ToolCall{Name: block.Name, InputSummary: summarizeInput(block.Input)}
	if timestamp == "" {
		return call
	}
	started, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return call
	}
	held := int(now.Sub(started).Seconds())
	if held < 0 {
		held = 0
	}
	call.StartedAt = &timestamp
	call.HeldSeconds = &held
	return call
}

// summarizeInput flattens a tool input to a bounded one-line summary.
func summarizeInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")
	if len(flat) > 200 {
		return flat[:200]
	}
	return flat
}
