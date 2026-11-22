// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

// ErrVaultAPIUnavailable reports that the local vault-api could not be reached,
// answered a non-200, or answered a body this client could not decode.
//
// It exists so the caller can tell an outage apart from "the index holds no
// eligible rows". Those two answers are opposite: an empty set means the tick
// has nothing to do, while an outage means the tick does not know — and a tick
// that reads an outage as "no rows" silently drops every row the operator
// flagged. Callers must test this with errors.Is and serve the last good set (or
// skip the tick) rather than acting on an empty slice.
var ErrVaultAPIUnavailable = stderrors.New("vault-api unavailable")

// VaultAPIRow is one row as the list endpoints report it.
//
// It is the eligibility answer only: the fields the list endpoints carry. The
// enrichment inputs (role, mode, flag_set_by, blocked_by) live on the record
// endpoint and are read through Page, because the list projection does not carry
// them.
type VaultAPIRow struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Path  string `json:"path"`
	// Status is served so a caller can tell a terminal row from an open one
	// without a second read; the eligibility filter has already applied it.
	Status string `json:"status"`
	Flag   bool   `json:"flag"`
	Vault  string `json:"vault"`
	// SessionID is the page's recorded claude_session_id. Empty on a never-opened
	// row, which is the state the tick opens fresh.
	SessionID string   `json:"claude_session_id"`
	Goals     []string `json:"goals"`
	// FlagSetBy is the page's `flag_set_by`. The index serves it for every kind
	// since vault-api v0.31.0, so an empty value means the page genuinely carries
	// no provenance — which the task rule refuses. Before that release the field
	// was absent for goals and topics and had to be read as UNKNOWN.
	FlagSetBy string `json:"flag_set_by"`
	// Role, Mode, DeferDate, BlockedBy are the per-kind enrichment a consumer
	// needs and a task's record endpoint already served. Goals and topics carry
	// them since v0.31.0; without them the tick applied the task defaults, so a
	// future-deferred goal opened its manager early.
	Role      string   `json:"role"`
	Mode      string   `json:"mode"`
	DeferDate string   `json:"defer_date"`
	BlockedBy []string `json:"blocked_by"`
}

// VaultAPIPage is one record's full frontmatter and body, as the record endpoint
// serves it.
//
// Frontmatter is the raw YAML map, not a typed struct: the tick's eligibility
// predicate already has one reader for it, and a second typed projection here
// would be a second place for a renamed field to decode to its zero value
// silently.
type VaultAPIPage struct {
	Frontmatter map[string]any `json:"frontmatter"`
	Body        string         `json:"body"`
}

// VaultAPIClient answers the flagged tick's two questions: which rows are
// eligible, and what each one's record says.
//
// An interface rather than a concrete HTTP client so the tick's decision rule is
// testable against a stub that returns an outage, an empty set, or a row — the
// three answers a fake must be able to produce without a server.
type VaultAPIClient interface {
	// List returns the eligible rows of one kind, already filtered by vault-api.
	// An outage is ErrVaultAPIUnavailable, never an empty slice.
	List(ctx context.Context, kind string) ([]VaultAPIRow, error)
	// ListAll returns every row of one kind, unfiltered — the subject index needs
	// the goals and topics that are not themselves eligible to open.
	ListAll(ctx context.Context, kind string) ([]VaultAPIRow, error)
	// Page returns one record's frontmatter and body.
	Page(ctx context.Context, kind, id string) (VaultAPIPage, error)
}

// vaultAPIKindPath maps a row kind to its endpoint segment.
//
// The three kinds are the tick's vocabulary (`task`, `goal`, `topic`); the API
// pluralises them. An unknown kind is refused rather than mapped to tasks: a
// silent fallback would answer a question about goals with tasks.
func vaultAPIKindPath(kind string) (string, bool) {
	switch kind {
	case "task":
		return "tasks", true
	case "goal":
		return "goals", true
	case "topic":
		return "topics", true
	}
	return "", false
}

// HTTPVaultAPIClient reads from the local vault-api.
type HTTPVaultAPIClient struct {
	// BaseURL is the API root, e.g. http://localhost:9091/api/v1.
	BaseURL string
	// HTTPClient defaults to a client bounded by Timeout.
	HTTPClient *http.Client
	// Timeout bounds one request. nil means DefaultVaultAPITimeout.
	Timeout time.Duration
}

// DefaultVaultAPITimeout bounds one request to the local index.
//
// It is deliberately short: the service answers an in-memory index in
// milliseconds, so a slow answer means it is unhealthy, and the tick has a
// two-minute interval to lose rather than a whole read to spend. The page-scan
// path it replaces took seconds; anything approaching that is a fault, not a
// large vault.
const DefaultVaultAPITimeout = 5 * time.Second

