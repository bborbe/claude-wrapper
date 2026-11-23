// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg"
	"github.com/bborbe/claude-wrapper/pkg/handler"
)

const openTestTaskFolder = "25 Tasks"

// openTestUUID returns a full 36-character UUID-shaped id.
func openTestUUID(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

func openTestPtr[T any](value T) *T { return &value }

// openFakeVaults is a VaultLister over a fixed list.
type openFakeVaults struct {
	vaults []pkg.Vault
	err    error
}

func (f *openFakeVaults) List(context.Context) ([]pkg.Vault, error) {
	return f.vaults, f.err
}

// openFakeRegistry is a SessionRegistry over a fixed live set.
type openFakeRegistry struct {
	live []pkg.RegistryEntry
	err  error
}

func (f *openFakeRegistry) Status(context.Context, string) (*string, error) {
	return nil, nil
}

func (f *openFakeRegistry) Cwd(context.Context, string) (*string, error) { return nil, nil }

func (f *openFakeRegistry) Live(context.Context) ([]pkg.RegistryEntry, error) {
	return f.live, f.err
}

func (f *openFakeRegistry) Entries(context.Context) ([]pkg.RegistryEntry, error) {
	return f.live, f.err
}

func (f *openFakeRegistry) Named(context.Context, string) ([]string, error) {
	return nil, nil
}

func (f *openFakeRegistry) NamesForSession(context.Context, string) ([]string, error) {
	return nil, nil
}

// openFakePanes is a PaneJoiner with a fixed pid→pane map.
type openFakePanes struct {
	panes map[int]string
	err   error
}

func (f *openFakePanes) PanesForPIDs(context.Context, []int) (map[int]string, error) {
	return f.panes, f.err
}

// openFakeRoleMap is a RoleMapReader over a fixed map.
type openFakeRoleMap struct {
	entries map[string]pkg.RoleMapEntry
	err     error
	reads   int
}

func (f *openFakeRoleMap) Read(context.Context) (map[string]pkg.RoleMapEntry, error) {
	f.reads++
	return f.entries, f.err
}

// openFakeTabs is a TabClient; the executor uses Activate for a jump row.
type openFakeTabs struct {
	spawned   []pkg.TabSpawnOptions
	activated []string
	texts     []string
}

func (f *openFakeTabs) Spawn(
	_ context.Context,
	options pkg.TabSpawnOptions,
) (string, error) {
	f.spawned = append(f.spawned, options)
	return "pane-tab", nil
}

func (f *openFakeTabs) Activate(_ context.Context, paneID string) error {
	f.activated = append(f.activated, paneID)
	return nil
}

func (f *openFakeTabs) SendText(_ context.Context, _ string, text string) error {
	f.texts = append(f.texts, text)
	return nil
}

func (f *openFakeTabs) Text(context.Context, string) (string, error) {
	return "❯ \n", nil
}

// openFakeTabOpener records every tab request.
type openFakeTabOpener struct {
	requests []pkg.OpenTabRequest
	result   pkg.OpenTabResult
	err      error
}

func (f *openFakeTabOpener) Open(
	_ context.Context,
	request pkg.OpenTabRequest,
) (*pkg.OpenTabResult, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return nil, f.err
	}
	result := f.result
	if result.PaneID == "" {
		result.PaneID = "pane-tab"
	}
	if result.SessionID == "" {
		result.SessionID = openTestUUID(900)
	}
	return &result, nil
}

// openFakeHeadless records every headless request.
type openFakeHeadless struct {
	requests []pkg.OpenHeadlessRequest
	err      error
}

func (f *openFakeHeadless) SpawnOpenHeadless(
	_ context.Context,
	request pkg.OpenHeadlessRequest,
) (*pkg.Session, *pkg.OpenBind, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return nil, nil, f.err
	}
	id := request.Resume
	if id == "" {
		id = openTestUUID(500)
	}
	return &pkg.Session{ID: id}, &pkg.OpenBind{
		Task: request.Task, SessionID: id, Action: "stamped",
	}, nil
}

// openFakeRunner records every write-back argv.
type openFakeRunner struct {
	argvs [][]string
	err   error
}

func (f *openFakeRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	f.argvs = append(f.argvs, append([]string{name}, args...))
	return nil, f.err
}

