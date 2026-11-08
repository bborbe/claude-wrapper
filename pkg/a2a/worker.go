// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a2a

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/bborbe/errors"
)

// Agent is one A2A worker this daemon created.
//
// ⚠️ THE TOKEN IS A SECRET AND IS NEVER PROJECTED. `View` is an explicit
// allowlist of named fields with no spread, so a token held here is invisible to
// `list_agents` / `agent_status` and never reaches a transcript.
type Agent struct {
	AgentID         string
	WorkerID        string
	Task            string
	Target          string
	ContextID       string
	A2AURL          string
	Token           string
	ProtocolVersion string
	State           string
	CreatedAt       time.Time
	LastReply       *string
	Error           string
	// PushRegistration records the status channel's outcome: `not-attempted`,
	// `attempted-ok` or `attempted-error`.
	PushRegistration string
	PushError        string
	// PushTaskID is the A2A task the push registration was armed against.
	//
	// ⚠️ HELD BECAUSE THE READ-BACK NEEDS IT AND NOTHING ELSE DOES. The
	// registration call learns the task id mid-stream and the read-back is keyed
	// by it; without a stored copy, `read_push_config` could only answer "a
	// registration was attempted", which is the exact question it exists to
	// look past. Not projected by `View` — no caller lists it.
	PushTaskID string
}

// View is the projection every tool returns.
func (a *Agent) View(registry *PushRegistry) map[string]any {
	view := map[string]any{
		"agent_id":          a.AgentID,
		"worker_id":         a.WorkerID,
		"task":              a.Task,
		"target":            a.Target,
		"state":             a.State,
		"created_at":        a.CreatedAt.UTC().Format(time.RFC3339),
		"push_registration": a.PushRegistration,
	}
	if a.LastReply != nil {
		view["last_reply"] = *a.LastReply
	}
	if a.Error != "" {
		view["error"] = a.Error
	}
	if a.PushError != "" {
		view["push_error"] = a.PushError
	}
	if registry != nil {
		view["status_messages"] = registry.StatusMessages(a.AgentID)
	}
	return view
}

// Manager owns the daemon's A2A workers: the create/stop calls, the turn plane
// and the push channel.
type Manager struct {
	Client   *Client
	Registry *PushRegistry
	Config   Config
	Now      func() time.Time

	// CardReadAttempts and CardReadDelay bound the wait for a freshly created
	// worker's agent card. Defaults: 60 attempts 2 s apart, so a spawn waits up
	// to two minutes for the ingress — measured at roughly one minute on nukedev.
	CardReadAttempts int
	CardReadDelay    time.Duration

	mu       sync.RWMutex
	agents   map[string]*Agent
	order    []string
	byTask   map[string]string
	byWorker map[string]string
}

// NewManager creates the manager.
func NewManager(client *Client, registry *PushRegistry, cfg Config) *Manager {
	return &Manager{
		Client:           client,
		Registry:         registry,
		Config:           cfg,
		Now:              time.Now,
		CardReadAttempts: 60,
		CardReadDelay:    2 * time.Second,
		agents:           map[string]*Agent{},
		byTask:           map[string]string{},
		byWorker:         map[string]string{},
	}
}

// SpawnArgs is one spawn request.
type SpawnArgs struct {
	Target      string
	Task        string
	Vault       string
	Prompt      string
	Resume      bool
	Policy      string
	Interactive *bool
	WindowID    string
	Role        string
	Env         map[string]string
	Shipping    bool
}

