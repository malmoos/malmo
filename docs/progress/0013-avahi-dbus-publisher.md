# 0013 — Avahi DBus publisher (replaces 0012 false start)

- **Status:** done
- **Date:** 2026-05-24
- **Specs touched:** `DISCOVERY.md` (§ "Per-app A records" rewritten), `DECISIONS.md` (new entry 2026-05-24)

## The 0012 false start

Slice 0012 wrote `/etc/avahi/services/app-<slug>.service` XML files as the per-app A-record mechanism. We tested this against a real Avahi daemon and it does not work: static service files announce *services*, not bare A-record aliases. The file loaded without error (Avahi logged "Service ... successfully established") but `avahi-resolve -n <slug>.malmo.local` timed out. Avahi will not publish a standalone A record for a `<host-name>` declared inside a service-group file — it owns only the service record. See `DECISIONS.md` entry 2026-05-24 for the full rationale.

0012 is deleted. Its `FilePublisher` implementation, test file, and progress doc are gone. The nspawn lane entry ("provision Avahi, write a service file, verify the A record resolves") is dropped — the premise was wrong.

## What shipped in 0013

### `internal/hostagent/avahipublisher/` — rewritten

**`publisher.go`** (`//go:build linux`) — `DBusPublisher`:

- Connects to the system bus (lazily on first `Publish` call).
- Detects the box's local IPv4 via `net.InterfaceAddrs()` (first non-loopback, non-link-local address; cached after first call).
- Per `Publish(slug)`: calls `EntryGroupNew` → `AddAddress(ifaceUnspec, protoUnspec, flagNone, hostname, ip)` → `Commit`. Stores the group path in `p.groups[slug]`. Returns the hostname and `ErrCollision` for name-collision DBus errors.
- Per `Unpublish(slug)`: calls `EntryGroup.Free` and removes the group from the map. Idempotent (missing slug = nil).
- `Close()`: frees all groups, closes the bus connection. Called on host-agent shutdown.
- `ErrCollision` sentinel (package-level `errors.New`) for callers to distinguish collision from other failures.

**`slug.go`** (no build tag) — `slugRE := regexp.MustCompile(^[a-z0-9-]+$)`. Shared across Linux and the non-Linux stub; the test file references it without a build tag.

**`dbus_other.go`** (`//go:build !linux`) — no-op stub. `DBusPublisher` and `ErrCollision` defined; all methods return "not supported on this OS". Allows `go build ./...` on macOS without errors. `cmd/host-agent-real` is Linux-only and is the only binary that instantiates `DBusPublisher`.

### Tests

**`helpers_test.go`** (no build tag) — runs everywhere:
- `TestSlugRE_RejectsInvalidSlugs` — path traversal, slashes, uppercase, dots, spaces, empty
- `TestSlugRE_AcceptsValidSlugs` — representative valid slugs
- `TestPublish_RejectsInvalidSlug` — invalid slugs return error before any DBus call
- `TestErrCollision_Sentinel` — `errors.Is` contract

**`dbus_linux_test.go`** (`//go:build linux && avahitest`) — skipped by default; run with `-tags avahitest` against a real avahi-daemon:
- Publish + Unpublish round-trip
- Unpublish idempotency (never-published slug)
- Double-Publish idempotency (old group freed, one group left)
- ErrCollision sentinel wrapping behavior

No DBus mocking. Rationale: mocking the Avahi server side is more code than the publisher itself and wrong in different ways. The nspawn CI lane is the right place for real Avahi coverage.

### Brain-side replay

The startup reconcile (`lifecycle.Reconcile`) already calls `m.host.Publish(ctx, inst.Slug)` for every running instance via `reassertRouting`. This is the replay hook: it fires on every brain startup (with or without host-agent having restarted). `reassertRouting` now returns a bool (Avahi success/failure) so `Reconcile` can emit a summary log line:

```
avahi replay  total=N  ok=M  failed=K
```

This covers both "brain restart while host-agent was running" and "both restart together."

### Binary wiring

- `cmd/host-agent-real/main.go` — instantiates `&avahipublisher.DBusPublisher{HostSuffix: ".malmo.local"}`, passes it to `hostagent.New`. Defers `pub.Close()` on shutdown.
- `cmd/host-agent/main.go` — unchanged; still uses `FakePublisher`.
- `internal/hostagent/agent.go` — unchanged; `Publisher` interface is unmodified.

### Dependency

`github.com/godbus/dbus/v5 v5.2.2`. Pure Go. MIT license. Added to `go.mod` / `go.sum`.

## How it maps to the specs

- `DISCOVERY.md` § "Per-app A records" — implementation is now accurate: DBus `EntryGroup.AddAddress`, not static files.
- `BRAIN_HOST_PROTOCOL.md` — wire shape unchanged; `publish` still returns `{name, state: "established"}` immediately after DBus Commit succeeds. No protocol change needed.
- CLAUDE.md "consumer-side interfaces" — `Publisher` interface stays in `internal/hostagent`; `DBusPublisher` is the provider.
- CLAUDE.md `log/slog` with standard fields — `slug`, `name`, `ip`, `err` used throughout.

## Known gaps (loud)

- **Mid-life host-agent restart while brain is running is not covered.** The brain does not detect that host-agent restarted mid-life (it only knows the agent is reachable). A future mitigation: poll `GET /v1/system/status` for `uptime_s` decreasing and replay on detection.
- **No collision-resolution UX.** `ErrCollision` is returned to the caller but the brain's install transaction only logs a warning — no slug rename or retry. Tracked in `NEXT.md`.
- **Local-IP detection assumes single primary IPv4.** First non-loopback non-link-local address wins. Multi-homed or dual-stack boxes may announce on the wrong interface. Future: prefer the NM primary connection's address.
- **DBus calls not real-tested in CI until the nspawn lane lands.** `go test ./...` does not run `avahitest`-tagged tests. Unit tests cover slug validation and the ErrCollision sentinel only.
- **`/etc/avahi/avahi-daemon.conf` interface scoping not written.** `DISCOVERY.md` § "Interface scoping" specifies host-agent computes the allow/deny-interfaces list from NetworkManager state at boot. Deferred.
- **The host record (`malmo.local`)** — published at boot time, not by the per-app reconciler. Deferred to boot-sequence integration.
- **`setPassword`, `setRole`, `deleteUser` still fake in `host-agent-real`.** Carried from 0011.

## What's next

- nspawn lane: provision avahi-daemon, run `-tags avahitest` tests, verify `avahi-resolve` output.
- Mid-life host-agent restart detection (uptime_s polling or DBus signal subscription).
- Real `setPassword` / `setRole` / `deleteUser` in `host-agent-real`.
- `/etc/avahi/avahi-daemon.conf` allow/deny-interface config from NM state.