// openFakeScripts records every script argv and answers from a map.
type openFakeScripts struct {
	results map[string]pkg.ScriptResult
	errs    map[string]error
	calls   [][]string
	stdin   []string
}

func (f *openFakeScripts) RunScript(
	_ context.Context,
	name string,
	args ...string,
) (pkg.ScriptResult, error) {
	argv := append([]string{name}, args...)
	f.calls = append(f.calls, argv)
	key := ""
	if len(argv) > 1 {
		key = argv[1]
	}
	if err, ok := f.errs[key]; ok {
		return f.results[key], err
	}
	return f.results[key], nil
}

// RunScriptWithStdin records the stdin it was handed, then answers from the map.
func (f *openFakeScripts) RunScriptWithStdin(
	_ context.Context,
	stdin string,
	name string,
	args ...string,
) (pkg.ScriptResult, error) {
	f.stdin = append(f.stdin, stdin)
	return f.RunScript(context.Background(), name, args...)
}

// probeCalls counts the answer-path probe invocations.
func (f *openFakeScripts) probeCalls() int {
	count := 0
	for _, argv := range f.calls {
		if len(argv) > 1 && argv[1] == "/scripts/answer-path-check.py" {
			count++
		}
	}
	return count
}

// openFakeRoster is the roster the MCP face needs; open_rows never reads it.
type openFakeRoster struct{}

func (openFakeRoster) List(context.Context) ([]pkg.AgentRow, error) { return nil, nil }

func (openFakeRoster) Get(context.Context, string) (*pkg.AgentRow, error) {
	return nil, pkg.ErrAgentNotFound
}

// openTestFixture wires a real OpenSurface over handler-local fakes.
type openTestFixture struct {
	dir       string
	vaults    *openFakeVaults
	registry  *openFakeRegistry
	panes     *openFakePanes
	roleMap   *openFakeRoleMap
	tabs      *openFakeTabs
	tabOpener *openFakeTabOpener
	headless  *openFakeHeadless
	runner    *openFakeRunner
	scripts   *openFakeScripts
	surface   *pkg.OpenSurface
}

func newOpenTestFixture(dir string) *openTestFixture {
	vault := pkg.Vault{
		Name:            "v1",
		Path:            dir,
		ClaudeScript:    "/scripts/cc-v1",
		ManagerLauncher: "/scripts/mgr",
	}
	f := &openTestFixture{
		dir:      dir,
		vaults:   &openFakeVaults{vaults: []pkg.Vault{vault}},
		registry: &openFakeRegistry{},
		panes:    &openFakePanes{panes: map[int]string{}},
		roleMap: &openFakeRoleMap{entries: map[string]pkg.RoleMapEntry{
			"Direct":   {Chip: openTestPtr("cyan"), WindowID: openTestPtr(12)},
			"Managers": {Chip: openTestPtr("orange"), WindowID: openTestPtr(14)},
			"Hold":     {WindowID: openTestPtr(2)},
		}},
		tabs:      &openFakeTabs{},
		tabOpener: &openFakeTabOpener{},
		headless:  &openFakeHeadless{},
		runner:    &openFakeRunner{},
		scripts: &openFakeScripts{results: map[string]pkg.ScriptResult{
			"/scripts/answer-path-check.py": {ExitCode: 0},
		}},
	}
	f.surface = &pkg.OpenSurface{
		Executor: pkg.NewOpenExecutor(pkg.OpenExecutorDeps{
			Vaults:           f.vaults,
			Registry:         f.registry,
			Panes:            f.panes,
			RoleMap:          f.roleMap,
			Tabs:             f.tabs,
			TabOpener:        f.tabOpener,
			Headless:         f.headless,
			Runner:           f.runner,
			Scripts:          f.scripts,
			AnswerPathScript: "/scripts/answer-path-check.py",
			Logf:             func(string, ...any) {},
		}),
		Vaults: f.vaults,
	}
	return f
}

// newOpenTestFixtureWithDir creates a fixture over a fresh temp vault.
func newOpenTestFixtureWithDir() *openTestFixture {
	return newOpenTestFixture(GinkgoT().TempDir())
}

// writeOpenTestPage writes one task page with the given frontmatter lines.
func writeOpenTestPage(dir, name string, frontmatter ...string) {
	Expect(os.MkdirAll(filepath.Join(dir, openTestTaskFolder), 0o755)).To(Succeed())
	body := "---\n" + strings.Join(frontmatter, "\n") + "\n---\n"
	Expect(os.WriteFile(
		filepath.Join(dir, openTestTaskFolder, name+".md"), []byte(body), 0o644,
	)).To(Succeed())
}

