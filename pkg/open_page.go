// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
)

// readOpenPage reads the page an open row names.
//
// Folders are looked up by kind, in the same order the rest of the package
// uses. The surfaces validate a name before it reaches here; this guard is
// defensive, because the name is joined into a path and a separator or a `..`
// segment must never read outside the vault.
func readOpenPage(ctx context.Context, vault Vault, kind, name string) OpenPage {
	if !validOpenPageName(name) {
		return OpenPage{}
	}
	if strings.TrimSpace(vault.Path) == "" {
		return OpenPage{ReadErr: errors.Errorf(ctx, "vault %q has no path", vault.Name)}
	}
	for _, folder := range openFoldersForKind(kind) {
		path := filepath.Join(vault.Path, folder, name+".md")
		// #nosec G304 -- the path is composed from a configured vault path, a
		// fixed folder constant and a name this function has already validated.
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return OpenPage{ReadErr: errors.Wrapf(ctx, err, "stat %s page %q failed", kind, name)}
		}
		fields, err := readFrontmatter(ctx, path)
		if err != nil {
			return OpenPage{Found: true, Path: path, ReadErr: err}
		}
		return OpenPage{Found: true, Path: path, Fields: fields}
	}
	return OpenPage{}
}

// validOpenPageName reports whether a title may be joined into a vault folder.
func validOpenPageName(name string) bool {
	if name == "" {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	return !hasDotDotSegment(name)
}

// openFoldersForKind maps a row kind to the folders its page may live in.
func openFoldersForKind(kind string) []string {
	switch kind {
	case OpenKindTask:
		return taskFolders
	case OpenKindGoal:
		return goalDirs
	case OpenKindTopic:
		return topicDirs
	}
	return nil
}
