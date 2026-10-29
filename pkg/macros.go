// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"slices"
	"time"
	"unicode"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// Macro is one named, fixed sequence of user turns.
//
// The name is the whole API surface: a caller sends a name and the wrapper
// looks the list up in code. That is deliberate. The endpoint can drive
// /compact and /exit, so what it is able to run has to be reviewable as a
// diff rather than supplied at call time.
type Macro struct {
	// Name is the identifier a caller sends.
	Name string
	// Steps are the commands, in order.
	Steps []string
}

// RequiresConfirmation reports whether any step is production-touching.
//
// A macro that can rewrite a context or end a child is a remote kill switch,
// so it may not be started fire-and-forget.
func (m Macro) RequiresConfirmation() bool {
	return slices.ContainsFunc(m.Steps, productionTouchingCommands.Contains)
}

// StringList is an ordered set of strings.
type StringList []string

// Contains reports whether the list holds the given value.
func (l StringList) Contains(value string) bool {
	return slices.Contains(l, value)
}

// productionTouchingCommands are the steps that change what a live child will
// do next, rather than merely asking it a question.
var productionTouchingCommands = StringList{"/compact", "/clear", "/exit"}

// AvailableMacros returns every sequence the wrapper will run, in definition
// order, with each macro's steps copied.
//
// The table itself stays unexported: it is what decides which commands the
// endpoint can drive, so an importer able to rewrite it would defeat the
// reviewable-as-a-diff property the whole design rests on.
func AvailableMacros() []Macro {
	available := make([]Macro, 0, len(macros))
	for _, macro := range macros {
		available = append(available, Macro{Name: macro.Name, Steps: slices.Clone(macro.Steps)})
	}
	return available
}

// macros is every sequence the wrapper will run, in definition order.
//
// A slice rather than a map: a map would lose the order the steps are declared
// in, and a macro whose steps are unordered is not a macro.
var macros = []Macro{
	{
		Name:  "compact",
		Steps: []string{"/vault-cli:prepare-compact", "/compact", "/vault-cli:post-compact"},
	},
	{
		Name:  "terminal",
		Steps: []string{"/vault-cli:sync-progress", "/vault-cli:session-close", "/exit"},
	},
	// probe and probe-slow are the endpoint's own smoke test: a sequence with
	// no production-touching step, so it can be run against a real child to
	// show the advance rule without spending a session. probe-slow's middle
	// step deliberately takes half a minute, which is what makes a timer-driven
	// implementation distinguishable from this one.
	{
		Name:  "probe",
		Steps: []string{"/cost", "/cost", "/cost"},
	},
	{
		Name: "probe-slow",
		Steps: []string{
			"/cost",
			"Write the integers from 1 to 400 on one line, comma separated, then the word done.",
			"/cost",
		},
	},
}

// ValidateModelName refuses a model name that cannot be delivered safely as the
// child's own `/model` command.
//
// The name is interpolated into a user turn, so a control character is the only
// thing that could change what the child reads — everything else is ordinary
// text, and the turn is JSON-encoded either way. Refused rather than sanitised:
// a silently rewritten name would report a model the caller never asked for.
func ValidateModelName(ctx context.Context, model string) error {
	if model == "" {
		return errors.Wrapf(ctx, ErrInvalidSessionChange, "model must not be empty")
	}
	for _, r := range model {
		if unicode.IsControl(r) {
			return errors.Wrapf(ctx, ErrInvalidSessionChange,
				"model %q contains a control character", model)
		}
	}
	return nil
}

// ModelChangeMacro returns the sequence a model change runs.
//
// Two steps, not one: `/model <name>` is answered locally, so its own turn
// reports no model at all. The second step is what makes the new model's first
// real answer — and therefore the read-back — exist.
//
// It is built here rather than added to the macro table because it is not a
// named sequence a caller may invoke by name: the model is a parameter, and a
// table entry would have to be rewritten per call.
func ModelChangeMacro(model string) Macro {
	return Macro{
		Name:  "set-model",
		Steps: []string{"/model " + model, modelProbeTurn},
	}
}