// openTestRequest builds a POST /api/1.0/open request with the loopback host,
// JSON content type and caller session.
func openTestRequest(method, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/1.0/open", strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(pkg.SessionHeader, "caller-1")
	return req
}

// serveOpen sends one request through the REST handler.
func serveOpen(surface *pkg.OpenSurface, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.Open(surface).ServeHTTP(rec, req)
	return rec
}

// openTestCallerRows is the three-row batch AC4 and AC5 use: an agent resume,
// a human create and a row whose page does not exist.
const openTestCallerRows = `{"rows":[` +
	`{"kind":"task","vault":"v1","name":"Agent"},` +
	`{"kind":"task","vault":"v1","name":"Human"},` +
	`{"kind":"task","vault":"v1","name":"Missing"}]}`

// writeOpenTestCallerPages writes the pages that batch needs.
func writeOpenTestCallerPages(dir string) {
	writeOpenTestPage(dir, "Agent",
		"role: agent", "mode: headless", "claude_session_id: "+openTestUUID(1))
	writeOpenTestPage(dir, "Human", "role: human", "mode: interactive")
}

// openControlNameBody carries the JSON escape sequence for BEL, not a raw
// control character: a raw one is invalid JSON and would be refused at decode
// rather than by ValidateOpenRows.
var openControlNameBody = fmt.Sprintf(
	`{"rows":[{"kind":"task","vault":"v1","name":"a%sb"}]}`,
	"\\"+"u0007",
)

var _ = Describe("Open handler", func() {
	It("AC2: opens a well-formed two-row request in request order", func() {
		f := newOpenTestFixtureWithDir()
		writeOpenTestPage(f.dir, "A", "role: agent", "mode: headless")
		writeOpenTestPage(f.dir, "B", "role: agent", "mode: headless")

		rec := serveOpen(f.surface, openTestRequest(http.MethodPost,
			`{"rows":[{"kind":"task","vault":"v1","name":"A"},`+
				`{"kind":"task","vault":"v1","name":"B"}]}`))

		Expect(rec.Code).To(Equal(http.StatusOK))
		var body struct {
			Verdicts []pkg.OpenVerdict `json:"verdicts"`
		}
		Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
		Expect(body.Verdicts).To(HaveLen(2))
		Expect(body.Verdicts[0].Name).To(Equal("A"))
		Expect(body.Verdicts[1].Name).To(Equal("B"))
	})

	DescribeTable("AC2: refuses a malformed request with 400 and opens nothing",
		func(body string) {
			f := newOpenTestFixtureWithDir()
			writeOpenTestPage(f.dir, "A", "role: agent", "mode: headless")

			rec := serveOpen(f.surface, openTestRequest(http.MethodPost, body))

			Expect(rec.Code).To(Equal(http.StatusBadRequest))
			Expect(f.headless.requests).To(BeEmpty())
			Expect(f.tabOpener.requests).To(BeEmpty())
			Expect(f.scripts.probeCalls()).To(Equal(0))
		},
		Entry("empty rows", `{"rows":[]}`),
		Entry("unknown kind",
			`{"rows":[{"kind":"widget","vault":"v1","name":"A"}]}`),
		Entry("empty name",
			`{"rows":[{"kind":"task","vault":"v1","name":""}]}`),
		Entry("empty vault",
			`{"rows":[{"kind":"task","vault":"","name":"A"}]}`),
		Entry("unconfigured vault",
			`{"rows":[{"kind":"task","vault":"nope","name":"A"}]}`),
		Entry("name with a slash",
			`{"rows":[{"kind":"task","vault":"v1","name":"a/b"}]}`),
		Entry("name with dot-dot",
			`{"rows":[{"kind":"task","vault":"v1","name":"a..b"}]}`),
		Entry("name with a control character", openControlNameBody),
		Entry("body is not JSON", `not json`),
	)

	It("AC2: refuses a body over 1 MiB with 400", func() {
		f := newOpenTestFixtureWithDir()
		rec := serveOpen(f.surface, openTestRequest(http.MethodPost,
			strings.Repeat("x", pkg.MaxOpenBody+1)))

		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(rec.Body.String()).To(ContainSubstring("body exceeds 1 MiB"))
		Expect(f.headless.requests).To(BeEmpty())
	})

	It("AC2: refuses a non-POST method with 405 and Allow: POST", func() {
		f := newOpenTestFixtureWithDir()
		rec := serveOpen(f.surface, openTestRequest(http.MethodGet, ""))

		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
		Expect(rec.Header().Get("Allow")).To(Equal("POST"))
	})

	It("AC2: refuses a non-loopback Host with 403 before the method", func() {
		f := newOpenTestFixtureWithDir()
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			req := openTestRequest(method, `{"rows":[]}`)
			req.Host = "example.com"
			rec := serveOpen(f.surface, req)
			Expect(rec.Code).To(Equal(http.StatusForbidden),
				"method %s must be refused for a non-loopback host", method)
		}
	})

	It("AC2: refuses a non-JSON content type with 415", func() {
		f := newOpenTestFixtureWithDir()
		req := openTestRequest(http.MethodPost, `{"rows":[]}`)
		req.Header.Set("Content-Type", "text/plain")
		rec := serveOpen(f.surface, req)

		Expect(rec.Code).To(Equal(http.StatusUnsupportedMediaType))
	})
})

