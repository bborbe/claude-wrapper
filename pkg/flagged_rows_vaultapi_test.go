// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

type nativeSupervisor struct {
	children []Session
	err      error
}

func (n nativeSupervisor) List(context.Context) ([]Session, error) { return n.children, n.err }

// fakeVaultAPI answers the two questions the source asks. It is the whole point
// of the client interface: an outage, an empty set and a row are all producible
// without a server or a vault on disk.
type fakeVaultAPI struct {
	eligible map[string][]VaultAPIRow
	all      map[string][]VaultAPIRow
	pages    map[string]VaultAPIPage
	err      error
}

func (f fakeVaultAPI) List(_ context.Context, kind string) ([]VaultAPIRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	if rows, ok := f.eligible[kind]; ok {
		return rows, nil
	}
	return []VaultAPIRow{}, nil
}

func (f fakeVaultAPI) ListAll(_ context.Context, kind string) ([]VaultAPIRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	if rows, ok := f.all[kind]; ok {
		return rows, nil
	}
	return []VaultAPIRow{}, nil
}

func (f fakeVaultAPI) Page(_ context.Context, _, id string) (VaultAPIPage, error) {
	if f.err != nil {
		return VaultAPIPage{}, f.err
	}
	return f.pages[id], nil
}

// taskRow is an operator-flagged row: the index serves flag_set_by for every kind
// now, so a fixture without it is refused rather than opened.
func taskRow(id, title string) VaultAPIRow {
	return VaultAPIRow{
		ID:        id,
		Title:     title,
		Status:    "in_progress",
		Flag:      true,
		Vault:     "private-personal",
		FlagSetBy: "operator",
	}
}

func taskPage(fields map[string]any) VaultAPIPage {
	return VaultAPIPage{Frontmatter: fields}
}

