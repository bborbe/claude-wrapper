// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// SpawnOptions is what a caller may ask for when starting a child.
type SpawnOptions struct {
	Cwd              string `json:"cwd"`
	Model            string `json:"model"`
	PermissionMode   string `json:"permission_mode"`
	PermissionPrompt string `json:"permission_prompt"`
	// Env is merged OVER this process's environment, never substituted for it.
	//
	// A caller that resolved an environment for the child — a mode handover, a
	// router base URL, a per-call override — has no other way to hand it over,
	// and the loss is invisible from the outside: the child starts, answers and
	// behaves differently. Replacing the environment instead of merging would
	// drop PATH and HOME and break the child outright, which is why the
	// inherited half is always kept.
	Env map[string]string `json:"env"`
	// Resume continues an existing conversation instead of starting one.
	//
	// The session's id is then the RESUMED id, so the record id and the child's
	// claude session id are the same value in both spawn shapes. Resuming an id
	// whose child is still running is refused (ErrSessionLive).
	Resume string `json:"resume"`
	// MCPConfigs are additional MCP config files, loaded alongside the generated
	// one.
	//
	// This is what makes a launcher's own config reachable: it arrives as a CLI
	// flag, not as a setting, so no setting source can reach it, and a child
	// without it has measurably fewer servers than its hand-started twin.
	MCPConfigs []string `json:"mcp_configs"`
	// Label is the agent's human name, carried into the permissions record so a
	// decision is attributable without a second lookup.
	Label string `json:"label"`
	// Policy is a per-spawn policy overlay path, evaluated ahead of the user
	// overlay and the bundled defaults.
	//
	// A relative path resolves against the child's cwd, the same rule the node
	// server applies. A file that cannot be read refuses the spawn rather than
	// quietly becoming "no override".
	Policy string `json:"policy"`
	// Mode is "headless" or "interactive". Empty means headless, which is what
	// every wrapper child is today.
	//
	// ⚠️ It gates ONE thing: a board answer may release a `permission` gate only
	// for a headless producer. A tab worker answers its own prompts in its tab, so
	// a board click for one is a defect rather than an answer — and a permission
	// approval EXECUTES a command.
	Mode string `json:"mode"`
}

// Supervisor owns the claude children this process spawned.
//
// It owns them deliberately: a store that observes someone else's session can
// report a question, but only the process holding the child can answer it.
type Supervisor interface {
	Spawn(ctx context.Context, options SpawnOptions) (*Session, error)
	List(ctx context.Context) ([]Session, error)
	Get(ctx context.Context, id string) (*Session, error)
	Send(ctx context.Context, id string, text string) error
	// SetPermissionMode changes the permission mode of a running child and
	// returns the mode the child itself reports for it.
	//
	// The caller must hold the session lock: a control request written between
	// two steps of a sequence would land in the middle of a run.
	SetPermissionMode(ctx context.Context, id string, mode string) (string, error)
	Answer(ctx context.Context, id string, requestID string, answer Answer) error
	RegisterQuestion(ctx context.Context, id string, question Question) error
	// ForgetQuestion drops a request the child has stopped waiting on.
	//
	// It must be called when the child-facing tool's hold expires: without it the
	// session keeps reporting a question nothing is waiting on, so every later
	// read says "parked" and every later send returns that stale request instead
	// of awaiting its own turn.
	ForgetQuestion(ctx context.Context, id string, requestID string) error
	PollAnswer(ctx context.Context, requestID string) (PendingAnswer, error)
	Stop(ctx context.Context, id string) error
	Subscribe(ctx context.Context, id string) ([]Event, <-chan Event, func(), error)
	// SubscribeFrom streams a child's events from a resume point, so a client
	// whose connection broke can continue where it stopped instead of replaying
	// the session and re-delivering events it already consumed.
	//
	// from is a sequence number previously handed out in a frame id; a negative
	// value means "from the start of what is retained". ErrStreamGap reports that
	// the requested point is no longer retained, which a caller must surface
	// rather than silently substituting a different one.
	SubscribeFrom(ctx context.Context, id string, from int) (
		replay []Event, firstSeq int, live <-chan Event, cancel func(), err error)
	// Adopt registers every child a previous daemon left running, so a restart
	// does not lose the sessions streaming across it. It returns how many were
	// adopted.
	Adopt(ctx context.Context) (int, error)
	// Drain waits for each child to reach a turn boundary, up to bound, and
	// reports what it found. It never kills a child.
	Drain(ctx context.Context, bound time.Duration) DrainReport
}

// NewSupervisor creates a Supervisor that runs the given claude binary.
//
// selfURL is this process's own base URL (e.g. "http://localhost:18090"); every
// child's permission tool is pointed at it so questions come back here.
//
// The supervisor deliberately takes no context. A child is bound to a durable
// event file and a FIFO, not to this process, so nothing about it should be
// cancelled when this process's own work ends — an earlier version bound the
// child to a process context and every daemon shutdown killed every worker.
func NewSupervisor(
	claudeBin string,
	now libtime.CurrentDateTimeGetter,
	selfURL string,
	store SessionStore,
) Supervisor {
	return NewSupervisorWithPolicy(
		context.Background(), claudeBin, now, selfURL, store, SupervisorPolicy{})
}

// SupervisorPolicy wires the in-process approval policy into a supervisor.
//
// It is a separate argument rather than a constructor option so a caller that
// has no policy (every existing test) keeps the old two-argument behaviour and
// gets an undecided park, which is what it had before this slice.
type SupervisorPolicy struct {
	// Decider applies the rules and writes the permissions record. Nil leaves
	// every park for the manager.
	Decider *PermissionDecider
	// PermissionTimeout is how long a headless park waits before it is denied.
	//
	// The wrapper is the only process that can release a headless child's park,
	// so this is where the 15-minute window the node server enforced now lives.
	// Zero disables the timeout entirely.
	PermissionTimeout time.Duration
}

// NewSupervisorWithPolicy creates a Supervisor with an approval policy.
func NewSupervisorWithPolicy(
	ctx context.Context,
	claudeBin string,
	now libtime.CurrentDateTimeGetter,
	selfURL string,
	store SessionStore,
	policy SupervisorPolicy,
) Supervisor {
	return &supervisor{
		ctx:       ctx,
		claudeBin: claudeBin,
		now:       now,
		selfURL:   selfURL,
		store:     store,
		mutex:     sync.RWMutex{},
		sessions:  map[string]*child{},
		pending:   NewPending(),
		policy:    policy,
	}
}

