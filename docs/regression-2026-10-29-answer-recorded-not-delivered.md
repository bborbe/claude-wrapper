# Regression — 2026-10-29: an answer recorded but never delivered

## What happened

A fleet-manager session answered a worker's `AskUserQuestion`. The answer route returned
`ANSWERED`, the attention board card cleared, and the worker stayed parked on its modal for
**over three hours**.

Every surface said the answer had landed. None of them could tell whether the *child* had
consumed it, because the registry only stored an allow/deny string — there was nowhere to put
"option 2 of 4" — so a selection had to be coerced into a verdict the child could not use. The
answer was recorded; the child never moved.

## Why the obvious check does not catch it

A `200`, an `ANSWERED` response, and a cleared card all coexist with the failure. They report the
**registry**, not the child.

The same trap exists on the permission path and is easier to miss: `pkg/mcp.go`'s hold expiry
returns the *default* verdict, and `Allow` defaults to `true`. So a permission prompt that was
never answered still produces `{"behavior":"allow"}` and the child proceeds — a "completed tool
call" is not evidence that anything was delivered. That is why a permission proof must pin the
answer to **deny**: a delivered deny returns `denied by claude-wrapper`, which a timeout cannot
produce.

## What to check instead

The **child's own next event**, after the answer:

- permission: a `tool_result` block whose `content` is `denied by claude-wrapper`
- question: a `tool_result` block carrying
  `Your questions have been answered: "<question>"="<label>"`, followed by an assistant turn that
  quotes the chosen label

## The three defects this exposed

1. The registry had no shape for a selection (`behavior string` only).
2. `send_message` waited for the turn to close, but the child's permission hold is 90 s — so a
   caller could never answer inside it. It now returns the moment a request is registered.
3. `updatedInput` for an `AskUserQuestion` must satisfy the tool's own input schema. Returning
   `{"answers": …}` alone fails with `The required parameter "questions" is missing`; the original
   `questions` must be echoed back beside the answers.

## Do not weaken

A future change that makes SC5 pass on a status code, a cleared card, or a completed tool call is
re-introducing this exact failure. The evidence is the child's next event, and nothing else.
