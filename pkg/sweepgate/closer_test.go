// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	stderrors "errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// fakeExitRunner is a scripted ExitRunner: it never spawns anything.
type fakeExitRunner struct {
	stdout   []byte
	exitCode int
	err      error
	calls    int
	lastName string
	lastArgs []string
}

func (f *fakeExitRunner) Run(
	_ context.Context, name string, args ...string,
) ([]byte, int, error) {
	f.calls++
	f.lastName = name
	f.lastArgs = args
	return f.stdout, f.exitCode, f.err
}

var _ = ginkgo.Describe("maps the closer exit codes", func() {
	ctx := context.Background()
	cfg := Config{Subject: "S"}

	ginkgo.It("maps reap-closer.py exits to closed / open / unreadable", func() {
		cases := []struct {
			name string
			exit int
			err  error
			want string
		}{
			{name: "closed", exit: 0, want: "closed"},
			{name: "open", exit: 1, want: "open"},
			{name: "unreadable-exit", exit: 2, want: "unreadable"},
			{name: "unparsable", exit: 99, want: "unreadable"},
			{name: "spawn-failure", exit: -1, err: stderrors.New("spawn failed"), want: "unreadable"},
		}
		for _, tc := range cases {
			runner := &fakeExitRunner{exitCode: tc.exit, err: tc.err}
			result := CloserOf(ctx, runner, cfg, "session-1")
			gomega.Expect(result).To(gomega.Equal(tc.want))
			ginkgo.GinkgoWriter.Printf("case=%s exit=%d result=%q", tc.name, tc.exit, result)
		}
	})

	ginkgo.It("maps close-line.py exits to a line, a decline and an error", func() {
		cases := []struct {
			name    string
			stdout  string
			exit    int
			err     error
			want    string
			wantErr bool
		}{
			{name: "line", stdout: "DONE — CLOSE session-1\n", exit: 0, want: "DONE — CLOSE session-1"},
			{name: "declined", stdout: "candidate\n", exit: 1, want: ""},
			{name: "unexpected", stdout: "", exit: 2, wantErr: true},
			{name: "spawn-failure", stdout: "", exit: -1, err: stderrors.New("spawn failed"), wantErr: true},
		}
		for _, tc := range cases {
			runner := &fakeExitRunner{stdout: []byte(tc.stdout), exitCode: tc.exit, err: tc.err}
			result, err := CloseLine(ctx, runner, cfg, "Task", "session-1", "live", "open")
			if tc.wantErr {
				gomega.Expect(err).To(gomega.HaveOccurred())
			} else {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(result).To(gomega.Equal(tc.want))
			}
			ginkgo.GinkgoWriter.Printf("case=%s exit=%d result=%q", tc.name, tc.exit, result)
		}
	})

	ginkgo.It("passes the task name as an argv element, never a shell string", func() {
		runner := &fakeExitRunner{exitCode: 0, stdout: []byte("ok\n")}
		_, err := CloseLine(ctx, runner, cfg, `evil $(rm -rf /) %s`, "session-1", "live", "open")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(runner.lastArgs).To(gomega.ContainElement(`evil $(rm -rf /) %s`))
	})
})
