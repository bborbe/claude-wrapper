// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// The replay window is the only place a session's line index is computed rather
// than counted, so an off-by-one here renumbers every replayed event. The handler
// asks for Last-Event-ID+1, so a shift of one silently drops an event — possibly a
// `result` — on the first reconnect after any restart, and the drift grows by one
// per restart. These specs pin the numbering against the file's own contents.
//
// Every line's payload is its own decimal index, so the spec can compare the
// sequence number the tailer assigned against the number written in the line
// itself — two independent sources that must agree.
var _ = ginkgo.Describe("replaying a large event file", func() {
	const lineDigits = 96

	// writeBigEventFile writes lines whose payload is the line's own index, padded
	// to a fixed width so no line length divides the window evenly and the
	// partial-line discard is exercised.
	writeBigEventFile := func() (path string, lines int) {
		dir := ginkgo.GinkgoT().TempDir()
		path = filepath.Join(dir, sessionEventsName)

		var builder strings.Builder
		for builder.Len() <= maxBackfillBytes+4096 {
			fmt.Fprintf(&builder, "%0*d\n", lineDigits, lines)
			lines++
		}
		gomega.Expect(os.WriteFile(path, []byte(builder.String()), 0o600)).To(gomega.Succeed())
		return path, lines
	}

	ginkgo.It("reports a skip count equal to the number of lines before the replay", func() {
		path, _ := writeBigEventFile()

		file, err := os.Open(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer func() { _ = file.Close() }()

		start, skipped := backfillWindow(file)
		gomega.Expect(start).To(gomega.BeNumerically(">", 0))
		gomega.Expect(start).To(gomega.BeNumerically("<", maxBackfillBytes+4096))
		// Independent of how the function arrived at the number: the lines before
		// `start` are exactly the lines whose index is below the skip count.
		gomega.Expect(countNewlines(file, start)).To(gomega.Equal(skipped))
	})

	ginkgo.It("numbers the first replayed line by its own index", func() {
		path, lines := writeBigEventFile()

		var (
			mu        sync.Mutex
			firstSeq  int
			firstLine string
			seen      bool
			emitted   int
		)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan struct{})
		go func() {
			defer close(done)
			tailFile(
				ctx,
				path,
				func(seq int, line []byte, _ bool) {
					mu.Lock()
					defer mu.Unlock()
					if !seen {
						firstSeq = seq
						firstLine = string(line)
						seen = true
					}
					emitted++
				},
				func() bool { return true },
			)
		}()
		<-done

		mu.Lock()
		defer mu.Unlock()

		gomega.Expect(seen).To(gomega.BeTrue(), "the tailer emitted nothing")

		index, err := strconv.Atoi(strings.TrimSpace(firstLine))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// The line says which line it is; the tailer must agree. Under the
		// off-by-one this reads index-1.
		gomega.Expect(firstSeq).To(gomega.Equal(index))
		gomega.Expect(index).To(gomega.BeNumerically(">", 0))
		gomega.Expect(emitted).To(gomega.Equal(lines - index))
	})
})
