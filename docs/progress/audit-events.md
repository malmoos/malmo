# 0007 — Audit events

- **Status:** done
- **Date:** 2026-05-23
- **Specs touched:** `LOGGING.md`, `NEXT.md`

The brain had session-authenticated endpoints but no audit trail. This slice
lands the append-only `audit_events` table, the single write function, client
IP plumbing through the auth middleware, call sites at all v1 action points,
and a paginated read endpoint with member-vs-admin visibility.

## What was done

### Store — `audit_events` table + triggers (`internal/store`)

New table in the existing `brain.db` migration (idempotent `CREATE TABLE IF NOT
EXISTS`):

- Schema exactly as `LOGGING.md` # Schema sketch: `id AUTOINCREMENT`, `ts`
  (epoch ms), `actor_user_id` nullable FK `ON DELETE SET NULL`, `actor_role`,
  `action`, `target_kind`, `target_id`, `source_ip`, `success`, `metadata`.
- `BEFORE UPDATE` trigger raises `'audit_events is append-only'` for all
  updates except the FK-cascade `SET NULL` on actor_user_id (SQLite fires
  `BEFORE UPDATE` for FK nullification; the trigger's `WHEN` clause passes
  through only that exact shape).
- `BEFORE DELETE` trigger raises unconditionally.
- `InsertAuditEvent` and `ListAuditEvents(AuditFilter)` on `*Store`.
  `ListAuditEvents` returns newest-first with an `after_id` cursor and an
  optional `ActorUserID` restriction (member view: actor or target).

Layer-2 tests (7 new): insert + list, UPDATE blocked, DELETE blocked,
`actor_user_id` SET NULL on user delete (history preserved), system event with
null actor, member visibility filter, cursor pagination.

### Audit package (`internal/audit`)

New package. Single concrete type `Recorder`; `EventStore` interface declared
here (consumer-side, per CLAUDE.md). Exported action constants for the full v1
vocabulary: `setup.complete`, `login.success`, `login.failure`, `logout`,
`app.install`, `app.uninstall`, `app.custom.create`.

`Record(ctx, action, target, metadata, success)` reads `auth.Identity` and
client IP from context; falls back to `actor_role='system'` when no identity is
present. INSERT failure is logged at Error level and swallowed — never
propagated.

`WithClientIP` / `ClientIPFromContext` — context helpers so the IP flows from
the middleware without leaking HTTP types into handlers.

Layer-2 tests (7 new): authenticated actor, system actor, target population,
metadata serialisation, INSERT failure doesn't panic, login.failure success=false,
IP context round-trip.

### Client IP plumbing (`internal/api`)

`authMiddleware` now calls `audit.WithClientIP(r.Context(), clientIP(r))` before
any other context mutation — both public and authenticated paths get the IP so
login.failure (which has no identity) still records a source IP.

`clientIP(r)` takes X-Forwarded-For first hop (Caddy sets this in production),
strips port from `RemoteAddr` as fallback.

1 new unit test: `TestClientIP` covering RemoteAddr, single XFF, multi-hop XFF,
IPv6.

### Call sites (`internal/api`)

`Server` gains `*audit.Recorder`; `NewServer` signature updated (one new
parameter, same position pattern as `authMgr`). `cmd/brain/main.go` constructs
`audit.New(st)` and passes it through.

Audit records written at:
- `setup` handler — `setup.complete` (identity constructed from the freshly
  created user + issued session; system actor until session is issued, but the
  handler calls Record after issuing the session).
- `login` handler — `login.success` (identity manually attached to ctx) and
  `login.failure` (no identity, system actor, captures `username` in metadata).
- `logout` handler — `logout` (only when a valid identity is present).
- `installApp` job — `app.install` with manifest_id + slug in metadata.
- `installCustomApp` job — `app.custom.create` with name + slug.
- `uninstallApp` job — `app.uninstall`.

Job goroutines capture the handler's `ctx` (which has the IP and identity) at
dispatch time, not the goroutine's `context.Background()`.

### GET /api/v1/audit (`internal/api`)

Huma-registered endpoint (shows up in `openapi.json`). Query params: `limit`
(default 50, cap 200), `after_id` (cursor). Admin sees all rows; member sees
rows where `actor_user_id = self` OR (`target_kind='user'` AND
`target_id=self.ID`). Returns `{events: [...]}` newest-first.

2 new Layer-3 tests: admin sees all rows after setup+login, unauthenticated GET
returns 401.

## How it maps to the specs

- **`LOGGING.md`** — `audit_events` table, append-only triggers, and write path
  are now realised. v1 action vocabulary pinned in the spec and as exported consts.
- **`AUTH.md`** — login/logout/setup audit entries land per the spec's call-out
  that `audit_events` rows should accompany these actions.

## Known gaps & deviations

- **`BEFORE UPDATE` trigger WHEN clause.** The spec says "RAISE(ABORT) —
  defends against buggy migrations." SQLite fires the trigger for FK-cascade
  SET NULL updates too, so a `WHEN` guard is required to let user deletes
  nullify the FK without tripping the trigger. This is correct behaviour (the
  intent is to prevent tampering, not to block the cascade); documented here
  so it isn't removed as "unnecessary complexity."
- **SSH / SMB / sudo ingestion deferred.** `ssh.login.*`, `smb.login.*`,
  `sudo.invoke`, `su.invoke` are not wired — requires the `journal_follow`
  host-agent protocol and a `pamparse` package per `LOGGING.md` # External
  auth ingestion.
- **Notifications fan-out not wired.** Audit rows don't yet fan out to the
  `notifications` table (`NOTIFICATIONS.md`).
- **No hash-chain / sequence-number integrity.** Append-only via triggers is
  the v1 invariant; cryptographic chain is deferred per `NEXT.md`.
- **No retention / prune.** Audit log is forever-retained in v1; prune
  mechanics are deferred.
- **No export-to-file.** The dashboard "Activity" CSV/JSON export is a UI
  follow-up.
- **Future call sites.** Users, shares, and tier2 packages don't exist yet;
  their audit entries are listed in `LOGGING.md` # Write path as future work.

## What's next

1. **Notifications fan-out** — emit to `notifications` table on the allowlisted
   subset of audit actions (`NOTIFICATIONS.md`).
2. **Multi-user CRUD** — add member, change role, delete user. Each of these
   needs an audit record.
3. **SSH / SMB ingestion** — `journal_follow` + `pamparse`.
4. **Activity UI** — dashboard "Activity" view consuming `GET /api/v1/audit`.
