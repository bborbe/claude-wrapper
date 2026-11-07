// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

// OperatorPrefix is the voice a board answer is delivered in.
//
// ⚠️ It is a provenance claim: a manager answering in its own voice must use
// `Manager answer, via supervisor:` instead, or the worker reads an inference as
// the operator's own words. The board IS the operator, so this is the prefix its
// answers carry.
const OperatorPrefix = "Operator answer, via supervisor:"

// FreshnessSkew is how far an item may predate the park it would settle.
//
// The two timestamps are written by different processes, so an exact ordering
// cannot be asserted. It is small on purpose: the guard exists to stop an answer
// from an EARLIER gate settling a LATER park.
const FreshnessSkew = 5 * time.Second

// BoardItem is one attention-store item, in the shape the delivery arms read.
type BoardItem struct {
	ItemID          string        `json:"item_id"`
	State           string        `json:"state"`
	AnswerMechanism string        `json:"answer_mechanism"`
	ProducerID      string        `json:"producer_id"`
	Decision        string        `json:"decision"`
	Answers         []BoardAnswer `json:"answers"`
	Answer          *BoardAnswer  `json:"answer"`
	ResolvedBy      string        `json:"resolved_by"`
	CreatedAt       string        `json:"created_at"`
}

// BoardAnswer is one answer unit inside an item.
type BoardAnswer struct {
	Kind   string   `json:"kind"`
	Value  string   `json:"value"`
	Values []string `json:"values"`
	Tab    string   `json:"tab"`
}

// BoardStore is the attention store's read and record surface.
type BoardStore interface {
	// Open lists the items currently open. The list is the render path and
	// carries open items only.
	Open(ctx context.Context) ([]BoardItem, error)
	// Get reads one item in any state — the post-transition read a settlement
	// needs.
	Get(ctx context.Context, itemID string) (BoardItem, error)
	// RecordAttempt records what this process observed about a delivery.
	RecordAttempt(ctx context.Context, itemID string, carrier string, outcome string) error
}

// NewBoardStore creates a store client.
func NewBoardStore(baseURL string, client *http.Client) BoardStore {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &boardStore{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

type boardStore struct {
	baseURL string
	client  *http.Client
}

func (s *boardStore) Open(ctx context.Context) ([]BoardItem, error) {
	var items []BoardItem
	if err := s.getJSON(ctx, "/api/1.0/attention", &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *boardStore) Get(ctx context.Context, itemID string) (BoardItem, error) {
	var item BoardItem
	if err := s.getJSON(ctx, "/api/1.0/attention/"+itemID, &item); err != nil {
		return BoardItem{}, err
	}
	return item, nil
}

func (s *boardStore) RecordAttempt(
	ctx context.Context,
	itemID string,
	carrier string,
	outcome string,
) error {
	body, err := json.Marshal(map[string]string{"carrier": carrier, "outcome": outcome})
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal attempt failed")
	}
	url := s.baseURL + "/api/1.0/attention/" + itemID + "/attempt"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return errors.Wrapf(ctx, err, "build attempt request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return errors.Wrapf(ctx, err, "record attempt failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		return errors.Errorf(ctx, "attempt record rejected with %d", resp.StatusCode)
	}
	return nil
}

func (s *boardStore) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return errors.Wrapf(ctx, err, "build request failed")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return errors.Wrapf(ctx, err, "request %s failed", path)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		return errors.Errorf(ctx, "store returned %d for %s", resp.StatusCode, path)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return errors.Wrapf(ctx, err, "decode %s failed", path)
	}
	return nil
}

// BoardDelivery carries a board answer to the park it settles.
//
// It runs in the DAEMON, once, rather than in every session's own supervisor:
// the wrapper is the process that holds the parks, so one poller reaches every
// child on this machine. That is the whole point of the slice — it is what lets
// the per-session node supervisor go away for the headless path.
type BoardDelivery struct {
	Supervisor Supervisor
	Store      BoardStore
	Decider    *PermissionDecider
	// Interval is the fast cadence, used while anything is parked.
	Interval time.Duration
	// IdleInterval is the slow cadence, used with nothing parked. It is not zero:
	// a server holding no park still reads occasionally so the delivery trail
	// keeps its `failed` record.
	IdleInterval time.Duration
	// IdleCheck is how often the park set is re-examined without an HTTP read.
	IdleCheck time.Duration
	Carrier   string
}

