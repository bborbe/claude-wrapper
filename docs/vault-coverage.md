# Vault coverage — which vaults a daemon serves

The flagged tick can only open a row whose vault a running daemon serves. The
guard is **per vault family**: a daemon started with `-sweep-gate-vault` (or
`-vault`) for one vault admits every vault sharing that prefix, and drops rows
from another family at selection as `foreign-vault` (see `pkg/vault_guard.go`,
`pkg/flagged_tick.go`).

A vault in a family no daemon serves can never have a row opened, and the failure
is silent: every daemon reports healthy while the row stays dark for the whole
window.

## The served set (recorded 2026-11-24)

| Daemon | Named vault | Family served | Vaults covered |
|---|---|---|---|
| `de.bborbe.claude-wrapper` | `private-personal` | `private` | private-personal, private-boss, private-family, private-gaming, private-starcitizen, private-trading, private-agent |
| `de.bborbe.claude-wrapper-brogrammers` | `seibert-brogrammers` | `seibert` | seibert-brogrammers, seibert-agent, seibert-personal, seibert-data |
| `de.bborbe.claude-wrapper-soak` | — | — | not a serving daemon (runs `sample.sh once`) |

The `assistant` family (assistant-data, assistant-personal, assistant-brogrammers)
has no serving daemon. `assistant-data/24 Tasks` is empty and the other two have
no task folder, so the gap is latent — but nothing fails if a flagged row appears.

## The check

```bash
claude-wrapper -vault-coverage-check -served-vault-families=private,seibert
```

- exit `0` — every configured vault holding a flagged row is in a served family
- exit `1` — at least one such vault is not; each is printed as
  `UNSERVED vault=<name> family=<family> flagged_task=<path>`
- exit `2` — the check could not run (no served families named, or the vault
  config could not be read)

An empty `-served-vault-families` is refused rather than answered: a check told
nothing would pass on every vault and read as evidence the coverage was verified.

## Adding a vault

Add it to the table above and to the `-served-vault-families` value wherever the
check runs. A vault in a new family needs a daemon started for that family, or the
check will (correctly) fail the moment the vault holds a flagged row.
