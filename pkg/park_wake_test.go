package pkg

import (
	"context"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// recordingTurnSender records the turns a wake wrote. It is a real
// implementation of the seam the wake uses, not a mock of the supervisor.
type recordingTurnSender struct {
	sent []string
}

func (s *recordingTurnSender) Send(_ context.Context, id string, _ string) error {
	s.sent = append(s.sent, id)
	return nil
}

type staticRoster struct {
	rows []AgentRow
	err  error
}

func (r *staticRoster) List(context.Context) ([]AgentRow, error) { return r.rows, r.err }

func (r *staticRoster) Get(_ context.Context, agentID string) (*AgentRow, error) {
	for index := range r.rows {
		if r.rows[index].AgentID == agentID {
			return &r.rows[index], nil
		}
	}
	return nil, nil
}

type staticLiveness struct {
	live map[string]struct{}
}

func (l *staticLiveness) Live(context.Context) (map[string]struct{}, error) {
	return l.live, nil
}

type recordingWakeRecorder struct {
	records []WakeRecord
}

func (r *recordingWakeRecorder) Record(_ context.Context, record WakeRecord) error {
	r.records = append(r.records, record)
	return nil
}

var _ = ginkgo.Describe("ParkWake", func() {
	var (
		ctx      context.Context
		sender   *recordingTurnSender
		recorder *recordingWakeRecorder
		clock    libtime.CurrentDateTime
		start    time.Time
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		sender = &recordingTurnSender{}
		recorder = &recordingWakeRecorder{}
		start = time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(libtime.DateTime(start))
	})

	newWake := func(rows []AgentRow, live map[string]struct{}) *ParkWake {
		return &ParkWake{
			Sender:   sender,
			Roster:   &staticRoster{rows: rows},
			Liveness: &staticLiveness{live: live},
			Clock:    clock,
			Recorder: recorder,
		}
	}

	parent := func() *string {
		id := "manager-1"
		return &id
	}

	ginkgo.It("wakes a live manager and records the delivery", func() {
		wake := newWake(
			[]AgentRow{{SessionID: "worker-1", ParentSession: parent()}},
			map[string]struct{}{"manager-1": {}},
		)

		gomega.Expect(wake.Wake(ctx, "worker-1", "gate-1")).To(gomega.Succeed())

		gomega.Expect(sender.sent).To(gomega.Equal([]string{"manager-1"}))
		gomega.Expect(recorder.records).To(gomega.HaveLen(1))
		gomega.Expect(recorder.records[0].Result).To(gomega.Equal(ParkWakeDelivered))
		gomega.Expect(recorder.records[0].GateID).To(gomega.Equal("gate-1"))
		gomega.Expect(recorder.records[0].ManagerID).To(gomega.Equal("manager-1"))
		gomega.Expect(recorder.records[0].Coalesced).To(gomega.Equal(0))
	})

	ginkgo.It("coalesces a second park inside the window and counts it", func() {
		wake := newWake(
			[]AgentRow{{SessionID: "worker-1", ParentSession: parent()}},
			map[string]struct{}{"manager-1": {}},
		)
		gomega.Expect(wake.Wake(ctx, "worker-1", "gate-1")).To(gomega.Succeed())
		clock.SetNow(libtime.DateTime(start.Add(10 * time.Second)))

		gomega.Expect(wake.Wake(ctx, "worker-1", "gate-2")).To(gomega.Succeed())

		gomega.Expect(sender.sent).To(gomega.Equal([]string{"manager-1"}))
		gomega.Expect(recorder.records).To(gomega.HaveLen(2))
		gomega.Expect(recorder.records[1].Result).To(gomega.Equal(ParkWakeCoalesced))
		gomega.Expect(recorder.records[1].GateID).To(gomega.Equal("gate-2"))
		gomega.Expect(recorder.records[1].Coalesced).To(gomega.Equal(1))
	})

	ginkgo.It("wakes again once the window has passed", func() {
		wake := newWake(
			[]AgentRow{{SessionID: "worker-1", ParentSession: parent()}},
			map[string]struct{}{"manager-1": {}},
		)
		gomega.Expect(wake.Wake(ctx, "worker-1", "gate-1")).To(gomega.Succeed())
		clock.SetNow(libtime.DateTime(start.Add(ParkWakeWindow + time.Second)))

		gomega.Expect(wake.Wake(ctx, "worker-1", "gate-2")).To(gomega.Succeed())

		gomega.Expect(sender.sent).To(gomega.Equal([]string{"manager-1", "manager-1"}))
		gomega.Expect(recorder.records).To(gomega.HaveLen(2))
		gomega.Expect(recorder.records[1].Result).To(gomega.Equal(ParkWakeDelivered))
	})

	ginkgo.It("records no-manager and writes nothing when the row names no spawner", func() {
		wake := newWake(
			[]AgentRow{{SessionID: "worker-1"}},
			map[string]struct{}{},
		)

		gomega.Expect(wake.Wake(ctx, "worker-1", "gate-1")).To(gomega.Succeed())

		gomega.Expect(sender.sent).To(gomega.BeEmpty())
		gomega.Expect(recorder.records).To(gomega.HaveLen(1))
		gomega.Expect(recorder.records[0].Result).To(gomega.Equal(ParkWakeNoManager))
	})

	ginkgo.It("records dead-manager and writes nothing when the manager is not live", func() {
		wake := newWake(
			[]AgentRow{{SessionID: "worker-1", ParentSession: parent()}},
			map[string]struct{}{},
		)

		gomega.Expect(wake.Wake(ctx, "worker-1", "gate-1")).To(gomega.Succeed())

		gomega.Expect(sender.sent).To(gomega.BeEmpty())
		gomega.Expect(recorder.records).To(gomega.HaveLen(1))
		gomega.Expect(recorder.records[0].Result).To(gomega.Equal(ParkWakeDeadManager))
	})

	ginkgo.It("does not wake a manager on its own park", func() {
		self := "manager-1"
		wake := newWake(
			[]AgentRow{{SessionID: "manager-1", ParentSession: &self}},
			map[string]struct{}{"manager-1": {}},
		)

		gomega.Expect(wake.Wake(ctx, "manager-1", "gate-1")).To(gomega.Succeed())

		gomega.Expect(sender.sent).To(gomega.BeEmpty())
		gomega.Expect(recorder.records).To(gomega.HaveLen(1))
		gomega.Expect(recorder.records[0].Result).To(gomega.Equal(ParkWakeSelfManager))
	})
})
