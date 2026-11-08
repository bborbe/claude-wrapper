// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/bborbe/errors"
)

// Names inside one session's directory.
const (
	sessionRecordName = "record.json"
	sessionStdinName  = "stdin.fifo"
	sessionEventsName = "events.jsonl"
)

// StateDirEnv overrides where session state lives.
//
// It exists so a test never writes into the operator's real state directory, and
// so a second instance on another port cannot adopt the first one's children by
// accident: two daemons sharing a directory would each write the same children's
// stdin.
const StateDirEnv = "CLAUDE_WRAPPER_STATE_DIR"

// SessionRecord is the durable half of a session.
//
// It is written by the daemon that spawned the child and read by whichever
// daemon starts next. Everything a successor needs to re-adopt the child is
// here: the pid to check liveness against, and the two endpoint paths. The
// child's own state is deliberately absent — that is rebuilt from
// events.jsonl, so there is exactly one source of truth for it and no second
// record that can disagree with the stream.
type SessionRecord struct {
	ID        string       `json:"id"`
	ChildPID  int          `json:"child_pid"`
	Cwd       string       `json:"cwd"`
	Options   SpawnOptions `json:"options"`
	StartedAt time.Time    `json:"started_at"`
	// Interrupted names the reason a drain could not reach this child's turn
	// boundary before its bound expired. Empty means it never was interrupted.
	//
	// It is written rather than inferred: a child that is mid-turn when the drain
	// gives up is not a child that failed, and the next reader has to be able to
	// tell the two apart without a log line.
	Interrupted string `json:"interrupted,omitempty"`
	// Question is the permission request the child is parked on, when it is
	// parked.
	//
	// It is persisted because the tool that asked outlives the daemon too: the
	// MCP stdio server is a grandchild of the child, it keeps polling for its
	// answer, and a successor that forgot the question would make a deliverable
	// answer undeliverable — the recorded-but-not-delivered defect this
	// repository exists to remove.
	Question *Question `json:"question,omitempty"`
}

// SessionFiles is one session's durable endpoints.
type SessionFiles struct {
	// Stdin is the write end of the child's stdin FIFO, opened O_RDWR so that
	// opening it never blocks and so the descriptor is never the last writer.
	Stdin *os.File
	// EventsPath is the append-only file carrying the child's stdout.
	EventsPath string
	// EventsWriter is the append-only handle handed to the child as its stdout.
	// It is nil on a re-opened (adopted) session, which only reads.
	EventsWriter *os.File
}

// SessionStore owns the per-session directory: the child's stdin FIFO, the
// append-only file its stdout lands in, and the record that lets the next daemon
// find both.
//
// The shape is what makes a child outlive its parent. Anonymous pipes cannot be
// re-attached, so both endpoints are named and both survive the daemon that
// created them: the FIFO because the child holds a write end of its own, the
// event file because it is a file.
type SessionStore interface {
	Dir(id string) string
	Create(ctx context.Context, record SessionRecord) (*SessionFiles, error)
	Open(ctx context.Context, id string) (*SessionFiles, error)
	Save(ctx context.Context, record SessionRecord) error
	// Update applies mutate to the stored record under the store's own lock, so a
	// read-modify-write cannot interleave with another one.
	Update(ctx context.Context, id string, mutate func(*SessionRecord)) error
	Load(ctx context.Context, id string) (*SessionRecord, error)
	List(ctx context.Context) ([]SessionRecord, error)
	Remove(ctx context.Context, id string) error
}

// NewSessionStore creates a store rooted at dir.
//
// dir defaults to $CLAUDE_WRAPPER_STATE_DIR/sessions and then to
// ~/.claude/state/claude-wrapper/sessions.
func NewSessionStore(dir string) SessionStore {
	return &sessionStore{root: dir}
}

type sessionStore struct {
	root string
	// mu serialises record writes. Callers persist from different goroutines — a
	// question from an HTTP handler, an interruption verdict from the drain — and
	// both do a read-modify-write of the same file.
	mu sync.Mutex
}

func (s *sessionStore) sessionsRoot(ctx context.Context) (string, error) {
	if s.root != "" {
		return s.root, nil
	}
	if env := os.Getenv(StateDirEnv); env != "" {
		return filepath.Join(env, "sessions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve home dir failed")
	}
	return filepath.Join(home, ".claude", "state", "claude-wrapper", "sessions"), nil
}

func (s *sessionStore) Dir(id string) string {
	root, err := s.sessionsRoot(context.Background())
	if err != nil {
		// A home directory that cannot be resolved leaves no sensible place to
		// write; the empty string makes Create fail loudly instead of writing into
		// the process's working directory.
		return ""
	}
	return filepath.Join(root, id)
}