// Spawn creates one worker and returns its record immediately.
//
// ⚠️ THE FIRST TURN IS SENT ASYNCHRONOUSLY, AND THAT IS THE POINT. The spawn
// must return an agent id in under two seconds; a cluster card read alone can
// legitimately take ~45 s (the ingress answers before the pod does), so waiting
// for it here would turn a fast control call into a slow spawn. The record
// carries no result — nothing has been collected yet.
func (m *Manager) Spawn(ctx context.Context, args SpawnArgs) (*Agent, error) {
	target, err := ParseTarget(ctx, args.Target)
	if err != nil {
		return nil, err
	}
	if err := RefuseSpawnArguments(ctx, SpawnArguments{
		Target:      args.Target,
		Task:        args.Task,
		Resume:      args.Resume,
		Policy:      args.Policy,
		Interactive: args.Interactive,
		WindowID:    args.WindowID,
		Role:        args.Role,
		Env:         args.Env,
		Shipping:    args.Shipping,
	}); err != nil {
		return nil, err
	}
	if args.Vault != "" {
		if err := ClusterAllowlistRefusal(ctx, args.Vault, target, m.Config); err != nil {
			return nil, err
		}
	}
	cfg, err := ResolveController(ctx, target, m.Config)
	if err != nil {
		return nil, err
	}

	callbackURL := m.callbackURLFor(target)
	token, pushURL, err := m.Registry.BeginCreate(ctx, callbackURL)
	if err != nil {
		return nil, err
	}
	response, err := m.Client.CreateWorker(ctx, cfg, args.Task)
	if err != nil {
		// A failed create does NOT prove no worker exists: the controller's store
		// can fail after the executor ran. Release the token so a live push is not
		// filed under a credential nothing can deliver.
		m.Registry.Release(token)
		return nil, errors.Wrapf(
			ctx, err,
			"create the worker for task %q failed — ⚠️ this does NOT prove no worker "+
				"exists: reconcile against the controller's own `list` before retrying, "+
				"because a repeat create returns the existing worker and a retry after a "+
				"dropped response can orphan one",
			args.Task,
		)
	}
	worker, workerToken, err := createdWorker(ctx, response)
	if err != nil {
		m.Registry.Release(token)
		return nil, err
	}
	if err := m.Registry.Bind(ctx, token, worker.WorkerID); err != nil {
		return nil, err
	}

	agent := &Agent{
		AgentID:          worker.WorkerID,
		WorkerID:         worker.WorkerID,
		Task:             args.Task,
		Target:           cfg.Target.String(),
		ContextID:        m.Client.newID(),
		A2AURL:           worker.A2AURL,
		Token:            workerToken,
		State:            worker.State,
		CreatedAt:        m.now(),
		PushRegistration: "not-attempted",
	}
	m.remember(agent)

	// The first turn, and the push registration that must happen before the
	// worker's mid-turn status fires (~10 s in).
	firstCtx := context.WithoutCancel(ctx)
	safeGo("first turn", func() {
		m.firstTurn(firstCtx, agent, args.Prompt, pushURL, token)
	})
	return agent, nil
}

// createdWorker validates the worker a create reply names.
//
// ⚠️ A WORKER WITH NO `a2a_url` IS REFUSED rather than defaulted to the
// controller: a fallback would send every worker turn to the controller instead.
// The token is required for the same reason — every follow-up authenticates with
// it, so a create that returned none would produce a worker nothing can talk to.
func createdWorker(ctx context.Context, response *ControlResponse) (Worker, string, error) {
	if response.Create == nil {
		return Worker{}, "", errors.Errorf(ctx, "the create reply carried no `create` response")
	}
	worker := response.Create.Worker
	if worker.WorkerID == "" {
		return Worker{}, "", errors.Errorf(ctx, "the create reply named no worker id")
	}
	if worker.A2AURL == "" {
		return Worker{}, "", errors.Errorf(
			ctx,
			"the create reply named a worker with no `a2a_url`, so there is no route to "+
				"send a turn to. Refusing rather than falling back to the controller, which "+
				"would send every worker turn to the controller instead",
		)
	}
	if response.Create.Token == "" {
		return Worker{}, "", errors.Errorf(
			ctx, "the create reply carried no worker token, so no turn could authenticate",
		)
	}
	return worker, response.Create.Token, nil
}

// readCardWithRetry waits for a freshly created worker's agent card.
//
// ⚠️ THE POD EXISTING IS NOT THE CARD ANSWERING. A k8s worker's ingress is
// created after the create call returns, so the first read is routinely 404 or
// 503 — both observed on consecutive nukedev spawns, with the card answering 200
// about a minute later. A single read therefore loses the status channel
// permanently: no card means no endpoint, no turn, no task id and no push
// registration, which leaves a running, healthy worker reporting
// `push_registration: not-attempted` forever. Retry inside a bounded window, and
// fail only once that window is spent.
func (m *Manager) readCardWithRetry(ctx context.Context, agent *Agent) (*AgentCard, error) {
	attempts := m.CardReadAttempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, errors.Wrapf(ctx, lastErr, "read the worker card was abandoned")
			case <-time.After(m.CardReadDelay):
			}
		}
		card, err := m.Client.ReadCard(ctx, agent.A2AURL, "")
		if err == nil {
			return card, nil
		}
		lastErr = err
	}
	return nil, errors.Wrapf(ctx, lastErr, "read the worker card failed after %d attempts", attempts)
}

