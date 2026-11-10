// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// writeRegistryEntry writes one session-registry file into dir.
//
// The file name is the pid, exactly as Claude Code names it: the registry is
// pid-keyed, and a test that wrote some other name would not exercise the read.
func writeRegistryEntry(dir string, entry registryFile) string {
	return writeRegistryEntryNamed(dir, strconv.Itoa(entry.PID), entry)
}

// writeRegistryEntryNamed writes an entry under an explicit file name.
//
// The default keys the file on the pid, which is the registry's real shape — but
// two entries sharing one live pid (the only one a test can be sure of) would then
// overwrite each other, so a fixture that needs several live entries for one
// session must name them itself.
func writeRegistryEntryNamed(dir, name string, entry registryFile) string {
	encoded, err := json.Marshal(entry)
	if err != nil {
		panic(err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		panic(err)
	}
	return path
}

// writeTranscript writes a transcript for cwd/sessionID under projectsDir.
func writeTranscript(projectsDir, cwd, sessionID string, lines []string) string {
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
	dir := filepath.Join(projectsDir, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		panic(err)
	}
	return path
}
