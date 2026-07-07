# Oatmeal token sweep — remaining component `<style>` blocks + raw hex onto the olive tokens

- **Status:** done
- **Date:** 2026-07-02
- **Specs touched:** docs/specs/WEB_UI.md (# Styling — the status-token set)
- **Closes:** #262
- **Stacked on:** #261 (`oatmeal-element-idioms.md`) → #260 (`oatmeal-theme.md`). This branch builds on `feat/261-oatmeal-idioms`; its PR targets that branch, which in turn targets `feat/260-oatmeal-theme`.

## What was done

The #260 token remap recolored the ~1,000 utility-class references from one `@theme` block, but a handful of components still carried **hand-rolled `<style>` colors** (blues, ambers, greens, grays as raw hex) that never picked up the theme. This slice sweeps those stragglers onto the olive semantic tokens so nothing renders off-palette.

- **Two muted status tokens added to `@theme`** (`web-ui/src/style.css`): `--color-warning` (`oklch(54% 0.13 70)`, ochre-amber) and `--color-info` (`oklch(52% 0.12 245)`, muted blue), in the same retuned-OKLCH register as the existing `--color-destructive` / `--color-success`. This realizes #260's foreshadowed "dedicated warning token if more amber-severity surfaces appear" — the notification inbox and health surfaces *are* those surfaces. The four status tokens now back the full severity vocabulary (`NOTIFICATIONS.md` # Severities: info | warning | error | critical): **info → info, warning → warning, error/critical → destructive** (red reads as "bad" for both; the badge label carries the finer distinction), plus success for resolved/clear.
- **Seven component `<style>` blocks converted** to the semantic tokens:
  - `NotificationBell.vue` — chrome (bell button, inbox popover, rows) → `card`/`border`/`muted`/`muted-foreground`/`foreground`; severity dots → the status tokens; the unread-count badge → `destructive` with an `olive-50` glyph and a `background`-colored ring; link/action text → `accent`; the unread-row hue (previously a blue tint) → an olive `muted` resting tint, leaning on the bold summary + colored dot for the signal the palette can't carry in hue.
  - `LiveResources.vue` — same chrome mapping; the gauge track → `muted`, the **gauge fill → `accent`** (dark olive, matching cloud's monochrome bars, replacing the blue `#4dabf7`).
  - `ToastHost.vue` — error/success variants rebuilt as `color-mix(… <token> 12/35% , card)` tints with a solid-token accent stripe and text.
  - `HealthBanner.vue` — **not in the issue's named-seven** but carried raw hex (the original per-file inventory had been produced by a `grep` that silently skipped it; a robust rescan caught it — covered by the issue's "and any others still carrying raw hex"). The degraded-mode bar's error/critical tints + badge → the `color-mix` + solid-token pattern, mapping error → warning, critical → destructive to match `HomeView`.
  - `HomeView.vue` — the inline health-issue `.health-dot` / `.health-sev` severity palette → the status tokens (same mapping as `NotificationBell`).
  - `AppDetailView.vue` — the markdown `:deep()` link/code/pre colors used `var(--accent, #2563eb)` / `var(--muted, #f1f3f5)` fallbacks whose token names **don't exist** (the real tokens are `--color-accent` / `--color-muted`), so the fallback hex was what actually rendered — markdown links were blue, code blocks gray. Fixed to the real tokens, so they now recolor with the theme.
  - `RecoverView.vue` — already token-clean from #260's auth pass; its `<style>` block needed no color change (listed in the issue for completeness).
- **Radius reconciled to `--radius`** where it had drifted: the popover containers and icon buttons (`NotificationBell` bell + inbox, `LiveResources` button + panel, `ToastHost` toast) moved from ad-hoc `8px`/`10px` to `var(--radius)`. Pills (badges, dots, gauge tracks) stay `999px` by intent.

## How it maps to the specs

- **WEB_UI.md # Styling** — updated: the status-token line now names all four (`destructive`/`warning`/`info`/`success`), retuned to muted OKLCH, keeping each severity distinguishable while staying olive-adjacent. Cross-references `NOTIFICATIONS.md` # Severities for the vocabulary.
- **NOTIFICATIONS.md # Severities / HEALTH.md** — the severity → color mapping honors the four-level vocabulary (health surfaces use warning|error|critical; notifications add info); no severity behavior changed, only its rendering.
- **DASHBOARD.md** (calm posture) — the near-white chrome becomes the warm olive canvas end-to-end; signal colors are muted rather than vivid, which reads calmer without losing the signal.

## Known gaps & deviations

- **`error` and `critical` share `destructive`.** They were distinct hex hues before (orange vs. red); in the monochrome-leaning palette both map to `destructive`, with the severity label (badge text / row) carrying the distinction. An `error`-specific token would restore the hue split if it proves needed — deferred, not built.
- **`info` blue is the one non-olive status hue.** Olive has no blue; a muted blue is the least-surprising "informational" color and matches the notification vocabulary. It's retuned into the muted register so it doesn't shout.
- **Exact OKLCH values are visually tunable.** `warning` was deepened from an initial `L=58%` to `L=54%` so white badge text and banner body text clear contrast (the old amber `#f59f00` at ~L72% was *worse*); the values are eyeballed, not measured against a WCAG target. Documented so a later contrast pass can adjust the four tokens in one place.
- **Login glyph hues stay raw.** `Login.vue`'s six per-user identity colors (`GLYPHS`) remain literal hex — the one intentional non-olive accent, unchanged from #260 (AUTH.md user-list identity pattern). A repo-wide hex scan now returns *only* these six.
- **Drop-shadows stay `rgba(0,0,0,.12)`.** Neutral elevation shadows are not palette colors; left as-is.
- **Tooling note.** The environment's `grep`/`ugrep` silently omitted `HealthBanner.vue` from the initial hex inventory; the authoritative scan was done with a small Python pass. No repo change — flagged so a future sweep doesn't trust a single `grep`.
- **Visual verification.** The severity dots/badges, tinted toasts, degraded banner, and gauge bar were rendered headlessly (Chrome) against the **built** production CSS: warning reads as a legible ochre with white badge text, info a muted blue, error/critical muted red, the gauge fill dark olive on a light olive track, and the toast/banner tints sit inside the palette. `make check-web` (vue-tsc + build) green.

## What's next

1. **#290 — Home / Settings composition restructure** (already queued): brings those pages onto the theme's component shapes; will consume `<Button>`/`<Heading>` from #261 and these status tokens.
2. **A WCAG contrast pass** on the four status tokens if an accessibility review wants measured ratios — all four live in the one `@theme` block, so it's a single-file change.
3. **Dark mode** — unchanged from #260; the olive ramp + status tokens give a clean token-swap path, still a deferred downstream decision (WEB_UI.md # Open questions).