// LookupMacro returns the macro with the given name.
func LookupMacro(name string) (Macro, error) {
	for _, macro := range macros {
		if macro.Name == name {
			return Macro{Name: macro.Name, Steps: slices.Clone(macro.Steps)}, nil
		}
	}
	return Macro{}, ErrUnknownMacro
}

// StepStatus is where one step of a run stands.
type StepStatus string

const (
	// StepNotRun is a step that was never written because the run stopped first.
	StepNotRun StepStatus = "not_run"
	// StepRunning is a step whose turn is open.
	StepRunning StepStatus = "running"
	// StepDone is a step whose turn closed.
	StepDone StepStatus = "done"
	// StepAborted is a step whose write failed or whose turn never closed.
	StepAborted StepStatus = "aborted"
)

// StepResult is one step's outcome, including the two timestamps that make the
// advance rule observable.
//
// WroteAt and ClosedAt come from one clock — this process's — so a consumer
// subtracts them without worrying about skew. The gap between step N's
// ClosedAt and step N+1's WroteAt is the advance latency: it stays near zero
// however long a turn takes, which is the number an ungated writer cannot
// produce.
type StepResult struct {
	Index    int               `json:"index"`
	Command  string            `json:"command"`
	Status   StepStatus        `json:"status"`
	WroteAt  *libtime.DateTime `json:"wrote_at,omitempty"`
	ClosedAt *libtime.DateTime `json:"closed_at,omitempty"`
}

// SequenceResult is what a run returns: every step, run or not.
type SequenceResult struct {
	Macro   string       `json:"macro"`
	Steps   []StepResult `json:"steps"`
	Aborted bool         `json:"aborted"`
	Reason  string       `json:"reason,omitempty"`
}

// abort marks the run stopped at index and records why.
func (r *SequenceResult) abort(index int, err error) {
	r.Aborted = true
	r.Reason = err.Error()
	r.Steps[index].Status = StepAborted
}

// Sequencer runs a macro against one child, advancing on the child's own
// result events rather than on a timer.
type Sequencer interface {
	Run(ctx context.Context, sessionID string, macro Macro) (*SequenceResult, error)
}

// NewSequencer creates a Sequencer.
//
// stepTimeout bounds one turn, not the advance: it is a safety net for a child
// that never closes its turn, never the thing that starts the next step.
func NewSequencer(
	supervisor Supervisor,
	now libtime.CurrentDateTimeGetter,
	locks *SessionLocks,
	stepTimeout time.Duration,
) Sequencer {
	return &sequencer{
		supervisor:  supervisor,
		now:         now,
		locks:       locks,
		stepTimeout: stepTimeout,
	}
}

type sequencer struct {
	supervisor  Supervisor
	now         libtime.CurrentDateTimeGetter
	stepTimeout time.Duration
	// locks is shared with every other route that can write a turn. A lock
	// private to the sequencer would still race POST …/message, whose turn's
	// result would be consumed as the current step's close.
	locks *SessionLocks
}

