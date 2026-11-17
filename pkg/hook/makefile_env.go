// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxDeployRootDepth bounds the ancestor walk for a Makefile.env. The script
// used 8; a deeper mapping is not found, and the dev arm then denies because a
// missing mapping is not evidence of a dev deploy.
const maxDeployRootDepth = 8

var (
	makefileOpener     = regexp.MustCompile(`^\s*ifeq\s*\(\$\(BRANCH\)\s*,\s*master\s*\)`)
	makefileFilterList = regexp.MustCompile(`^\s*ifneq\s*\(\$\(filter\s+\$\(BRANCH\)\s*,`)
	makefileNested     = regexp.MustCompile(`^\s*(ifeq|ifneq|ifdef|ifndef)`)
	makefileEndif      = regexp.MustCompile(`^\s*endif`)
	makefileElse       = regexp.MustCompile(`^\s*else`)
	makefileComment    = regexp.MustCompile(`^\s*#`)
	makefileProdStage  = regexp.MustCompile(`prod\.env|STAGE\s*:?=\s*prod`)
	makefileDevStage   = regexp.MustCompile(
		`(^|[^[:alnum:]_.-])dev\.env([^[:alnum:]._-]|$)|STAGE\s*:?=\s*dev([^[:alnum:]_-]|$)`,
	)
)

// makefileDevOpener returns the block opener for one branch spelling, matched
// exactly: `BRANCH=develop` is not accepted by a repo that only declares `dev`.
// `else ifeq (...)` is the fleet's usual spelling, so the leading else is
// allowed.
func makefileDevOpener(branch string) *regexp.Regexp {
	return regexp.MustCompile(
		`^\s*(else\s+)?ifeq\s*\(\$\(BRANCH\)\s*,\s*` + regexp.QuoteMeta(branch) + `\s*\)`,
	)
}

// deployRoot returns the nearest ancestor of dir holding a Makefile.env.
func deployRoot(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	current := abs
	for i := 0; i < maxDeployRootDepth; i++ {
		if _, statErr := os.Stat(filepath.Join(current, "Makefile.env")); statErr == nil {
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
	return "", false
}

// mapsMasterToProd reports whether the repo rooted at dir maps BRANCH=master
// onto prod.
func mapsMasterToProd(dir string) bool {
	root, ok := deployRoot(dir)
	if !ok {
		return false
	}
	for _, name := range []string{"Makefile.env", "Makefile"} {
		if makefileMapsBranch(
			filepath.Join(root, name), "master", makefileOpener, makefileProdStage, false,
		) {
			return true
		}
	}
	return false
}

// mapsBranchToDev reports whether the repo rooted at dir maps the given branch
// onto a dev stage.
func mapsBranchToDev(dir string, branch string) bool {
	root, ok := deployRoot(dir)
	if !ok {
		return false
	}
	opener := makefileDevOpener(branch)
	for _, name := range []string{"Makefile.env", "Makefile"} {
		if makefileMapsBranch(
			filepath.Join(root, name), branch, opener, makefileDevStage, true,
		) {
			return true
		}
	}
	return false
}

// makefileMapsBranch scans one makefile for a conditional block that selects
// branch and, inside it, assigns a stage matching stage.
//
// The scan mirrors the script's awk state machine: a blank line or a comment
// must not hide the mapping, a nested conditional must not end the scan early,
// and the `ifneq ($(filter $(BRANCH),…),)` spelling opens the block too.
func makefileMapsBranch(
	path string,
	branch string,
	opener *regexp.Regexp,
	stage *regexp.Regexp,
	skipComments bool,
) bool {
	data, err := os.ReadFile(path) //#nosec G304 -- the path is a fixed name under a resolved repo root
	if err != nil {
		return false
	}
	scanner := blockScanner{
		branch: branch, opener: opener, stage: stage, skipComments: skipComments,
	}
	for _, line := range strings.Split(string(data), "\n") {
		if scanner.consume(line) {
			return true
		}
	}
	return false
}

// blockScanner is the conditional-block state machine, one line at a time.
type blockScanner struct {
	branch       string
	opener       *regexp.Regexp
	stage        *regexp.Regexp
	skipComments bool
	inBlock      bool
	depth        int
}

// consume advances the scan by one line and reports whether the open block
// assigned a matching stage.
func (s *blockScanner) consume(line string) bool {
	if !s.inBlock {
		s.open(line)
		return false
	}
	switch {
	case makefileNested.MatchString(line):
		s.depth++
		return false
	case makefileEndif.MatchString(line):
		s.closeBlock()
		return false
	case s.depth == 0 && makefileElse.MatchString(line):
		s.inBlock = false
		return false
	case s.depth == 0 && s.skipComments && makefileComment.MatchString(line):
		return false
	}
	return s.depth == 0 && s.stage.MatchString(line)
}

// open starts the block when the line opens a conditional selecting branch.
func (s *blockScanner) open(line string) {
	if s.opener.MatchString(line) || filterListNames(line, s.branch) {
		s.inBlock, s.depth = true, 0
	}
}

// closeBlock ends a nested conditional, or the block itself at depth zero.
func (s *blockScanner) closeBlock() {
	if s.depth > 0 {
		s.depth--
		return
	}
	s.inBlock = false
}

// filterListNames reports whether a `ifneq ($(filter $(BRANCH),…),)` line lists
// branch as a whole word.
func filterListNames(line string, branch string) bool {
	if !makefileFilterList.MatchString(line) {
		return false
	}
	_, rest, found := strings.Cut(line, "$(BRANCH)")
	if !found {
		return false
	}
	rest = strings.TrimPrefix(rest, ",")
	if index := strings.Index(rest, ")"); index >= 0 {
		rest = rest[:index]
	}
	word := regexp.MustCompile(`(^|[^[:alnum:]_])` + regexp.QuoteMeta(branch) + `([^[:alnum:]_]|$)`)
	return word.MatchString(rest)
}

// componentDeploys reports whether the component declares an `upgrade:` target,
// which is what makes `install` a deploy rather than a local sync.
func componentDeploys(dir string) bool {
	for _, name := range []string{"Makefile", "makefile", "GNUmakefile"} {
		data, err := os.ReadFile(filepath.Join(dir, name)) //#nosec G304 -- fixed names
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "upgrade:") {
				return true
			}
		}
	}
	return false
}