type supervisor struct {
	// ctx is the process lifetime. Goroutines the supervisor starts on its own
	// behalf — the policy timeout, the drain — read it so a draining daemon stops
	// them rather than leaving them to fire against a dead process.
	ctx       context.Context
	claudeBin string
	now       libtime.CurrentDateTimeGetter
	// selfURL is this process's own base URL, handed to every child's
	// permission tool so it reports its questions HERE.
	//
	// It is a field rather than the constant it used to be: a wrapper started on
	// any other port gave its children `http://localhost:18090`, so the child
	// registered its question against a different process — silently, because the
	// tool only logs a failed registration. Measured 2026-10-29 on a :18091
	// instance, where the child's question never reached the wrapper that spawned
	// it.
	selfURL string
	// store owns the durable half: the child's endpoints and the record that lets
	// a successor find them.
	store    SessionStore
	mutex    sync.RWMutex
	sessions map[string]*child
	// starting holds the ids of resume spawns in flight; see reserve.
	starting map[string]struct{}
	pending  Pending
	// policy is the in-process approval policy; the zero value leaves every park
	// for the manager.
	policy SupervisorPolicy
}

// Spawn starts one claude child in stream-json duplex mode.
//
// --include-partial-messages is load-bearing rather than cosmetic: without it
// the stream carries only completed assistant messages, so an open turn is not
// observable and State can only ever read idle between turns. The partial
// stream events are what make StateGenerating distinguishable from StateIdle.
//
// A resume is keyed by the RESUMED id, not a fresh one: one claude conversation
// is one record, however often it is resumed. Minting a new id per resume left
// the original record behind as `exited` under the claude session id while the
// live question sat under an id nothing else knew — so every reader that looked
// a session up by its claude id (the attention watcher, the board) found the
// dead record and refused an answer that was deliverable. Measured 2026-11-05 on
// session 5cceb1ea, whose live question sat under 127ca1b0.
//
// An exited record under that id is replaced. A live one is refused: two
// children on one conversation would both write its transcript, and the second
// would silently take over the first's record and question.
//
// ⚠️ Known window: the exited child's `--mcp-stdio` grandchild carries the same
// --session-id. It exits on its parent's stdin EOF, but a registration it had
// already in flight can land on the replacing record, where it used to hit the
// exited one and get 410. Such a question names a gate no running child waits
// on; answering it resolves nothing. Closing it needs a per-child generation in
// the MCP args, which is not worth the argv change for a sub-second window.
func (s *supervisor) Spawn(ctx context.Context, options SpawnOptions) (*Session, error) {
	id := newID()
	if options.Resume != "" {
		id = options.Resume
		if err := s.reserve(ctx, id); err != nil {
			return nil, err
		}
		defer s.release(id)
	}
	c, err := s.startChild(ctx, id, options)
	if err != nil {
		return nil, err
	}
	s.mutex.Lock()
	s.sessions[id] = c
	s.mutex.Unlock()
	return c.snapshot(), nil
}

// reserve claims id for one resume spawn, refusing when a live child or another
// in-flight spawn already holds it. The claim is held across startChild, which
// runs outside the lock, so two concurrent resumes of one conversation cannot
// both pass the check.
func (s *supervisor) reserve(ctx context.Context, id string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.starting == nil {
		s.starting = map[string]struct{}{}
	}
	if _, busy := s.starting[id]; busy {
		return errors.Wrapf(ctx, ErrSessionLive, "session %q is already being resumed", id)
	}
	if c, ok := s.sessions[id]; ok && !c.isExited() {
		return errors.Wrapf(ctx, ErrSessionLive, "session %q is still running", id)
	}
	s.starting[id] = struct{}{}
	return nil
}

func (s *supervisor) release(id string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	delete(s.starting, id)
}

func (s *supervisor) List(ctx context.Context) ([]Session, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	result := make([]Session, 0, len(s.sessions))
	for _, c := range s.sessions {
		result = append(result, *c.snapshot())
	}
	return result, nil
}

func (s *supervisor) Get(ctx context.Context, id string) (*Session, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	c, ok := s.sessions[id]
	if !ok {
		return nil, errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	return c.snapshot(), nil
}

// Subscribe streams one child's events: the replay of what it has already
// emitted, then everything that follows.
//
// The channel closes when the child exits, or when the caller falls more than
// subscriberBuffer events behind — the two cases are deliberately the same
// signal, because in both the stream has stopped being a faithful account of
// the child. Cancel must be called once the caller stops reading; it is safe to
// call more than once.
func (s *supervisor) Subscribe(ctx context.Context, id string) ([]Event, <-chan Event, func(), error) {
	replay, _, live, cancel, err := s.SubscribeFrom(ctx, id, -1)
	return replay, live, cancel, err
}

// SubscribeFrom streams one child's events from a resume point.
//
// firstSeq is the sequence number of replay[0]; a caller labels the events that
// follow the replay by counting up from firstSeq+len(replay). The child hands out
// no per-event number because Event is the child's own JSON, kept verbatim, and a
// supervision field smuggled into it would be a lie about what the child said.
func (s *supervisor) SubscribeFrom(
	ctx context.Context,
	id string,
	from int,
) ([]Event, int, <-chan Event, func(), error) {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return nil, 0, nil, nil, errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	return c.subscribeFrom(from)
}

// Adopt registers every child a previous daemon left running.
//
// It is what makes a restart cost a turn boundary rather than a turn. Each
// record names a pid and two endpoint paths; a pid that is still alive is a child
// that survived, and its state is rebuilt by replaying the event file from the
// beginning — state is derived from events, so a replay reconstructs exactly what
// the previous daemon reported, including a turn left open across the restart.
func (s *supervisor) Adopt(ctx context.Context) (int, error) {
	records, err := s.store.List(ctx)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "list session records failed")
	}
	adopted := 0
	for _, record := range records {
		if record.ID == "" {
			continue
		}
		if !processAlive(record.ChildPID) {
			// The child left while no daemon was watching. Its record has nothing
			// left to serve — no stream, no stdin — so keeping it would make every
			// later list report a session that cannot be reached.
			if removeErr := s.store.Remove(ctx, record.ID); removeErr != nil {
				log.Printf("WARNING: prune session %s failed: %v", record.ID, removeErr)
			}
			continue
		}
		files, openErr := s.store.Open(ctx, record.ID)
		if openErr != nil {
			log.Printf("WARNING: adopt session %s failed: %v", record.ID, openErr)
			continue
		}
		c := &child{
			id:          record.ID,
			pid:         record.ChildPID,
			now:         s.now,
			stdin:       files.Stdin,
			eventsPath:  files.EventsPath,
			subscribers: map[int]chan Event{},
			tailDone:    make(chan struct{}),
			session: Session{
				ID:        record.ID,
				State:     StateStarting,
				Controls:  []Control{ControlMessage},
				Cwd:       record.Cwd,
				StartedAt: record.StartedAt,
			},
		}
		if record.Question != nil {
			// The tool that asked is a grandchild of the child, so it survived the
			// restart too and is still polling for its answer. Restoring the question
			// is what keeps that answer deliverable.
			question := *record.Question
			c.session.Question = &question
			c.session.Controls = []Control{ControlAnswer, ControlMessage}
			s.pending.Register(question.RequestID, question)
		}
		s.mutex.Lock()
		s.sessions[record.ID] = c
		s.mutex.Unlock()
		go c.tail()
		go c.watch()
		adopted++
	}
	return adopted, nil
}

