package pkg

import (
	"os"
	"regexp"
	"strings"
)

// launcherMCPConfigFlag matches `--mcp-config` at a whitespace boundary, so a
// hypothetical `--mcp-config-dir` flag cannot match, in every shape a launcher
// actually writes.
//
// The separator alternation is load-bearing, not tidiness: the real cc-* launchers
// write `--mcp-config="$HOME/.claude/mcp-<vault>.json"` with an EQUALS SIGN, and a
// pattern requiring whitespace matches none of them while passing every fixture
// written by hand. Measured 2026-11-11 on `cc-private-deepseek` — the first
// version of this regex would have shipped a fix that did nothing on the one
// launcher it was written for.
var launcherMCPConfigFlag = regexp.MustCompile(`(?:^|\s)--mcp-config(?:=|[ \t]+)(?:"([^"]*)"|'([^']*)'|(\S+))`)

// ParseLauncherMCPConfig returns the MCP config file a launcher script passes to
// the CLI, or "" when it passes none.
//
// The same shape as ParseLauncherModel, and for the same reason: a headless child
// is a bare `claude --print`, so the launcher script is never executed and
// neither its `--model` nor its `--mcp-config` reaches the child unless this
// package passes it explicitly.
//
// Comment lines are stripped first. The real cc-* launchers mention
// `--mcp-config` in prose as well as in the flag, and a naive scan takes the
// comment for the answer — the same trap ParseLauncherModel documents.
func ParseLauncherMCPConfig(scriptText string) string {
	if strings.TrimSpace(scriptText) == "" {
		return ""
	}
	scriptText = launcherCommentLine.ReplaceAllString(scriptText, "")
	flag := launcherMCPConfigFlag.FindStringSubmatch(scriptText)
	if flag == nil {
		return ""
	}
	for _, group := range flag[1:] {
		raw := strings.TrimSpace(group)
		if raw == "" {
			continue
		}
		// The value arrives as an argument, not through a shell, so nothing expands
		// it. The real launchers write `"$HOME/.claude/mcp-<vault>.json"`; a `~` form
		// is handled too. A value that expands to nothing is returned unchanged
		// rather than dropped: a config the CLI may still resolve beats none.
		expanded := os.ExpandEnv(raw)
		if expanded != raw {
			return expanded
		}
		if resolved, err := expandHome(raw); err == nil {
			return resolved
		}
		return raw
	}
	return ""
}

// LauncherMCPConfig reads a launcher script and returns the MCP config file it
// passes, or "".
//
// No error return, matching LauncherModel: an unreadable launcher is a MISSING
// CONFIG, not a failed spawn. A worker with fewer MCP servers is degraded; a
// refused spawn is stopped.
//
// A bare name is NOT resolved through PATH, unlike LauncherModel. The MCP config
// is an argument the launcher passes, and a bare name that resolved to some other
// script's flag would attach an unrelated vault's servers to this spawn.
func LauncherMCPConfig(launcher string) string {
	if !strings.Contains(launcher, "/") {
		return ""
	}
	raw, err := os.ReadFile(launcher)
	if err != nil {
		return ""
	}
	return ParseLauncherMCPConfig(string(raw))
}
