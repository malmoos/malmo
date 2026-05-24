# Implementation progress

Numbered, ADR-style entries — one per unit of work. Each records **what was
done** and **what's next**, so the history of the build is legible without
reading every commit. New entries get the next number; never renumber.

Every change ships with a progress entry or an update to one (see
[`../../CLAUDE.md`](../../CLAUDE.md) # Documentation discipline).

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
| [0012](0012-host-agent-avahi-files.md) | Real Avahi service-file writes in host-agent-real | done |