// openVerdictKeys is the literal field set docs/open-endpoint.md § Verdict
// freezes. A field added or dropped must change this list.
var openVerdictKeys = []string{
	"kind", "vault", "name", "role", "role_source", "mode", "mode_note",
	"branch", "path", "window_key", "window_id", "session_id", "pane_id",
	"bind", "reason", "remedy", "message",
}

var openTokenSets = map[string][]string{
	"role":        {"agent", "human", "manager"},
	"role_source": {"on-disk", "defaulted", "kind"},
	"mode":        {"headless", "interactive"},
	"mode_note":   {"normalised interactive->headless"},
	"branch":      {"jump", "live", "resume", "create", "yours", "refused"},
	"path":        {"headless", "tab", "none"},
	"reason": {
		"missing-row", "row-unreadable", "undecidable-role", "manager-mode-headless",
		"truncated-id", "liveness-unreadable", "answer-path", "role-map-key-missing",
		"hold-window", "goal-loop-unsupported", "target-unsupported", "in-flight",
		"spawn-refused",
	},
}

var _ = Describe("Open verdict shape", func() {
	It("AC5: every verdict carries all 17 fields and only documented tokens", func() {
		f := newOpenTestFixtureWithDir()
		writeOpenTestCallerPages(f.dir)

		rec := serveOpen(f.surface, openTestRequest(http.MethodPost, openTestCallerRows))
		Expect(rec.Code).To(Equal(http.StatusOK))

		var body struct {
			Verdicts []map[string]any `json:"verdicts"`
		}
		Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
		Expect(body.Verdicts).To(HaveLen(3))

		for index, verdict := range body.Verdicts {
			Expect(verdict).To(HaveLen(len(openVerdictKeys)),
				"verdict %d carries %d fields", index, len(verdict))
			for _, key := range openVerdictKeys {
				Expect(verdict).To(HaveKey(key), "verdict %d is missing %q", index, key)
			}
			for field, tokens := range openTokenSets {
				value, _ := verdict[field].(string)
				if value == "" {
					continue
				}
				Expect(tokens).To(ContainElement(value),
					"verdict %d field %q carries undocumented token %q", index, field, value)
			}
		}

		Expect(body.Verdicts[0]["branch"]).To(Equal("resume"))
		Expect(body.Verdicts[1]["branch"]).To(Equal("create"))
		Expect(body.Verdicts[2]["reason"]).To(Equal("missing-row"))

		// The bind the executor computed must actually reach the response. A
		// verdict that binds the task but drops the record is the exact
		// regression the `bind` field exists to prevent, and the key-presence
		// loop above cannot see it (a nil bind still serialises as `bind: null`).
		headlessBind, ok := body.Verdicts[0]["bind"].(map[string]any)
		Expect(ok).To(BeTrue(), "the headless verdict must carry a bind object")
		Expect(headlessBind["action"]).To(Equal("stamped"))
		Expect(headlessBind["task"]).To(Equal("Agent"))
		Expect(headlessBind["session_id"]).To(Equal(openTestUUID(1)))
		Expect(body.Verdicts[1]["bind"]).To(BeNil(), "a tab row binds nothing")
	})
})
