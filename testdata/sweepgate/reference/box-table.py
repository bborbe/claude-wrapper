#!/usr/bin/env python3
"""Render a box-drawn table (used by topic-manager / topic-status / fleet-loop sweeps).

stdin: JSON {"header": [...], "rows": [[...]], "widths": [...]}  (widths optional, default 20)

Columns are separated in the border rows (┬ ┼ ┴), header and cells are both
left-aligned, and over-long cells truncate with '…'.

Padding is computed on DISPLAY width, not len(): the bucket icons used in the
Status column (🔄 ⌛ ✅ 🚀) occupy two terminal cells but count as one character
in Python, so len()-based padding leaves every row with an icon one cell short
and the right-hand border ragged.

⚠️ A character followed by VS16 is NOT widened — measured, not assumed. The probe
is a DSR query: write the glyph, emit `\\x1b[6n`, read the reported cursor column.
In WezTerm 20260716 `⏸️` (U+23F8 U+FE0F) and `⚠️` (U+26A0 U+FE0F) each paint in
ONE cell — VS16 is zero-width and does not widen a Neutral base — while every true
emoji (🟢 🔄 ⌛ ✅ 🚀 🔧) paints in two. Until 2026-09-24 this script widened any
character followed by VS16, which over-counted every `⏸️` row by one cell and
stepped its right border in; measured against the real
`~/.claude/state/sweep-gate/manager-layer.tick.txt`, the VS16 rows were the only
lines that disagreed with the terminal (112 cells against 113).

A cell shaped `[<sid8>]` is wrapped in an OSC 8 hyperlink to its jump target, so
the operator can SHIFT+CMD+click a session id to reach that pane instead of
hand-resolving it through `/supervisor:jump`. The visible text is unchanged: OSC 8
occupies zero cells, and the terminal consumes the sequence rather than printing it
(measured — `wezterm cli get-text` returns only the visible text). The wrap lives
here rather than in a caller because all three cell producers — `sweep-gate.py`'s
`session_cell()`, the sweep-reader agent, and the two command fallbacks — render
through this script, so one change covers them all.

⚠️ The link carries the jump token, because the only usable URI is the one
`jump-link.py` builds from it. Set `BOX_TABLE_NO_LINKS=1` before rendering anything
that will be pasted into a git-tracked file.
"""
import json, os, re, subprocess, sys, unicodedata

_HERE = os.path.dirname(os.path.abspath(__file__))
WHO_NEEDS_ME = os.path.join(_HERE, "who-needs-me.py")
JUMP_LINK = os.path.join(_HERE, "jump-link.py")

# Where WezTerm lives when it is not on PATH. The detached gate host runs under
# launchd with a PATH that omits it (com.bborbe.sweep-gate-notify.plist), and there
# `who-needs-me.py --pane-for` fails with "WezTerm pane list unreadable" — measured
# 2026-09-24. Appending this to the child's PATH makes resolution work on that host
# without touching either sibling script, both of which call a bare `wezterm` and
# are outside this script's remit. Only appended when the directory exists, so a
# host without WezTerm degrades to plain text exactly as it would anyway.
WEZTERM_DIRS = ("/Applications/WezTerm.app/Contents/MacOS",)

# `[<sid8>]` — exactly what the sweep frame emits for a session cell. Deliberately
# narrow: the fleet table's Session column carries a session NAME, and a cell that
# merely looks like a hex id is not a session reference.
SESSION_CELL_RE = re.compile(r"^\[([0-9a-fA-F]{6,8})\]$")

# OSC 8: ESC ] 8 ; params ; URI ST ... ESC ] 8 ; ; ST, where ST is ESC \ or BEL.
OSC8_OPEN = "\x1b]8;;"
OSC8_ST = "\x1b\\"
OSC8_RE = re.compile(r"\x1b\]8;;[^\x1b\x07]*(?:\x1b\\|\x07)")

_UNLINKED = ("1", "true", "yes", "on")


def cwidth(ch):
    """Display cells occupied by one character."""
    if ch == "️":  # variation selector-16: zero-width, and does not widen
        return 0
    if unicodedata.combining(ch):
        return 0
    return 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1


def strip_osc8(s):
    """Drop OSC 8 sequences. They are zero-width, so they must never reach a width sum."""
    return OSC8_RE.sub("", str(s))


def dwidth(s):
    """Display width of a string. OSC 8 escapes cost nothing."""
    return sum(cwidth(ch) for ch in strip_osc8(s))


def osc8(text, url):
    """Wrap text in an OSC 8 hyperlink. Adds zero display width."""
    if not url:
        return text
    return f"{OSC8_OPEN}{url}{OSC8_ST}{text}{OSC8_OPEN}{OSC8_ST}"


