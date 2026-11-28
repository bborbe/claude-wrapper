package pkg

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// ParkWaker wakes a manager when a gate parks for a worker it spawned.
//
// ⚠️ **The pane wake cannot do this.** `claude-supervisor`'s `manager-wake.py`
// types into a WezTerm pane. A live manager has no pane when it runs headless
// (`scripts/manager-wake.py:520-527` prints `HEADLESS`), and a manager that is
// mid-turn is refused by `pane-wake-guard.py:157` (`skip:spinner`). The two
// populations this wake exists for — headless and busy — are exactly the two
// the pane path cannot reach. This daemon holds the child's stdin, so it can
// write a turn where no pane exists.
//
// ⚠️ **A wake is not an authorisation.** The text says so, and the wake never
// answers a gate: it only makes the manager's next turn happen sooner.
//
// The record matters as much as the wake. A wake that cannot be observed
// cannot be measured, so every decision — delivered, coalesced, no manager,
// dead manager, send failed — writes one line carrying `ts`, `manager_id`,
// `gate_id` and the coalesced count.
type ParkWaker interface {
	// Wake asks the manager of parkedSessionID to take a turn. It never returns
	// an error for a park it chose not to wake: a missing manager is a recorded
	// decision, not a failure of the park registration that called it.
	Wake(ctx context.Context, parkedSessionID, gateID string) error
}

const (
	// ParkWakeWindow is the per-manager coalescing window.
	//
	// A worker that parks three gates in a second must cost its manager one
	// turn, not three, and a wake that keeps re-firing is the hot loop the cap
	// exists to prevent. Wakes suppressed inside the window are counted, so a
	// suppressed wake is visible rather than invisible.
	ParkWakeWindow = 60 * time.Second

	// ParkWakeDelivered and its siblings name every outcome the record carries,
	// so a probe asserts on the same string the operator reads.
	ParkWakeDelivered        = "delivered"
	ParkWakeCoalesced        = "coalesced"
	ParkWakeNoManager        = "no-manager"
	ParkWakeDeadManager      = "dead-manager"
	ParkWakeSelfManager      = "self-manager"
	ParkWakeSendFailed       = "send-failed"
	ParkWakeRosterUnreadable = "roster-unreadable"
)

// WakeRecord is one line of the wake ledger.
//
// ⚠️ `gate_id` is the join to the park, `manager_id` is the session that was
// woken, and `coalesced` is how many wakes this delivery absorbed. Without the
// last field a suppressed wake is indistinguishable from a wake that never
// fired, which is the blindness this record exists to remove.
type WakeRecord struct {
	TS        time.Time `json:"ts"`
	ManagerID string    `json:"manager_id"`
	GateID    string    `json:"gate_id"`
	Result    string    `json:"result"`
	Coalesced int       `json:"coalesced"`
}

// WakeRecorder appends one WakeRecord.
type WakeRecorder interface {
	Record(ctx context.Context, record WakeRecord) error
}

// FileWakeRecorder appends JSON lines to Path, creating the directory on first
// write. It is the default recorder; a test passes its own.
type FileWakeRecorder struct {
	Path string
}

// Record appends one line. The file is opened per write rather than held open,
// because a daemon that keeps a descriptor across a log rotation writes to the
// rotated file for ever.
func (r *FileWakeRecorder) Record(ctx context.Context, record WakeRecord) error {
	if r == nil || r.Path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o750); err != nil {
		return errors.Wrapf(ctx, err, "create park-wake directory for %s failed", r.Path)
	}
	file, err := os.OpenFile(r.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return errors.Wrapf(ctx, err, "open park-wake record %s failed", r.Path)
	}
	defer file.Close()
	encoded, err := json.Marshal(record)
	if err != nil {
		return errors.Wrapf(ctx, err, "encode park-wake record failed")
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return errors.Wrapf(ctx, err, "write park-wake record %s failed", r.Path)
	}
	return nil
}

// TurnSender writes one turn to a session this daemon holds.
//
// It is the Supervisor's `Send`, narrowed to what a wake needs, so a test can
// supply a real recorder of turns instead of a mock of the supervisor.
type TurnSender interface {
	Send(ctx context.Context, id string, text string) error
}

// ParkWake is the concrete ParkWaker: resolve the spawner, coalesce, write.
type ParkWake struct {
	Sender   TurnSender
	Roster   Roster
	Liveness Liveness
	Clock    libtime.CurrentDateTimeGetter
	Recorder WakeRecorder
	// Window defaults to ParkWakeWindow when zero.
	Window time.Duration

	mutex sync.Mutex
	// last holds the newest delivery per manager, and the count suppressed
	// since it. Guarded by mutex: wakes run on the park's own goroutine and
	// parks arrive concurrently.
	last map[string]wakeState
}

type wakeState struct {
	at        time.Time
	coalesced int
}