// DefaultVaultAPITaskQuery is the eligibility query the flagged tick sends.
//
// It is the same predicate the page scan applied in Go: flagged, open, and due
// now. The difference is that the index owns it, so the tick and every other
// consumer of "which rows are eligible" cannot disagree.
const DefaultVaultAPITaskQuery = "flag=true&status=in_progress&due=now"

// ListAll returns every row of one kind, unfiltered.
//
// The subject index needs every goal and topic, not only the flagged ones: owner
// resolution asks "which topic declares this goal" and "what session does this
// subject record", and both questions are about subjects that are not themselves
// eligible to open. Asking the eligibility query here would build a half index
// and resolve owners wrongly, silently.
func (c HTTPVaultAPIClient) ListAll(
	ctx context.Context,
	kind string,
) ([]VaultAPIRow, error) {
	segment, ok := vaultAPIKindPath(kind)
	if !ok {
		return nil, errors.Wrapf(ctx, ErrVaultAPIUnavailable, "unknown kind %q", kind)
	}
	var payload struct {
		Items []VaultAPIRow `json:"items"`
	}
	if err := c.get(ctx, segment, &payload); err != nil {
		return nil, err
	}
	if payload.Items == nil {
		return []VaultAPIRow{}, nil
	}
	return payload.Items, nil
}

// List returns the eligible rows of one kind.
func (c HTTPVaultAPIClient) List(ctx context.Context, kind string) ([]VaultAPIRow, error) {
	segment, ok := vaultAPIKindPath(kind)
	if !ok {
		return nil, errors.Wrapf(ctx, ErrVaultAPIUnavailable, "unknown kind %q", kind)
	}
	query := "flag=true"
	if kind == "task" {
		query = DefaultVaultAPITaskQuery
	}
	var payload struct {
		Items []VaultAPIRow `json:"items"`
	}
	if err := c.get(ctx, segment+"?"+query, &payload); err != nil {
		return nil, err
	}
	// A nil slice would read as "no rows" to a caller that only checks len; the
	// tick distinguishes an empty set from an outage by the error, so an empty
	// answer is an empty slice and never nil.
	if payload.Items == nil {
		return []VaultAPIRow{}, nil
	}
	return payload.Items, nil
}

// Page returns one record's frontmatter and body.
func (c HTTPVaultAPIClient) Page(
	ctx context.Context,
	kind, id string,
) (VaultAPIPage, error) {
	segment, ok := vaultAPIKindPath(kind)
	if !ok {
		return VaultAPIPage{}, errors.Wrapf(ctx, ErrVaultAPIUnavailable, "unknown kind %q", kind)
	}
	// Only the task endpoint serves frontmatter; a goal or topic record is its
	// list projection, which the caller already has. Refusing here is better than
	// returning an empty map that reads as "no fields set".
	if kind != "task" {
		return VaultAPIPage{}, errors.Wrapf(
			ctx,
			ErrVaultAPIUnavailable,
			"no frontmatter endpoint for kind %q",
			kind,
		)
	}
	var page VaultAPIPage
	if err := c.get(ctx, segment+"/"+url.PathEscape(id), &page); err != nil {
		return VaultAPIPage{}, err
	}
	return page, nil
}

// get performs one GET and decodes the JSON body into out.
//
// Every failure — transport, non-200, undecodable body — is wrapped with
// ErrVaultAPIUnavailable so the caller has one test for "do not trust this
// answer". A 404 is included deliberately: a record the index cannot resolve is
// an incomplete index, and treating it as an empty page would silently drop a
// row's enrichment.
func (c HTTPVaultAPIClient) get(ctx context.Context, path string, out any) error {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		return errors.Wrap(ctx, ErrVaultAPIUnavailable, "vault-api base url is empty")
	}
	client := c.HTTPClient
	if client == nil {
		timeout := c.Timeout
		if timeout == 0 {
			timeout = DefaultVaultAPITimeout
		}
		client = &http.Client{Timeout: timeout}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+path, nil)
	if err != nil {
		return errors.Wrap(ctx, ErrVaultAPIUnavailable, "build vault-api request")
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.Wrap(ctx, ErrVaultAPIUnavailable, "call vault-api")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return errors.Wrapf(
			ctx,
			ErrVaultAPIUnavailable,
			"vault-api answered %d for %s",
			response.StatusCode,
			path,
		)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return errors.Wrap(ctx, ErrVaultAPIUnavailable, "read vault-api body")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return errors.Wrap(ctx, ErrVaultAPIUnavailable, "decode vault-api body")
	}
	return nil
}

// String renders the client's target for a log line.
func (c HTTPVaultAPIClient) String() string {
	return fmt.Sprintf("vault-api %s", strings.TrimRight(c.BaseURL, "/"))
}