// DrainReport is what a shutdown found when it waited for turn boundaries.
type DrainReport struct {
	// Sessions is one row per child the daemon was holding when it drained.
	Sessions []DrainResult `json:"sessions"`
}

// DrainResult is one child's drain outcome.
type DrainResult struct {
	ID    string `json:"id"`
	State State  `json:"state"`
	// WaitedMillis is how long the drain held this child before giving up.
	WaitedMillis int64 `json:"waited_millis"`
	// Interrupted is true when the bound expired while the child was still
	// mid-turn, and Reason names why.
	//
	// Interrupted is a statement about the DRAIN, not about the child: the child
	// keeps running and is adopted by the next daemon. Recording it is what keeps
	// "the restart cost a turn boundary" and "the restart cut a turn short" from
	// reading the same afterwards.
	Interrupted bool   `json:"interrupted"`
	Reason      string `json:"reason,omitempty"`
}

// Drain waits for every child to reach a turn boundary, up to bound, and reports
// what it found. It never kills a child.
//
// The children are not this drain's to end: each one owns a durable event file
// and a FIFO, so it survives the daemon by construction. What a drain buys is the
// difference between a restart that lands between turns and one that lands inside
// one — the second is not an error, but a caller that asked for a clean stop is
// entitled to know which happened.
func (s *supervisor) Drain(ctx context.Context, bound time.Duration) DrainReport {
	s.mutex.RLock()
	children := make([]*child, 0, len(s.sessions))
	for _, c := range s.sessions {
		children = append(children, c)
	}
	s.mutex.RUnlock()

	results := make([]DrainResult, len(children))
	var waitGroup sync.WaitGroup
	for index, c := range children {
		waitGroup.Add(1)
		go func(index int, c *child) {
			defer waitGroup.Done()
			results[index] = s.drainOne(ctx, c, bound)
		}(index, c)
	}
	waitGroup.Wait()
	return DrainReport{Sessions: results}
}

