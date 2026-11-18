---
status: draft
spec: [001-bug-headless-spawn-drops-shipping-permission-mode]
created: "2026-11-18T18:45:00Z"
branch: dark-factory/bug-headless-spawn-drops-shipping-permission-mode
---

# Pin the headless shipping-mode mapping with a regression spec

<summary>
- A table-driven test pins both directions of the `shipping` → permission-mode mapping.
- It reads the mode from what the fake supervisor was handed, not from the reply and not from a test constant.
- It proves a shipping spawn requests `acceptEdits` and a non-shipping spawn requests `auto`.
- It proves a stray `permission_mode` argument is ignored, so the bypass mode cannot be reached through this tool.
- It proves a resumed shipping spawn requests `acceptEdits` and carries both `--resume` and `--permission-mode acceptEdits` on its command line.
- It proves the tab-path `shipping` refusal still fires before any spawn.
- The test prints one `case=` line per entry, which is the evidence the spec's acceptance criterion reads.
- Existing `buildArgs` and A2A refusal specs stay unedited and keep passing.
</summary>

<objective>
Add `pkg/roster_shipping_mode_test.go` with a ginkgo `DescribeTable` whose focus string is `headless spawn permission mode`. The spec drives the real `spawnAgent` adapter with raw JSON arguments, records the `SpawnOptions` the supervisor received, asserts the requested mode and source in both the captured options and the decoded reply, and asserts the `buildArgs` output for the resume case. It also pins the stray-argument and tab-refusal behaviours. This test must fail if the `shipping` mapping is dropped or inverted.
</objective>

<context>
Read these before writing the spec:

- `pkg/roster_mcp.go` — `spawnAgent`, `spawnLocalAgent`, `spawnAgentArgs`, `ShippingPermissionMode`, `PermissionModeSourceShipping`, `PermissionModeSourceDefault`, and the reply map. The first prompt in this sequence adds these; if they are absent, stop and report rather than inventing a different name.
- `pkg/supervisor.go` — `SpawnOptions.PermissionMode`, `DefaultPermissionMode`, and `buildArgs`.
- `pkg/mcptools_test.go` — `toolSupervisor`: `Spawn` appends the `SpawnOptions` it received to `spawned` and returns `&Session{ID: "s1", State: StateStarting, Model: options.Model}`.
- `pkg/tab_spawn_test.go` — `fakeSpawner`, the tab spawner double. `&fakeSpawner{}` is enough for a headless call.
- `pkg/roster_resume_test.go` — the established pattern: `server.spawnAgent(ctx, json.RawMessage(args), "")`, then read `supervisor.spawned[0]`.
- `pkg/supervisor_test.go` — `indexOf(args []string, want string) int`, and the spec `"still passes the model and permission mode"` (do not edit it).
- `pkg/pkg_suite_test.go` — the `Pkg Suite`; new specs are in-package and use qualified `ginkgo` / `gomega`, never dot-imports.
- `pkg/resume_prompt.go` — `refusePromptlessResume`. A headless resume with no `prompt` is refused before `Spawn`, so the resume case must carry one.

