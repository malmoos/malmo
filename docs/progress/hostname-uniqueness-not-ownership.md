# Hostname encodes uniqueness, not ownership

- **Status:** done
- **Date:** 2026-05-31
- **Specs touched:** docs/specs/DASHBOARD.md (# instance naming rewritten), docs/specs/DECISIONS.md (new 2026-05-31 entry), docs/specs/DISCOVERY.md (# Per-app A records slug description), docs/specs/APP_LIFECYCLE.md (# slug derivation note), docs/specs/APP_ISOLATION.md (# Routing per instance, # Owner-scoped instances), docs/specs/NEXT.md (slug-cap item updated, resolved note updated)

## What was done

Changed the slug-allocation rule from "scope-determines-hostname" to "first-come, then disambiguate on collision." Previously: household always got the bare `<slug>`, personal always got `<slug>--<user>`. Now: the bare `<slug>` is first-come for any scope — the first instance installed wins the clean name. Collisions use `--<user>` for personal instances and a numeric suffix (`-2`, `-3`) for household instances.

The practical impact: a single-user box installing Immich as a personal instance now gets `immich.local`, not `immich--admin.local`.

### Code

- `internal/lifecycle/lifecycle.go` — `allocateSlug` now builds the candidate list as: bare `<slug>` (any scope), `<slug>--<user>` (personal only, on collision), `<slug>-2`, `<slug>-3`. The old code started personal instances at `<slug>--<user>`, bypassing the bare form entirely.

### Tests

- `internal/lifecycle/helpers_test.go` — three personal-scope test cases rewritten for first-come semantics: "personal scope gets the bare name first-come" (empty store → `immich`); "personal scope suffixes the owner on collision" (bare taken → `immich--alex`); "personal double collision falls back to numeric" (bare + owner-qualified taken → `immich-2`). All existing household-scope cases still pass unchanged.
- `make check` green.

## How it maps to the specs

Realizes the revised `DASHBOARD.md` # instance naming: "the bare slug is first-come, any scope." The separator rationale and flat-label constraints from `DECISIONS.md` 2026-05-29 are unchanged — only the trigger condition for the suffix changes.

## Known gaps & deviations

None. The API test seeds (`immich--alex` as a collision-shaped slug) remain valid and needed no change.

## What's next

- **Dashboard single-user simplification.** When the box has exactly one user, collapse the Household / Yours grouping into a flat list and suppress the scope picker at install (the distinction is meaningless with one user). This is the UI half of the same principle — decouple ownership-label display from the (now-rare) ownership-in-URL case. Track as a frontend slice in `docs/progress/README.md` # Up next or `NEXT.md`.

Builds on [single-label-app-local.md](single-label-app-local.md) (the single-label change that established `AppHostSuffix` and first-come fallback for LAN-namespace collisions).