// drainOne waits for one child's turn boundary.
func (s *supervisor) drainOne(ctx context.Context, c *child, bound time.Duration) DrainResult {
	deadline := time.Now().Add(bound)
	for {
		snapshot := c.snapshot()
		if snapshot.State != StateGenerating && snapshot.PendingTurns == 0 {
			return DrainResult{ID: c.id, State: snapshot.State}
		}
		if time.Now().After(deadline) {
			reason := fmt.Sprintf("drain bound %s expired with the turn still open", bound)
			if err := s.recordInterruption(ctx, c.id, reason); err != nil {
				log.Printf("WARNING: record interruption for %s failed: %v", c.id, err)
			}
			c.mutex.Lock()
			c.session.Interrupted = reason
			c.mutex.Unlock()
			return DrainResult{
				ID:           c.id,
				State:        snapshot.State,
				WaitedMillis: bound.Milliseconds(),
				Interrupted:  true,
				Reason:       reason,
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// recordInterruption writes the drain's verdict into the session's durable
// record, so it survives the daemon that wrote it.
func (s *supervisor) recordInterruption(ctx context.Context, id string, reason string) error {
	record, err := s.store.Load(ctx, id)
	if err != nil {
		return err
	}
	record.Interrupted = reason
	return s.store.Save(ctx, *record)
}

// persistQuestion writes the session's current question into its record, so a
// successor can restore it.
func (s *supervisor) persistQuestion(ctx context.Context, id string, question *Question) {
	record, err := s.store.Load(ctx, id)
	if err != nil {
		return
	}
	record.Question = question
	if saveErr := s.store.Save(ctx, *record); saveErr != nil {
		log.Printf("WARNING: persist question for %s failed: %v", id, saveErr)
	}
}

func (s *supervisor) Answer(ctx context.Context, id string, requestID string, answer Answer) error {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	if err := s.pending.Resolve(ctx, requestID, answer); err != nil {
		return err
	}
	c.clearQuestion(requestID)
	s.persistQuestion(ctx, id, c.snapshot().Question)
	return nil
}

// syntheticModel is the message.model the child reports for a turn a local
// slash command answered instead of a model.
//
// Measured 2026-10-29: `/model <name>` written as a user turn produces an
// assistant event whose message.model is exactly this string. Storing it would
// replace the last real model with a value that names no model at all, so every
// later read would report it as the one that answered.
const syntheticModel = "<synthetic>"

// modelProbeTurn is written after a model change to obtain the new model's
// first real answer.
//
// A change cannot read its own effect: the `/model` command's own turn is
// synthetic. One cheap turn makes the read-back the next assistant event rather
// than an inference from the caller's argument.
const modelProbeTurn = "Reply with only the word OK."

// controlRequestTimeout bounds one control exchange. The child answers a
// set_permission_mode request immediately, so a silence here is a child that
// will never answer and must surface rather than hang the caller.
const controlRequestTimeout = 30 * time.Second

// Send writes one user turn to the child's stdin.
//
// Without this the wrapper can start a child and watch it, but never make it
// do anything — and a child that is never asked never reaches a permission
// gate, so the question path could not be exercised at all.
func (s *supervisor) Send(ctx context.Context, id string, text string) error {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	return c.send(ctx, text)
}

// SetPermissionMode changes the permission mode of a running child and returns
// the mode the child itself reports for it.
func (s *supervisor) SetPermissionMode(
	ctx context.Context,
	id string,
	mode string,
) (string, error) {
	if !isPermissionMode(mode) {
		return "", errors.Wrapf(ctx, ErrInvalidSessionChange,
			"permission mode %q is not one of %v", mode, AvailablePermissionModes())
	}
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return "", errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	return c.setPermissionMode(ctx, mode)
}

// RegisterQuestion records a permission request raised by the child's MCP tool.
func (s *supervisor) RegisterQuestion(ctx context.Context, id string, question Question) error {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	if c.isExited() {
		return errors.Wrapf(ctx, ErrSessionExited, "session %q exited", id)
	}
	question.RequestedAt = s.now.Now().Time()
	s.pending.Register(question.RequestID, question)
	c.setQuestion(question)
	// Persisted, because the question has to be answerable after a restart: the
	// tool that raised it is a grandchild of the child and outlives the daemon, so
	// a successor that forgot the request would make a deliverable answer
	// undeliverable.
	s.persistQuestion(ctx, id, &question)
	// A park the policy already released needs no timeout: scheduling one would
	// leave a goroutine holding a 15-minute timer for the common case.
	if !s.applyPolicy(ctx, id, c, question) {
		s.scheduleTimeout(id, c, question)
	}
	return nil
}

// resolvePolicy builds the rule set a child is spawned under.
//
// A relative per-spawn path resolves against the child's cwd, the same rule the
// node server applies. An unreadable overlay refuses the spawn: a typo must not
// quietly become "no override", which would apply the bundled rules to a worker
// the operator deliberately narrowed.
func (s *supervisor) resolvePolicy(
	ctx context.Context,
	options SpawnOptions,
) (PolicyContext, error) {
	// ⚠️ An empty cwd is resolved to this process's working directory rather than
	// left empty: `match: "cwd"` compares against it, so an empty one makes every
	// `Write`/`Edit` escalate — the bundled defaults would silently stop applying
	// to the workers they were written for.
	cwd := options.Cwd
	if cwd == "" {
		if resolved, err := os.Getwd(); err == nil {
			cwd = resolved
		}
	}
	spawnPath := options.Policy
	if spawnPath != "" && !filepath.IsAbs(spawnPath) && cwd != "" {
		spawnPath = filepath.Join(cwd, spawnPath)
	}
	rules, err := ResolvePolicy(ctx, spawnPath)
	if err != nil {
		return PolicyContext{}, errors.Wrapf(ctx, err, "resolve policy for spawn failed")
	}
	return PolicyContext{
		Label:      options.Label,
		Cwd:        cwd,
		PolicyPath: spawnPath,
		Rules:      rules,
	}, nil
}

// applyPolicy decides a permission park in-process.
//
// ⚠️ The decision happens HERE, not on a poll. This process holds the park, so
// an allow or deny is recorded and answered before the child's tool has made its
// first read — which is what removes the node server's 2-second interval from
// the latency of a policy-allowed prompt.
func (s *supervisor) applyPolicy(ctx context.Context, id string, c *child, question Question) bool {
	if s.policy.Decider == nil || !IsPermissionQuestion(question) {
		return false
	}
	verdict := s.policy.Decider.Decide(ctx, id, c.policy, question)
	if verdict.Action != ActionAllow && verdict.Action != ActionDeny {
		// Escalate: leave the park exactly where it is, for the manager — and
		// record it, or the log reads as "the policy saw nothing".
		s.policy.Decider.RecordDecided(ctx, id, c.policy, question, verdict)
		return false
	}
	behavior := string(verdict.Action)
	// ⚠️ The answer's kind must match the park's. A question-class park answered
	// with a permission-kind answer is refused by the registry, which would leave
	// the park open while the log claimed it was released.
	kind := question.Kind
	if kind == "" {
		kind = AnswerKindPermission
	}
	if err := s.Answer(ctx, id, question.RequestID, Answer{
		Kind:     kind,
		Behavior: behavior,
	}); err != nil {
		log.Printf(
			"WARNING: policy %s for %s could not be delivered: %v",
			behavior, question.RequestID, err,
		)
		return false
	}
	// Recorded only now: the row means the policy released this park, so it must
	// follow the delivery rather than precede it.
	s.policy.Decider.RecordDecided(ctx, id, c.policy, question, verdict)
	matched := ""
	if rule := verdict.MatchedRule(); rule != nil {
		matched = *rule
	}
	log.Printf("policy %s %s for %s (%s)", behavior, question.ToolName, id, matched)
	return true
}

// scheduleTimeout denies a park nobody answered in time.
//
// The window is the node server's 15 minutes, and it lives here because this is
// the only process that can release a wrapper child's park. A timeout writes its
// own record: a deny that reached no log would be indistinguishable from a policy
// deny, which is the attribution gap `decided_by` exists to close.
// scheduleTimeout denies a park nothing settled, once the policy's window expires.
//
// ⚠️ It takes no caller context, deliberately. `RegisterQuestion` is called with
// the POST /question request's context, which is cancelled the moment that
// handler returns — watching it would end the timer immediately and the deny
// would never fire, leaving the child's own tool hold to answer `allow` by
// default. The timer is tied to the supervisor's lifetime and nothing else.
func (s *supervisor) scheduleTimeout(
	id string,
	c *child,
	question Question,
) {
	if s.policy.PermissionTimeout <= 0 || s.policy.Decider == nil {
		return
	}
	if question.ToolName == askUserQuestionToolName {
		// A question's timeout is the child tool's own hold; the wrapper only
		// times out a permission park, which is the one the policy can deny.
		return
	}
	requestedAt := s.now.Now().Time()
	go func() {
		timer := time.NewTimer(s.policy.PermissionTimeout)
		defer timer.Stop()
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
		}
		pending, err := s.pending.Peek(question.RequestID)
		if err != nil || pending.Answered || pending.Question.RequestID == "" {
			return
		}
		if err := s.Answer(s.ctx, id, question.RequestID, Answer{
			Kind:     AnswerKindPermission,
			Behavior: "deny",
		}); err != nil {
			log.Printf("WARNING: permission timeout for %s could not be delivered: %v",
				question.RequestID, err)
			return
		}
		latency := int(s.now.Now().Time().Sub(requestedAt).Milliseconds())
		s.policy.Decider.RecordSettled(
			s.ctx, id, c.policy, question,
			"deny", DecidedByTimeout, "", &latency,
		)
		log.Printf("permission timeout: denied %s for %s after %s",
			question.ToolName, id, s.policy.PermissionTimeout)
	}()
}

// PollAnswer reports whether the pending request has been answered yet.
func (s *supervisor) PollAnswer(ctx context.Context, requestID string) (PendingAnswer, error) {
	return s.pending.Peek(requestID)
}

// ForgetQuestion drops a request nothing is waiting on any more.
func (s *supervisor) ForgetQuestion(ctx context.Context, id string, requestID string) error {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	s.pending.Forget(requestID)
	c.clearQuestion(requestID)
	s.persistQuestion(ctx, id, c.snapshot().Question)
	return nil
}

func (s *supervisor) Stop(ctx context.Context, id string) error {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	return c.stop(ctx)
}

// newID returns a random UUID v4.
//
// It must be a UUID: `claude --session-id` rejects anything else outright
// ("Invalid session ID. Must be a valid UUID."), and the child exits before
// producing a single event — which reads as a spawn that silently failed.
func newID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is not recoverable in any useful way here.
		panic(err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func (s *supervisor) startChild(ctx context.Context, id string, options SpawnOptions) (*child, error) {
	// Validated first, so a refusal costs no temp file: writeMCPConfig below
	// already writes one, and a rejected spawn must leave nothing behind.
	if err := validateEnv(ctx, options.Env); err != nil {
		return nil, err
	}
	if err := validateArgv(ctx, options); err != nil {
		return nil, err
	}
	// Resolved BEFORE the child starts: an unreadable overlay must refuse the
	// spawn with no process behind it, not return an error while a live, unreaped
	// child holds stdin and an undrained stdout pipe.
	policy, err := s.resolvePolicy(ctx, options)
	if err != nil {
		return nil, err
	}
	mcpConfig, err := writeMCPConfig(ctx, id, s.selfURL, s.childHold())
	if err != nil {
		return nil, err
	}
	files, err := s.store.Create(ctx, SessionRecord{
		ID:        id,
		Cwd:       options.Cwd,
		Options:   options,
		StartedAt: s.now.Now().Time().UTC(),
	})
	if err != nil {
		return nil, err
	}
	// exec.Command, never exec.CommandContext: a child bound to a context dies
	// with it, and this child is supposed to outlive the daemon that spawned it.
	// That binding was the second half of the "every restart kills every worker"
	// defect — the first half was the daemon dying, which took the pipes with it.
	// #nosec G204 -- claudeBin is an operator-supplied flag, never request input.
	cmd := exec.Command(s.claudeBin, buildArgs(id, options, mcpConfig)...)
	cmd.Dir = options.Cwd
	// The child inherits THIS process's environment unless it is given its own,
	// so a caller's resolved environment would otherwise be discarded without a
	// word — the child starts, answers, and behaves differently.
	cmd.Env = childEnv(os.Environ(), options.Env)
	cmd.Stdin = files.Stdin
	cmd.Stdout = files.EventsWriter
	// The child is given its OWN write end of its own stdin, so a parent death
	// never produces an EOF on it. A FIFO reports EOF when its last writer closes,
	// and without this descriptor the daemon is that writer — so the child would
	// read EOF the instant the daemon died and finish its input stream, which is
	// how a survivor would still lose its turn. The descriptor is held, never
	// written: it exists only to keep the FIFO open from the child's own side.
	cmd.ExtraFiles = []*os.File{files.Stdin}
	// Setsid detaches the child from this process's session and process group, so
	// the group-wide signal launchd sends when the job stops cannot reach it. The
	// child has to outlive a `launchctl kickstart -k`, and that kill is delivered
	// to the group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = files.Stdin.Close()
		_ = files.EventsWriter.Close()
		_ = s.store.Remove(ctx, id)
		return nil, errors.Wrapf(ctx, err, "start claude failed")
	}
	// The child holds its own copy of both endpoints now, so the spawning side
	// closes its stdout handle: a regular file needs no reader-side keep-alive, and
	// leaving it open would keep the file alive for the daemon's lifetime for no
	// benefit.
	_ = files.EventsWriter.Close()

	record := SessionRecord{
		ID:        id,
		ChildPID:  cmd.Process.Pid,
		Cwd:       options.Cwd,
		Options:   options,
		StartedAt: s.now.Now().Time().UTC(),
	}
	if err := s.store.Save(ctx, record); err != nil {
		// The child is already running and adoptable by its pid only if the record
		// says so; a record that cannot be written means the next daemon will not
		// find it. Killing it here would be the old defect, so it keeps running and
		// the loss is logged.
		log.Printf("WARNING: session %s is not adoptable: %v", id, err)
	}

	c := &child{
		id:          id,
		pid:         cmd.Process.Pid,
		now:         s.now,
		cmd:         cmd,
		stdin:       files.Stdin,
		eventsPath:  files.EventsPath,
		subscribers: map[int]chan Event{},
		tailDone:    make(chan struct{}),
		policy:      policy,
		session: Session{
			ID:        id,
			State:     StateStarting,
			Controls:  []Control{ControlMessage},
			Cwd:       options.Cwd,
			Mode:      sessionMode(options.Mode),
			StartedAt: s.now.Now().Time().UTC(),
		},
	}
	go c.tail()
	go c.watch()
	return c, nil
}

// DefaultPermissionMode is how a child is spawned unless the caller says
// otherwise.
//
// `auto` rather than `bypassPermissions`: auto approves what policy permits and
// routes the remainder to the permission-prompt tool, which is what makes a
// pending question observable at all. Measured 2026-10-28, `--permission-prompts
// host` emits no control_request on stdout or stderr, so a child that asks with
// no tool configured blocks until killed — the failure this default avoids.
const DefaultPermissionMode = "auto"

// writeMCPConfig points the child at this binary's stdio MCP mode.
//
// A permission-prompt tool has to resolve to a real MCP tool, so the child is
// given a generated config whose `command` is the running wrapper. The log path
// is what makes the tool's payload observable: its schema is undocumented, so
// the first runs record it verbatim rather than parsing a guess.
func writeMCPConfig(
	ctx context.Context,
	id string,
	selfURL string,
	hold time.Duration,
) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve own binary failed")
	}
	config := map[string]any{
		"mcpServers": map[string]any{
			"claude-wrapper": map[string]any{
				"command": self,
				"args": append([]string{
					"--mcp-stdio",
					"--mcp-log", filepath.Join(os.TempDir(), "claude-wrapper-"+id+".mcp.log"),
					"--wrapper-url", selfURL,
					"--session-id", id,
				}, holdArgs(hold)...),
			},
		},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "marshal mcp config failed")
	}
	path := filepath.Join(os.TempDir(), "claude-wrapper-"+id+".mcp.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return "", errors.Wrapf(ctx, err, "write mcp config failed")
	}
	return path, nil
}

