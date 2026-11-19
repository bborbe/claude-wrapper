// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"

	"github.com/bborbe/errors"
)

// openRows is the MCP half of the one open operation.
//
// It hands the tool's arguments to OpenSurface.Serve, which POST /api/1.0/open
// calls too, so both surfaces decode, validate, open and marshal identically
// (AC4). A nil surface is a daemon wiring slip, refused by name.
func (s *RosterToolServer) openRows(
	ctx context.Context,
	args json.RawMessage,
	sessionID string,
) (string, error) {
	if s.Open == nil {
		return "", errors.Errorf(ctx, "open_rows is not wired")
	}
	out, err := s.Open.Serve(ctx, args, sessionID)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// openToolDefinitions describes the open_rows tool.
func openToolDefinitions() []any {
	return []any{
		toolDef(ToolOpenRows,
			"Open task, goal or topic rows the way each role requires: an agent goes "+
				"headless, a human opens in the Direct window, and a goal or topic manager "+
				"opens in the Managers window. Returns one verdict per row in request order, "+
				"identical to `POST /api/1.0/open`, and runs the answer-path probe for this "+
				"session before any headless spawn.",
			map[string]any{
				"rows": map[string]any{
					"type":        "array",
					"description": "rows to open, in order",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"kind":       stringProp("task, goal or topic"),
							"vault":      stringProp("a configured vault name"),
							"name":       stringProp("the page title"),
							"first_step": stringProp("optional text typed before the work command"),
						},
						"required": []string{"kind", "vault", "name"},
					},
				},
			},
			"rows"),
	}
}
