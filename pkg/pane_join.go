// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"

	"github.com/bborbe/errors"
)

// PaneJoiner resolves a session's pid to the WezTerm pane its terminal is
// attached to.
//
// This is the one piece claude-wrapper did not have. The registry is keyed by pid
// and WezTerm is keyed by pane id, and the tty is the only value both carry — so
// the join is pid → tty (`ps`) → pane (`wezterm cli list`). Ported from
// `plugin-version-census.py` `wezterm_panes` / `_pid_ttys` / `panes_for_pids`.
type PaneJoiner interface {
	// PanesForPIDs returns {pid: pane id} for every pid whose tty has a pane.
	//
	// A read failure is an error, never an empty map: collapsing the two would
	// report "this fleet has no panes" for a wezterm that is merely unreachable,
	// which is a measurement the caller cannot tell from a real empty fleet.
	PanesForPIDs(ctx context.Context, pids []int) (map[int]string, error)
}

// NewWeztermPaneJoiner creates a PaneJoiner over the wezterm binary.
//
// bin defaults to "wezterm" when empty.
func NewWeztermPaneJoiner(bin string) PaneJoiner {
	if bin == "" {
		bin = "wezterm"
	}
	return &weztermPaneJoiner{bin: bin}
}

type weztermPaneJoiner struct {
	bin string
}

// PanesForPIDs performs the two reads and the join.
func (j *weztermPaneJoiner) PanesForPIDs(
	ctx context.Context,
	pids []int,
) (map[int]string, error) {
	panes, err := j.panes(ctx)
	if err != nil {
		return nil, err
	}
	ttys, err := pidTTYs(ctx, pids)
	if err != nil {
		return nil, err
	}
	return JoinPanes(panes, ttys), nil
}

// panes returns {pane id: tty name} from `wezterm cli list --format json`.
func (j *weztermPaneJoiner) panes(ctx context.Context) (map[string]string, error) {
	// #nosec G204 -- j.bin is the configured wezterm binary and the argument
	// list is fixed.
	output, err := exec.CommandContext(
		ctx, j.bin, "cli", "list", "--format", "json",
	).Output()
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "wezterm cli list failed")
	}
	return parseWeztermPanes(output)
}

// parseWeztermPanes reads the `pane_id` / `tty_name` pairs out of the list JSON.
func parseWeztermPanes(raw []byte) (map[string]string, error) {
	var rows []struct {
		PaneID  json.Number `json:"pane_id"`
		TTYName string      `json:"tty_name"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, errors.Wrapf(context.Background(), err, "parse wezterm panes failed")
	}
	panes := make(map[string]string, len(rows))
	for _, row := range rows {
		paneID := row.PaneID.String()
		if paneID == "" {
			continue
		}
		panes[paneID] = row.TTYName
	}
	return panes, nil
}

// pidTTYs returns {pid: tty} from `ps`, with the `/dev/` prefix removed so the
// two halves compare on the same string.
func pidTTYs(ctx context.Context, pids []int) (map[int]string, error) {
	if len(pids) == 0 {
		return map[int]string{}, nil
	}
	args := make([]string, 0, len(pids)+2)
	args = append(args, "-o", "pid=,tty=", "-p")
	values := make([]string, 0, len(pids))
	for _, pid := range pids {
		values = append(values, strconv.Itoa(pid))
	}
	args = append(args, strings.Join(values, ","))
	// #nosec G204 -- the arguments are numeric pids this package read from the
	// registry, and `ps` is a fixed binary path.
	output, err := exec.CommandContext(ctx, "ps", args...).Output()
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "ps failed")
	}
	return parsePIDTTYs(string(output)), nil
}

// parsePIDTTYs reads `pid tty` pairs from `ps -o pid=,tty=` output.
func parsePIDTTYs(output string) map[int]string {
	ttys := map[int]string{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ttys[pid] = strings.TrimPrefix(fields[1], "/dev/")
	}
	return ttys
}

// JoinPanes returns {pid: pane id} for every pid whose tty names a pane.
//
// Pure: both halves are passed in, so the join is testable without a terminal.
// The tty comparison strips `/dev/` on both sides because `ps` and wezterm name
// the same device differently.
func JoinPanes(panes map[string]string, ttys map[int]string) map[int]string {
	byTTY := make(map[string]string, len(panes))
	for paneID, tty := range panes {
		name := strings.TrimPrefix(tty, "/dev/")
		if name == "" {
			continue
		}
		if _, ok := byTTY[name]; !ok {
			byTTY[name] = paneID
		}
	}
	joined := make(map[int]string, len(ttys))
	for pid, tty := range ttys {
		name := strings.TrimPrefix(tty, "/dev/")
		if paneID, ok := byTTY[name]; ok {
			joined[pid] = paneID
		}
	}
	return joined
}
