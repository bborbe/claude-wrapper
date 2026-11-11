---
status: idea
kind: bug
---

## Summary

`make precommit` fails inside the dark-factory container on two specs that pass on macOS. The completion-report validator refuses `status: partial`, so **every** dark-factory run on this repo is marked failed and commits nothing — the cluster route is unusable here regardless of the change.

## Reproduction

Container run 2026-11-11 (task `06fdbae9`, pod `github-dark-factory-agent-06fdbae9-20261111110541-g7p44`), verbatim from its report:

> `pkg/procexit_test.go:41 'returns as soon as the process leaves' fails on Linux because a SIGKILLed unreaped child stays a zombie, so poll-based waitForProcessExit never returns; it fails identically at HEAD without this change and the darwin kqueue path is unaffected.`

> `pkg/spawn_axes_test.go:335 'matches case-insensitively' fails on Linux because ManagerKindForLabel relies on filesystem case-insensitivity and this overlay fs is case-sensitive; it fails identically at HEAD without this change.`

Host control (macOS), same two specs:

```
go test ./pkg -run TestPkg -args -ginkgo.focus='returns as soon as the process leaves|matches case-insensitively'
ok  	github.com/bborbe/claude-wrapper/pkg	0.544s
```

## Expected vs Actual

- Expected: the repo's only gate (`make precommit`; there is no CI workflow in this repo) passes in the environment the pipeline runs it in.
- Actual: it fails on two specs that are environment-dependent, so the pipeline cannot complete any change.

## Why this is a bug

A gate that cannot pass in the environment that must pass it is not a gate; it is a permanent stop. The two specs each assume a host property — Linux process reaping for one, a case-insensitive filesystem for the other — that the container does not provide.

## Open Questions

- Fix the specs to be environment-independent, or tag them `Pending`/skip on Linux with a stated reason?
- The `procexit` one may be a real Linux production bug rather than a test artefact: if the daemon ever runs on Linux, `waitForProcessExit` never returning is a hang, not a flaky spec.

Found 2026-11-11 while landing the `shipping` permission-mode fix.