// DefaultPermissionTimeout is how long an unanswered headless park waits before
// it is denied.
//
// The node server's value, kept because it is the number operators and runbooks
// already reason about.
const DefaultPermissionTimeout = 15 * time.Minute

// childHoldMargin keeps the child's own hold just past the daemon's timeout, so
// the daemon's deny is the verdict the tool reads rather than the tool giving up
// first with its own default.
const childHoldMargin = time.Minute

// childHold is how long a child's permission tool waits for an answer.
//
// Zero timeout (no policy wired) keeps the historical 90-second hold: with no
// daemon-side timeout there is nothing to wait for, and a child parked forever on
// an unanswered prompt is the failure the channel exists to avoid.
func (s *supervisor) childHold() time.Duration {
	if s.policy.PermissionTimeout <= 0 {
		return 90 * time.Second
	}
	return s.policy.PermissionTimeout + childHoldMargin
}

// sessionMode resolves a spawn's mode, defaulting to headless.
//
// Empty means headless because every child this daemon spawns today is one: a
// tab worker arrives with slice 2 and names itself then.
func sessionMode(mode string) string {
	if mode == "interactive" {
		return "interactive"
	}
	return "headless"
}

// holdArgs renders the child tool's hold as CLI arguments.
func holdArgs(hold time.Duration) []string {
	if hold <= 0 {
		return nil
	}
	return []string{"--mcp-hold", hold.String()}
}