def links_enabled():
    return os.environ.get("BOX_TABLE_NO_LINKS", "").strip().lower() not in _UNLINKED


def child_env():
    """The environment for our resolver children: the caller's, plus WezTerm's own dir."""
    env = dict(os.environ)
    extra = [d for d in WEZTERM_DIRS if os.path.isdir(d)]
    if extra:
        env["PATH"] = env.get("PATH", "") + os.pathsep + os.pathsep.join(extra)
    return env


_url_cache = {}


def jump_url(sid8, run=subprocess.run, env=None):
    """The jump URL for a sessionId prefix, or None when nothing resolves.

    Two hops, because neither script does both: `who-needs-me.py --pane-for` turns
    the prefix into a live pane — the only sound join, since the session registry
    carries no pane field and a title join breaks on `/rename` — and `jump-link.py`
    turns a pane into the URL the jump server accepts.

    Every failure returns None rather than raising, so a cell that cannot be linked
    renders as the plain text it was before this existed. That is also what makes a
    dead link impossible: `jump-link.py` prints its `/supervisor:jump <pane>`
    fallback when the server or the token is missing, and that is a command, not a
    URI, so it is deliberately not turned into a hyperlink.
    """
    if sid8 in _url_cache:
        return _url_cache[sid8]
    env = child_env() if env is None else env
    url = None
    try:
        pane = run([sys.executable, WHO_NEEDS_ME, "--pane-for", sid8],
                   capture_output=True, text=True, timeout=15, env=env)
        pane_id = (pane.stdout or "").strip()
        if pane.returncode == 0 and pane_id.isdigit():
            link = run([sys.executable, JUMP_LINK, pane_id],
                       capture_output=True, text=True, timeout=15, env=env)
            out = (link.stdout or "").strip()
            if link.returncode == 0 and out.startswith(("http://", "https://")):
                url = out
    except (OSError, subprocess.SubprocessError):
        url = None
    _url_cache[sid8] = url
    return url


def fmt(s, w, url=None):
    """One cell: truncated to `w`, padded to `w`, optionally linked.

    The link wraps the visible text only — padding stays outside it, so the escape
    bytes never enter a width sum and a click on the trailing padding does nothing.
    """
    s = strip_osc8(s)
    if dwidth(s) > w:
        out = ""
        for ch in s:
            if dwidth(out) + cwidth(ch) > w - 1:
                break
            out += ch
        s = out + "…"
    pad = " " * (w - dwidth(s))
    return (osc8(s, url) if url else s) + pad


def cell_url(value, link=True):
    """The jump URL for a cell, or None. Only `[<sid8>]`-shaped cells are ever linked."""
    if not link:
        return None
    m = SESSION_CELL_RE.match(strip_osc8(value).strip())
    return jump_url(m.group(1)) if m else None


def line(vals, widths, link=True, first_url=None):
    """One row. `first_url` overrides the Session cell's link.

    `cell_url` resolves a cell only when it is *exactly* a `[sid8]` token. The
    fleet table's Session cell holds a **name** — the operator-facing label, and
    the thing `/rename` changes — so it cannot be resolved from its own text and
    the producer supplies the session id instead. Every other cell keeps the
    existing whole-cell-token behaviour, and a `None` here falls through to it,
    so the manager tables that share this renderer are unaffected.
    """
    out = []
    for i, (v, w) in enumerate(zip(vals, widths)):
        url = first_url if i == 0 else None
        out.append(fmt(v, w, url if url is not None else cell_url(v, link)))
    return "│ " + " │ ".join(out) + " │"


def rule(left, mid, right, widths):
    return left + mid.join("─" * (w + 2) for w in widths) + right


def render(doc, link=True):
    """The whole box as one string. Pure apart from `jump_url`'s resolution.

    `doc["urls"]`, when present, is a list parallel to `rows` carrying each
    row's session-id prefix (or `None` for a group header, which is not a
    session). It is optional so every existing producer keeps working: without
    it the Session cell falls back to the `[sid8]` whole-cell rule.
    """
    hdr = doc["header"]
    rows = doc["rows"]
    widths = doc.get("widths") or [20] * len(hdr)
    sids = doc.get("urls") or []
    out = [rule("┌", "┬", "┐", widths),
           # The header is labels, never session references — never linked.
           line(hdr, widths, False),
           rule("├", "┼", "┤", widths)]
    for i, r in enumerate(rows):
        sid = sids[i] if i < len(sids) else None
        out.append(line(r, widths, link, jump_url(sid) if (link and sid) else None))
    out.append(rule("└", "┴", "┘", widths))
    return "\n".join(out)


def main():
    print(render(json.load(sys.stdin), link=links_enabled()))
    return 0


if __name__ == "__main__":
    sys.exit(main())
