// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "context"

// NewTopiclessGoalManagerProcedure builds the keep-alive act for a goal manager
// whose goal NO TOPIC declares.
//
// It is the same procedure as the topic tier — same decision script, same claim,
// same guards — differing only in the population its decision source reads and
// in the `action=` name it writes. That is why it is a constructor over
// LapsedManagerProcedure rather than a second implementation: the two tiers
// share one rule, and a copy would be the place they drifted apart.
//
// The population is the caller's: `topic-keepalive.py` takes the subject it is
// asked about, and the topicless tier asks it about the goals no topic lists.
// Nothing here decides which goals those are.
func NewTopiclessGoalManagerProcedure(
	decide func(ctx context.Context) (LapsedManagerDecision, error),
	claim func(ctx context.Context, sessionID string) (bool, error),
	act func(ctx context.Context, verdict string, sessionID string) error,
) KeepaliveProcedure {
	return &LapsedManagerProcedure{
		Action: "topicless-goal-manager",
		Decide: decide,
		Claim:  claim,
		Act:    act,
	}
}
