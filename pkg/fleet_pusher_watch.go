// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

const (
	// DefaultFleetPusherWatchInterval is how often the watchdog checks the
	// heartbeat. It matches the retired launchd job's StartInterval (300 s).
	DefaultFleetPusherWatchInterval = 5 * time.Minute

	// DefaultFleetPusherMaxAge is the heartbeat age past which the cadence is
	// declared dead. It is 2x the tick's interval, so one slow tick raises nothing.
	DefaultFleetPusherMaxAge = 30 * time.Minute

	// FleetPusherWatchProducerID is the fixed slug every card this job posts
	// carries. It names the agent, never a session.
	FleetPusherWatchProducerID = "fleet-pusher-watch"
)

// The three dedup keys — one per failure mode.
const (
	DedupHeartbeatMissing     = "fleet-pusher-heartbeat-missing"
	DedupHeartbeatUnparseable = "fleet-pusher-heartbeat-unparseable"
	DedupHeartbeatStale       = "fleet-pusher-heartbeat-stale"
)

// FleetPusherWatch reports a dead wake cadence by reading its heartbeat.
//
// The tick cannot report its own death: if it stopped, nothing inside it runs.
// This is the second, independent job whose only work is to read the first one's
// heartbeat and post a condition card when it ages past the limit.
type FleetPusherWatch struct {
	// HeartbeatPath is the file the tick stamps.
	HeartbeatPath string
	Now           libtime.CurrentDateTimeGetter
	// MaxAge defaults to DefaultFleetPusherMaxAge.
	MaxAge time.Duration
	// BootTime returns the machine's boot instant. A boot younger than MaxAge is
	// a startup grace: the cadence has not had a chance to tick yet, so an absent
	// or old heartbeat is expected rather than a dead agent. Nil uses sysctl.
	BootTime func() (time.Time, error)
	// Poster creates the condition card. Nil disables posting (and the spec drives
	// the branch through a fake).
	Poster AttentionPoster
	// Cards finds a card this job already posted, so a repeated cycle posts
	// nothing rather than leaning on the store's own suppression. Nil skips the
	// pre-check.
	Cards CardLookup
	// Interval defaults to DefaultFleetPusherWatchInterval.
	Interval time.Duration
	Logf     func(format string, args ...any)
}

// CardLookup finds an already-open card by producer and dedup key.
type CardLookup interface {
	// ExistingCard returns the item id of an open card with this producer and
	// dedup key, or "" when there is none.
	ExistingCard(ctx context.Context, producerID string, dedupKey string) (string, error)
}

// NewBoardCardLookup creates a CardLookup over the attention store.
func NewBoardCardLookup(store BoardStore) CardLookup {
	return &boardCardLookup{store: store}
}

type boardCardLookup struct {
	store BoardStore
}

// ExistingCard scans the open items for a matching producer and dedup key.
func (l *boardCardLookup) ExistingCard(
	ctx context.Context,
	producerID string,
	dedupKey string,
) (string, error) {
	items, err := l.store.Open(ctx)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if item.ProducerID == producerID && item.DedupKey == dedupKey {
			return item.ItemID, nil
		}
	}
	return "", nil
}

// Run cycles until the context ends, the first cycle immediately.
func (w *FleetPusherWatch) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = DefaultFleetPusherWatchInterval
	}
	w.Cycle(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			w.Cycle(ctx)
		}
	}
}