// buildArgs pins the flags the wrapper depends on. The stream formats are
// --print-only, which is why phase 1 wraps only children it spawns.
func buildArgs(id string, options SpawnOptions, mcpConfig string) []string {
	mode := options.PermissionMode
	if mode == "" {
		mode = DefaultPermissionMode
	}
	args := []string{
		"--print",
		"--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
	}
	// Resuming and minting an id are mutually exclusive: the resumed
	// conversation already has one, and passing both would leave the CLI holding
	// two identities for a single conversation.
	if options.Resume != "" {
		args = append(args, "--resume", options.Resume)
	} else {
		args = append(args, "--session-id", id)
	}
	args = append(args,
		"--permission-mode", mode,
		// --mcp-config is variadic, so the extra files belong immediately after
		// the generated one and before the next flag.
		"--mcp-config", mcpConfig,
	)
	args = append(args, options.MCPConfigs...)
	args = append(args, "--permission-prompt-tool", "mcp__claude-wrapper__"+PermissionToolName)
	if options.Model != "" {
		args = append(args, "--model", options.Model)
	}
	if options.PermissionPrompt != "" {
		args = append(args, "--permission-prompts", options.PermissionPrompt)
	}
	return args
}

// validateEnv refuses an environment key that cannot be expressed as KEY=VALUE.
//
// Refused rather than filtered, on the same reasoning the option itself exists
// for: a key silently dropped is a value the child does not have, and a child
// missing one value looks exactly like one that was never given it.
//
// A NUL in the VALUE is refused too, and not for symmetry: execve treats each
// entry as an opaque NUL-terminated string, so a value containing one reaches
// os/exec and fails at cmd.Start with EINVAL — which surfaces as a 500 for what
// is plainly a caller error, the exact confusion the statusFor mapping exists to
// avoid.
func validateEnv(ctx context.Context, env map[string]string) error {
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return errors.Wrapf(ctx, ErrInvalidSpawnOptions, "env key %q is not a valid environment name", key)
		}
		if strings.ContainsRune(value, 0) {
			return errors.Wrapf(ctx, ErrInvalidSpawnOptions, "env value for %q contains a NUL byte", key)
		}
	}
	return nil
}

// uuidPattern is the shape `claude` accepts for a session id. newID mints one
// and its doc records the CLI's refusal ("Invalid session ID. Must be a valid
// UUID.") when the shape is wrong; `--resume` takes the same kind of value, so a
// malformed one has to be refused here rather than left to exit before producing
// a single event.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validateArgv refuses values that would not reach the child as the argument
// they look like.
//
// ⚠️ This is not cosmetic validation. `--mcp-config` is VARIADIC, so every entry
// is appended straight into argv — an entry beginning with `-` is parsed as a
// FLAG rather than a path, and `mcp_configs: ["--dangerously-skip-permissions"]`
// would silently change the child's permission posture instead of being refused.
// The same reasoning covers `resume`, which sits immediately after `--resume` and
// would otherwise consume the next flag as its value.
func validateArgv(ctx context.Context, options SpawnOptions) error {
	for _, config := range options.MCPConfigs {
		if config == "" {
			return errors.Wrapf(ctx, ErrInvalidSpawnOptions, "mcp_configs contains an empty entry")
		}
		if strings.HasPrefix(config, "-") {
			return errors.Wrapf(ctx, ErrInvalidSpawnOptions,
				"mcp config %q begins with '-' and would be parsed as a flag", config)
		}
		// Same reason as the env-value check: a NUL survives JSON, is rejected by
		// os/exec at cmd.Start, and surfaces as a 500 for what is plainly a caller
		// error. `resume` cannot carry one — uuidPattern already excludes it.
		if strings.ContainsRune(config, 0) {
			return errors.Wrapf(ctx, ErrInvalidSpawnOptions, "mcp config %q contains a NUL byte", config)
		}
	}
	if options.Resume != "" && !uuidPattern.MatchString(options.Resume) {
		return errors.Wrapf(ctx, ErrInvalidSpawnOptions, "resume %q is not a UUID", options.Resume)
	}
	return nil
}

// childEnv merges overrides over the inherited environment.
//
// An override REPLACES the inherited value for its key rather than appending a
// second copy: a duplicated key resolves to whichever copy the child reads
// first, which is not something a caller can observe from outside. Keys are
// emitted in sorted order so one spawn's environment is reproducible.
func childEnv(inherited []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return inherited
	}
	merged := make([]string, 0, len(inherited)+len(overrides))
	for _, entry := range inherited {
		if key, _, ok := strings.Cut(entry, "="); ok {
			if _, replaced := overrides[key]; replaced {
				continue
			}
		}
		merged = append(merged, entry)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		merged = append(merged, key+"="+overrides[key])
	}
	return merged
}

// child is one running claude process and the state read off its stream.
type child struct {
	mutex       sync.RWMutex
	writeMu     sync.Mutex
	id          string
	pid         int
	now         libtime.CurrentDateTimeGetter
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	eventsPath  string
	lastEventAt time.Time
	session     Session
	// events retains what the child has already emitted so a subscriber that
	// arrives mid-turn still sees the turn it missed.
	events []Event
	// firstSeq is the sequence number of events[0]. Sequence numbers are line
	// indexes in the event file, which is what makes them stable across a restart:
	// a resume point handed to a client before the restart still names the same
	// line afterwards.
	firstSeq int
	// subscribers holds one channel per live Subscribe caller, keyed by an id
	// that only exists so cancel can find its own entry.
	subscribers map[int]chan Event
	nextSubID   int
	// pendingTurns counts turns written here that the child has not closed.
	//
	// It is the only signal that covers the gap between our write and the
	// child's own message_start: State still reads idle in that window, so a
	// caller that wrote a turn would be invisible to anything reading State.
	pendingTurns int
	// procGone reports that the child's process is no longer running. The tailer
	// reads it to decide whether an empty read means "wait" or "finished", which is
	// what lets it drain the last events before the session is settled.
	procGone atomic.Bool
	// tailDone closes once the event tailer has returned, so the watcher settles
	// the session only after every event the child wrote has been applied.
	tailDone chan struct{}
	// policy is the rule set this child was SPAWNED under, held so a park is
	// judged by the overlay in force at spawn rather than by whatever the files
	// say when it parks.
	policy PolicyContext
}

