// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("counts open boxes", func() {
	ginkgo.It("subtracts the ticked count from the total", func() {
		gomega.Expect(OpenBoxCount("1/3")).To(gomega.Equal(2))
		gomega.Expect(OpenBoxCount("3/3")).To(gomega.Equal(0))
		gomega.Expect(OpenBoxCount("—")).To(gomega.Equal(0))
		gomega.Expect(OpenBoxCount("x/y")).To(gomega.Equal(0))
	})
})

var _ = ginkgo.Describe("detects an idle stuck row", func() {
	old := fixedNow.Add(-31 * time.Minute)

	ginkgo.It("flags a stale execution row with an open box", func() {
		gomega.Expect(IdleStuck(Task{Phase: "execution", Met: "0/2", Mtime: old},
			fixedNow)).To(gomega.BeTrue())
	})

	ginkgo.It("refuses a parked row the stuck flag", func() {
		gomega.Expect(IdleStuck(Task{
			Phase: "execution", Met: "0/2", Mtime: old, Liveness: LivenessParked,
		}, fixedNow)).To(gomega.BeFalse())
	})

	ginkgo.It("refuses a non-execution row", func() {
		gomega.Expect(IdleStuck(Task{Phase: "planning", Met: "0/2", Mtime: old},
			fixedNow)).To(gomega.BeFalse())
	})

	ginkgo.It("refuses a row with no open box", func() {
		gomega.Expect(IdleStuck(Task{Phase: "execution", Met: "2/2", Mtime: old},
			fixedNow)).To(gomega.BeFalse())
	})

	ginkgo.It("refuses a freshly written row", func() {
		gomega.Expect(IdleStuck(Task{
			Phase: "execution", Met: "0/2", Mtime: fixedNow.Add(-time.Minute),
		}, fixedNow)).To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("applies the stuck clock", func() {
	registry := map[string]RegistryRecord{
		"busy-session": {Status: "busy", Alive: true},
	}

	ginkgo.It("resets the busy clock on a fresh observation", func() {
		tasks := []Task{{Name: "A", Session: "busy-session", ProgressHash: "p1"}}
		busy := ApplyStuck(tasks, registry, nil, fixedNow)
		gomega.Expect(tasks[0].Stuck).To(gomega.BeFalse())
		gomega.Expect(busy["A"].Progress).To(gomega.Equal("p1"))
	})

	ginkgo.It("flags a busy row after thirty unchanged minutes", func() {
		tasks := []Task{{Name: "A", Session: "busy-session", ProgressHash: "p1"}}
		prev := map[string]BusyStamp{
			"A": {Since: float64(fixedNow.UnixNano()) / float64(time.Second), Progress: "p1"},
		}
		ApplyStuck(tasks, registry, prev, fixedNow.Add(31*time.Minute))
		gomega.Expect(tasks[0].Stuck).To(gomega.BeTrue())
	})

	ginkgo.It("clears the clock when Progress moves", func() {
		tasks := []Task{{Name: "A", Session: "busy-session", ProgressHash: "p2"}}
		prev := map[string]BusyStamp{
			"A": {Since: float64(fixedNow.UnixNano()) / float64(time.Second), Progress: "p1"},
		}
		ApplyStuck(tasks, registry, prev, fixedNow.Add(31*time.Minute))
		gomega.Expect(tasks[0].Stuck).To(gomega.BeFalse())
	})

	ginkgo.It("takes the idle branch for a non-busy row", func() {
		tasks := []Task{{
			Name: "B", Phase: "execution", Met: "0/1",
			Mtime: fixedNow.Add(-31 * time.Minute),
		}}
		ApplyStuck(tasks, registry, nil, fixedNow)
		gomega.Expect(tasks[0].Stuck).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("applies blocked-upstream", func() {
	ginkgo.It("refuses blocked-upstream to a row whose blockers all completed", func() {
		tasks := []Task{{Name: "A", BlockedBy: []string{"B"}}}
		ApplyBlockedUpstream(tasks, map[string]string{"B": "completed"})
		gomega.Expect(tasks[0].BlockedUpstream).To(gomega.BeFalse())
		gomega.Expect(tasks[0].BlockedByUnmet).To(gomega.BeEmpty())
	})

	ginkgo.It("flags a row with one unmet blocker", func() {
		tasks := []Task{{Name: "A", BlockedBy: []string{"B", "C"}}}
		ApplyBlockedUpstream(tasks, map[string]string{"B": "completed", "C": "aborted"})
		gomega.Expect(tasks[0].BlockedUpstream).To(gomega.BeTrue())
		gomega.Expect(tasks[0].BlockedByUnmet).To(gomega.Equal([]string{"C"}))
	})

	ginkgo.It("does not flag a row with no blockers", func() {
		tasks := []Task{{Name: "A"}}
		ApplyBlockedUpstream(tasks, map[string]string{})
		gomega.Expect(tasks[0].BlockedUpstream).To(gomega.BeFalse())
	})

	ginkgo.It("reads statuses vault-wide", func() {
		statuses, err := BlockerStatuses(context.Background(), fixtureVault())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(statuses["Completed Task"]).To(gomega.Equal("completed"))
		gomega.Expect(statuses["Next Task"]).To(gomega.Equal("next"))
	})
})

var _ = ginkgo.Describe("reads the park threshold", func() {
	ctx := context.Background()

	ginkgo.It("arms two cadence intervals from the recorded cadence", func() {
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "topic--parity-topic.cadence")
		gomega.Expect(os.WriteFile(path, []byte("300\n"), 0o600)).To(gomega.Succeed())
		seconds, basis, armed := ParkThreshold(Config{StateDir: dir}, "topic", "Parity Topic",
			fixedNow)
		gomega.Expect(seconds).To(gomega.Equal(600))
		gomega.Expect(basis).To(gomega.ContainSubstring("2 x cadence 300s (armed)"))
		gomega.Expect(armed).To(gomega.BeTrue())
	})

	ginkgo.It("announces the fixed fallback basis when no cadence exists", func() {
		dir := ginkgo.GinkgoT().TempDir()
		seconds, basis, armed := ParkThreshold(Config{StateDir: dir}, "topic", "Parity Topic",
			fixedNow)
		gomega.Expect(seconds).To(gomega.Equal(1800))
		gomega.Expect(basis).To(gomega.ContainSubstring("no cadence record at"))
		gomega.Expect(basis).To(gomega.ContainSubstring("fixed 1800s"))
		gomega.Expect(armed).To(gomega.BeFalse())
		_ = ctx
	})
})

var _ = ginkgo.Describe("detects a park on an unregistered gate", func() {
	base := func() Task {
		return Task{
			Status: "in_progress", Phase: "execution", Met: "0/1",
			Mtime: fixedNow.Add(-2 * time.Hour), Liveness: LivenessLive,
		}
	}

	ginkgo.It("flags a live working row parked on an unregistered gate", func() {
		gomega.Expect(ParkedOnUnregisteredGate(base(), fixedNow, 1800, nil)).
			To(gomega.BeTrue())
	})

	ginkgo.It("refuses the park cell to a registered gate", func() {
		task := base()
		task.FeedGate = true
		gomega.Expect(ParkedOnUnregisteredGate(task, fixedNow, 1800, nil)).
			To(gomega.BeFalse())
	})

	ginkgo.It("refuses the park cell to an ownerless row", func() {
		task := base()
		task.Liveness = LivenessNone
		gomega.Expect(ParkedOnUnregisteredGate(task, fixedNow, 1800, nil)).
			To(gomega.BeFalse())
	})

	ginkgo.It("refuses the park cell to an in-flight row", func() {
		task := base()
		task.InFlight = true
		gomega.Expect(ParkedOnUnregisteredGate(task, fixedNow, 1800, nil)).
			To(gomega.BeFalse())
	})

	ginkgo.It("refuses the park cell to a working row", func() {
		task := base()
		task.Mtime = fixedNow.Add(-time.Minute)
		gomega.Expect(ParkedOnUnregisteredGate(task, fixedNow, 1800, nil)).
			To(gomega.BeFalse())
	})

	ginkgo.It("refuses the park cell to a terminal row", func() {
		task := base()
		task.Status = "completed"
		gomega.Expect(ParkedOnUnregisteredGate(task, fixedNow, 1800, nil)).
			To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("applies the wait flags", func() {
	feed := map[string]FeedRecord{
		"stop-session": {Event: "Stop", Detail: "⏰ Ends: CI"},
	}

	ginkgo.It("sets external wait from the current owner", func() {
		tasks := []Task{{Session: "stop-session"}, {Session: "other"}, {}}
		ApplyExternalWaits(tasks, feed)
		gomega.Expect(tasks[0].ExternalWait).To(gomega.BeTrue())
		gomega.Expect(tasks[1].ExternalWait).To(gomega.BeFalse())
		gomega.Expect(tasks[2].ExternalWait).To(gomega.BeFalse())
	})

	ginkgo.It("sets declared wait only for a real declaration", func() {
		tasks := []Task{
			{DeclaredWait: "external CI pipeline"},
			{DeclaredWait: "null"},
			{DeclaredWait: ""},
		}
		ApplyDeclaredWaits(tasks)
		gomega.Expect(tasks[0].DeclaredWaitActive).To(gomega.BeTrue())
		gomega.Expect(tasks[1].DeclaredWaitActive).To(gomega.BeFalse())
		gomega.Expect(tasks[2].DeclaredWaitActive).To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("detects a session in flight", func() {
	ctx := context.Background()

	ginkgo.It("trusts the registry's shell status", func() {
		registry := map[string]RegistryRecord{"s1": {Status: "shell", Alive: true}}
		gomega.Expect(SessionInFlight(ctx, Config{}, "s1", registry, fixedNow)).
			To(gomega.BeTrue())
	})

	ginkgo.It("trusts the registry's waiting status", func() {
		registry := map[string]RegistryRecord{"s1": {Status: "waiting", Alive: true}}
		gomega.Expect(SessionInFlight(ctx, Config{}, "s1", registry, fixedNow)).
			To(gomega.BeFalse())
	})

	ginkgo.It("reads a fresh open tool marker", func() {
		dir := ginkgo.GinkgoT().TempDir()
		body := `{"state":"open","ts":` +
			itoa(int(fixedNow.Add(-time.Minute).Unix())) + `}`
		gomega.Expect(os.WriteFile(filepath.Join(dir, "s1.tool.json"),
			[]byte(body), 0o600)).To(gomega.Succeed())
		gomega.Expect(SessionInFlight(ctx, Config{ToolMarkerDir: dir}, "s1", nil,
			fixedNow)).To(gomega.BeTrue())
	})

	ginkgo.It("falls through an old open tool marker", func() {
		dir := ginkgo.GinkgoT().TempDir()
		body := `{"state":"open","ts":` +
			itoa(int(fixedNow.Add(-21*time.Minute).Unix())) + `}`
		gomega.Expect(os.WriteFile(filepath.Join(dir, "s1.tool.json"),
			[]byte(body), 0o600)).To(gomega.Succeed())
		gomega.Expect(SessionInFlight(ctx, Config{ToolMarkerDir: dir}, "s1", nil,
			fixedNow)).To(gomega.BeFalse())
	})

	ginkgo.It("reads a fresh transcript", func() {
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "s1.jsonl")
		gomega.Expect(os.WriteFile(path, []byte("{}\n"), 0o600)).To(gomega.Succeed())
		gomega.Expect(os.Chtimes(path, fixedNow, fixedNow)).To(gomega.Succeed())
		gomega.Expect(SessionInFlight(ctx, Config{TranscriptDir: dir}, "s1", nil,
			fixedNow)).To(gomega.BeTrue())
	})

	ginkgo.It("returns false when no transcript exists", func() {
		gomega.Expect(SessionInFlight(ctx, Config{TranscriptDir: ginkgo.GinkgoT().TempDir()},
			"s1", nil, fixedNow)).To(gomega.BeFalse())
	})
})