// Run polls until the context is cancelled.
func (d *BoardDelivery) Run(ctx context.Context) error {
	interval := d.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	idleInterval := d.IdleInterval
	if idleInterval <= 0 {
		idleInterval = 30 * time.Second
	}
	idleCheck := d.IdleCheck
	if idleCheck <= 0 {
		idleCheck = 250 * time.Millisecond
	}
	// The first read must always happen, so the watch set is populated.
	lastRead := time.Time{}
	watching := map[string]string{}
	for {
		parked := len(d.parkedSessions(ctx)) > 0
		due := lastRead.IsZero() || time.Since(lastRead) >= idleInterval
		if parked || due {
			lastRead = time.Now()
			d.tick(ctx, watching)
		}
		wait := idleCheck
		if parked {
			wait = interval
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// tick reads the open items, then settles every watched item that was answered.
func (d *BoardDelivery) tick(ctx context.Context, watching map[string]string) {
	items, err := d.Store.Open(ctx)
	if err != nil {
		log.Printf("board delivery: the store is unreadable, parks keep their own timeout: %v", err)
		return
	}
	for _, item := range items {
		if item.AnswerMechanism == "permission" || item.AnswerMechanism == "message" {
			watching[item.ItemID] = item.AnswerMechanism
		}
	}
	for itemID, mechanism := range watching {
		item, readErr := d.Store.Get(ctx, itemID)
		if readErr != nil {
			// Guarded per item: one pruned item must not wedge delivery for every
			// later item, and a dropped id must not be re-read forever.
			delete(watching, itemID)
			log.Printf("board delivery: item %s could not be read (%v) — dropped", itemID, readErr)
			continue
		}
		if item.State != "answered" {
			if item.State != "open" {
				delete(watching, itemID)
			}
			continue
		}
		delete(watching, itemID)
		d.deliver(ctx, item, mechanism)
	}
}

// deliver settles the park an answered item belongs to.
func (d *BoardDelivery) deliver(ctx context.Context, item BoardItem, mechanism string) {
	sessions, err := d.Supervisor.List(ctx)
	if err != nil {
		log.Printf("board delivery: sessions unreadable: %v", err)
		return
	}
	session := findSession(sessions, item.ProducerID)
	if session == nil || session.Question == nil {
		// Not this process's park, or nothing parked. Only the owner records an
		// attempt; every other reader stays silent so ~N non-owners cannot
		// outvote the owner's real outcome.
		return
	}
	question := *session.Question
	answer, ok := answerFor(item, mechanism, question, session)
	if !ok {
		return
	}
	if err := d.Supervisor.Answer(ctx, session.ID, question.RequestID, answer); err != nil {
		log.Printf("board delivery: settling %s failed: %v", question.RequestID, err)
		if d.Store != nil {
			_ = d.Store.RecordAttempt(ctx, item.ItemID, d.carrier(), "failed")
		}
		return
	}
	if d.Decider != nil {
		d.Decider.RecordSettled(
			ctx, session.ID, PolicyContext{Cwd: session.Cwd}, question,
			answer.Behavior, DecidedByStore, answer.Message, nil,
		)
	}
	if d.Store != nil {
		if err := d.Store.RecordAttempt(ctx, item.ItemID, d.carrier(), "delivered"); err != nil {
			log.Printf("board delivery: attempt record for %s failed: %v", item.ItemID, err)
		}
	}
	log.Printf("board delivery: %s settled %s (%s)", mechanism, question.RequestID, item.ItemID)
}

// answerFor builds the answer one answered item settles its park with.
//
// It reports false when the item cannot settle anything, so the caller neither
// answers nor records an attempt for a card that carried no verdict.
func answerFor(item BoardItem, mechanism string, question Question, session *Session) (Answer, bool) {
	switch mechanism {
	case "permission":
		if item.Decision != "allow" && item.Decision != "deny" {
			log.Printf("board delivery: item %s carries no decision", item.ItemID)
			return Answer{}, false
		}
		// ⚠️ The arm guard, ported verbatim: a permission gate releases only on an
		// arm answer, and the sole exception is a board answer on a HEADLESS park.
		// A permission approval EXECUTES a command, so it is the one class a
		// scripted click must not be able to take; a tab worker answers in its own
		// tab, so a board answer for one is a defect rather than an answer.
		if item.ResolvedBy == "" && session.Mode != "headless" {
			log.Printf(
				"board delivery: item %s carries no resolved_by and its producer is not headless — refusing to release a permission gate",
				item.ItemID,
			)
			return Answer{}, false
		}
		return Answer{Kind: question.Kind, Behavior: item.Decision}, true
	case "message":
		return messageAnswer(item, question)
	default:
		return Answer{}, false
	}
}

// messageAnswer turns an answered `message` card into a park settlement.
//
// ⚠️ deny + a message, never allow: an allow runs an AskUserQuestion in a
// tty-less session, where it waits and the worker exits unanswered.
func messageAnswer(item BoardItem, question Question) (Answer, bool) {
	text := messageTextOf(item)
	if text == "" {
		log.Printf("board delivery: item %s carries no answer content", item.ItemID)
		return Answer{}, false
	}
	if stale(item, question) {
		log.Printf("board delivery: item %s predates its park — refusing to settle", item.ItemID)
		return Answer{}, false
	}
	return Answer{
		Kind:     question.Kind,
		Behavior: "deny",
		Message:  OperatorPrefix + " " + text,
	}, true
}

// stale reports whether an item was answered at an earlier gate than the park it
// would settle.
//
// The park is joined by producer, and one producer answers many cards over its
// life, so without this an old answer would settle a new park.
func stale(item BoardItem, question Question) bool {
	if item.CreatedAt == "" || question.RequestedAt.IsZero() {
		return false
	}
	createdAt, err := time.Parse(time.RFC3339, item.CreatedAt)
	if err != nil {
		return false
	}
	return createdAt.Before(question.RequestedAt.Add(-FreshnessSkew))
}

func (d *BoardDelivery) carrier() string {
	if d.Carrier != "" {
		return d.Carrier
	}
	return "claude-wrapper:board"
}

// parkedSessions returns the sessions holding a park right now.
func (d *BoardDelivery) parkedSessions(ctx context.Context) []Session {
	sessions, err := d.Supervisor.List(ctx)
	if err != nil {
		return nil
	}
	parked := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if session.Question != nil {
			parked = append(parked, session)
		}
	}
	return parked
}

// findSession returns the session whose id equals the item's producer.
func findSession(sessions []Session, producerID string) *Session {
	if producerID == "" {
		return nil
	}
	for index := range sessions {
		if sessions[index].ID == producerID {
			return &sessions[index]
		}
	}
	return nil
}

// messageTextOf renders an item's answer content as one line of text.
//
// A refusal rather than a default: an item that reads answered with no content is
// not an answer to anything, and settling a park on it would tell the worker it
// had been answered when nobody said anything.
func messageTextOf(item BoardItem) string {
	lines := make([]string, 0, len(item.Answers))
	for _, entry := range item.Answers {
		text := oneAnswerText(entry)
		if text == "" {
			return ""
		}
		if entry.Tab != "" {
			lines = append(lines, fmt.Sprintf("%s: %s", entry.Tab, text))
			continue
		}
		lines = append(lines, text)
	}
	if len(lines) > 0 {
		return strings.Join(lines, "\n")
	}
	if item.Answer != nil {
		return oneAnswerText(*item.Answer)
	}
	return ""
}

// oneAnswerText is one answer unit's text, or empty when it carries none.
//
// `skip` is empty deliberately: it is the operator declining to answer, which is
// not a thing to type at a worker.
func oneAnswerText(answer BoardAnswer) string {
	switch answer.Kind {
	case "text":
		return strings.TrimSpace(answer.Value)
	case "option":
		values := make([]string, 0, len(answer.Values))
		for _, value := range answer.Values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				values = append(values, trimmed)
			}
		}
		if len(values) > 0 {
			return strings.Join(values, ", ")
		}
		return strings.TrimSpace(answer.Value)
	default:
		return ""
	}
}
