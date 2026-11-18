// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

const parkedGateSessionID = "123e4567-e89b-12d3-a456-426614174000"

// writeSessionCloser writes one real events.jsonl whose last assistant record
// carries text.
func writeSessionCloser(sessionsDir, sessionID, text string) {
	ginkgo.GinkgoT().Helper()
	dir := filepath.Join(sessionsDir, sessionID)
	gomega.Expect(os.MkdirAll(dir, 0o700)).To(gomega.Succeed())
	record := fmt.Sprintf(
		`{"type":"assistant","message":{"content":[{"type":"text","text":%q}]}}`,
		text,
	)
	gomega.Expect(
		os.WriteFile(filepath.Join(dir, sessionEventsName), []byte(record+"\n"), 0o600),
	).To(gomega.Succeed())
}

// openedCount reads the spec's "opened" column from a held answer.
func openedCount(held bool) int {
	if held {
		return 0
	}
	return 1
}

var _ = ginkgo.Describe("parked gate", func() {
	ginkgo.It("expires an unprobeable park after one hour", func() {
		ctx := context.Background()
		t0 := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
		sessionsDir := ginkgo.GinkgoT().TempDir()
		parkedDir := ginkgo.GinkgoT().TempDir()
		writeSessionCloser(
			sessionsDir,
			parkedGateSessionID,
			"⏰ Ends: task ABC-123 flips to a terminal status on disk",
		)
		row := FlaggedRow{
			Kind:      "task",
			Vault:     "private-personal",
			Name:      "ABC-123",
			SessionID: parkedGateSessionID,
		}

		clock := libtime.NewCurrentDateTime()
		clock.SetNow(libtime.DateTime(t0))
		gate := NewParkedGate(sessionsDir, parkedDir, nil, nil, clock)

		_, held := gate.Hold(ctx, row)
		fmt.Printf("age=0m opened=%d\n", openedCount(held))
		gomega.Expect(held).To(gomega.BeTrue())

		clock.SetNow(libtime.DateTime(t0.Add(59 * time.Minute)))
		_, held = gate.Hold(ctx, row)
		fmt.Printf("age=59m opened=%d\n", openedCount(held))
		gomega.Expect(openedCount(held)).To(gomega.Equal(0))

		clock.SetNow(libtime.DateTime(t0.Add(61 * time.Minute)))
		_, held = gate.Hold(ctx, row)
		fmt.Printf("age=61m opened=%d\n", openedCount(held))
		gomega.Expect(held).To(gomega.BeFalse())
	})

	ginkgo.It("keeps a baseline inside its directory", func() {
		ctx := context.Background()
		parkedDir := ginkgo.GinkgoT().TempDir()
		row := FlaggedRow{Vault: "private-personal", Kind: "task", Name: "../../escape"}
		store := NewParkedStore(parkedDir)
		gomega.Expect(store.Save(ctx, row.Key(), ParkedBaseline{
			Class:       ParkedOnCard,
			Fingerprint: "open",
			FirstSeen:   time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
			SessionID:   parkedGateSessionID,
			CloserHash:  "hash",
		})).To(gomega.Succeed())

		entries, err := os.ReadDir(parkedDir)
		gomega.Expect(err).To(gomega.BeNil())

		outside := 0
		if _, statErr := os.Stat(filepath.Join(parkedDir, row.Key()+".json")); statErr == nil {
			outside++
		}
		name := ""
		if len(entries) == 1 {
			name = entries[0].Name()
		}
		fmt.Printf("entries=%d name=%s outside=%d\n", len(entries), name, outside)
		gomega.Expect(entries).To(gomega.HaveLen(1))
		gomega.Expect(name).To(gomega.MatchRegexp(`^[0-9a-f]{64}\.json$`))
		gomega.Expect(outside).To(gomega.Equal(0))
	})

	ginkgo.It("treats a corrupt baseline as missing and holds once", func() {
		ctx := context.Background()
		t0 := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
		sessionsDir := ginkgo.GinkgoT().TempDir()
		parkedDir := ginkgo.GinkgoT().TempDir()
		writeSessionCloser(
			sessionsDir,
			parkedGateSessionID,
			"⏰ Ends: task ABC-123 flips to a terminal status on disk",
		)
		row := FlaggedRow{
			Kind:      "task",
			Vault:     "private-personal",
			Name:      "ABC-123",
			SessionID: parkedGateSessionID,
		}
		gomega.Expect(
			os.WriteFile(parkedBaselinePath(parkedDir, row.Key()), []byte("{not json"), 0o600),
		).To(gomega.Succeed())

		clock := libtime.NewCurrentDateTime()
		clock.SetNow(libtime.DateTime(t0))
		gate := NewParkedGate(sessionsDir, parkedDir, nil, nil, clock)

		reason, held := gate.Hold(ctx, row)
		fmt.Printf("corrupt held=%t reason=%s\n", held, reason)
		gomega.Expect(held).To(gomega.BeTrue())
		gomega.Expect(reason).To(gomega.Equal(string(ParkedUntil)))
		gomega.Expect(NewParkedStore(parkedDir).Load(ctx, row.Key())).NotTo(gomega.BeNil())
	})

	ginkgo.It("does not hold a closer with no Ends line", func() {
		ctx := context.Background()
		sessionsDir := ginkgo.GinkgoT().TempDir()
		parkedDir := ginkgo.GinkgoT().TempDir()
		writeSessionCloser(sessionsDir, parkedGateSessionID, "all done, nothing to wait for")
		row := FlaggedRow{
			Kind:      "task",
			Vault:     "private-personal",
			Name:      "ABC-123",
			SessionID: parkedGateSessionID,
		}
		gate := NewParkedGate(sessionsDir, parkedDir, nil, nil, libtime.NewCurrentDateTime())

		reason, held := gate.Hold(ctx, row)
		fmt.Printf("no-ends held=%t reason=%q\n", held, reason)
		gomega.Expect(held).To(gomega.BeFalse())
		gomega.Expect(reason).To(gomega.Equal(""))
	})

	ginkgo.It("starts a new baseline when the closer text changed", func() {
		ctx := context.Background()
		t0 := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
		sessionsDir := ginkgo.GinkgoT().TempDir()
		parkedDir := ginkgo.GinkgoT().TempDir()
		first := "⏰ Ends: task ABC-123 flips to a terminal status on disk"
		second := "⏰ Ends: task ABC-124 flips to a terminal status on disk"
		writeSessionCloser(sessionsDir, parkedGateSessionID, first)
		row := FlaggedRow{
			Kind:      "task",
			Vault:     "private-personal",
			Name:      "ABC-123",
			SessionID: parkedGateSessionID,
		}

		clock := libtime.NewCurrentDateTime()
		clock.SetNow(libtime.DateTime(t0))
		gate := NewParkedGate(sessionsDir, parkedDir, nil, nil, clock)
		store := NewParkedStore(parkedDir)

		_, held := gate.Hold(ctx, row)
		gomega.Expect(held).To(gomega.BeTrue())
		gomega.Expect(store.Load(ctx, row.Key())).NotTo(gomega.BeNil())
		gomega.Expect(store.Load(ctx, row.Key()).CloserHash).To(gomega.Equal(sha256hex(first)))

		writeSessionCloser(sessionsDir, parkedGateSessionID, second)
		_, held = gate.Hold(ctx, row)
		changed := store.Load(ctx, row.Key())
		fmt.Printf(
			"changed held=%t old_hash=%t new_hash=%t\n",
			held,
			changed != nil && changed.CloserHash == sha256hex(first),
			changed != nil && changed.CloserHash == sha256hex(second),
		)
		gomega.Expect(held).To(gomega.BeTrue())
		gomega.Expect(changed).NotTo(gomega.BeNil())
		gomega.Expect(changed.CloserHash).To(gomega.Equal(sha256hex(second)))
	})
})
