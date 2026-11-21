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

func taskRow(id, title string) VaultAPIRow {
	return VaultAPIRow{
		ID:     id,
		Title:  title,
		Status: "in_progress",
		Flag:   true,
		Vault:  "private-personal",
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

	ginkgo.It("does not refuse an operator-flagged goal or topic whose provenance is unknown", func() {
		// The index serves no flag_set_by for goals or topics. Treating that
		// unknown as "agent-set" would stamp every flagged goal and topic with a
		// refusal, and the tick never opens a refused row.
		client := fakeVaultAPI{
			eligible: map[string][]VaultAPIRow{
				"goal":  {{ID: "g1", Title: "A Goal", Status: "in_progress", Flag: true, Vault: "v"}},
				"topic": {{ID: "p1", Title: "A Topic", Status: "in_progress", Flag: true, Vault: "v"}},
			},
		}

		rows, err := source(client, nil).Rows(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(2))
		for _, row := range rows {
			gomega.Expect(row.Refused).To(gomega.BeEmpty(), row.Kind+" must not be refused")
		}
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
