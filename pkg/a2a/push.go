// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a2a

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
)

// PushMaxMessages bounds one worker's status channel.
//
// A status channel is a rolling window of recent facts, not a log: an unbounded
// array here is a slow leak in a process that lives for the whole daemon.
const PushMaxMessages = 50

// TokenHeader is the header the A2A SDK's push sender sets.
//
// Go's `http.Header.Get` is case-insensitive, so the canonical spelling works
// even though the wire carries it in whatever case the sender chose.
const TokenHeader = "A2A-Notification-Token"

// TunnelSecretHeader authenticates a pull from the in-cluster relay.
//
// Deliberately NOT the token header: a push is a worker's claim and a pull is
// the operator's channel, and one header for both would let a worker drain the
// queue.
const TunnelSecretHeader = "X-A2A-Tunnel-Secret" //nolint:gosec // a header NAME, not a credential

// PushPath is the receiver's route.
const PushPath = "/a2a/push"

// StatusEntry is one status a worker reported.
type StatusEntry struct {
	WorkerID string  `json:"worker_id"`
	State    string  `json:"state"`
	Text     *string `json:"text"`
	TS       string  `json:"ts"`
	// Source is written ONLY by the receive path, so an entry carrying `push` can
	// only have arrived through a real push — which is what lets a reader tell a
	// pushed completion from a synthesised one.
	Source    string  `json:"source"`
	MessageID *string `json:"message_id"`
}

// PushRegistry owns the daemon's ONE push receiver and the per-worker tokens it
// accepts.
//
// ⚠️ THE TOKEN IS MINTED HERE, NOT BY THE CONTROLLER. The party that must VERIFY
// a credential is the party that should issue it: the receiver compares the
// header against this registry's own map, so a controller-chosen token would
// make the receiver trust a value it never generated.
type PushRegistry struct {
	// CallbackHost is the host a docker container uses to reach this daemon's
	// loopback. Measured rather than assumed: `host.docker.internal` reaches a
	// listener bound to 127.0.0.1.
	CallbackHost string
	// ListenHost / ListenPort: loopback by default, ephemeral by default.
	ListenHost string
	ListenPort int
	// Now is the clock the receive instant is taken from.
	Now func() time.Time

	mu       sync.RWMutex
	tokens   map[string]string
	messages map[string][]StatusEntry
	urls     map[string]string
	listener net.Listener
	port     int
	server   *http.Server
	once     sync.Once
	listenEr error
}

// NewPushRegistry creates the registry.
func NewPushRegistry(callbackHost string) *PushRegistry {
	return &PushRegistry{
		CallbackHost: callbackHost,
		ListenHost:   "127.0.0.1",
		Now:          time.Now,
		tokens:       map[string]string{},
		messages:     map[string][]StatusEntry{},
		urls:         map[string]string{},
	}
}

// EnsureListening binds the receiver once and returns its port.
func (r *PushRegistry) EnsureListening(ctx context.Context) (int, error) {
	r.once.Do(func() {
		address := net.JoinHostPort(r.ListenHost, fmt.Sprintf("%d", r.ListenPort))
		listener, err := net.Listen("tcp", address)
		if err != nil {
			r.listenEr = errors.Wrapf(
				ctx, err, "the A2A status receiver could not bind a loopback port",
			)
			return
		}
		r.listener = listener
		if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
			r.port = tcpAddr.Port
		}
		mux := http.NewServeMux()
		mux.HandleFunc(PushPath, r.handlePush)
		r.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		safeGo("push receiver", func() {
			if serveErr := r.server.Serve(listener); serveErr != nil &&
				serveErr != http.ErrServerClosed {
				log.Printf("WARNING: A2A push receiver stopped: %v", serveErr)
			}
		})
	})
	if r.listenEr != nil {
		return 0, r.listenEr
	}
	return r.port, nil
}

// Port is the bound port, or 0 before EnsureListening.
func (r *PushRegistry) Port() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.port
}

