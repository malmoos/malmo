# 0025 — Owner-scoped app instances

- **Status:** done
- **Date:** 2026-05-29
- **Specs touched:** `DASHBOARD.md`, `BRAIN_UI_PROTOCOL.md`, `APP_MANIFEST.md`, `FIRST_RUN.md`

The load-bearing backend slice behind `DASHBOARD.md`'s "instances are owner-scoped" model — the first piece of dashboard implementation. Every app instance now has an **owner** and a **scope** (household vs. personal), the install API enforces who may create which, the slug is derived `<slug>--<user>` for personal instances, and read endpoints are scoped per caller. No UI in this slice; that's the next step on the dashboard build (`docs/README.md` # Frontend).

## What was done

### Data model (`internal/store`)

- `store.Instance` gains `OwnerUserID` and `Scope` (`ScopeHousehold` / `ScopePersonal` constants). The `instances` table gets `owner_user_id` and `scope` columns (fresh DBs via `CREATE TABLE`, existing dev DBs via idempotent `ALTER TABLE` guarded by a new `hasColumn` probe). Legacy rows backfill to the bootstrap admin and `scope='household'`.
- New scoped reads: `ListVisibleTo(userID, isAdmin)` (all household + own personal; admins see all) and `InstancesByManifest(manifestID)` (for duplicate detection). `Create`, `scan`, and the `Get`/`List` queries thread the two new columns through a shared `instanceColumns` constant.
- `owner_user_id` is a plain reference, **not** a foreign key — user-deletion-vs-owned-instances is unspecced (`NEXT.md`), so this slice deliberately doesn't couple `deleteUser` to ownership.

### Slug derivation (`internal/lifecycle`)

- `allocateSlug` is owner-aware: household instances take the bare slug (`immich`, `immich-2`, …); personal instances take `<slug>--<user>` (`immich--alex`, `immich--alex-2` for the same user installing twice).
- `Install` / `InstallCustom` / the shared `install` take a new `Owner{UserID, Username}` and `scope`, persisted onto the instance row.

### API (`internal/api`)

- `POST /apps` and `/apps/custom` accept `scope`; `resolveOwnerScope` applies the authorization table — members are forced to `personal` (explicit `household` → 403), admins choose with `household` as the omitted-default. Owner is always the calling user.
- **Warn-don't-block** (`checkDuplicate`): an install whose manifest already has a caller-visible instance and no `confirm: true` returns `409 duplicate-install` with per-copy summaries; a confirmed retry skips the check. Other members' personal instances are invisible to the check (and to `GET /apps`).
- `GET /apps` uses `ListVisibleTo`; `GET /apps/:id` 404s (not 403s) an instance the caller can't see (leak guard). `InstanceDTO` exposes `owner_user_id`, `owner_username`, `scope`. Uninstall mirrors the authorization: members can only uninstall their own personal instances; household uninstall is admin-only.
- Failure paths audit symmetrically (member-household 403, duplicate 409) per the elevation-class rule in `CLAUDE.md`.

### Validation

- Usernames (`createUser`, `setup`) reject `--` and an `xn--` prefix; manifest slugs (`manifest.validate`) require strict kebab-case (lowercase alphanumerics, single internal hyphens — which rejects `--`, `xn--`, and leading/trailing hyphens that would otherwise produce a malformed `<slug>--<user>`). So `<slug>--<user>` always parses back into slug + user. Documented in `FIRST_RUN.md` (usernames) and `APP_MANIFEST.md` (slugs), making good on `DASHBOARD.md`'s claim that these docs carry the constraint.

### Tests

- store: owner/scope round-trip via the shared fixture; existing suites updated.
- lifecycle: personal-scope slug derivation + same-owner-twice fallback.
- api (`instances_test.go`): member-household 403, `resolveOwnerScope` table (admin/member × scope), duplicate warn→confirm, cross-member privacy, `ListVisibleTo` scoping over HTTP, and the `GET /apps/:id` leak guard. Direct-method tests use a new `harness.srvServer()` accessor + `auth.WithIdentity`.

## How it maps to the specs

- Realizes `DASHBOARD.md` # the apps model (owner-scoped instances), # install authorization (the actor table), # instance naming (`<slug>--<user>`, `--` separator, `xn--` reservation), and # warn, don't block.
- Pins the install/warn wire shape and the owner-scoped DTO/read-scoping in `BRAIN_UI_PROTOCOL.md` # Pattern B.
- Reuses the per-instance compose-project machinery already locked in `APP_LIFECYCLE.md` (slug-derivation note there already matched).

## Known gaps & deviations

- **Per-app member grants are not built.** All household instances are visible to (and openable by) every member, per the product call to defer per-user sharing (`DASHBOARD.md` says "permitted to open"; the grant mechanism is the open gap). Tighten when grants land.
- **`owner_user_id` has no FK** (see above) — deleting a user leaves their owned instances orphaned with a dangling owner id; that lifecycle is unspecced and out of scope here.
- **Migrated DBs lack the `scope` CHECK constraint** — SQLite can't add a CHECK via `ALTER TABLE`, so only fresh DBs carry it. `store.Create` enforces the invariant in Go for both paths, so no code path can persist an out-of-range scope; a direct SQL write still could.
- **Happy-path install isn't covered by an api-level test** — the api harness builds the server with `life=nil`, so only the pre-job authorization/duplicate logic is exercised over HTTP; the install transaction itself is covered in `internal/lifecycle`.
- `host-agent`'s `internal/hostagent/pamverifier` fails to build in this environment (CGO `C.RTLD_NEXT`); pre-existing and unrelated to this slice.

## What's next

- **Frontend stack chassis + home grid** — bring `web-ui` up to `WEB_UI.md` (Router/Pinia/Tailwind/shadcn) and render the grouped Household / Yours grid against the now owner-scoped `GET /apps`. See `docs/README.md` # Frontend.
- **Per-app member grant mechanism** — needs a spec home (likely `AUTH.md` + the instance model) before the "permitted to open" subset can be enforced.
- **User-deletion vs. owned instances** — decide the lifecycle (reassign, block, or cascade) and add the FK or guard then. Today an admin can *see* an orphaned personal instance (the admin fast-path in `canSee`) but there's no API to reassign or adopt it — the next person to touch user deletion inherits both the orphan and the missing recovery path.