func (s *sessionStore) Create(ctx context.Context, record SessionRecord) (*SessionFiles, error) {
	dir := s.Dir(record.ID)
	if dir == "" {
		return nil, errors.Errorf(ctx, "session state directory is not resolvable")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.Wrapf(ctx, err, "create session dir %q failed", dir)
	}
	stdinPath := filepath.Join(dir, sessionStdinName)
	if err := syscall.Mkfifo(stdinPath, 0o600); err != nil && !os.IsExist(err) {
		return nil, errors.Wrapf(ctx, err, "create stdin fifo %q failed", stdinPath)
	}
	// O_RDWR, not O_WRONLY: opening a FIFO for writing blocks until a reader
	// exists, and the reader here is the child, which is not started yet. O_RDWR
	// never blocks and gives this process both ends, so this descriptor is never
	// the last writer even before the child holds its own.
	stdin, err := os.OpenFile(stdinPath, os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "open stdin fifo %q failed", stdinPath)
	}
	eventsPath := filepath.Join(dir, sessionEventsName)
	events, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		_ = stdin.Close()
		return nil, errors.Wrapf(ctx, err, "open events file %q failed", eventsPath)
	}
	if err := s.Save(ctx, record); err != nil {
		_ = stdin.Close()
		_ = events.Close()
		return nil, err
	}
	return &SessionFiles{Stdin: stdin, EventsPath: eventsPath, EventsWriter: events}, nil
}

func (s *sessionStore) Open(ctx context.Context, id string) (*SessionFiles, error) {
	dir := s.Dir(id)
	if dir == "" {
		return nil, errors.Errorf(ctx, "session state directory is not resolvable")
	}
	stdinPath := filepath.Join(dir, sessionStdinName)
	stdin, err := os.OpenFile(stdinPath, os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "open stdin fifo %q failed", stdinPath)
	}
	return &SessionFiles{Stdin: stdin, EventsPath: filepath.Join(dir, sessionEventsName)}, nil
}

func (s *sessionStore) Save(ctx context.Context, record SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(ctx, record)
}

// saveLocked writes one record; the caller holds s.mu.
//
// The temp file carries a random suffix rather than a fixed one. A shared
// "record.json.tmp" let two concurrent saves open the same path with O_TRUNC and
// write at the same offsets, which produces an interleaved, unparseable record —
// and List skips a record it cannot parse, so the child would become unadoptable
// and its event history orphaned. Rename is atomic, so the last writer wins
// whole.
func (s *sessionStore) saveLocked(ctx context.Context, record SessionRecord) error {
	dir := s.Dir(record.ID)
	if dir == "" {
		return errors.Errorf(ctx, "session state directory is not resolvable")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal session record %q failed", record.ID)
	}
	path := filepath.Join(dir, sessionRecordName)
	tmp, err := os.CreateTemp(dir, sessionRecordName+".*.tmp")
	if err != nil {
		return errors.Wrapf(ctx, err, "create session record temp for %q failed", record.ID)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return errors.Wrapf(ctx, err, "write session record %q failed", tmpPath)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return errors.Wrapf(ctx, err, "close session record %q failed", tmpPath)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return errors.Wrapf(ctx, err, "rename session record %q failed", path)
	}
	return nil
}

// Update applies mutate to the stored record under the store's lock.
//
// The lock spans the whole read-modify-write on purpose. Two callers that each
// loaded, mutated and saved would both succeed and the later write would drop
// the earlier one's field, so a question persisted just before the drain's
// interruption verdict would vanish — and an answer that arrives during the
// restart gap would then be undeliverable.
func (s *sessionStore) Update(
	ctx context.Context,
	id string,
	mutate func(*SessionRecord),
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.Load(ctx, id)
	if err != nil {
		return err
	}
	mutate(record)
	return s.saveLocked(ctx, *record)
}

func (s *sessionStore) Load(ctx context.Context, id string) (*SessionRecord, error) {
	path := filepath.Join(s.Dir(id), sessionRecordName)
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read session record %q failed", path)
	}
	var record SessionRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		return nil, errors.Wrapf(ctx, err, "parse session record %q failed", path)
	}
	return &record, nil
}

func (s *sessionStore) List(ctx context.Context) ([]SessionRecord, error) {
	root, err := s.sessionsRoot(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.Wrapf(ctx, err, "read session root %q failed", root)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	records := make([]SessionRecord, 0, len(names))
	for _, name := range names {
		record, loadErr := s.Load(ctx, name)
		if loadErr != nil {
			// A directory with no readable record is not a session; it is either a
			// half-written spawn or debris. Skipping it is the difference between a
			// restart that adopts what it can and one that refuses to start.
			continue
		}
		records = append(records, *record)
	}
	return records, nil
}

func (s *sessionStore) Remove(ctx context.Context, id string) error {
	if err := os.RemoveAll(s.Dir(id)); err != nil {
		return errors.Wrapf(ctx, err, "remove session dir %q failed", id)
	}
	return nil
}
