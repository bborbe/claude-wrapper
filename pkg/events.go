// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// Event is one JSONL line the child wrote to stdout, kept verbatim.
//
// It is deliberately untyped. The wrapper folds events into Session state and
// models no SDK envelope, so a consumer that needs the envelope is given
// exactly what the child emitted rather than a lossy re-encoding of it.
type Event map[string]any

// maxRetainedEvents bounds the replay buffer.
//
// This stream is a supervision channel, not a transcript: a consumer that needs
// the whole conversation reads the child's own transcript. 4096 lines covers a
// long turn comfortably while keeping a child that never stops talking from
// growing this process without limit.
const maxRetainedEvents = 4096

// subscriberBuffer is how far a subscriber may fall behind before its stream is
// ended rather than silently gapped.
//
// A supervision stream with an invisible hole in it is worse than one that
// ends and says so, so publish closes a subscriber that overflows instead of
// dropping the event.
const subscriberBuffer = 512