// Wake resolves the manager of the parked session and writes one turn to it,
// coalescing inside Window.
func (w *ParkWake) Wake(ctx context.Context, parkedSessionID, gateID string) error {
	if w == nil || w.Sender == nil {
		return nil
	}
	now := w.clock().Now().Time()
	managerID, result, err := w.resolveManager(ctx, parkedSessionID)
	if err != nil {
		w.record(ctx, WakeRecord{TS: now, GateID: gateID, Result: ParkWakeRosterUnreadable})
		return nil
	}
	if result != "" {
		w.record(ctx, WakeRecord{TS: now, ManagerID: managerID, GateID: gateID, Result: result})
		return nil
	}

	w.mutex.Lock()
	state, seen := w.last[managerID]
	if seen && now.Sub(state.at) < w.window() {
		state.coalesced++
		w.last[managerID] = state
		w.mutex.Unlock()
		w.record(ctx, WakeRecord{
			TS: now, ManagerID: managerID, GateID: gateID,
			Result: ParkWakeCoalesced, Coalesced: state.coalesced,
		})
		return nil
	}
	// Reserve the slot BEFORE releasing the lock. Two parks arriving together
	// both read "no recent wake" if the check and the write are not one step, and
	// both send — the cap would then hold only for sequential parks, which is not
	// the case it exists for.
	if w.last == nil {
		w.last = map[string]wakeState{}
	}
	w.last[managerID] = wakeState{at: now, coalesced: 0}
	w.mutex.Unlock()

	if err := w.Sender.Send(ctx, managerID, ParkWakeMessageText(gateID)); err != nil {
		// Release the reservation so the next park retries instead of being
		// coalesced against a wake that never landed. The record is written and
		// the error is NOT returned: the park itself registered successfully, and
		// failing the caller would make a wake failure look like a park failure.
		w.mutex.Lock()
		delete(w.last, managerID)
		w.mutex.Unlock()
		w.record(ctx, WakeRecord{TS: now, ManagerID: managerID, GateID: gateID, Result: ParkWakeSendFailed})
		return nil
	}

	w.record(ctx, WakeRecord{TS: now, ManagerID: managerID, GateID: gateID, Result: ParkWakeDelivered})
	return nil
}

// resolveManager returns the live manager of the parked session, or the result
// token that explains why there is none. An empty result means "wake this id".
func (w *ParkWake) resolveManager(ctx context.Context, parkedSessionID string) (string, string, error) {
	if w.Roster == nil {
		return "", ParkWakeNoManager, nil
	}
	rows, err := w.Roster.List(ctx)
	if err != nil {
		return "", "", errors.Wrapf(ctx, err, "list roster for park wake failed")
	}
	parent := ""
	for _, row := range rows {
		if row.SessionID == parkedSessionID && row.ParentSession != nil {
			parent = *row.ParentSession
			break
		}
	}
	if parent == "" {
		return "", ParkWakeNoManager, nil
	}
	if parent == parkedSessionID {
		return parent, ParkWakeSelfManager, nil
	}
	if w.Liveness != nil {
		live, liveErr := w.Liveness.Live(ctx)
		// A nil map with a nil error means the probe could not run; it is never
		// read as "nothing is live". An unreadable probe does not suppress the
		// wake — a missed wake re-arms through the gate, and suppressing on a
		// blind probe would silence the whole mechanism during a store outage.
		if liveErr == nil && live != nil {
			if _, ok := live[parent]; !ok {
				return parent, ParkWakeDeadManager, nil
			}
		}
	}
	return parent, "", nil
}

func (w *ParkWake) window() time.Duration {
	if w.Window > 0 {
		return w.Window
	}
	return ParkWakeWindow
}

func (w *ParkWake) clock() libtime.CurrentDateTimeGetter {
	if w.Clock != nil {
		return w.Clock
	}
	return libtime.NewCurrentDateTime()
}

// record writes one line and never fails the wake: a wake that happened must
// not be reported as failed because its ledger was unwritable.
func (w *ParkWake) record(ctx context.Context, record WakeRecord) {
	if w.Recorder == nil {
		return
	}
	_ = w.Recorder.Record(ctx, record)
}

// ParkWakeMessageText formats the wake turn for one gate. It is deliberately
// narrow: it names the gate, it does not answer it, and it says a wake is not
// an authorisation — the same rule the fleet pusher's message follows.
func ParkWakeMessageText(gateID string) string {
	return "Parked gate wake: a permission gate is parked for a worker you spawned " +
		"(gate " + gateID + "). Answer it from your gate feed. Nothing was authorised on your behalf and " +
		"this is not a green light past any gate."
}

// SetParkWaker installs the manager wake. It is set after construction because
// the waker needs the roster, which is built on top of this supervisor.
func (s *supervisor) SetParkWaker(waker ParkWaker) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.parkWaker = waker
}

// wakeParkOwner asks the parked session's manager to take a turn.
//
// It runs only for a park the policy did NOT release: a park answered in-process
// has nobody waiting, and waking a manager for it would spend a turn on work
// that is already done.
func (s *supervisor) wakeParkOwner(ctx context.Context, id string, question Question) {
	s.mutex.RLock()
	waker := s.parkWaker
	s.mutex.RUnlock()
	if waker == nil {
		return
	}
	if err := waker.Wake(ctx, id, question.RequestID); err != nil {
		log.Printf("park wake for session %s gate %s failed: %v", id, question.RequestID, err)
	}
}
