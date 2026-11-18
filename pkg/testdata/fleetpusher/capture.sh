#!/bin/bash
# Capture one pane's screen as a guard fixture, with its provenance line.
#
#   capture.sh <pane-id> <out-file>
#
# `wezterm cli get-text` emits screen text only, so this wrapper writes the
# `# captured <ISO8601> pane=<id>` line above it. PaneGuardVerdict skips that
# line, so the fixture reaches the verdict function exactly as a live pane does.
#
# Drive a scratch pane into the state first, then capture it:
#
#   pane=$(wezterm cli spawn --new-window -- bash -c '<print the state>; sleep 30')
#   ./capture.sh "$pane" pkg/testdata/fleetpusher/<name>.txt
#   wezterm cli kill-pane --pane-id "$pane"
#
# To re-capture an existing fixture, print its body (everything below line 1)
# into the scratch pane and capture as above — the replay reproduces the screen
# and gives it a real pane id.
set -eu

pane="${1:?usage: capture.sh <pane-id> <out-file>}"
out="${2:?usage: capture.sh <pane-id> <out-file>}"
captured="$(date +%Y-%m-%dT%H:%M:%S%z)"

{
	printf '# captured %s pane=%s\r\n' "$captured" "$pane"
	wezterm cli get-text --pane-id "$pane" --escapes
} >"$out"
