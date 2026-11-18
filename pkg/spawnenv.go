// Copyright (c) 2026 Benjamin Borbe All rights reserved.
package pkg

import "strings"

// compactionDefaults are the environment values a spawned child must carry even
// when neither the caller nor this process supplies them.
//
// The daemon inherits its own launchd environment, which carries only the six
// keys the plist declares (ANTHROPIC_BASE_URL, HOME, LANG, PATH, SHELL, USER).
// The `cc-*` launcher exports CLAUDE_CODE_AUTO_COMPACT_WINDOW=900000, but that
// export never reaches a child this daemon starts: childEnv merges over the
// daemon's own os.Environ(), not the launcher's. A session therefore took its
// window from the model's `[1m]` marker alone, and a manager that could not read
// its own fill level inferred exhaustion at 42% of a 967k window.
//
// 900000 is the launcher's own value, not a new choice — this makes the daemon
// agree with the launcher that was previously the only source of it.
//
// ⚠️ CLAUDE_CODE_MAX_OUTPUT_TOKENS is deliberately absent. Nothing in this fleet
// names a value for it, and a default here would cap every spawned session's
// output on a guess: a wrong window costs an early compaction, a wrong output
// cap truncates a turn. It stays the caller's (or the plist's) to set.
var compactionDefaults = map[string]string{
	"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "900000",
}

// spawnEnv is the environment a child is started with: the compaction defaults
// filled in where nothing upstream supplies them, then merged over the inherited
// environment.
func spawnEnv(inherited []string, overrides map[string]string) []string {
	return childEnv(inherited, withDefaults(inherited, overrides, compactionDefaults))
}

// withDefaults returns overrides plus every key of defaults that neither the
// inherited environment nor the overrides already carry.
//
// An inherited key counts as supplied even though it arrives through a different
// channel: a plist or a launcher that sets the value has made a decision, and a
// default that overwrote it would be the daemon silently overruling its own
// configuration.
func withDefaults(inherited []string, overrides map[string]string, defaults map[string]string) map[string]string {
	present := make(map[string]struct{}, len(inherited))
	for _, entry := range inherited {
		if key, _, ok := strings.Cut(entry, "="); ok {
			present[key] = struct{}{}
		}
	}
	merged := make(map[string]string, len(overrides)+len(defaults))
	for key, value := range overrides {
		merged[key] = value
	}
	for key, value := range defaults {
		if _, ok := merged[key]; ok {
			continue
		}
		if _, ok := present[key]; ok {
			continue
		}
		merged[key] = value
	}
	return merged
}
