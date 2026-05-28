# Implementation progress

Numbered, ADR-style entries — one per unit of work. Each records **what was
done** and **what's next**, so the history of the build is legible without
reading every commit. New entries get the next number; never renumber.

Every change ships with a progress entry or an update to one (see
[`../../CLAUDE.md`](../../CLAUDE.md) # Documentation discipline).

## Up next

The implementation slice queue, ordered. Each item links back to the progress entry whose "what's next" it satisfies. Pull the top item; when it lands, write the next numbered entry and re-order. Design topics (not implementation slices) live in [`../specs/NEXT.md`](../specs/NEXT.md).

This is the **maintainer's critical-path** queue. Work carved off for **parallel contributors** lives in [GitHub Issues](https://github.com/onel/malmo/issues) (some items there are pulled from these "what's next" follow-ups). The two are kept from overlapping on purpose. See [`../dev/contributing.md`](../dev/contributing.md) for the contributor loop.

1. **0023 — LUKS root + first-boot TPM enrollment + unseal verification.** Builds on [0021](0021-qemu-medium-lane-scaffolding.md)'s QEMU+swtpm medium-lane scaffolding. Critical-path slice: realizes the encryption posture in `STORAGE.md` and the boot ordering in `BOOT.md`, exercises the medium lane the scaffolding was built for. *(In-flight follow-up from 0021 / 0022.)*
2. **Per-issue health audit records.** Small finish on [0022](0022-health-persistence.md). Change `ApplyStorageFindings` to return affected IDs so `health.issue.raised` / `health.issue.cleared` audit records can target `{kind: "health_issue", id: "<id>"}` instead of bulk count records.
3. **Notification wiring: health raise/clear → dashboard bell.** `NOTIFICATIONS.md` is locked but no `notifications` table or emitter exists yet. First consumer of the seam: on health-issue raise (transition), enqueue a notification routed to admin users; on clear, mark resolved. Follow-up from [0022](0022-health-persistence.md).

## Entry template

```markdown
# NNNN — <title>

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

| # | Title | Status |
|---|-------|--------|
| [0001](0001-walking-skeleton.md) | Walking skeleton — install an app end-to-end | done |
| [0002](0002-reconcile-and-health-wait.md) | Startup reconcile + health-wait & splash flip | done |
| [0003](0003-door-2-and-admission.md) | Door-2 custom apps + admission policy | done |
| [0004](0004-image-digest-pinning.md) | Image digest pinning (TOFU + catalog verify) | done |
| [0005](0005-brain-test-pyramid.md) | Brain test pyramid: DockerDriver refactor + Layers 1–3 | done |
| [0006](0006-auth-and-users.md) | Auth + initial user model (setup, login, sessions, middleware, UI router) | done |
| [0007](0007-audit-events.md) | Audit events (append-only table, Record(), client IP, call sites, GET /api/v1/audit) | done |
| [0008](0008-user-crud.md) | User CRUD (admin list/create/patch-role/delete/reset-password + self-service password change) | done |
| [0009](0009-recovery-redemption.md) | Recovery-code redemption (`POST /api/v1/recover`) | done |
| [0010](0010-session-expiry-elevation.md) | Session expiry (idle + hard cap) + 5-minute elevation window | done |
| [0011](0011-host-agent-pam-verify.md) | Real PAM-based password verification in host-agent-real | done |
| [0013](0013-avahi-dbus-publisher.md) | Avahi DBus publisher — per-app A records via EntryGroup.AddAddress | done |
| [0014](0014-caddy-routing-verified.md) | Caddy subdomain routing verified (Host-header routing end-to-end, path-based confirmed absent) | done |
| [0015](0015-host-agent-set-password.md) | Real set-password in host-agent-real (useradd + chpasswd; /etc/shadow is now the source of truth) | done |
| [0016](0016-host-agent-set-role.md) | Real set-role in host-agent-real (gpasswd) + brain bootstrap wires SetRole into /setup and createUser | done |
| [0017](0017-host-agent-delete-user.md) | Real delete-user in host-agent-real (userdel -r -f) + close orphan-on-rollback gap in /setup and createUser | done |
| [0018](0018-nspawn-usermgr-lane.md) | nspawn fast-lane wiring for usermgrtest (bootstrap.sh + run-usermgr-tests.sh + make test-usermgr-nspawn) | done |
| [0019](0019-boot-pipeline-units.md) | Boot pipeline: storage-ready target, malmo-storage-verify reporter, brain health registry + `GET /api/v1/health` | done |
| [0020](0020-nspawn-boot-chain-lane.md) | nspawn boot-chain fast lane: `--boot` of `dist/systemd/` units + dependency-shape assertions | done |
| [0021](0021-qemu-medium-lane-scaffolding.md) | QEMU+swtpm medium-lane scaffolding: real kernel + real systemd + TPM plumbing | done |
| [0022](0022-health-persistence.md) | SQLite persistence for health issues (`health_issues` table, store write-through, boot-time restore) | done |
