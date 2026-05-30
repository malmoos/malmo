# malmo docs

The map of all documentation. Three homes:

- **[`specs/`](specs/)** — design source of truth. What malmo *is* and the
  locked decisions behind it. Read the relevant spec end-to-end before changing
  behavior in that area.
- **[`progress/`](progress/)** — implementation log. ADR-style entries recording **what was done** and **what's next** for each unit of work.
- **[`dev/`](dev/)** — developer how-to. Running locally, code-level
  architecture, tooling.

Every change ships with documentation (see [`../CLAUDE.md`](../CLAUDE.md) #
Documentation discipline).

**New contributor?** Start with [`dev/contributing.md`](dev/contributing.md) — the
end-to-end loop (orient → pick a task → branch → build → test → document → PR).
Actionable parallel work lives in [GitHub Issues](https://github.com/onel/malmo/issues)
(`gh issue list --label P1`).

## Specs

`specs/` holds the design docs. [`specs/SPEC.md`](specs/SPEC.md) is the entry
point; the full annotated list (what each doc owns and its headline decisions)
is the **Documents** section of [`../CLAUDE.md`](../CLAUDE.md). Cross-references
inside the specs are bare filenames, relative to `specs/`.

Orientation:

- **Start here:** `SPEC.md`, `CONTROL_PLANE.md`.
- **Apps:** `APP_LIFECYCLE.md`, `APP_MANIFEST.md`, `APP_STORE.md`, `APP_ISOLATION.md`, `SERVICE_PROVISIONING.md`.
- **Protocols:** `BRAIN_UI_PROTOCOL.md`, `BRAIN_HOST_PROTOCOL.md`.
- **Frontend:** `WEB_UI.md` (stack/deploy), `DASHBOARD.md` (logged-in IA + the owner-scoped apps model).
- **System:** `STORAGE.md`, `BOOT.md`, `DISCOVERY.md`, `MALMO_NETWORK.md`, `TIME.md`, `USERS_AND_GROUPS.md`, `AUTH.md`.
- **Operations:** `UPDATES.md`, `RELEASE_MANIFEST.md`, `BUILD.md`, `TESTING.md`, `HEALTH.md`, `LOGGING.md`, `TELEMETRY.md`, `LOCAL_ANALYTICS.md`, `NOTIFICATIONS.md`, `FIRST_RUN.md`.
- **Cross-cutting:** `THREAT_MODEL.md`, `DECISIONS.md` (decision log), `NEXT.md` (open questions).

## Progress

See [`progress/README.md`](progress/README.md) for the full index and the
**Up next** queue (next implementation slices). Latest:

- [`notification-category-mute.md`](progress/notification-category-mute.md)
  — Per-category notification mute: `notification_mutes` table, read-time filter on list/count/mark-all, `GET`/`PUT`/`DELETE` mute API. Backend only — settings UI deferred.
- [`notification-clears-transparency.md`](progress/notification-clears-transparency.md)
  — Notification clears + member transparency variant: `members` audience, info-only member notice on box-blocking storage issues, "all clear" on resolve, flap retraction. Backend only.
- [`notification-web-ui.md`](progress/notification-web-ui.md)
  — Notification Web UI: dashboard bell + unread badge + dropdown inbox (`useNotifications()`, SSE-invalidated TanStack Query, plain CSS). Bell re-homed into `TopBar.vue`.
- [`notification-read-surface.md`](progress/notification-read-surface.md)
  — the read half of the bell: `/api/v1/notifications` family (audience-scoped
  list, unread-count, mark-read, mark-all-read, dismiss), the
  `notification_reads` per-recipient join, and `notification.created` /
  `notification.updated` SSE kinds. Backend only — Vue bell deferred.
- [`health-notifications.md`](progress/health-notifications.md)
  — first consumer of the notification seam: `notifications` SQLite table +
  `internal/notify` emitter. Health raise → admin-routed notification
  (coalesced by `dedup_key`); clear → resolved. Write seam only — bell API,
  SSE, and read-state deferred.
- [`dashboard-chassis-home-grid.md`](progress/dashboard-chassis-home-grid.md)
  — `web-ui` brought up to the `WEB_UI.md` stack (Vue Router, Pinia, Tailwind 4,
  reka-ui, lucide); the dev screen replaced with the `DASHBOARD.md` shell: the
  grouped Household / Yours home grid + four-item dock against the scoped
  `GET /apps`. First dashboard frontend slice.
- [`owner-scoped-instances.md`](progress/owner-scoped-instances.md)
  — `owner_user_id` + `scope` on instances, `<slug>--<user>` slug derivation,
  install authorization (member→personal, admin→choice), warn-don't-block
  duplicate installs, caller-scoped app reads. First dashboard backend slice.
- [`per-issue-health-audit.md`](progress/per-issue-health-audit.md)
  — `ApplyStorageFindings` returns affected `IssueKey`s; one per-issue
  `health.issue.raised`/`cleared` audit record (`target_kind: health_issue`)
  instead of a bulk count.
- [`health-persistence.md`](progress/health-persistence.md)
  — `health_issues` SQLite table, store write-through, boot-time
  `LoadFromStore` restore, `health.issue.raised/cleared` audit actions.
- [`qemu-medium-lane-scaffolding.md`](progress/qemu-medium-lane-scaffolding.md)
  — QEMU+swtpm medium-lane scaffolding (real kernel + real systemd + TPM
  plumbing); runway for the LUKS/TPM slice.
- [`nspawn-boot-chain-lane.md`](progress/nspawn-boot-chain-lane.md)
  — nspawn `--boot` of `dist/systemd/` units + dependency-shape assertions.
- [`boot-pipeline-units.md`](progress/boot-pipeline-units.md)
  — boot pipeline slice #1: `malmo-storage-ready.target`,
  `malmo-storage-verify` reporter, host-agent `GET /v1/health/storage`,
  brain `internal/health` registry, `GET /api/v1/health`. Userspace half of
  `BOOT.md`; initramfs/LUKS/TPM + boot-ordering tests are follow-ups.
- [`nspawn-usermgr-lane.md`](progress/nspawn-usermgr-lane.md)
  — nspawn fast-lane harness for usermgr integration tests against a real
  `/etc/passwd` rootfs.
- [`host-agent-pam-verify.md`](progress/host-agent-pam-verify.md) through [`host-agent-delete-user.md`](progress/host-agent-delete-user.md) + [`avahi-dbus-publisher.md`](progress/avahi-dbus-publisher.md) + [`caddy-routing-verified.md`](progress/caddy-routing-verified.md) — host-agent-real auth surface (PAM verify, set-password, set-role, delete-user) + Avahi DBus publisher + Caddy subdomain routing verified end-to-end.
- [`auth-and-users.md`](progress/auth-and-users.md)
  — first-admin bootstrap, password login, opaque cookie sessions, auth
  middleware gating all mutations.
- [`walking-skeleton.md`](progress/walking-skeleton.md) — first
  vertical slice: install/uninstall an app end-to-end through the real
  architecture spine.

## Dev guides

- [`dev/contributing.md`](dev/contributing.md) — the contributor loop: orient,
  pick a task from [GitHub Issues](https://github.com/onel/malmo/issues), branch
  off `main`, build, test, document, PR into `main`. Read this first if you're new.
- [`dev/running-locally.md`](dev/running-locally.md) — run the whole stack
  natively (no VM), and the two-loop dev model.
- [`dev/testing-brain.md`](dev/testing-brain.md) — six-layer test plan for
  `malmo-brain` (unit → store → lifecycle-with-fakes → API → integration
  → e2e). Companion to `specs/TESTING.md`, which covers boot-level lanes.
