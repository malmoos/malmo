# Implementation progress

ADR-style entries — one per unit of work. Each records **what was done** and
**what's next**, so the history of the build is legible without reading every
commit. Entries are named by kebab-slug (`<slug>.md`) and are **not numbered** —
sequential numbering was dropped because parallel branches kept colliding on the
next number.

Because the filename no longer carries order, **the [Index](#index) below is the
only record of build order** — entries are listed oldest-first and a new entry is
**appended to the bottom**, never inserted mid-list. Keep that order; it's the
chronological spine the old `NNNN-` prefix used to provide.

Every change ships with a progress entry or an update to one (see
[`../../CLAUDE.md`](../../CLAUDE.md) # Documentation discipline).

## Up next

The implementation slice queue, ordered. Each item links back to the progress entry whose "what's next" it satisfies. Pull the top item; when it lands, write the next entry and re-order. Design topics (not implementation slices) live in [`../specs/NEXT.md`](../specs/NEXT.md).

This is the **maintainer's critical-path** queue. Work carved off for **parallel contributors** lives in [GitHub Issues](https://github.com/onel/malmo/issues) (some items there are pulled from these "what's next" follow-ups). The two are kept from overlapping on purpose. See [`../dev/contributing.md`](../dev/contributing.md) for the contributor loop.

1. **GPU + device capacity enforcement.** `install-permissions-enforcement.md` deferred `gpu` enforcement and device-existence validation (both need a host hardware-introspection endpoint). A 422 from the brain will surface correctly in the UI via the existing `dialogError` path ([install-consent-ui.md](install-consent-ui.md)) once the host endpoint lands. See `NEXT.md` # GPU.

## Entry template

```markdown
# <title>

- **Status:** done | in progress
- **Date:** YYYY-MM-DD
- **Specs touched:** docs/specs/X.md, …

## What was done
…

## How it maps to the specs
Which locked decisions this exercises / realizes.

## Known gaps & deviations
Honest list of what's stubbed, faked, or diverges from spec (with why).

## What's next
Ordered follow-ups. Update as they land.
```

## Index

Oldest first; append new entries to the bottom.

| Title | Status |
|-------|--------|
| [walking-skeleton.md](walking-skeleton.md) — Walking skeleton — install an app end-to-end | done |
| [reconcile-and-health-wait.md](reconcile-and-health-wait.md) — Startup reconcile + health-wait & splash flip | done |
| [door-2-and-admission.md](door-2-and-admission.md) — Door-2 custom apps + admission policy | done |
| [image-digest-pinning.md](image-digest-pinning.md) — Image digest pinning (TOFU + catalog verify) | done |
| [brain-test-pyramid.md](brain-test-pyramid.md) — Brain test pyramid: DockerDriver refactor + Layers 1–3 | done |
| [auth-and-users.md](auth-and-users.md) — Auth + initial user model (setup, login, sessions, middleware, UI router) | done |
| [audit-events.md](audit-events.md) — Audit events (append-only table, Record(), client IP, call sites, GET /api/v1/audit) | done |
| [user-crud.md](user-crud.md) — User CRUD (admin list/create/patch-role/delete/reset-password + self-service password change) | done |
| [recovery-redemption.md](recovery-redemption.md) — Recovery-code redemption (`POST /api/v1/recover`) | done |
| [session-expiry-elevation.md](session-expiry-elevation.md) — Session expiry (idle + hard cap) + 5-minute elevation window | done |
| [host-agent-pam-verify.md](host-agent-pam-verify.md) — Real PAM-based password verification in host-agent-real | done |
| [avahi-dbus-publisher.md](avahi-dbus-publisher.md) — Avahi DBus publisher — per-app A records via EntryGroup.AddAddress | done |
| [caddy-routing-verified.md](caddy-routing-verified.md) — Caddy subdomain routing verified (Host-header routing end-to-end, path-based confirmed absent) | done |
| [host-agent-set-password.md](host-agent-set-password.md) — Real set-password in host-agent-real (useradd + chpasswd; /etc/shadow is now the source of truth) | done |
| [host-agent-set-role.md](host-agent-set-role.md) — Real set-role in host-agent-real (gpasswd) + brain bootstrap wires SetRole into /setup and createUser | done |
| [host-agent-delete-user.md](host-agent-delete-user.md) — Real delete-user in host-agent-real (userdel -r -f) + close orphan-on-rollback gap in /setup and createUser | done |
| [nspawn-usermgr-lane.md](nspawn-usermgr-lane.md) — nspawn fast-lane wiring for usermgrtest (bootstrap.sh + run-usermgr-tests.sh + make test-usermgr-nspawn) | done |
| [boot-pipeline-units.md](boot-pipeline-units.md) — Boot pipeline: storage-ready target, malmo-storage-verify reporter, brain health registry + `GET /api/v1/health` | done |
| [nspawn-boot-chain-lane.md](nspawn-boot-chain-lane.md) — nspawn boot-chain fast lane: `--boot` of `dist/systemd/` units + dependency-shape assertions | done |
| [qemu-medium-lane-scaffolding.md](qemu-medium-lane-scaffolding.md) — QEMU+swtpm medium-lane scaffolding: real kernel + real systemd + TPM plumbing | done |
| [health-persistence.md](health-persistence.md) — SQLite persistence for health issues (`health_issues` table, store write-through, boot-time restore) | done |
| [luks-tpm-enrollment.md](luks-tpm-enrollment.md) — LUKS root + first-boot TPM enrollment + PCR-7 unseal verification | done |
| [per-issue-health-audit.md](per-issue-health-audit.md) — Per-issue health audit records (`ApplyStorageFindings` returns affected keys; one `health.issue.*` record per issue) | done |
| [owner-scoped-instances.md](owner-scoped-instances.md) — Owner-scoped app instances (owner_user_id + scope, `<slug>--<user>` derivation, install authorization, warn-don't-block) | done |
| [dashboard-chassis-home-grid.md](dashboard-chassis-home-grid.md) — Dashboard frontend: stack chassis (Router/Pinia/Tailwind 4/reka-ui/lucide) + grouped Household/Yours home grid + four-item dock | done |
| [health-notifications.md](health-notifications.md) — Health raise/clear → dashboard notifications (`notifications` table + `internal/notify` emitter; coalesced, admin-routed, resolved-on-clear) | done |
| [notification-read-surface.md](notification-read-surface.md) — Notification read surface: `/api/v1/notifications` family + `notification_reads` per-recipient read/dismiss + `notification.created`/`.updated` SSE kinds | done |
| [notification-web-ui.md](notification-web-ui.md) — Notification Web UI: dashboard bell + unread badge + dropdown inbox (`useNotifications()`, SSE-invalidated TanStack Query, plain CSS) | done |
| [notification-clears-transparency.md](notification-clears-transparency.md) — Notification clears + member transparency variant (`members` audience, info-only member notice, "all clear" on resolve, flap retraction) | done |
| [notification-category-mute.md](notification-category-mute.md) — Per-category notification mute (`notification_mutes` table, read-time filter on list/count/mark-all, `GET`/`PUT`/`DELETE` mute API) | done |
| [install-permissions-folders-schema.md](install-permissions-folders-schema.md) — Parse permission fields (`folders`/`devices`/`gpu`) + collapse `user_folders`/`shared_folders` into `folders` with installer-elected source (slice 1 of install consent flow) | done |
| [host-agent-resolve-home.md](host-agent-resolve-home.md) — host-agent `GET /v1/users/{username}/home` → `{home_path, uid, gid}` (slice 2 of install consent flow) | done |
| [install-plan-endpoint.md](install-plan-endpoint.md) — `GET /api/v1/catalog/{id}/install-plan`: role-derived scope options + per-folder source menus for the install consent screen (slice 3 of install consent flow) | done |
| [host-agent-well-known-identity.md](host-agent-well-known-identity.md) — host-agent `GET /v1/identity/well-known` → `{malmo_app_uid, malmo_app_gid, malmo_shared_gid}` for household `user:`/shared `group_add` (slice 4a of install consent flow) | done |
| [install-permissions-enforcement.md](install-permissions-enforcement.md) — enforce folder/identity permissions in the override (`user:`, source-elected bind mounts, `group_add`, `devices`, `MALMO_FOLDER_*`) + authoritative election validation (slice 4 of install consent flow) | done |
| [install-consent-ui.md](install-consent-ui.md) — install consent + config UI in StoreView: InstallDialog (scope picker, permissions display, per-folder source/subfolder elections), 409 duplicate-install confirm flow, 422 inline error (slice 5 of install consent flow) | done |
| [single-label-app-local.md](single-label-app-local.md) — app LAN names go single-label `<slug>.local` (was `<slug>.malmo.local`, which Linux nss-mdns rejects) + Avahi collision fallback `<slug>-<box>.local`; brain trusts the published name for route + URL | done |
| [hostname-uniqueness-not-ownership.md](hostname-uniqueness-not-ownership.md) — Hostname encodes uniqueness, not ownership (first-come bare slug, `--<user>` only on collision) | done |
| [single-user-simplification.md](single-user-simplification.md) — Single-user simplification + split-button install (suppress household/personal UI when one user; scope moved from dialog to button) | done |
| [notification-retention-prune.md](notification-retention-prune.md) — Notification retention/pruning (`store.PruneNotifications`: 90-day age cap + 1000-row resolved-first ceiling, boot + hourly loop in brain) | done |
| [notification-mute-settings-ui.md](notification-mute-settings-ui.md) — Notification mute settings UI: per-category toggle list in Settings → Notifications (reka-ui Switch, optimistic `useNotificationMutes()` over the mute API) | done |
| [health-system-report.md](health-system-report.md) — Generalize health reporting: `GET /v1/health/system` + per-category `ApplyFindings`, with `service-down` as the first cross-category locus-B detector (two-axis category model, debounce) | done |
| [version-mismatch-detector.md](version-mismatch-detector.md) — Health detector: `version-mismatch` (locus C) — brain reconciles host-agent's reported `agent_version` against a brain-side expected version; raise on mismatch, clear on match, 1-shot | done |
| [brain-db-corrupt-detector.md](brain-db-corrupt-detector.md) — Health detector: `brain-db-corrupt` (locus C) — brain runs `PRAGMA integrity_check` at boot + every 6h, raises on a non-`ok` result, clears on `ok`; best-effort, non-blocking, never gates startup | done |
| [container-restart-loop-detector.md](container-restart-loop-detector.md) — container-restart-loop health detector (locus D): brain polls Docker `RestartCount` deltas over a 5-min window, raises per-`instance_id` past >3 restarts, clears on stabilize/uninstall | done |
| [system-live-sse.md](system-live-sse.md) — Live system-resources SSE (`GET /api/v1/system/live`): host-agent raw-counter `GET /v1/system/resources` + brain `internal/systemlive` ref-counted 1 Hz poller diffing counters into rates, all-users top-bar dropdown (`LiveResources.vue`) | done |
| [notification-error-toasts.md](notification-error-toasts.md) — Surface notification mutation failures as ephemeral error toasts (`toasts.ts` singleton + `ToastHost.vue`, `onError` on mute toggle + bell read-state mutations); error-only, no backend change | done |
| [sse-stream-cap.md](sse-stream-cap.md) — SSE per-session stream cap (`internal/api/streamCap` + `beginStream`): ≤16 concurrent SSE streams per session shared across `/api/v1/events` and `/api/v1/system/live`, excess → `429` | done |
| [manifest-lint-cli.md](manifest-lint-cli.md) — `malmo manifest lint <path>` CLI (`cmd/malmo`): parses + schema-validates a `manifest.yml` via `manifest.Parse` and confirms its sibling `compose_file` exists, parses, and declares `main_service` (exported `manifest.ComposeServiceNames`); author inner-loop tool backing the catalog CI schema-lint, no running brain needed | done |
| [openapi-codegen.md](openapi-codegen.md) — OpenAPI codegen pipeline: server-less spec emitter (`cmd/openapi-gen` → `api.OpenAPIDocument`, committed `api/openapi.{json,yaml}`) + `make openapi-check` freshness gate (CI), and a generated TS client (`openapi-typescript` → `web-ui/src/generated/openapi.ts`) replacing the hand-rolled `api.ts` wire interfaces; `openapi-fetch` client swap deferred | done |