func (c *child) snapshot() *Session {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	clone := c.session
	if c.session.Question != nil {
		question := *c.session.Question
		clone.Question = &question
	}
	clone.Controls = append([]Control{}, c.session.Controls...)
	clone.PendingTurns = c.pendingTurns
	if !c.lastEventAt.IsZero() {
		age := int(c.now.Now().Time().Sub(c.lastEventAt).Seconds())
		clone.LastEventAgeSeconds = &age
	}
	return &clone
}

func (c *child) stop(ctx context.Context) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.session.State == StateExited {
		return errors.Wrapf(ctx, ErrSessionExited, "session %q already exited", c.id)
	}
	// Signalled by pid rather than through *exec.Cmd: an adopted child was started
	// by a daemon that no longer exists, so this process holds no Cmd for it and
	// Process would be nil.
	if err := syscall.Kill(c.pid, syscall.SIGKILL); err != nil {
		return errors.Wrapf(ctx, err, "kill session %q failed", c.id)
	}
	return nil
}

// send writes one user turn to the child's stdin as JSONL.
func (c *child) send(ctx context.Context, text string) error {
	c.mutex.RLock()
	state := c.session.State
	c.mutex.RUnlock()
	if state == StateExited {
		return errors.Wrapf(ctx, ErrSessionExited, "session %q exited", c.id)
	}
	payload := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal message failed")
	}
	// Count the turn BEFORE the write, not after it.
	//
	// apply() takes only c.mutex, so a result applied between the write
	// returning and a later increment would find the counter at zero, skip its
	// decrement, and leave the count stuck at one for the child's lifetime —
	// refusing every subsequent run on a child that is actually idle. Counting
	// first makes the window impossible; a failed write rolls it back.
	c.mutex.Lock()
	c.pendingTurns++
	c.mutex.Unlock()

	c.writeMu.Lock()
	_, writeErr := c.stdin.Write(append(encoded, '\n'))
	c.writeMu.Unlock()
	if writeErr != nil {
		c.mutex.Lock()
		c.pendingTurns--
		c.mutex.Unlock()
		return errors.Wrapf(ctx, writeErr, "write message to session %q failed", c.id)
	}
	return nil
}

// setPermissionMode writes the SDK's own control request and waits for the
// child's answer.
//
// A permission mode is not reachable as a user turn: measured 2026-10-29, none
// of the child's 219 slash commands changes it, and send wraps text into a user
// message. The control channel is the only path, and the child answers it with
// the mode it now holds — which is the read-back, since this process stores no
// mode of its own.
func (c *child) setPermissionMode(ctx context.Context, mode string) (string, error) {
	if c.isExited() {
		return "", errors.Wrapf(ctx, ErrSessionExited, "session %q exited", c.id)
	}
	_, live, cancel := c.subscribe()
	defer cancel()

	requestID := "perm-mode-" + newID()
	if err := c.writeControl(ctx, requestID, map[string]any{
		"subtype": "set_permission_mode",
		"mode":    mode,
	}); err != nil {
		return "", err
	}

	waitCtx, cancelWait := context.WithTimeout(ctx, controlRequestTimeout)
	defer cancelWait()
	for {
		select {
		case <-waitCtx.Done():
			return "", errors.Wrapf(ctx, waitCtx.Err(),
				"waiting for the child to answer permission mode %q", mode)
		case event, open := <-live:
			if !open {
				return "", errors.Wrapf(ctx, ErrSessionExited,
					"stream closed before the child answered permission mode %q", mode)
			}
			reported, done, err := permissionModeAnswer(ctx, event, requestID, mode)
			if err != nil {
				return "", err
			}
			if done {
				return reported, nil
			}
		}
	}
}

// permissionModeAnswer reports whether one event answers the given request, and
// with which mode.
//
// It is a function rather than an inline block so the response shape is testable
// without a live child: the envelope is the SDK's, not this repo's, and a silent
// shape change must fail a test rather than read as a timeout.
func permissionModeAnswer(
	ctx context.Context,
	event Event,
	requestID string,
	mode string,
) (string, bool, error) {
	if event["type"] != "control_response" {
		return "", false, nil
	}
	response, ok := event["response"].(map[string]any)
	if !ok || response["request_id"] != requestID {
		return "", false, nil
	}
	if subtype, _ := response["subtype"].(string); subtype != "success" {
		return "", false, errors.Errorf(ctx, "child refused permission mode %q: %v", mode, response)
	}
	inner, _ := response["response"].(map[string]any)
	reported, _ := inner["mode"].(string)
	if reported == "" {
		return "", false, errors.Errorf(ctx, "child answered %q without reporting a mode", requestID)
	}
	return reported, true, nil
}

// writeControl writes one SDK control request as JSONL.
//
// Unlike send it does not touch pendingTurns: a control request is not a turn
// and the child never answers it with a result event, so counting it would leave
// the counter one too high for the child's lifetime and refuse every later run.
func (c *child) writeControl(ctx context.Context, requestID string, request map[string]any) error {
	encoded, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request":    request,
	})
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal control request failed")
	}
	c.writeMu.Lock()
	_, writeErr := c.stdin.Write(append(encoded, '\n'))
	c.writeMu.Unlock()
	if writeErr != nil {
		return errors.Wrapf(ctx, writeErr, "write control request to session %q failed", c.id)
	}
	return nil
}

func (c *child) isExited() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.session.State == StateExited
}

func (c *child) setQuestion(question Question) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	clone := question
	c.session.Question = &clone
	c.session.Controls = []Control{ControlAnswer, ControlMessage}
}

func (c *child) clearQuestion(requestID string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.session.Question != nil && c.session.Question.RequestID == requestID {
		c.session.Question = nil
	}
}

// tail reads the child's event file as it grows and folds each line into state.
func (c *child) tail() {
	defer close(c.tailDone)
	tailFile(context.Background(), c.eventsPath, func(seq int, line []byte) {
		if len(line) == 0 {
			return
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			return
		}
		c.apply(seq, event)
	}, func() bool { return c.procGone.Load() })
}