// Cycle runs one watchdog pass and writes its one line.
//
// Every branch is reachable through the injectable seams — the heartbeat path,
// the clock, the boot time and the poster — so the grace, missing, unparseable,
// stale and healthy cases are each forced by a spec rather than left as
// unreachable code.
func (w *FleetPusherWatch) Cycle(ctx context.Context) {
	if grace, uptime, readable := w.withinGrace(); readable && grace {
		w.logf("job=fleet-pusher-watch action=grace age=%ds", uptime)
		return
	}

	raw, err := os.ReadFile(expandTilde(w.HeartbeatPath))
	if err != nil {
		if os.IsNotExist(err) {
			w.post(ctx, DedupHeartbeatMissing, fmt.Sprintf(
				"Fleet pusher cadence: the heartbeat file %s does not exist, so the wake "+
					"cadence has never run on this machine. launchd may report the job as "+
					"loaded and healthy while nothing ticks.",
				expandTilde(w.HeartbeatPath),
			))
			return
		}
		w.post(ctx, DedupHeartbeatUnparseable, fmt.Sprintf(
			"Fleet pusher cadence: the heartbeat file %s could not be read (%v), so the "+
				"watchdog cannot tell whether the cadence is alive.",
			expandTilde(w.HeartbeatPath), err,
		))
		return
	}
	stamp := strings.TrimSpace(string(raw))
	parsed, parseErr := time.Parse("2006-01-02T15:04:05Z", stamp)
	if parseErr != nil {
		w.post(ctx, DedupHeartbeatUnparseable, fmt.Sprintf(
			"Fleet pusher cadence: the heartbeat file holds '%s', which is not an ISO-8601 "+
				"UTC timestamp. The watchdog cannot tell whether the cadence is alive.",
			stamp,
		))
		return
	}
	age := w.now().Sub(parsed)
	if age > w.maxAge() {
		w.post(ctx, DedupHeartbeatStale, fmt.Sprintf(
			"Fleet pusher cadence: the heartbeat is %ds old (threshold %ds), so the cheap "+
				"wake layer has stopped. launchd may still list the job as loaded — that is "+
				"exactly the state that looks healthy and wakes nothing.",
			int(age.Seconds()), int(w.maxAge().Seconds()),
		))
		return
	}
	w.logf("job=fleet-pusher-watch action=ok age=%ds", int(age.Seconds()))
}

// withinGrace reports whether the machine booted inside the max age.
//
// ok is false when the boot time cannot be read: an unreadable boot is not a
// grace, and staying quiet on it would hide a dead cadence behind a probe
// failure.
func (w *FleetPusherWatch) withinGrace() (bool, int, bool) {
	bootFn := w.BootTime
	if bootFn == nil {
		bootFn = systemBootTime
	}
	boot, err := bootFn()
	if err != nil || boot.IsZero() {
		return false, 0, false
	}
	uptime := w.now().Sub(boot)
	if uptime < w.maxAge() {
		return true, int(uptime.Seconds()), true
	}
	return false, int(uptime.Seconds()), true
}

// post creates the condition card unless this job already has one on the board.
func (w *FleetPusherWatch) post(ctx context.Context, key string, payload string) {
	if w.Cards != nil {
		existing, err := w.Cards.ExistingCard(ctx, FleetPusherWatchProducerID, key)
		if err == nil && existing != "" {
			w.logf("job=fleet-pusher-watch action=skip key=%s card=%s", key, existing)
			return
		}
	}
	if w.Poster == nil {
		w.logf("job=fleet-pusher-watch action=post key=%s outcome=disabled", key)
		return
	}
	itemID, err := w.Poster.Post(ctx, AttentionPostRequest{
		ProducerID:      FleetPusherWatchProducerID,
		ProducerKind:    "cron",
		LivenessRef:     "owner:" + FleetPusherWatchProducerID,
		DedupKey:        key,
		InterruptClass:  "routine",
		Payload:         payload,
		AnswerMechanism: "ack",
	})
	if err != nil {
		w.logf("job=fleet-pusher-watch action=post key=%s outcome=failed error=%v", key, err)
		return
	}
	w.logf("job=fleet-pusher-watch action=post key=%s card=%s", key, itemID)
}

func (w *FleetPusherWatch) now() time.Time {
	if w.Now == nil {
		return time.Now()
	}
	return w.Now.Now().Time()
}

func (w *FleetPusherWatch) maxAge() time.Duration {
	if w.MaxAge <= 0 {
		return DefaultFleetPusherMaxAge
	}
	return w.MaxAge
}

func (w *FleetPusherWatch) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

var bootSecPattern = regexp.MustCompile(`sec = (\d+)`)

// systemBootTime reads the machine's boot instant from `sysctl kern.boottime`.
func systemBootTime() (time.Time, error) {
	output, err := exec.Command("sysctl", "-n", "kern.boottime").Output()
	if err != nil {
		return time.Time{}, errors.Wrapf(context.Background(), err, "sysctl kern.boottime failed")
	}
	match := bootSecPattern.FindStringSubmatch(string(output))
	if match == nil {
		return time.Time{}, errors.Errorf(
			context.Background(), "kern.boottime carried no sec: %q", string(output),
		)
	}
	seconds, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return time.Time{}, errors.Wrapf(context.Background(), err, "parse boot seconds failed")
	}
	return time.Unix(seconds, 0), nil
}