// Run writes each step in turn, waiting for the previous turn to close.
func (s *sequencer) Run(
	ctx context.Context,
	sessionID string,
	macro Macro,
) (*SequenceResult, error) {
	release, err := s.locks.Acquire(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	defer release()

	// Idle before the first write, checked under the lock so no other writer
	// can slip in between the check and the send. A run started mid-turn would
	// consume the in-flight turn's result as step 1's close and fire every
	// later step one turn early — including /compact and /exit.
	session, err := s.supervisor.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	// `starting` is allowed: a freshly spawned child has no turn in flight and
	// emits no init event until it is first written to, so requiring `idle`
	// would refuse the very first run — the normal case. Only a child that is
	// mid-turn, or gone, is refused.
	//
	// PendingTurns is checked as well as State, and it is the half that matters:
	// State flips to generating only when the child's own message_start
	// arrives, so a turn written a moment ago still reads idle here.
	if session.PendingTurns > 0 ||
		(session.State != StateIdle && session.State != StateStarting) {
		return nil, errors.Wrapf(ctx, ErrSessionNotIdle,
			"session %q is %s with %d turn(s) pending",
			sessionID, session.State, session.PendingTurns)
	}

	replay, live, cancel, err := s.supervisor.Subscribe(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	defer cancel()

	result := newSequenceResult(macro)
	expected := expectedTurn(replay)
	for index := range macro.Steps {
		// Checked between steps, not inside one. The wait itself derives from
		// this context, so a cancelled request also abandons a step already in
		// flight; this check is what stops the NEXT write.
		if err := ctx.Err(); err != nil {
			result.abort(index, err)
			return result, err
		}
		if err := s.advance(ctx, sessionID, index, expected, live, result); err != nil {
			return result, err
		}
		expected++
	}
	return result, nil
}

// advance writes one step and waits for its turn to close.
func (s *sequencer) advance(
	ctx context.Context,
	sessionID string,
	index int,
	expected int,
	live <-chan Event,
	result *SequenceResult,
) error {
	wroteAt := s.now.Now()
	if err := s.supervisor.Send(ctx, sessionID, result.Steps[index].Command); err != nil {
		result.abort(index, err)
		return err
	}
	result.Steps[index].Status = StepRunning
	result.Steps[index].WroteAt = &wroteAt

	closedAt, err := s.waitTurnClosed(ctx, live, expected)
	if err != nil {
		result.abort(index, err)
		return err
	}
	result.Steps[index].Status = StepDone
	result.Steps[index].ClosedAt = &closedAt
	return nil
}

// waitTurnClosed blocks until the child reports turn `expected` closed.
//
// The timeout lives on a derived context rather than a time.After, so the
// advance path holds no timer of its own: nothing here fires the next step.
func (s *sequencer) waitTurnClosed(
	ctx context.Context,
	live <-chan Event,
	expected int,
) (libtime.DateTime, error) {
	waitCtx, cancel := context.WithTimeout(ctx, s.stepTimeout)
	defer cancel()
	for {
		select {
		case <-waitCtx.Done():
			return libtime.DateTime{}, errors.Wrapf(
				ctx, waitCtx.Err(), "waiting for turn %d to close", expected)
		case event, open := <-live:
			if !open {
				return libtime.DateTime{}, errors.Wrapf(
					ctx, ErrSessionExited, "stream closed before turn %d closed", expected)
			}
			if !isTurnClosed(event, expected) {
				continue
			}
			return s.now.Now(), nil
		}
	}
}

// isTurnClosed reports whether event closes the expected turn.
func isTurnClosed(event Event, expected int) bool {
	if event["type"] != "result" {
		return false
	}
	index, ok := eventIndex(event)
	if !ok {
		return true
	}
	return index == expected
}

// eventIndex reads the child's own turn counter, when it sent one.
//
// Measured 2026-10-29 against the installed CLI: a `--output-format stream-json`
// child's `result` line carries `"result_index":0` and increments per turn, so
// this is the production path rather than a hypothetical. The fallback below
// exists for a child that omits it, and is only safe because a run starts on an
// idle child under the session lock — there, the next `result` is necessarily
// the step's own.
func eventIndex(event Event) (int, bool) {
	value, ok := event["result_index"]
	if !ok {
		return 0, false
	}
	number, ok := value.(float64)
	if !ok {
		return 0, false
	}
	return int(number), true
}

// expectedTurn returns the index of the turn the child will close next.
//
// It reads the LAST retained result's own `result_index` rather than counting
// results, because the replay is a bounded tail (maxRetainedEvents) and
// `--include-partial-messages` emits an event per token delta — a long-lived
// session drops its earliest results, so a count undercounts and every step
// would wait for an index that has already passed until the step timeout. The
// count is only the fallback, for a child that sends no index.
func expectedTurn(events []Event) int {
	last, count := -1, 0
	for _, event := range events {
		if event["type"] != "result" {
			continue
		}
		count++
		if index, ok := eventIndex(event); ok {
			last = index
		}
	}
	if last >= 0 {
		return last + 1
	}
	return count
}

// newSequenceResult builds the full step list up front, so a run that stops
// early still names every step it did not reach.
func newSequenceResult(macro Macro) *SequenceResult {
	steps := make([]StepResult, 0, len(macro.Steps))
	for index, command := range macro.Steps {
		steps = append(steps, StepResult{
			Index:   index + 1,
			Command: command,
			Status:  StepNotRun,
		})
	}
	return &SequenceResult{Macro: macro.Name, Steps: steps}
}
