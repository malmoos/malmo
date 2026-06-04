# Activity view — audit-log browser (Settings → Activity)

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** `DASHBOARD.md` # global navigation, `SETTINGS.md` # panel inventory / role gating (both reconciled — Activity is member-reachable, not admin-only); `DECISIONS.md` 2026-06-05. Realizes `LOGGING.md` # Activity (audit log) + # Visibility rules — already specified, no edit needed.

Closes issue #11. A Settings → Activity view over the **already-built** `GET /api/v1/audit` — the user-meaningful event history. Pure frontend on a finished backend: no Go changes. Admins see the full box-wide feed; members see only their own events (the brain row-filters by role; the UI renders whatever it returns). Open to all signed-in users, unlike the sibling admin-only Users view.

## What was done

### Frontend

- `web-ui/src/views/ActivityView.vue` (new) — the browser. A table of newest-first events (When / Who / Action / Target / From / Result):
  - **Pagination** via `useInfiniteQuery`: `GET /audit?limit=50&after_id=<oldest id loaded>` walks backward into older rows; the "Load more" button shows while the last page came back full (`events.length === 50`), since the API has no `has_more` field by design. Loaded pages are flattened for rendering.
  - **Actor/target names:** `actor_user_id` → username via the admin-only `GET /users` (fetched only when `isAdmin`, since members only ever receive their own events, which `currentUser` already names). System events (`actor_role === "system"`, no actor) render as "System". A `user`-kind target resolves to a username when nameable, else the raw id.
  - **Action labels:** a map of the v1 vocabulary (`LOGGING.md` # v1 action vocabulary) to plain English ("Signed in", "App installed", …); an unmapped action degrades to its raw string so a newly added action never silently disappears.
  - **Time:** relative ("3m ago") with the absolute timestamp on hover (`title`), mirroring `NotificationBell`'s wording.
  - **Result:** a green "OK" / red "Failed" pill off `success`.
  - **Export** (CSV + JSON) of the currently-loaded rows via a Blob download (`LOGGING.md` # Activity: export-to-file is v1). CSV quotes cells containing `",\n`.
  - States: loading / error / empty.
- `web-ui/src/router.ts` — `/settings/activity` route (lazy), sibling to `/settings/users`.
- `web-ui/src/views/SettingsView.vue` — the Administration section is no longer admin-gated as a whole: it now shows an **Activity** link to everyone and keeps the **Users** link admin-only (`v-if`).
- `web-ui/src/api.ts` — `export type AuditEvent = Schemas["AuditEventDTO"]` (generated type, not hand-written).

### Spec reconciliation

`LOGGING.md` # Visibility rules already grants members their own audit events, and the shipped `listAudit` handler returns `200` to members (row-filtered), never `403`. But `DASHBOARD.md` # global navigation and `SETTINGS.md` had framed Activity as admin-surface ("a member's Settings is My account and nothing else"). Issue #11 (accepted) made the member-visible call explicit. Reconciled both IA docs to match LOGGING.md + the implementation, with a `DECISIONS.md` 2026-06-05 entry recording the delta. No behavior was widened — members could already query their own events; only the docs and the nav link caught up.

## How it maps to the specs

- `LOGGING.md` # Activity (audit log) — table view (timestamp, actor, action, target, source IP, success/failure), structured (non-free-text) presentation, export-to-file, system events admin-only (server-enforced).
- `LOGGING.md` # Visibility rules — admins see all; members see their own. Enforced in the brain; the UI does no row filtering.
- `BRAIN_UI_PROTOCOL.md` — consumes `GET /api/v1/audit?limit=&after_id=` (Pattern A); cursor pagination on the monotonic `id`.
- `DASHBOARD.md` / `SETTINGS.md` — Activity nests under Settings as a gated route (now: open to all, scoped server-side), not a top-level dock item.
- `WEB_UI.md` / CLAUDE.md (frontend) — TanStack Query for server state; generated OpenAPI type used directly (no duplicate wire type); `UsersView.vue` followed as the closest prior art (`useQuery`, `useAuth`, lazy route).

## Known gaps & deviations

- **Members see actor-only, not target, events.** `LOGGING.md` # Visibility rules says members see events where they are actor **or target**; the shipped `listAudit` filters by actor only (`f.ActorUserID = id.User.ID`). So "admin reset *my* password" (member is target, admin is actor) doesn't reach a member today. This is a pre-existing **backend** limitation, not introduced here — the UI renders whatever the API returns. Flagged for a brain-side follow-up (widen the member filter to actor-or-target).
- **Structured filtering (by actor / action / date range) is not built.** The issue's Done-when lists pagination + export, not filters; `LOGGING.md` lists structured filtering as part of the eventual view but free-text search is already deferred there. Left as a follow-up — the table + Load-more + export satisfy the issue.
- **Export scope is the loaded rows, not the entire history.** Export serialises what's on screen (including pages opened via Load more), per the issue's "serialises the current page or fetches all pages" — the lighter of the two. A full-history export (fetch-all-then-download) is a follow-up if it's wanted.
- **Relative-time helper duplicated** from `NotificationBell.vue` (7 lines). Kept local rather than extracting a shared util — extraction + rewiring the bell is scope creep for this issue; promote when a third consumer appears.
- **Section header label.** A member now sees an "Administration" section containing just the Activity link. The header reads slightly broad for a member's own history, but matches the issue's "add an Activity nav link in the Administration section." Renaming the header is a UX call left to the maintainer.

## What's next

- **Widen the member audit filter to actor-or-target** in the brain (`listAudit` / `store.AuditFilter`) so members see "admin acted on me" events, closing the gap above.
- **Structured filters** (actor / action type / date range) on the Activity view.
- **Full-history export** (fetch all pages) if the loaded-rows export proves too narrow.
- **SSH/SMB/sudo ingestion** rows will appear here automatically once that audit path lands (`LOGGING.md` # External auth ingestion) — they're just more `action` strings; add labels for them then.
