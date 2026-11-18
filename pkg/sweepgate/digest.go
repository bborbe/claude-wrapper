// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// Digest is the sweep's change signal.
//
// ⚠️ This string is a cross-tool contract: the vault-side script and the
// manager-predispatch port both compute it. A field added here silently changes
// what "unchanged" means. For each task sorted by name it hashes
// `<name>|<status>|<phase>|<session>|<progress_hash>|<liveness>|<0 or 1>\n`, then
// the literal line `--members--\n`, then each name sorted followed by `\n`.
//
// `liveness` defaults to `none` and `stuck` to false when the caller has not
// enriched them (the fixture calls the digest before enrichment).
func Digest(tasks []Task) string {
	sorted := make([]Task, len(tasks))
	copy(sorted, tasks)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	hash := sha256.New()
	for _, task := range sorted {
		liveness := task.Liveness
		if liveness == "" {
			liveness = "none"
		}
		stuck := 0
		if task.Stuck {
			stuck = 1
		}
		fmt.Fprintf(hash, "%s|%s|%s|%s|%s|%s|%d\n",
			task.Name, task.Status, task.Phase, task.Session,
			task.ProgressHash, liveness, stuck)
	}
	hash.Write([]byte("--members--\n"))
	for _, task := range sorted {
		hash.Write([]byte(task.Name + "\n"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
