#!/bin/bash
# Capture one pane's screen as a guard fixture, with its provenance line.
#
#   capture.sh <pane-id> <out-file>
#
# `wezterm cli get-text` emits screen text only, so this wrapper writes the
# `# captured <ISO8601> pane=<id>` line above it. PaneGuardVerdict skips that
# line, so the fixture is replayed unmodified.
#
# The write goes to a temp file beside the target and is moved into place only
# after the capture succeeds, so a dead pane id or a stopped mux cannot destroy
# a committed fixture.
#
# Drive a scratch pane into the state first, let it render, then capture it:
#
#   pane=$(wezterm cli spawn --new-window -- bash -c '<print the state>; sleep 30')
#   sleep 1                      # let the pane render before reading it
#   ./capture.sh "$pane" pkg/testdata/fleetpusher/<name>.txt
#   wezterm cli kill-pane --pane-id "$pane"
#
# The pane's geometry decides the capture's row count — the fixtures were taken
# from a 27-row pane, and a taller one appends blank rows. To re-capture an
# existing fixture, print its body (everything below line 1) into the scratch
# pane and capture as above.
set -eu

pane="${1:?usage: capture.sh <pane-id> <out-file>}"
out="${2:?usage: capture.sh <pane-id> <out-file>}"
captured="$(date +%Y-%m-%dT%H:%M:%S%z)"
partial="$(mktemp "${out}.XXXXXX")"

cleanup() { rm -f "$partial"; }
trap cleanup EXIT

{
	printf '# captured %s pane=%s\r\n' "$captured" "$pane"
	wezterm cli get-text --pane-id "$pane" --escapes
} >"$partial"

mv "$partial" "$out"
trap - EXIT
