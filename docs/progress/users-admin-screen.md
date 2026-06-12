# Users admin screen (Settings → Users) + login re-enable

- **Status:** done
- **Date:** 2026-06-04
- **Specs touched:** `USERS_AND_GROUPS.md` (# Elevation in the UI), `AUTH.md` (# Login screen UX), `DASHBOARD.md` (# global navigation)
- **Closes:** #10

The dashboard exposed no user-management surface — the only views were Login / Setup / Dashboard, and sign-in itself was stubbed out during the single-user dev phase. This slice lands the admin Settings → Users screen against the already-built user-CRUD endpoints (slice `user-crud.md`) and re-enables the login flow it depends on.

## What was done

### Settings → Users view (`web-ui/src/views/UsersView.vue`, new)

Admin-only (the view redirects members to `/settings`, mirroring `CustomInstallView`; the backend `requireAdmin` is the real fence). Consumes `GET/POST /users`, `PATCH /users/{id}` (role), `DELETE /users/{id}`, `POST /users/{id}/password`. List + create form + per-row role `<select>`, reset-password (inline expander), and delete.

- **Guard rejections surface inline.** The brain's 409s (`cannot demote the last admin`, `cannot delete yourself`, `username already exists`) reach the row as `ApiError.message` and render under the row. Controls are never hidden — the issue's explicit requirement.
- **Delete is confirmed.** Clicking Delete opens an inline "Delete X? This can't be undone." confirm strip; the mutation only fires on the second click.
- **Role `<select>` snaps back on rejection.** It's bound `:value="u.role"`; on a failed change the query is invalidated so the control reverts to the server's value instead of showing the rejected pick.

### Elevation flow (`web-ui/src/elevate.ts` + `web-ui/src/components/ElevateDialog.vue`, new)

The four user-mutation endpoints are elevation-gated server-side (`requireElevated`, `USERS_AND_GROUPS.md` # Elevation in the UI) — a fresh login session is **not** elevated, so without this every mutation would 403. `withElevation(fn)` runs the mutation, catches `elevation_required` (403), drives a password re-prompt (`POST /api/v1/auth/elevate`), and retries once. Within the brain's 5-minute window the prompt never reappears. `ElevateDialog` is mounted once in `AppShell`; a dismissed prompt throws `elevation_cancelled`, which the view maps to "no message" (a deliberate no-op, not an error).

### Login re-enabled (`web-ui/src/App.vue`, `web-ui/src/Login.vue`)

The dev-phase "session unavailable" placeholder is replaced by the real `Login` view. `Login.vue` is rebuilt as the user-list picker (`AUTH.md` # Login screen UX): names + deterministic colored letter glyphs fetched from a new public endpoint, click a name → password → submit. The blank-username-form toggle for stricter-posture boxes (`AUTH.md`) is not built.

### Public login-picker endpoint (`internal/api/auth.go`)

`GET /api/v1/auth/users` (added to `publicPaths`) returns `{id, username}` only — the minimal set the picker needs, no role/hash. Public per `AUTH.md` # Login screen UX (the household trust model: the boundary is "authenticated to malmo," not "who lives here"). Test `TestAuthUsersPublicPicker` asserts it's reachable with no session and that the payload carries no `role` field.

### Plumbing

`api.patch` added to the fetch wrapper; `web-ui/src/style.css` gains the shared `.auth` base styles (extracted from `Login.vue` so `Setup.vue` and the picker share them); `web-ui/src/generated/openapi.ts` and `api/openapi.{json,yaml}` regenerated for the new endpoint.

## Known gaps & deviations

- **Scope.** The issue scoped only "new Users view + route + `api.ts`," but a usable multi-user screen requires sign-in, which was stubbed. Re-enabling login (App/Login/`authUsers`) is therefore bundled here rather than split into its own issue. It is spec-faithful (`AUTH.md` # Login screen UX) but is a larger surface than the issue named — called out so the conflation is on the record.
- **No avatars / no stricter-posture login toggle.** Glyphs only; the blank-username form toggle (`AUTH.md`) is deferred.
- **Elevation prompt is per-mutation-retry, not pre-emptive.** The screen doesn't pre-elevate on entry; the first mutation triggers the prompt. Acceptable — it matches the sudo-in-UI "ask when you act" shape.
- **No E2E/browser test.** The view is covered only by `vue-tsc`; the elevation retry path and picker are not exercised by an automated browser test (the repo has no frontend test harness yet).

## What's next

- Browser/E2E coverage for the elevation retry and the login picker once a frontend test harness exists.
- The stricter-posture blank-username login form toggle (`AUTH.md` # Login screen UX).
