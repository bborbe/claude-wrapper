# Reference copies — read-only, retained permanently

Both files are pinned copies of what the Python gate actually runs:

| File | Origin | Pin |
|---|---|---|
| `sweep-gate.py` | `<vault>/.claude/scripts/sweep-gate.py` | vault rev `1eb24d6d`, 2026-11-16, 3 337 lines |
| `box-table.py` | `~/.claude/plugins/marketplaces/claude-supervisor/scripts/box-table.py` | 217 lines |

**Why both:** `sweep-gate.py:176-178` resolves `BOX_TABLE` to a **marketplace clone path**
and shells out to it with `sys.executable` (`:2857-2863`). Neither file is in the
container mount, so the parity goldens cannot be produced without them.

**Retained permanently, not deleted.** The port's tests diff against these files, and
`docs/sweep-gate.md` cites their line numbers. Deleting them when the vault deletes its
original would delete the only record of what was ported.

**`generate.sh` must point `HOME` at a temp dir** containing
`~/.claude/plugins/marketplaces/claude-supervisor/scripts/box-table.py` — a symlink to
the copy here — because the reference resolves that path itself.
