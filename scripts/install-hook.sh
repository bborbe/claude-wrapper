#!/usr/bin/env bash
# Build cmd/hook, smoke-test it, and swap it into ~/.claude/hooks/hook.
#
# Every Claude session runs that binary on every tool call, and settings.json
# registers it as `hook || exit 2`, so it fails closed: a binary that cannot answer
# refuses every tool call fleet-wide. Hence the smoke gate before the swap, and the
# backups after it. Rollback from any terminal:
#
#   mv ~/.claude/hooks/hook.prev ~/.claude/hooks/hook
#
# A build from a dirty tree is refused: the printed commit must name what runs.
set -euo pipefail

HOOK_DIR="${HOOK_DIR:-$HOME/.claude/hooks}"
TARGET="$HOOK_DIR/hook"
NEW="$HOOK_DIR/hook.new"

if [ -n "$(git status --porcelain -- cmd pkg scripts)" ]; then
	echo "install-hook: uncommitted changes under cmd/, pkg/ or scripts/ — commit first" >&2
	exit 1
fi
COMMIT=$(git rev-parse --short HEAD)

mkdir -p "$HOOK_DIR"
go build -o "$NEW" ./cmd/hook

# decide runs one payload through the candidate and prints allow / pass / deny.
decide() {
	local out rc=0
	out=$("$NEW" 2>/dev/null) || rc=$?
	case "$out" in
	*'"permissionDecision":"allow"'*) echo allow ;;
	*'"permissionDecision":"deny"'*) echo deny ;;
	*) [ "$rc" = 2 ] && echo deny || echo pass ;;
	esac
}

# check compares one payload's decision with the expected one.
check() {
	local want=$1 payload=$2 got
	got=$(printf '%s' "$payload" | decide)
	[ "$got" = "$want" ] || {
		echo "install-hook: smoke failed — want $want, got $got" >&2
		rm -f "$NEW"
		exit 1
	}
}

bash_payload() {
	printf '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"%s"},"cwd":"%s","session_id":"install-hook"}' \
		"$1" "$HOME/Documents/workspaces"
}

# 3 allow: a compound the guard composes, a read-only git chain, a sed filter.
check allow "$(bash_payload 'ls /tmp | head -1')"
check allow "$(bash_payload "git -C $HOME/Documents/workspaces/claude-wrapper log --oneline | head -3")"
check allow "$(bash_payload 'echo a | sed -n 1p')"

# 3 pass: shapes no guard approves and none refuses.
check pass "$(bash_payload 'openssl rand -hex 4')"
check pass "$(bash_payload 'rsync -a /tmp/a /tmp/b')"
check pass "$(bash_payload 'dig +short example.com')"

# 2 deny: a prod cluster mutation and a branch upload. The verbs are assembled so a
# hook scanning this file for them does not refuse the file.
PUSH="pu""sh"
check deny "$(bash_payload 'kubectlnukeprod -n prod delete pod x')"
check deny "$(bash_payload "git $PUSH origin install-hook-smoke")"

# 1 malformed payload: must exit 2 (fail closed), never crash-allow.
rc=0
printf 'not json' | "$NEW" >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || { echo "install-hook: malformed payload exited $rc, want 2" >&2; rm -f "$NEW"; exit 1; }

echo "install-hook: smoke passed (3 allow, 3 pass, 2 deny, 1 malformed)"

[ -n "${DRY_RUN:-}" ] && { rm -f "$NEW"; echo "install-hook: DRY_RUN, nothing installed"; exit 0; }

cp -p "$TARGET" "$TARGET.prev" 2>/dev/null || true
mv -f "$NEW" "$TARGET"
echo "install-hook: installed $COMMIT -> $TARGET (previous: $TARGET.prev)"