// watch waits for the child's process to leave and then settles the session.
//
// It waits for the tailer first: the process can exit while its last events are
// still unread, and settling before they are applied would report a session as
// exited without the result event that closed its turn.
func (c *child) watch() {
	var exitCode *int
	if c.cmd != nil {
		err := c.cmd.Wait()
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		exitCode = &code
	} else {
		// An adopted child is not this process's child, so there is no exit status
		// to collect — only a pid that stops existing. Reporting a code here would
		// invent one.
		for processAlive(c.pid) {
			time.Sleep(eventTailPoll)
		}
	}
	c.procGone.Store(true)
	<-c.tailDone

	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.session.State = StateExited
	// A child killed mid-turn closes nothing, so the counter would otherwise
	// stay positive for the life of the process and misreport the field.
	c.pendingTurns = 0
	c.session.Question = nil
	c.session.Controls = []Control{}
	c.session.ExitCode = exitCode
	c.closeSubscribers()
}

// apply is the single place an event becomes state. Every branch is derived
// from the event itself; none reads a clock.
func (c *child) apply(seq int, event map[string]any) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.lastEventAt = c.now.Now().Time()
	switch event["type"] {
	case "system":
		if event["subtype"] == "init" {
			c.applyInit(event)
		}
	case "assistant":
		if message, ok := event["message"].(map[string]any); ok {
			if model, ok := message["model"].(string); ok && model != "" && model != syntheticModel {
				c.session.Model = model
				c.session.ModelSource = "assistant"
			}
		}
	case "stream_event":
		c.applyStreamEvent(event)
	case "control_response":
		c.applyControlResponse(event)
	case "result":
		c.session.State = StateIdle
		if c.pendingTurns > 0 {
			c.pendingTurns--
		}
	}
	c.publish(seq, Event(event))
}

// publish retains one event for replay and hands it to every live subscriber.
//
// The caller must hold c.mutex — apply is the only caller, and it already does.
// Taking the lock here instead would deadlock, so the precondition is stated
// rather than enforced: a second caller that forgets it races the reader
// goroutine on c.events and c.subscribers.
//
// A subscriber that has fallen subscriberBuffer events behind is closed rather
// than skipped: a supervision stream with an invisible hole in it is worse than
// one that ends and says so.
func (c *child) publish(seq int, event Event) {
	if len(c.events) == 0 {
		c.firstSeq = seq
	}
	c.events = append(c.events, event)
	if len(c.events) > maxRetainedEvents {
		drop := len(c.events) - maxRetainedEvents
		c.events = append([]Event{}, c.events[drop:]...)
		c.firstSeq += drop
	}
	for id, ch := range c.subscribers {
		select {
		case ch <- event:
		default:
			close(ch)
			delete(c.subscribers, id)
		}
	}
}

// subscribe returns everything the child has emitted so far plus a channel
// carrying what follows. The replay is copied under the lock, so a caller never
// races the reader goroutine.
func (c *child) subscribe() ([]Event, <-chan Event, func()) {
	replay, _, live, cancel, _ := c.subscribeFrom(-1)
	return replay, live, cancel
}

// subscribeFrom returns the events from a resume point onwards.
//
// from is a sequence number this child handed out before; a negative value asks
// for everything retained. firstSeq is the sequence number of replay[0], so a
// caller can label each replayed event without the child having to carry a
// number on every event.
func (c *child) subscribeFrom(from int) ([]Event, int, <-chan Event, func(), error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if from >= 0 && from < c.firstSeq {
		return nil, 0, nil, nil, errors.Wrapf(context.Background(), ErrStreamGap,
			"session %q no longer retains events before %d", c.id, c.firstSeq)
	}
	start := 0
	if from > c.firstSeq {
		start = from - c.firstSeq
	}
	if start > len(c.events) {
		start = len(c.events)
	}
	replay := append([]Event{}, c.events[start:]...)
	firstSeq := c.firstSeq + start
	live := make(chan Event, subscriberBuffer)
	if c.session.State == StateExited {
		close(live)
		return replay, firstSeq, live, func() {}, nil
	}
	id := c.nextSubID
	c.nextSubID++
	c.subscribers[id] = live
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			c.mutex.Lock()
			defer c.mutex.Unlock()
			if existing, ok := c.subscribers[id]; ok {
				close(existing)
				delete(c.subscribers, id)
			}
		})
	}
	return replay, firstSeq, live, cancel, nil
}

// closeSubscribers ends every live stream. Called once the child is gone, so a
// consumer stops waiting on events that can no longer arrive.
func (c *child) closeSubscribers() {
	for id, ch := range c.subscribers {
		close(ch)
		delete(c.subscribers, id)
	}
}

func (c *child) applyInit(event map[string]any) {
	if model, ok := event["model"].(string); ok && model != "" && c.session.Model == "" {
		c.session.Model = model
		c.session.ModelSource = "init"
	}
	// The mode is taken from init on every turn, not only the first: init is
	// re-emitted for each turn, so a mode changed on a running child appears
	// here on the next one. Reading it only at startup would pin the field to
	// the spawn value and report a stale mode as current.
	if mode, ok := event["permissionMode"].(string); ok && mode != "" {
		c.session.PermissionMode = mode
	}
	if c.session.State == StateStarting {
		c.session.State = StateIdle
	}
}

// applyControlResponse stores the mode a set_permission_mode answer reports.
//
// Without this the change would be visible only in the HTTP response that asked
// for it: GET would keep reporting whatever the last init event said, which is
// the mode the child started with. Measured 2026-10-29 — a change answered
// `acceptEdits` left the session reading `default` until the next turn's init.
//
// A `mode` field is the discriminator rather than a request id: no other control
// response this wrapper reads carries one, and tracking ids here would duplicate
// the waiter that already does.
func (c *child) applyControlResponse(event map[string]any) {
	response, ok := event["response"].(map[string]any)
	if !ok {
		return
	}
	if subtype, _ := response["subtype"].(string); subtype != "success" {
		return
	}
	inner, ok := response["response"].(map[string]any)
	if !ok {
		return
	}
	if mode, ok := inner["mode"].(string); ok && mode != "" {
		c.session.PermissionMode = mode
	}
}

func (c *child) applyStreamEvent(event map[string]any) {
	inner, ok := event["event"].(map[string]any)
	if !ok {
		return
	}
	switch inner["type"] {
	case "message_start":
		c.session.State = StateGenerating
	case "message_stop":
		c.session.State = StateIdle
	}
}
