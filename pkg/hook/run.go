// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"context"
	"io"

	"github.com/bborbe/errors"
)

// Run reads one hook payload from stdin, evaluates it and writes the decision to
// stdout.
//
// A PostToolUse payload gets no output: those guards record, they do not decide.
// An unknown event is a pass-through rather than an error, so a harness that
// adds an event does not break the binary.
func Run(ctx context.Context, registry *Registry, stdin io.Reader, stdout io.Writer) error {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return errors.Wrapf(ctx, err, "read hook input")
	}
	input, err := ParseInput(ctx, data)
	if err != nil {
		return err
	}
	decision := registry.Evaluate(input)
	if input.HookEventName != EventPreToolUse {
		return nil
	}
	encoded, err := EncodeDecision(ctx, input.HookEventName, decision)
	if err != nil {
		return err
	}
	if _, err := stdout.Write(encoded); err != nil {
		return errors.Wrapf(ctx, err, "write hook decision")
	}
	return nil
}