var _ = ginkgo.Describe("VaultAPIFlaggedRowsSource", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() { ctx = context.Background() })

	source := func(client VaultAPIClient, registry SessionRegistry) VaultAPIFlaggedRowsSource {
		return VaultAPIFlaggedRowsSource{
			Client:     client,
			Supervisor: nativeSupervisor{},
			Registry:   registry,
		}
	}

	ginkgo.It("reads an operator-flagged task from the client, not from disk", func() {
		client := fakeVaultAPI{
			eligible: map[string][]VaultAPIRow{"task": {taskRow("t1", "Alpha")}},
			pages: map[string]VaultAPIPage{
				"t1": taskPage(map[string]any{"role": "agent", "flag_set_by": "operator"}),
			},
		}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Name).To(gomega.Equal("Alpha"))
		gomega.Expect(rows[0].Kind).To(gomega.Equal("task"))
		gomega.Expect(rows[0].Role).To(gomega.Equal(RoleAgent))
		gomega.Expect(rows[0].Refused).To(gomega.BeEmpty())
	})

	ginkgo.It("carries the provenance refusal for an agent-set flag", func() {
		client := fakeVaultAPI{
			eligible: map[string][]VaultAPIRow{"task": {taskRow("t1", "Alpha")}},
			pages: map[string]VaultAPIPage{
				"t1": taskPage(map[string]any{"flag_set_by": "agent"}),
			},
		}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Refused).To(gomega.ContainElement(RefusalAgentSetFlag))
	})

	ginkgo.It("carries a DISTINCT refusal for unrecorded provenance, with the remedy", func() {
		// `unknown` is the no-actor default. It must not read as agent-set: the
		// refusal names the state and the one command that records the actor, so a
		// legitimate operator row is not permanently unopenable.
		client := fakeVaultAPI{
			eligible: map[string][]VaultAPIRow{"task": {taskRow("t1", "Alpha")}},
			pages: map[string]VaultAPIPage{
				"t1": taskPage(map[string]any{"flag_set_by": "unknown"}),
			},
		}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Provenance).To(gomega.Equal(ProvenanceUnrecorded))
		gomega.Expect(rows[0].Refused).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Refused[0]).To(gomega.ContainSubstring(RefusalProvenanceUnrecorded))
		gomega.Expect(rows[0].Refused[0]).NotTo(gomega.ContainSubstring(RefusalAgentSetFlag))
		gomega.Expect(rows[0].Refused[0]).To(gomega.ContainSubstring("--by operator"))
	})

	ginkgo.It("carries the provenance class on an admitted row", func() {
		client := fakeVaultAPI{
			eligible: map[string][]VaultAPIRow{"task": {taskRow("t1", "Alpha")}},
			pages: map[string]VaultAPIPage{
				"t1": taskPage(map[string]any{"flag_set_by": "operator"}),
			},
		}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Provenance).To(gomega.Equal(ProvenanceOperator))
		gomega.Expect(rows[0].Refused).To(gomega.BeEmpty())
	})

	ginkgo.It("applies the provenance rule to goals and topics too", func() {
		// The index serves flag_set_by for every kind, so the pre-swap rule applies
		// uniformly: an operator flag opens, an agent flag refuses.
		client := fakeVaultAPI{
			eligible: map[string][]VaultAPIRow{
				"goal": {
					{ID: "g1", Title: "Operator Goal", Status: "in_progress", Flag: true,
						Vault: "v", FlagSetBy: "operator"},
					{ID: "g2", Title: "Agent Goal", Status: "in_progress", Flag: true,
						Vault: "v", FlagSetBy: "agent"},
				},
				"topic": {{ID: "p1", Title: "A Topic", Status: "in_progress", Flag: true,
					Vault: "v", FlagSetBy: "operator"}},
			},
		}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(3))
		byName := map[string]FlaggedRow{}
		for _, row := range rows {
			byName[row.Name] = row
		}
		gomega.Expect(byName["Operator Goal"].Refused).To(gomega.BeEmpty())
		gomega.Expect(byName["A Topic"].Refused).To(gomega.BeEmpty())
		gomega.Expect(byName["Agent Goal"].Refused).To(gomega.ContainElement(RefusalAgentSetFlag))
	})

	ginkgo.It("applies the due rule to a goal deferred to the future", func() {
		// The deleted page-reading source parsed defer_date from every page. Reading
		// it only for tasks let a future-deferred goal open its manager early.
		client := fakeVaultAPI{
			eligible: map[string][]VaultAPIRow{
				"goal": {{ID: "g1", Title: "Later Goal", Status: "in_progress", Flag: true,
					Vault: "v", DeferDate: "2999-01-01"}},
			},
		}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.BeEmpty())
	})

	ginkgo.It("carries a goal's own role and blockers", func() {
		client := fakeVaultAPI{
			eligible: map[string][]VaultAPIRow{
				"goal": {{ID: "g1", Title: "A Goal", Status: "in_progress", Flag: true,
					Vault: "v", Role: "manager", BlockedBy: []string{"[[Unfinished]]"}}},
			},
		}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Role).To(gomega.Equal(RoleManager))
		gomega.Expect(rows[0].BlockedBy).To(gomega.ConsistOf("[[Unfinished]]"))
	})

	ginkgo.It("keys the goal→topic map by the case-preserving title", func() {
		// ResolveOwner looks the goal up with the page's own Title-Case title, so a
		// folded key made the declaring-topic rung unreachable for every Title-Case
		// page and the owner silently fell through to the fleet.
		client := fakeVaultAPI{
			all: map[string][]VaultAPIRow{
				"topic": {{
					ID: "p1", Title: "A Topic", Vault: "v",
					Goals: []string{"[[A Goal]]"},
				}},
			},
		}

		idx, err := source(client, nil).subjectIndex(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(topicOfForVault(idx, "v")).To(gomega.HaveKey("A Goal"))
		gomega.Expect(wikilinkTitle("[[A Goal|alias]]")).To(gomega.Equal("A Goal"))
	})

	ginkgo.It("emits rows_source only when the source names one", func() {
		gomega.Expect(FlaggedTickResult{RowsSource: "vault-api"}.Line()).
			To(gomega.ContainSubstring("rows_source=vault-api"))
		gomega.Expect(FlaggedTickResult{}.Line()).
			ToNot(gomega.ContainSubstring("rows_source="))
	})

	ginkgo.It("drops a blocker the index reports as completed", func() {
		client := fakeVaultAPI{
			eligible: map[string][]VaultAPIRow{
				"task": {{ID: "t1", Title: "Blocked", Status: "in_progress", Flag: true, Vault: "v"}},
			},
			all: map[string][]VaultAPIRow{
				"task": {{ID: "t2", Title: "Done Already", Status: "completed", Vault: "v"}},
			},
			pages: map[string]VaultAPIPage{
				"t1": taskPage(map[string]any{
					"flag_set_by": "operator",
					"blocked_by":  []any{"[[Done Already]]"},
				}),
			},
		}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].BlockedBy).To(gomega.BeEmpty(),
			"a blocker that already completed must not keep the row blocked")
	})

	ginkgo.It("reads goals and topics from their own lists", func() {
		client := fakeVaultAPI{
			eligible: map[string][]VaultAPIRow{
				"goal":  {{ID: "g1", Title: "A Goal", Status: "in_progress", Flag: true, Vault: "v"}},
				"topic": {{ID: "p1", Title: "A Topic", Status: "in_progress", Flag: true, Vault: "v"}},
			},
		}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(2))
		kinds := []string{rows[0].Kind, rows[1].Kind}
		gomega.Expect(kinds).To(gomega.ConsistOf("goal", "topic"))
	})

	ginkgo.It("reports an outage as ErrVaultAPIUnavailable, never as no rows", func() {
		client := fakeVaultAPI{err: ErrVaultAPIUnavailable}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(rows).To(gomega.BeNil())
		gomega.Expect(stderrors.Is(err, ErrVaultAPIUnavailable)).To(gomega.BeTrue())
	})

	ginkgo.It("fails rather than reporting no rows when the child table cannot be read", func() {
		client := fakeVaultAPI{}
		s := VaultAPIFlaggedRowsSource{
			Client:     client,
			Supervisor: nativeSupervisor{err: stderrors.New("boom")},
		}

		_, err := s.Rows(ctx)
		gomega.Expect(err).ToNot(gomega.BeNil())
	})

	ginkgo.It("excludes a flagged page deferred to the future", func() {
		client := fakeVaultAPI{
			eligible: map[string][]VaultAPIRow{"task": {taskRow("t1", "Later")}},
			pages: map[string]VaultAPIPage{
				"t1": taskPage(map[string]any{"defer_date": "2999-01-01"}),
			},
		}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.BeEmpty())
	})

	ginkgo.It("resolves a goal's owner through its declaring topic", func() {
		client := fakeVaultAPI{
			eligible: map[string][]VaultAPIRow{
				"goal": {{ID: "g1", Title: "A Goal", Status: "in_progress", Flag: true, Vault: "v"}},
			},
			all: map[string][]VaultAPIRow{
				"topic": {{
					ID: "p1", Title: "A Topic", Status: "in_progress", Vault: "v",
					Goals: []string{"[[A Goal]]"},
				}},
			},
		}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		// The goal is owned by the topic that declares it, so the branch is the
		// manager route rather than a fleet-level create.
		gomega.Expect(rows[0].Branch).ToNot(gomega.BeEmpty())
	})
})