Coding guides (in container):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`

Verify-or-create: if `pkg/roster_shipping_mode_test.go` already exists and matches this prompt, confirm it and leave it in place rather than duplicating the spec.
</context>

<requirements>
1. **Create `pkg/roster_shipping_mode_test.go`**, package `pkg`, with the standard BSD header used by the other test files:

   ```go
   // Copyright (c) 2026 Benjamin Borbe All rights reserved.
   // Use of this source code is governed by a BSD-style
   // license that can be found in the LICENSE file.
   ```

   Import `context`, `encoding/json`, `fmt`, `github.com/onsi/ginkgo/v2`, and `github.com/onsi/gomega`. Use qualified `ginkgo` / `gomega`.

2. **The describe text must contain the exact phrase `headless spawn permission mode`**, because the spec's evidence command focuses on it. Use:

   ```go
   var _ = ginkgo.Describe("RosterToolServer.spawnAgent headless spawn permission mode", func() { ... })
   ```

3. **Set up the server the way `pkg/roster_resume_test.go` does.** The fake supervisor needs a live channel because the resume case carries an opening turn and `Send` emits on `live`; a nil channel blocks forever.

   ```go
   const resumed = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

   var (
       ctx        context.Context
       supervisor *toolSupervisor
       server     *RosterToolServer
   )

   ginkgo.BeforeEach(func() {
       ctx = context.Background()
       supervisor = &toolSupervisor{live: make(chan Event, 64)}
       server = &RosterToolServer{Supervisor: supervisor, Spawner: &fakeSpawner{}}
   })
   ```

   Add a helper that calls the tool and decodes the reply:

   ```go
   spawn := func(args string) map[string]string {
       reply, err := server.spawnAgent(ctx, json.RawMessage(args), "")
       gomega.Expect(err).To(gomega.BeNil())
       var decoded map[string]string
       gomega.Expect(json.Unmarshal([]byte(reply), &decoded)).To(gomega.Succeed())
       return decoded
   }
   ```

4. **Add a `ginkgo.DescribeTable` with exactly these four entries, in this order.** The table body must:

   ```go
   ginkgo.DescribeTable("hands the supervisor the mode the shipping flag decides",
       func(name, args, wantMode, wantSource string, wantResume bool) {
           reply := spawn(args)
           gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
           options := supervisor.spawned[0]

           // Read from what the supervisor was HANDED, never from the reply alone:
           // a reply-only assertion would pass on a hardcoded echo.
           gomega.Expect(options.PermissionMode).To(gomega.Equal(wantMode))
           gomega.Expect(reply["permission_mode"]).To(gomega.Equal(wantMode))
           gomega.Expect(reply["permission_mode_source"]).To(gomega.Equal(wantSource))

           args2 := buildArgs("11111111-2222-4333-8444-555555555555", options, "/tmp/generated.json")
           gomega.Expect(indexOf(args2, "--permission-mode") + 1).
               To(gomega.Equal(indexOf(args2, wantMode)))
           if wantResume {
               gomega.Expect(indexOf(args2, "--resume") + 1).
                   To(gomega.Equal(indexOf(args2, resumed)))
           }

           // The verdict line the spec's evidence reads. `requested` comes from the
           // captured SpawnOptions, never from the test's wantMode and never from
           // the reply, so a dropped mapping prints the wrong mode instead of
           // echoing the expectation.
           fmt.Printf("case=%s requested=%s source=%s\n", name, options.PermissionMode, reply["permission_mode_source"])
       },
       ginkgo.Entry("shipping", "shipping",
           `{"shipping":true,"interactive":false,"cwd":"/tmp"}`,
           ShippingPermissionMode, PermissionModeSourceShipping, false),
       ginkgo.Entry("no-shipping", "no-shipping",
           `{"interactive":false,"cwd":"/tmp"}`,
           DefaultPermissionMode, PermissionModeSourceDefault, false),
       ginkgo.Entry("shipping-with-stray-permission-mode", "shipping-with-stray-permission-mode",
           `{"shipping":true,"permission_mode":"bypassPermissions","interactive":false,"cwd":"/tmp"}`,
           ShippingPermissionMode, PermissionModeSourceShipping, false),
       ginkgo.Entry("resume-shipping", "resume-shipping",
           `{"resume":"`+resumed+`","shipping":true,"interactive":false,"cwd":"/tmp","prompt":"continue"}`,
           ShippingPermissionMode, PermissionModeSourceShipping, true),
   )
   ```

   The printed lines must be exactly:

   ```
   case=shipping requested=acceptEdits source=shipping
   case=no-shipping requested=auto source=default
   case=shipping-with-stray-permission-mode requested=acceptEdits source=shipping
   case=resume-shipping requested=acceptEdits source=shipping
   ```

   `requested` is read from the captured `SpawnOptions.PermissionMode`, so the line reflects what the supervisor was actually handed. `source` is read from the reply's `permission_mode_source`.

5. **Add a spec proving a stray `permission_mode` argument is ignored.** The mode comes from `shipping` alone:

   ```go
   ginkgo.It("decodes no permission_mode argument", func() {
       reply := spawn(`{"shipping":false,"permission_mode":"bypassPermissions","interactive":false,"cwd":"/tmp"}`)
       gomega.Expect(reply["permission_mode"]).To(gomega.Equal(DefaultPermissionMode))
   })
   ```

6. **Add a spec proving the tab-path refusal still fires before any spawn.** It must assert both the error text and that the supervisor received no spawn:

   ```go
   ginkgo.It("keeps the tab refusal for shipping", func() {
       _, err := server.spawnAgent(ctx,
           json.RawMessage(`{"shipping":true,"interactive":true,"cwd":"/tmp","prompt":"hi"}`), "")
       gomega.Expect(err).NotTo(gomega.BeNil())
       gomega.Expect(err.Error()).To(gomega.ContainSubstring("needs the server to set the worker"))
       gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
   })
   ```

7. **Do not modify `pkg/supervisor_test.go`, `pkg/a2a/target_test.go`, or any production file.** The existing `buildArgs` spec `"still passes the model and permission mode"` and the A2A entry `"shipping is not supported"` are the untouched-refusal evidence; they must pass unedited under `make precommit`.

8. **Mutation sensitivity is built in, not a separate test.** With the pre-fix literal (no `PermissionMode` set on `SpawnOptions`), `options.PermissionMode` is `""` and every table entry except a hypothetical empty-mode expectation fails. Do not weaken the assertion to read only the reply, and do not compare against a constant instead of the captured `SpawnOptions`.

9. Run `make precommit` and the focused command in the Verification section before finishing.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, especially `pkg/supervisor_test.go`'s `"still passes the model and permission mode"` and `pkg/a2a/target_test.go`'s `"shipping is not supported"`; do not edit either.
- Tests are in-package ginkgo specs. Import `ginkgo` / `gomega` qualified, never dot-imported.
- Do NOT add a new `TestXxx` or `RunSpecs`; the spec joins the existing `Pkg Suite`.
- Do NOT assert the mode from the reply alone. The captured `SpawnOptions.PermissionMode` is the primary assertion; the reply is the second.
- Do NOT print a test constant as `requested`; print the captured `options.PermissionMode` so a dropped mapping is visible in the evidence line.
- Do NOT change production code in this prompt. If the mapping from the previous prompt is missing or wrong, report it rather than fixing it here.
- The four `case=` lines are a fixed interface: names, order and spacing are read by the spec's acceptance evidence.
- No network, no child process, no real supervisor: the `toolSupervisor` and `fakeSpawner` doubles are the whole harness.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Then run the focused spec and confirm the four evidence lines:

```
go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='headless spawn permission mode' -ginkgo.v | grep '^case='
```

The output must be exactly these four lines, in this order:

```
case=shipping requested=acceptEdits source=shipping
case=no-shipping requested=auto source=default
case=shipping-with-stray-permission-mode requested=acceptEdits source=shipping
case=resume-shipping requested=acceptEdits source=shipping
```

Also confirm the suite actually ran the spec rather than matching nothing: the same command without the `grep` must report a non-zero number of specs (the spec warns that `-ginkgo.focus` on a string matching nothing exits 0 with `Ran 0 of N Specs`).

The spec's mutation half is a verifier step, not a deliverable: replacing the `headlessPermissionMode` call in `spawnLocalAgent` with the pre-fix literal (no `PermissionMode`) must make this spec fail. The assertions above already do that; do not add a second mutation test.
</verification>

<!--
OPEN QUESTION FOR THE HUMAN REVIEWER (not an instruction to the agent):

The spec's AC2 describes the resume case's raw arguments as `{"resume":"<uuid>","shipping":true,"interactive":false,"cwd":"/tmp"}`. Since 2026-11-16 `refusePromptlessResume` (`pkg/resume_prompt.go`) refuses a headless resume with no opening turn, so those exact arguments never reach `Spawn`. This prompt adds `"prompt":"continue"` so the case still pins `--resume` plus `--permission-mode acceptEdits`. If the reviewer wants the literal AC2 arguments, the promptless-resume guard must be relaxed for this test, which would be a production-behaviour change outside this spec.
-->
