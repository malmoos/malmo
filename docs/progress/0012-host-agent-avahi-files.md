# 0012 — Real Avahi service-file writes in host-agent-real

- **Status:** done
- **Date:** 2026-05-24
- **Specs touched:** `DISCOVERY.md` (# Per-app A records — acknowledges dummy `<service>` element and `_malmo-app._tcp`)

Closes Tier-B point 6 of the roadmap (next slice after 0011 PAM verify). Narrow slice: only `publish` and `unpublish` become real (write/remove `/etc/avahi/services/app-<slug>.service`). All auth ops stay fake in `host-agent-real`.

## What was done

### `internal/hostagent/avahipublisher/` — new package

Pure Go (no CGO). Implements `FilePublisher` with two methods:

```go
func (p *FilePublisher) Publish(slug string) (name string, err error)
func (p *FilePublisher) Unpublish(slug string) error
```

`Publish` validates the slug against `^[a-z0-9-]+$` (defensive path-injection guard), then calls `os.WriteFile` with mode `0644`. Returns the published hostname (`slug + HostSuffix`).

`Unpublish` calls `os.Remove`; treats `os.IsNotExist` as success (idempotent).

### `_malmo-app._tcp` dummy service type

Avahi's static-file schema requires at least one `<service>` element to publish a `<host-name>` A record. We use the project-specific type `_malmo-app._tcp` rather than `_http._tcp` for a concrete reason: Finder, iOS Files, and Bonjour-aware apps browse `_http._tcp` records and surface them in "Network" sidebars. `DISCOVERY.md` §3 explicitly says "For individual apps: not in v1. We do not publish per-app `_http._tcp` service records. The browse-the-network experience for apps is the dashboard, not the OS file manager." Using a project-specific type achieves the A-record publication without creating the cosmetic noise.

### File approach vs DBus

Static files were chosen over the `avahi-publish` DBus `EntryGroup` API. `DISCOVERY.md` line 38: "The static-file approach is preferred over `avahi-publish` (which runs as a long-lived process per name) because Avahi already handles re-announcement, IP-change reannouncement, and link-up reannouncement for service-file entries. One reconciler write, durable across daemon restarts." The DBus path would also require the brain to replay all registrations on host-agent restart — static files survive restarts for free.

### `Publisher` interface in `internal/hostagent/agent.go`

Consumer-side interface (per CLAUDE.md rule):

```go
type Publisher interface {
    Publish(slug string) (name string, err error)
    Unpublish(slug string) error
}
```

`Agent` struct gains a `Publisher Publisher` field. `New()` signature updated to `New(v PasswordVerifier, pub Publisher)`. The `publish` and `unpublish` HTTP handlers delegate to `a.Publisher`. On Publisher error, they return 500 (not silent success) — the brain's install transaction needs a real failure signal.

Write-through cache: the `published` map on `Agent` is still updated on every successful `Publish`/`Unpublish` call, so `GET /v1/discovery/state` can answer without requiring the Publisher to expose a listing method. Documented in a comment.

### `FakePublisher` in `internal/hostagent/fake.go`

Same shape as `FakeVerifier`. Returns `slug + hostSuffix` as the name; `Unpublish` is a no-op. Used by `cmd/host-agent` (the fake binary), preserving its existing in-memory behavior.

### Binary wiring

- `cmd/host-agent/main.go` — `New(nil, hostagent.NewFakePublisher(".malmo.local"))`. Behavior unchanged.
- `cmd/host-agent-real/main.go` — `New(&pamverifier.PAMVerifier{...}, &avahipublisher.FilePublisher{Dir: "/etc/avahi/services", HostSuffix: ".malmo.local"})`. Updated startup `slog.Warn` to remove `publish/unpublish` from the "still fake" list; only three auth ops remain.

### Tests

**`internal/hostagent/avahipublisher/publisher_test.go`** — uses `t.TempDir()`:
- Publish writes file with expected XML fragments (snapshot style)
- Written file mode is 0644
- Publish twice for same slug overwrites cleanly (one file left)
- Unpublish removes the file
- Unpublish of missing slug is no-op (nil error)
- Slug validation rejects `../../etc/passwd`, `slug/with/slash`, `UPPERCASE`, `has space`, `has.dot`, `""` — all return errors
- Valid slugs `whoami`, `my-app`, `app123`, `a`, `123` all pass
- Returned name equals `slug + HostSuffix`

**`internal/hostagent/agent_test.go`** — extended:
- `TestPublish_DelegatesToPublisher` — injected Publisher is called with the slug; response name and state match
- `TestPublish_PublisherError_Returns500` — broken Publisher → 500, not silent success
- `TestFakePublisher_MatchesCurrentBehavior` — FakePublisher name contract preserved

## How it maps to the specs

- `DISCOVERY.md` # Per-app A records — "the install reconciler writes a static service file at `/etc/avahi/services/app-<slug>.service`" is now real in `host-agent-real`.
- `DISCOVERY.md` # Service records — "we do not publish per-app `_http._tcp` service records" preserved via `_malmo-app._tcp` dummy.
- `BRAIN_HOST_PROTOCOL.md` — wire shape unchanged; `publish` still returns `{name, state: "established"}`. File-write success maps to "established" — see known gaps below.
- CLAUDE.md "consumer-side interfaces" — `Publisher` lives in `internal/hostagent`, not in `avahipublisher`.
- CLAUDE.md "brain commits first, host is reconstructible" — no change needed here; the reconciler is in the brain, this is the host side.

## Known gaps (loud)

- **File-write success ≠ multicast complete.** We return `state: "established"` immediately after `os.WriteFile` succeeds. Avahi will actually multicast within <1 s on a healthy LAN, but we don't subscribe to the DBus `EntryGroup.StateChanged` signal. `DISCOVERY.md` install-latency note (line 59) says "the dashboard should not mark an app `ready` until the announcement has been emitted (Avahi's DBus `EntryGroup.StateChanged` → `ESTABLISHED`)." This is a slight overpromise in the current implementation — revisit when the brain's install-transaction health-wait lands.
- **`/etc/avahi/avahi-daemon.conf` interface scoping not written.** `DISCOVERY.md` §"Interface scoping" (lines 61-72) specifies that host-agent computes the `allow-interfaces`/`deny-interfaces` config from NetworkManager state at boot. Deferred.
- **No DBus `EntryGroup.StateChanged` subscription.** DBus polling for true ESTABLISHED signal is deferred (nspawn lane doesn't have Avahi running yet).
- **`setPassword`, `setRole`, `deleteUser` still fake in `host-agent-real`.** Carried from 0011; `useradd`/`passwd`/`gpasswd`/`userdel` integration is a Tier-B follow-up.
- **The host record (`malmo.local`)** — published at boot time, not by the per-app reconciler. Deferred to the boot-sequence integration.
- **nspawn lane for real Avahi** — a test fixture that actually runs `avahi-daemon` and verifies the file is picked up is out of scope. The unit tests cover the Go layer; end-to-end in nspawn is a future lane.

## What's next

- Real `setPassword` / `setRole` / `deleteUser` in `host-agent-real` (Linux: `useradd`, `passwd`, `gpasswd`, `userdel`).
- DBus `EntryGroup.StateChanged` subscription for true ESTABLISHED signal (pairs with brain install-transaction health-wait).
- `/etc/avahi/avahi-daemon.conf` allow/deny-interface config generation from NetworkManager state.
- nspawn test fixture: provision Avahi, write a service file, verify the A record resolves.
