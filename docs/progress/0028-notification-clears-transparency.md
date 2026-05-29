# 0028 — Notification clears + member transparency variant

- **Status:** done
- **Date:** 2026-05-29
- **Specs touched:** docs/specs/NOTIFICATIONS.md

## What was done

Completed the brain-side emit path for the two notification follow-ups that the bell already knew how to render (slice [0027](0027-notification-web-ui.md)): the **member transparency variant** and the **"all clear" on resolve** (`NOTIFICATIONS.md` # Member transparency variant, # Clears). Backend-only — no web-ui change.

- **Member transparency variant** (`internal/notify/notify.go`). On a raise of a box-blocking storage drive issue (`data-drive-missing`, `data-drive-wrong`, `data-drive-readonly`), the notifier now emits — alongside the admin actionable notification — an **info-only, non-actionable** notice broadcast to members: summary "Saving is paused", body pointing at the admin, no fix link. Gated by a per-rule `memberTransparency` flag on the curated allowlist, **not** by the issue's `blocks_writes` flag: `canary-mismatch` / `mergerfs-assembly-failed` also block writes but stay admin-only (they are System/state plumbing, not the member-legible "your saving is paused" condition — matching the spec's allowlist table exactly).
- **New `members` audience** (`internal/notify`, `internal/store`, `internal/api`). Added `AudienceMembers` (+ `VariantTransparency`) — a class broadcast that is the mirror of `admins`: visible to every non-admin, to no admin (admins get the actionable copy). The store's single `notificationVisibilityClause` chokepoint now routes members to `'members'` rows; the API's per-id visibility guard (404-not-403) accepts a member acting on a `members` row. Per-recipient read/dismiss rides the existing `notification_reads` join unchanged. No schema migration — `audience` was already free `TEXT`.
- **"All clear" on resolve** (`internal/notify/notify.go`). On a health-issue clear the notifier now (a) resolves the problem notification (unchanged) **and** (b) emits a brief `info` "all clear" — to admins always (per-rule `clearSummary`), and to members for transparency issues. The all-clear is a distinct row keyed `<problem>:cleared`, so the original stays on the timeline marked resolved while the fresh all-clear re-pings the bell.
- **Flap handling.** A raise retracts (resolves) the paired `:cleared` all-clear, so a clear→raise flap doesn't leave a false "reconnected" notice next to the fresh problem. Idempotent no-op on a first raise.

Dedup-key layout per issue (up to four coalescing slots): `health:<id>[:inst]` (admin problem), `…:member` (member notice), `…:cleared` / `…:member:cleared` (the all-clears).

Tests: `internal/notify` gained focused coverage for the member notice, the non-transparency exclusion, both all-clears, the admin-only all-clear, and stale-all-clear retraction; `internal/store` and `internal/api` gained `members`-audience scoping tests (member sees it, admin doesn't, member can act, admin gets 404). Pre-existing notify/cmd-brain tests whose counts shifted were updated. Full suite green with `-race` (excluding the PAM-cgo `host-agent`/`pamverifier` packages, which don't build in the sandbox).

## How it maps to the specs

- `NOTIFICATIONS.md` # Member transparency variant — realized: info-only, non-actionable, broadcast to members; actionable copy to admins; curated allowlist gate; clears with the issue.
- `NOTIFICATIONS.md` # Clears — realized: resolve-and-emit-all-clear; original kept resolved, not deleted ("the timeline stays honest").
- `NOTIFICATIONS.md` # Routing — `members` added as the transparency broadcast audience (reconciled the model's `audience` enum and the Recipients list in the spec, same change).
- Reuses the locked per-recipient read-state model (`notification_reads`) and the coalescing/dedup invariant from slices [0025](0025-health-notifications.md)/[0026](0026-notification-read-surface.md) without changing them.

## Known gaps & deviations

- **Severity flattening.** The member notice and both all-clears are always `info`, regardless of the source issue's severity — deliberate (the spec treats the variant as informational; the member's experience is identical whatever the drive fault). The admin *problem* notification still copies the issue's severity verbatim.
- **Shared member copy.** All transparency issues share one "Saving is paused" message — correct while the allowlist is the three drive issues (all pause saving). When `disk-full` lands (its detector is deferred) the copy may want an upload-specific variant; revisit then.
- **`disk-full` not yet wired.** The spec lists it as the canonical transparency example, but it has no detector yet (not in `healthRules`); it inherits this behavior the moment its rule is added.
- **No producer change.** `cmd/brain`'s `emitHealthNotifications` is unchanged — the new behavior is entirely inside `Notifier.HealthRaised`/`HealthCleared`.

## What's next

- **Per-user, per-category mute** (`NOTIFICATIONS.md` # Configuration) — the last v1 notification-config item; needs a small prefs table + a filter in the list/count queries and a settings UI.
- **Retention / pruning** for `notifications` + `notification_reads` (`NEXT.md` # Observability) — capped count/age; the all-clear adds rows that should age out.
- Out of this slice's scope but in the same family: SMART/`disk-full` detectors (would light up the storage transparency path), update-outcome and security-audit notification sources (`NOTIFICATIONS.md` # The notification list).
