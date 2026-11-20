// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// KeepaliveFolderFor resolves a vault's topics/tasks directory via
// `resolve-folders.py --vault <root path> --require topics_dir|tasks_dir`. A
// failure or empty result skips that vault.
type KeepaliveFolderFor func(ctx context.Context, vault Vault) (string, error)

// NewKeepalivePageSource builds the filesystem page source: it lists `*.md` files
// DIRECTLY in each vault's folder (no recursion) and reads `status` and
// `claude_session_id` with the same `readFrontmatter` the open reader uses.
//
// ⚠️ A `folderFor` failure or empty result skips that vault (logged as
// `result=error`, then continue); an unreadable directory skips it; an unparseable
// page is skipped like a missing session id. None of these fail the whole
// enumeration — a broken page must not blind the tick to the rest of the fleet.
func NewKeepalivePageSource(
	vaults []Vault,
	folderFor KeepaliveFolderFor,
	logf func(line string),
) KeepalivePageSource {
	return func(ctx context.Context) ([]KeepalivePage, error) {
		pages := make([]KeepalivePage, 0, len(vaults))
		for _, vault := range vaults {
			pages = append(pages, pagesForVault(ctx, vault, folderFor, logf)...)
		}
		return pages, nil
	}
}

// pagesForVault lists and reads one vault's pages, skipping (and logging) a vault
// whose folder cannot be resolved or read.
func pagesForVault(
	ctx context.Context,
	vault Vault,
	folderFor KeepaliveFolderFor,
	logf func(line string),
) []KeepalivePage {
	dir, err := folderFor(ctx, vault)
	if err != nil || dir == "" {
		logListerError(logf, err)
		return nil
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		logListerError(logf, readErr)
		return nil
	}
	return readPagesFromDir(ctx, vault, dir, entries)
}

// readPagesFromDir reads the *.md entries directly in dir into pages. A directory
// entry or a non-.md file is skipped; an unparseable page is skipped.
func readPagesFromDir(
	ctx context.Context,
	vault Vault,
	dir string,
	entries []fs.DirEntry,
) []KeepalivePage {
	pages := make([]KeepalivePage, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		fields, fmErr := readFrontmatter(ctx, filepath.Join(dir, entry.Name()))
		if fmErr != nil {
			continue
		}
		pages = append(pages, KeepalivePage{
			Vault:     vault.Name,
			Name:      strings.TrimSuffix(entry.Name(), ".md"),
			SessionID: fields["claude_session_id"],
			Status:    fields["status"],
		})
	}
	return pages
}

// logListerError logs a per-vault listing failure (nil logf is a no-op).
func logListerError(logf func(line string), err error) {
	if logf == nil {
		return
	}
	reason := "empty folder"
	if err != nil {
		reason = err.Error()
	}
	logf(JobLine(KeepaliveJobName, "lister", "result", "error", "reason", reason))
}
