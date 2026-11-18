// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"

	"github.com/bborbe/errors"
)

// Default script paths: the deployed marketplace clone the running fleet
// uses, matching --flagged-rows-script.
const (
	DefaultAnswerPathCheckScript = "~/.claude/plugins/marketplaces/claude-supervisor/scripts/answer-path-check.py"
	DefaultManagerLauncherScript = "~/.claude/plugins/marketplaces/claude-supervisor/scripts/manager-launcher.py"
	DefaultTopicSpawnScript      = "~/.claude/plugins/marketplaces/claude-supervisor/scripts/topic-spawn.py"
)

// MaxOpenBody is the open request's body cap — the same 1 MiB the MCP faces enforce.
const MaxOpenBody = maxMCPBody

// OpenRequestBody is the body of POST /api/1.0/open and the open_rows arguments.
type OpenRequestBody struct {
	Rows []OpenRow `json:"rows"`
}

// OpenResponseBody is what both surfaces return.
type OpenResponseBody struct {
	Verdicts []OpenVerdict `json:"verdicts"`
}

// OpenSurface is the one entry both open surfaces call, so REST and MCP
// decode, validate, open and marshal through the same code.
type OpenSurface struct {
	Executor OpenExecutor
	Vaults   VaultLister
}

// Serve decodes body, validates every row, opens them in request mode and
// returns the marshalled OpenResponseBody. A malformed request returns an
// error wrapping ErrInvalidOpenRequest before anything is opened.
//
// Unknown JSON fields are IGNORED on purpose: a row carrying `"mode": "tick"`
// behaves exactly as request mode, so no request can select the tick's caller
// mode (DB8, AC24).
func (s *OpenSurface) Serve(
	ctx context.Context,
	body []byte,
	callerSessionID string,
) ([]byte, error) {
	var request OpenRequestBody
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, errors.Wrapf(
			ctx, ErrInvalidOpenRequest, "body is not a JSON open request: %v", err,
		)
	}
	vaults, err := s.Vaults.List(ctx)
	if err != nil {
		// A server-side failure, not a malformed request: it never wraps
		// ErrInvalidOpenRequest, so the surfaces answer 500 rather than 400.
		return nil, errors.Wrapf(ctx, err, "list vaults failed")
	}
	if err := ValidateOpenRows(ctx, request.Rows, vaults); err != nil {
		return nil, err
	}
	// Only ever OpenRequest: neither surface can select tick mode.
	verdicts := s.Executor.OpenRequest(ctx, request.Rows, callerSessionID)
	encoded, err := json.Marshal(OpenResponseBody{Verdicts: verdicts})
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "marshal open verdicts failed")
	}
	return encoded, nil
}

// ValidateOpenRows refuses a malformed request before anything is opened.
//
// Every refusal wraps ErrInvalidOpenRequest and names the row index. first_step
// is not validated: it is composer text, bounded by the body cap, and it never
// reaches an argv or a shell.
func ValidateOpenRows(ctx context.Context, rows []OpenRow, vaults []Vault) error {
	if len(rows) == 0 {
		return errors.Wrapf(ctx, ErrInvalidOpenRequest, "rows is empty")
	}
	for index, row := range rows {
		if err := validateOpenRow(ctx, index, row, vaults); err != nil {
			return err
		}
	}
	return nil
}

// validateOpenRow validates one row, naming its index in every refusal.
func validateOpenRow(ctx context.Context, index int, row OpenRow, vaults []Vault) error {
	if row.Kind != OpenKindTask && row.Kind != OpenKindGoal && row.Kind != OpenKindTopic {
		return errors.Wrapf(
			ctx, ErrInvalidOpenRequest, "row %d: unknown kind %q", index, row.Kind,
		)
	}
	if strings.TrimSpace(row.Name) == "" {
		return errors.Wrapf(ctx, ErrInvalidOpenRequest, "row %d: name is empty", index)
	}
	if strings.TrimSpace(row.Vault) == "" {
		return errors.Wrapf(ctx, ErrInvalidOpenRequest, "row %d: vault is empty", index)
	}
	if unsafeOpenName(row.Name) {
		return errors.Wrapf(
			ctx,
			ErrInvalidOpenRequest,
			`row %d: name %q may not contain a path separator, ".." or a control character`,
			index,
			row.Name,
		)
	}
	if !openVaultConfigured(vaults, row.Vault) {
		return errors.Wrapf(
			ctx, ErrInvalidOpenRequest, "row %d: vault %q is not configured", index, row.Vault,
		)
	}
	return nil
}

// unsafeOpenName reports whether a title would escape its vault folder or
// carry a control character into a log line.
func unsafeOpenName(name string) bool {
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return true
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// openVaultConfigured reports whether name is one of the listed vaults.
func openVaultConfigured(vaults []Vault, name string) bool {
	for _, vault := range vaults {
		if vault.Name == name {
			return true
		}
	}
	return false
}

// LoopbackRequest and IsJSONRequest expose the MCP face's structural guards
// to the REST route, so both surfaces share them rather than copy them.
func LoopbackRequest(req *http.Request) bool { return originAllowed(req) }

// IsJSONRequest reports whether the request declares a JSON body.
func IsJSONRequest(req *http.Request) bool { return isJSONRequest(req) }