// firstTurn reads the card, registers the push channel and sends the prompt.
func (m *Manager) firstTurn(
	ctx context.Context,
	agent *Agent,
	prompt string,
	pushURL string,
	pushToken string,
) {
	card, err := m.readCardWithRetry(ctx, agent)
	if err != nil {
		m.fail(agent, "read the worker card failed: "+err.Error())
		return
	}
	endpoint, err := card.Endpoint(ctx)
	if err != nil {
		m.fail(agent, err.Error())
		return
	}
	agent.A2AURL = endpoint
	agent.ProtocolVersion = card.ProtocolVersion()

	if prompt == "" {
		return
	}
	result, err := m.Client.SendTurn(
		ctx, endpoint, agent.Token, agent.ContextID, prompt, agent.ProtocolVersion,
		func(taskID string) {
			safeGo("push registration", func() {
				m.registerPush(ctx, agent, endpoint, taskID, pushURL, pushToken)
			})
		},
	)
	if err != nil {
		m.fail(agent, "the first turn failed: "+err.Error())
		return
	}
	m.recordTurn(agent, result)
}

// registerPush arms the status channel for one task.
func (m *Manager) registerPush(
	ctx context.Context,
	agent *Agent,
	endpoint string,
	taskID string,
	pushURL string,
	pushToken string,
) {
	if taskID == "" || pushURL == "" {
		return
	}
	err := m.Client.RegisterPush(
		ctx, endpoint, agent.Token, taskID, pushURL, pushToken, agent.ProtocolVersion,
	)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		agent.PushRegistration = "attempted-error"
		agent.PushError = err.Error()
		return
	}
	agent.PushRegistration = "attempted-ok"
	agent.PushError = ""
	agent.PushTaskID = taskID
}

// PushReadRefusal names why an agent's push registration cannot be read back,
// or returns nil when it can.
//
// ⚠️ THE GATE IS THE POINT, NOT A GUARD AROUND IT. `attempted-ok` proves only
// that the worker ACCEPTED a registration; reading back a task it never accepted
// would report "no config" and read as a channel fault. So an absent or failed
// registration is refused with its state named, and the caller can tell
// "nothing to read yet" from "read it and it holds nothing".
func PushReadRefusal(ctx context.Context, agent *Agent) error {
	if agent.PushRegistration != "attempted-ok" || agent.PushTaskID == "" {
		state := agent.PushRegistration
		if state == "" {
			state = "none"
		}
		return errors.Errorf(
			ctx,
			"agent %s has no accepted push registration to read back (state: %s) — "+
				"a read of a task the worker never accepted would report a missing "+
				"config as a channel fault",
			agent.AgentID,
			state,
		)
	}
	if agent.A2AURL == "" || agent.Token == "" {
		return errors.Errorf(
			ctx,
			"agent %s is an A2A worker whose endpoint or bearer token this process "+
				"does not hold — a row re-adopted after a reconnect can be watched "+
				"but not read back",
			agent.AgentID,
		)
	}
	return nil
}

// ReadPushConfig reads back the push configs a worker HOLDS for its task.
//
// ⚠️ THE GATE IS THE POINT, NOT A GUARD AROUND IT. `attempted-ok` proves only
// that the worker ACCEPTED a registration; reading back a task it never accepted
// would report "no config" and read as a channel fault. So an absent or failed
// registration is refused with its state named, and the caller can tell
// "nothing to read yet" from "read it and it holds nothing".
func (m *Manager) ReadPushConfig(
	ctx context.Context,
	agentID string,
) (map[string]any, error) {
	agent, err := m.Get(ctx, agentID)
	if err != nil {
		return nil, errors.Errorf(ctx, "unknown agent %s", agentID)
	}
	if err := PushReadRefusal(ctx, agent); err != nil {
		return nil, err
	}
	configs, err := m.Client.ListPushConfigs(
		ctx, agent.A2AURL, agent.Token, agent.PushTaskID, agent.ProtocolVersion,
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"agent_id": agent.AgentID,
		"task_id":  agent.PushTaskID,
		"configs":  configs,
	}, nil
}