// BeginCreate mints this spawn's token and the URL it must be registered
// against.
//
// ⚠️ ONE CALL, SO A TOKEN CANNOT EXIST WITHOUT A URL. Minting and URL-building
// are the same step because a token with no URL registers nothing and reads
// downstream as a configured channel that is merely quiet.
func (r *PushRegistry) BeginCreate(
	ctx context.Context,
	callbackURL string,
) (token string, pushURL string, err error) {
	port, err := r.EnsureListening(ctx)
	if err != nil {
		return "", "", err
	}
	token, err = mintToken()
	if err != nil {
		return "", "", errors.Wrapf(ctx, err, "mint a push token failed")
	}
	if callbackURL == "" {
		callbackURL = fmt.Sprintf("http://%s:%d%s", r.CallbackHost, port, PushPath)
	}
	r.mu.Lock()
	r.tokens[token] = ""
	r.urls[token] = callbackURL
	r.mu.Unlock()
	return token, callbackURL, nil
}

// Bind attaches the controller's worker id to a minted token and moves any
// buffered entries onto it.
func (r *PushRegistry) Bind(ctx context.Context, token, workerID string) error {
	if workerID == "" {
		return errors.Errorf(ctx, "bind requires a non-empty worker id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tokens[token]; !ok {
		return errors.Errorf(ctx, "bind was called for a token this registry never minted")
	}
	r.tokens[token] = workerID
	if buffered, ok := r.messages[token]; ok {
		delete(r.messages, token)
		existing := r.messages[workerID]
		combined := append(append([]StatusEntry{}, buffered...), existing...)
		if len(combined) > PushMaxMessages {
			combined = combined[len(combined)-PushMaxMessages:]
		}
		r.messages[workerID] = combined
	}
	return nil
}

// Release drops a token whose create failed.
//
// ⚠️ ONLY AN UNBOUND TOKEN CAN BE RELEASED. Once a worker id is attached the
// entries are that worker's record, and dropping them would erase a status a
// live worker already reported.
func (r *PushRegistry) Release(token string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if bound, ok := r.tokens[token]; !ok || bound != "" {
		return false
	}
	delete(r.tokens, token)
	delete(r.messages, token)
	delete(r.urls, token)
	return true
}

// PushFor returns the callback URL and token a follow-up turn must register
// against its own task.
//
// ⚠️ A FOLLOW-UP NEEDS ITS OWN REGISTRATION: it is sent with a `contextId` and
// no `taskId`, so the worker creates a NEW task in that conversation and a
// config registered for the previous one does not apply.
func (r *PushRegistry) PushFor(workerID string) (url, token string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for candidate, bound := range r.tokens {
		if bound == workerID && workerID != "" {
			return r.urls[candidate], candidate, true
		}
	}
	return "", "", false
}

// StatusMessages returns every status received for a worker, oldest first.
func (r *PushRegistry) StatusMessages(workerID string) []StatusEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entries := r.messages[workerID]
	if len(entries) == 0 {
		return nil
	}
	out := make([]StatusEntry, len(entries))
	copy(out, entries)
	return out
}

// Close stops the receiver.
func (r *PushRegistry) Close() error {
	if r.server == nil {
		return nil
	}
	return r.server.Close()
}

