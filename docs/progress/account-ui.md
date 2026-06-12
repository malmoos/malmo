# Account UI — self-service password change + recovery-code redemption (frontend)

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** none (frontend wiring to already-built endpoints; AUTH.md unchanged)

Closes #13. This is the **frontend** for two flows whose brain endpoints already shipped: self-service password change (`POST /api/v1/me/password`, from [user-crud.md](user-crud.md)) and recovery-code redemption (`POST /api/v1/recover`, from [recovery-redemption.md](recovery-redemption.md)). It follows the elevation pattern established in [users-admin-screen.md](users-admin-screen.md) — and confirms that pattern is reusable as-is, with no new plumbing.

## What was done

Three account-facing surfaces, all UI:

1. **Self-service password change** — a "Change password" affordance in Settings → Account that expands an inline form (current + new password). On submit it calls the new `changeMyPassword()` helper. The brain verifies the current password via PAM (401 → "Incorrect password.") and revokes all of the user's sessions on success; the helper then forces a local `logout()`, so `App.vue`'s auth gate drops the user to the login screen to re-authenticate. **No elevation window** — supplying the current password *is* the verification step (AUTH.md # Password lifecycle), so this surface deliberately does not use `withElevation`.

2. **Recovery-code redemption** — a public `RecoverView.vue` (one form: username + recovery code + new password) reachable while logged out. On success the brain resets the password, consumes the old code, and returns a fresh one; the view shows it **exactly once** in the same `.recovery` monospace block first-run uses, with a copy button and an "I've saved this recovery code" checkbox that gates the "Continue to sign in" button, then redirects to the login screen. A 401 stays generic ("Recovery code incorrect.") so the screen never confirms whether a username exists.

3. **Elevation re-prompt** — confirmed already built and reusable. `elevate.ts` + `ElevateDialog.vue` + `withElevation()` (mounted once in `AppShell`, driven from `UsersView.vue`) need no changes; the password-change surface intentionally doesn't elevate, and recovery is public. Item #3 was a verification task, not new work.

### Routing the public recovery screen

`App.vue` — not the router — is malmo's auth gate: it picks Setup / AppShell / Login from auth state, and the `<router-view>` only mounts **inside** `AppShell` (i.e. once signed in). So a public route can't render through the router while logged out. The wiring:

- `router.ts` registers `/recover` so the catch-all (`/:pathMatch(.*)* → /`) doesn't redirect the path away and a deep-link/refresh keeps the URL.
- `App.vue` gains a reactive `useRoute()` and renders `RecoverView` directly in its logged-out branch (`v-else-if="route.path === '/recover'"`, before the `Login` fallback). The router is installed app-wide in `main.ts`, so navigation updates the reactive route even with no `router-view` mounted.
- `Login.vue` links to it with `<RouterLink to="/recover">Forgot password?</RouterLink>` on the password-entry form (the natural "stuck" moment).

### Files

- `web-ui/src/RecoverView.vue` (new) — recovery form + show-once code screen; mirrors `Setup.vue`'s two-phase shape and the plain-CSS `.auth` styles.
- `web-ui/src/auth.ts` — added `changeMyPassword()` and `redeemRecoveryCode()` next to `login`/`setup`/`logout`.
- `web-ui/src/App.vue` — route-aware logged-out branch renders `RecoverView`.
- `web-ui/src/router.ts` — `/recover` route.
- `web-ui/src/Login.vue` — "Forgot password?" link + `.forgot` style.
- `web-ui/src/views/SettingsView.vue` — Account-section password-change form.

## How it maps to the specs

- **AUTH.md # Password lifecycle # Setting a password** — self-service change requires the current password and forces re-auth (all sessions revoked). Realized.
- **AUTH.md # Using the recovery code** — "Forgot password" link → redeem → forced new password → fresh code shown once with the same "I have saved this" checkbox as first-run → land back at login. Realized.
- **AUTH.md # Login screen UX** — the user-list login screen now carries the recovery entry point.
- **USERS_AND_GROUPS.md # Elevation in the UI** — confirmed `withElevation` covers every destructive admin mutation; the two account surfaces here correctly fall outside it (one self-verifies, one is public).

## Known gaps & deviations

- **Helpers live in `auth.ts`, not `api.ts`** (issue #13's file table suggested `api.ts`). `login`/`setup`/`logout` already live in `auth.ts`, and `changeMyPassword` calls `logout()` — keeping the auth flows together matches the existing seam.
- **`RecoverView.vue` lives at `src/`, not `src/views/`** (issue suggested `views/`). Auth screens that render *outside* `AppShell` — `Login.vue`, `Setup.vue` — already sit at `src/` root; `views/` holds router-view children. RecoverView is an auth screen, so it follows that convention.
- **Recovery is one form, not two screens.** AUTH.md narrates "enter code → *then* a forced set-new-password screen," but the brain's `POST /api/v1/recover` takes username + code + new password in a single call, and issue #13 specifies a single three-field form. The UI matches the backend contract (one form), then shows the returned code. No behavior is lost.
- **Recovery-code input is plain text, no `XXXX-XXXX-XXXX-XXXX` mask.** The real code the brain issues is 24 hex chars (see `newRecoveryCode`, and what `Setup.vue` displays raw at first-run), not the 16-char masked form AUTH.md describes. A dash-mask would reject a pasted real code. This is a pre-existing spec-vs-backend gap, surfaced here, not introduced.
- **No recovery opt-out toggle.** AUTH.md mentions opting out of the regenerated code; the brain's `recover` always issues and returns a fresh code, so the UI always shows it. Adding a decline path needs a backend change — out of scope.
- **A logged-in user navigating to `/recover`** renders `RecoverView` inside the shell chrome (the route exists; AppShell wins the auth gate). Harmless and unreachable in normal use; not guarded to avoid scope creep.
- **No frontend test framework** exists in `web-ui/` (no vitest). Verified via the type-checked build (`vue-tsc --noEmit && vite build`) and manual flow reasoning; component tests are out of scope until a harness lands.

## Review fixes (PR #80)

Applied during code review, before merge:

- **Blocker — wrong current password no longer logs the user out.** `api.ts`'s global 401 handler fired `onUnauthenticated()` on *every* 401, clearing `currentUser`. But `POST /me/password` returns 401 when the *current* password is wrong while the caller is still authenticated, so a typo unmounted the Settings form (App.vue → Login) before the inline "Incorrect password." could render — the opposite of the specced behavior. Fix: `request`/`api.post` take a `RequestOpts.suppressAuthHandler` flag; `changeMyPassword` sets it, so a 401 here surfaces as an inline error and we drop to login only on success via the explicit `logout()`. The public `/recover` 401 was unaffected (RecoverView renders off `route.path`, not `currentUser`).
- **Spec reconciled (resolves the format + opt-out deviations above and the "reconcile" what's-next item).** `AUTH.md # The recovery code` / `# Using the recovery code` updated to match the shipped backend+UI: the recovery code is **24 hex chars shown raw** (no `XXXX-XXXX` mask), reissue on the recovery path is **mandatory** (no opt-out toggle — `POST /api/v1/recover` always returns a fresh code, and "recovery stays on" is the safer default for the target audience), and the inaccurate `argon2id` claim was dropped (the brain hashes the recovery code with bcrypt — `newRecoveryCode`). Spec moved to the backend, not vice versa.
- **Test harness tracked, not built.** `web-ui/` still has no JS test runner; the 401 regression that this PR fixed lived in an untested path. Standing up a Vue unit/component harness (and backfilling the `suppressAuthHandler` + recovery error-surface regressions) is recorded as a Tier-4 design topic in `NEXT.md` # Testing, gated on the repo's supply-chain / dependency-closure posture.

## What's next

- **Forced password change on first login** (AUTH.md # Setting a password: a new member with an admin-set temp password "is forced to change it before they can do anything else"). The self-service form exists now; the *forced* gate on first login is a separate, unbuilt flow.
- **Settings My-account / Box-settings split** (SETTINGS.md). Settings is still a flat list; the password form lives under the existing flat "Account" section. The split is its own slice.
- **Reconcile the recovery-code format + opt-out** between AUTH.md (16-char masked, opt-out path) and the backend (24 hex chars, always regenerates). Needs a maintainer decision on which side moves.