// SendMessage sends one further turn to an existing worker, continuing its
// conversation by contextId.
func (m *Manager) SendMessage(ctx context.Context, agentID, text string) (*TurnResult, error) {
	agent, err := m.Get(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, errors.Errorf(ctx, "text is required")
	}
	pushURL, pushToken, _ := m.Registry.PushFor(agent.AgentID)
	result, err := m.Client.SendTurn(
		ctx, agent.A2AURL, agent.Token, agent.ContextID, text, agent.ProtocolVersion,
		func(taskID string) {
			safeGo("push registration", func() {
				m.registerPush(ctx, agent, agent.A2AURL, taskID, pushURL, pushToken)
			})
		},
	)
	if err != nil {
		return nil, err
	}
	m.recordTurn(agent, result)
	return result, nil
}

// Stop stops one worker on the controller.
func (m *Manager) Stop(ctx context.Context, agentID string) error {
	agent, err := m.Get(ctx, agentID)
	if err != nil {
		return err
	}
	target, parseErr := ParseTarget(ctx, agent.Target)
	if parseErr != nil {
		return parseErr
	}
	cfg, resolveErr := ResolveController(ctx, target, m.Config)
	if resolveErr != nil {
		return resolveErr
	}
	// ⚠️ STOP IS KEYED ON WORKER ID, NEVER TASK NAME — a task can be respawned
	// after its first worker died.
	if stopErr := m.Client.StopWorker(ctx, cfg, agent.WorkerID); stopErr != nil {
		// ⚠️ A FAILED STOP REPLY DOES NOT MEAN THE WORKER STILL RUNS. Reconcile
		// against the controller's own list rather than assuming either way.
		return errors.Wrapf(
			ctx, stopErr,
			"stop worker %q failed — a failed stop reply does not mean the worker still "+
				"runs; reconcile against the controller's own `list`",
			agent.WorkerID,
		)
	}
	m.mu.Lock()
	agent.State = "stopped"
	m.mu.Unlock()
	return nil
}

// Get returns one agent.
func (m *Manager) Get(ctx context.Context, agentID string) (*Agent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	agent, ok := m.agents[agentID]
	if !ok {
		// A worker id and an agent id are the same value here; a caller holding
		// either resolves to the same record.
		if mapped, ok := m.byWorker[agentID]; ok {
			return m.agents[mapped], nil
		}
		return nil, errors.Errorf(ctx, "agent %q is not one this daemon created", agentID)
	}
	return agent, nil
}

// List returns every agent, in creation order.
func (m *Manager) List() []*Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Agent, 0, len(m.order))
	for _, id := range m.order {
		if agent, ok := m.agents[id]; ok {
			out = append(out, agent)
		}
	}
	return out
}

// StatusMessages returns a worker's pushed status entries.
func (m *Manager) StatusMessages(agentID string) []StatusEntry {
	return m.Registry.StatusMessages(agentID)
}

func (m *Manager) remember(agent *Agent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.agents[agent.AgentID]; !exists {
		m.order = append(m.order, agent.AgentID)
	}
	m.agents[agent.AgentID] = agent
	m.byTask[agent.Task] = agent.AgentID
	m.byWorker[agent.WorkerID] = agent.AgentID
}

func (m *Manager) recordTurn(agent *Agent, result *TurnResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if result.State != "" {
		agent.State = result.State
	}
	if result.Reply != "" {
		reply := result.Reply
		agent.LastReply = &reply
	}
}

func (m *Manager) fail(agent *Agent, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	agent.Error = message
	log.Printf("WARNING: A2A worker %s: %s", agent.WorkerID, message)
}

// callbackURLFor is the URL a worker of this target is told to POST to.
//
// A docker container reaches this daemon's loopback through
// `host.docker.internal`. A cluster worker cannot reach this machine inbound at
// all — its own egress policy drops it — so it is handed the in-cluster relay
// instead, named by `SUPERVISOR_A2A_K8S_CALLBACK_URL`.
func (m *Manager) callbackURLFor(target Target) string {
	if target.Kind == KindK8s {
		return m.Config.K8sCallbackURL
	}
	return ""
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// safeGo runs fn on its own goroutine and recovers a panic.
//
// ⚠️ A PANIC IN A BARE GOROUTINE KILLS THE WHOLE DAEMON, and this daemon owns
// the `claude` child process — every in-flight worker dies with it. None of the
// work started here (a first turn, a push registration) is on a caller's
// critical path, so the correct response to a panic in one is a logged warning,
// not a process exit that takes unrelated sessions down with it.
func safeGo(what string, fn func()) {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("WARNING: a2a %s panicked: %v", what, recovered)
			}
		}()
		fn()
	}()
}