// handlePush accepts one worker push.
func (r *PushRegistry) handlePush(resp http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		respondJSON(resp, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	presented := req.Header.Get(TokenHeader)
	// ⚠️ THE WORKER IS RESOLVED FROM THE TOKEN, NEVER FROM THE BODY: the body is
	// worker-supplied, so a `worker_id` field in it would let one worker claim
	// another's identity. The token is the credential and the identity together.
	workerID, ok := r.workerForToken(presented)
	if !ok {
		respondJSON(resp, http.StatusUnauthorized, map[string]any{"error": "unknown worker or bad token"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, 256<<10))
	if err != nil {
		respondJSON(resp, http.StatusBadRequest, map[string]any{"error": "read body failed"})
		return
	}
	kind, payload, err := parseStreamEnvelope(req.Context(), raw)
	if err != nil {
		respondJSON(resp, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if kind != "statusUpdate" {
		// Accepted, not stored: the record is a status channel, and a
		// task/artifact/message event is not a status. 200 rather than 4xx so the
		// SDK does not treat a valid event as a delivery failure.
		respondJSON(resp, http.StatusOK, map[string]any{
			"accepted": true, "stored": false, "kind": kind,
		})
		return
	}
	entry := StatusEntry{
		WorkerID:  workerID,
		State:     statusState(payload),
		Text:      statusText(payload),
		TS:        r.Now().UTC().Format(time.RFC3339Nano),
		Source:    "push",
		MessageID: statusMessageID(payload),
	}
	r.mu.Lock()
	list := append(r.messages[workerID], entry)
	if len(list) > PushMaxMessages {
		list = list[len(list)-PushMaxMessages:]
	}
	r.messages[workerID] = list
	r.mu.Unlock()
	respondJSON(resp, http.StatusOK, map[string]any{"accepted": true, "stored": true})
}

func (r *PushRegistry) workerForToken(presented string) (string, bool) {
	if presented == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for token, bound := range r.tokens {
		if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1 {
			// The provisional key is the token itself: until the controller names
			// a worker id, the token is the only identity this side holds, and it
			// is a sound one.
			if bound == "" {
				return token, true
			}
			return bound, true
		}
	}
	return "", false
}

// DeliverByToken files one status that arrived through the relay tunnel.
//
// It resolves the worker from the token exactly as the HTTP receiver does — the
// routing key is the token, never the body — and reports whether the token was
// one this registry minted.
func (r *PushRegistry) DeliverByToken(token string, payload map[string]any) bool {
	workerID, ok := r.workerForToken(token)
	if !ok {
		return false
	}
	r.Deliver(workerID, StatusEntry{
		WorkerID:  workerID,
		State:     statusState(payload),
		Text:      statusText(payload),
		TS:        r.Now().UTC().Format(time.RFC3339Nano),
		Source:    "push",
		MessageID: statusMessageID(payload),
	})
	return true
}

// Deliver pushes one status onto a worker's channel.
//
// Exported so the tunnel puller can file a status without a second copy of the
// buffering rule.
func (r *PushRegistry) Deliver(workerID string, entry StatusEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := append(r.messages[workerID], entry)
	if len(list) > PushMaxMessages {
		list = list[len(list)-PushMaxMessages:]
	}
	r.messages[workerID] = list
}

// ─── stream envelope ────────────────────────────────────────────────────────

// parseStreamEnvelope reads the SDK's `StreamResponse` union envelope.
//
// There is deliberately no `event` key: an earlier spec asserted one and was
// wrong, and a body keyed on `event` is a bespoke shape that is refused rather
// than guessed at.
func parseStreamEnvelope(
	ctx context.Context,
	raw []byte,
) (kind string, payload map[string]any, err error) {
	var body map[string]any
	if unmarshalErr := json.Unmarshal(raw, &body); unmarshalErr != nil {
		return "", nil, errors.Wrapf(ctx, unmarshalErr, "the push body is not JSON")
	}
	if len(body) != 1 {
		return "", nil, errors.Errorf(
			ctx, "expected exactly one envelope key, got %d", len(body),
		)
	}
	for key, value := range body {
		switch key {
		case "statusUpdate", "task", "artifactUpdate", "message":
			payload, _ = value.(map[string]any)
			return key, payload, nil
		default:
			return "", nil, errors.Errorf(ctx, "unknown envelope key %q", key)
		}
	}
	return "", nil, errors.Errorf(ctx, "empty envelope")
}

func statusState(payload map[string]any) string {
	if status, ok := payload["status"].(map[string]any); ok {
		if state, ok := status["state"].(string); ok {
			return state
		}
	}
	return ""
}

// statusText reads the mid-turn status text, which lives under
// `status.message.parts[].text`.
func statusText(payload map[string]any) *string {
	status, ok := payload["status"].(map[string]any)
	if !ok {
		return nil
	}
	message, ok := status["message"].(map[string]any)
	if !ok {
		return nil
	}
	parts, ok := message["parts"].([]any)
	if !ok {
		return nil
	}
	var builder strings.Builder
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := part["text"].(string); ok {
			builder.WriteString(text)
		}
	}
	text := strings.TrimSpace(builder.String())
	if text == "" {
		return nil
	}
	return &text
}

func statusMessageID(payload map[string]any) *string {
	status, ok := payload["status"].(map[string]any)
	if !ok {
		return nil
	}
	message, ok := status["message"].(map[string]any)
	if !ok {
		return nil
	}
	id, ok := message["messageId"].(string)
	if !ok || id == "" {
		return nil
	}
	return &id
}

func respondJSON(resp http.ResponseWriter, status int, payload map[string]any) {
	resp.Header().Set("content-type", "application/json")
	resp.WriteHeader(status)
	_ = json.NewEncoder(resp).Encode(payload)
}

// mintToken returns an unguessable per-worker token.
func mintToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
