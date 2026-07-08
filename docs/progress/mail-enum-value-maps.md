# Mail enum value maps — manifest-declared `mail.env`, brain-resolved tokens

- **Status:** done
- **Date:** 2026-07-08
- **Specs touched:** `APP_MANIFEST.md` (# D3 — new `mail.env` subsection), `SERVICE_PROVISIONING.md` (# BYO outgoing mail — enum-config paragraph), `docs/dev/catalog-import-gaps.md` (`mail-enable-enum — twenty` flipped to implemented), `docs/dev/capabilities.yml` (new id `mail-enable-enum`, version 1→2)

Closes #302, extending [byo-outgoing-mail.md](byo-outgoing-mail.md) (#122). BYO mail injects malmo-canonical `MALMO_MAIL_*` and relies on the app's compose to adapt them, but compose `environment:` interpolation can only substitute / default (`:-`) / test-presence (`:+`) — it cannot **remap one enum's tokens onto another's**. Two shipped apps are degraded purely by this: twenty's `EMAIL_DRIVER` (`logger|smtp`, boot-rejects the empty string the `${MALMO_MAIL_HOST:+smtp}` gate expands to on an unbound box) and vaultwarden's `SMTP_SECURITY` (`off|starttls|force_tls`, a value remap off malmo's `none|starttls|tls`). This generalizes the projection the brain already hand-computes for `MALMO_MAIL_USE_TLS`/`USE_SSL`/`DSN` (which `mail.go` does in Go *because* "compose can't derive these from the string") into a manifest-declared value map.

## What was done

### Manifest (`internal/manifest/manifest.go`)

- `Mail` gains `Env map[string]MailEnvMap`; new `MailEnvMap{From, Map}`. Each key is the app's own env-var name; `from` names a mail-state domain and `map` gives the app's token for each domain value.
- Two domains: `encryption` (`none|starttls|tls`, the bound provider's mode — treated as `none` when unbound) and the synthetic `bound` (`bound|unbound`). Domain names/values are exported constants (`MailFromEncryption`, `MailFromBound`, `MailBound`, `MailUnbound`) so lifecycle resolution references them, not string literals. The `encryption` domain values are kept as literals here (not imported from `store`) so the manifest layer stays free of a `store` dependency; a lifecycle test guards the two vocabularies against drift.
- `validateMail` extended: each env name is validated exactly like a config `app_env` (reuses `configEnvName` + the `MALMO_` / `reservedConfigEnv` guards — the token lands in `.env` under the app's own name, so it can't be allowed to clobber the `MALMO_` family or a loader var), `from` must be a known domain, and `map` must cover that domain **exactly** (right count + every key present + non-empty token) so resolution never hits an undeclared token.

### Lifecycle (`internal/lifecycle/mail.go`, `lifecycle.go`)

- New `mailAppEnvLines(mail, bound)` resolves each declared var to its token for the current state and renders `APP_VAR=token`, keys sorted for a byte-stable `.env`. `bound == nil` (unbound) resolves the `none`/`unbound` tokens. Validation guarantees the map covers the domain, so the lookup can't miss.
- **Emitted in both states** — the load-bearing change. Unlike `MALMO_MAIL_*` (bound-only), the declared vars are stamped whether or not a provider is bound, so an enum-driver app is present-and-valid unbound instead of boot-rejecting on an empty value. `writeEnv` (install) and `rewriteEnvMail` (rebind) both take `man.Mail` (already in scope at each call site — no extra manifest read) and append `mailAppEnvLines` after the `MALMO_MAIL_*` block.
- `rewriteEnvMail` now strips the declared vars **by name** (not just the `MALMO_MAIL_` prefix) before re-resolving, so a rebind — including an unbind, which drops `MALMO_MAIL_*` but keeps the enum vars with their unbound tokens — re-stamps cleanly with no duplicate lines.

## Verification

- `internal/manifest`: `mail.env` maps parse for both domains; `validateMail` rejects unknown `from`, an incomplete domain, an extra key, a misspelled key at the right count, an empty token, a `MALMO_`-prefixed name, a lowercase name, and a reserved runtime name (`PATH`).
- `internal/lifecycle`: install unbound stamps `EMAIL_DRIVER=logger` + `SMTP_SECURITY=off` and no `MALMO_MAIL_*`; install bound stamps `smtp`/`force_tls` alongside the `MALMO_MAIL_*` family; rebind bound→unbound→bound re-stamps the right tokens each way with exactly one line per var (no duplicates); a unit test drives `mailAppEnvLines` across all three encryption modes (also guarding the manifest-literal ↔ `store.MailEncryption*` alignment) and asserts an empty `mail.env` yields no lines (existing mail-only apps unaffected).
- Coverage on new code: `mailAppEnvLines` 100%, `validateMail` 100%; the mail branches added to `writeEnv`/`rewriteEnvMail` are all exercised (their residual uncovered lines are pre-existing `os` error paths).
- `make check` green.

## Known gaps & deviations

- **Catalog/store manifest wiring is a coordinated follow-up, not in this repo.** The `catalog/` tree was cut over to the store repo (`07aac3a`), so `catalog/twenty` (`EMAIL_DRIVER`) and the store repo's `apps/vaultwarden` (`SMTP_SECURITY`, its `outbound-mail-enum-mismatch` limitation) — plus the issue's live send checks for both — ship separately against this brain mechanism. The ledger entry is flipped to `implemented` per the "mechanism shipped" convention and names both apps for the revisit.
- **Reserved-name validation on `mail.env` keys is slightly beyond the issue's literal list** (it named only the domain-coverage + unknown-`from` rules). Kept deliberately: these vars land in the same `.env` as `MALMO_MAIL_*`, so a `MALMO_`-prefixed or loader-var key could clobber a brain-owned or process-critical value; the guard reuses the existing config `app_env` validator, matching the issue's "same direct-stamp convention as `config:`" framing.
- **Compose still needs a pass-through line** (`EMAIL_DRIVER: ${EMAIL_DRIVER}`) in the app's compose — the remap happens in Go, the compose just forwards the resolved value. That line is part of the store-repo manifest work above.

## What's next

- Wire `catalog/twenty` (`EMAIL_DRIVER` via `from: bound`) and the store repo's `apps/vaultwarden` (`SMTP_SECURITY` via `from: encryption`), flip vaultwarden's `outbound-mail-enum-mismatch` and twenty's row toward `full` after a live send check.
- The `capabilities.yml` id `mail-enable-enum` now fires the mechanical re-screen of any app that recorded a wait on that gap-class (`CAPABILITIES.md`).
