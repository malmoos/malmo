# Single-label `<slug>.local` app LAN names (+ collision fallback)

- **Status:** done
- **Date:** 2026-05-31
- **Specs touched:** docs/specs/DECISIONS.md (new 2026-05-31 entry), docs/specs/DISCOVERY.md (# Per-app A records, client-compat matrix), docs/specs/MOLMA_NETWORK.md (# URL-scheme table), docs/specs/SPEC.md (# LAN routing), docs/specs/DASHBOARD.md (# instance naming), docs/specs/APP_LIFECYCLE.md (publish mechanism + splash timing), docs/specs/AUTH.md + docs/specs/THREAT_MODEL.md (cookie-`Domain` warnings), plus mechanical example updates in APP_ISOLATION.md, BRAIN_HOST_PROTOCOL.md, SERVICE_PROVISIONING.md, CONTROL_PLANE.md, BUILD.md

## What was done

Changed the LAN URL scheme for app instances from the multi-label `<slug>.molma.local` to single-label `<slug>.local`. The old shape was rejected outright by Linux's `nss-mdns` (any name with a dot before `.local` is multi-label and returns NXDOMAIN without a network query — verified on a dev box, ~23ms instant fail; a single-label name published by the same Avahi resolved fine via the same `getaddrinfo` path). `systemd-resolved`'s mDNS rejected it too. So the foundational no-cloud LAN URL never resolved on Linux or the dev loop. The `.molma` infix bought nothing in mDNS (no zones/delegation/wildcards — each name is published individually regardless), so it was dropped. Full rationale + competitor research (Umbrel/Zima/Synology all use ports for LAN, not per-app `.local`) in `DECISIONS.md` (2026-05-31).

The box stays `molma.local`; the `<slug>.<box-id>.molma.network` HTTPS scheme is unchanged. Browser origin-isolation is preserved (each app keeps a distinct host).

### Code

- `internal/protocol/host.go` — new `AppHostSuffix = ".local"` constant. Both sides of the host socket build the name from it (brain for URL + Caddy route; host-agent for the Avahi announcement) so they can't drift.
- `internal/hostagent/avahipublisher/publisher.go` — `Publish` now tries `<slug>.local`, and on an Avahi collision retries once with a box-qualified `<slug>-<box>.local` (e.g. `photos-molma.local`), returning whichever name won. Refactored the single-attempt path into `tryPublish`; box label derived from the OS hostname via the pure, unit-tested `sanitizeBoxLabel` (in `slug.go`).
- `internal/lifecycle/lifecycle.go` — install and reconcile now use the **returned** published name (`pub.Name`) for the Caddy route and the URL, falling back to the reconstructed `<slug>` + `AppHostSuffix` only when publish failed (so host-header routing keeps working without mDNS). The published name is already persisted as `MDNSName`.
- `internal/api/api.go` — `toDTO` builds the app URL from the stored `MDNSName` (the name actually announced, possibly the collision fallback), falling back to the reconstructed primary.
- `cmd/host-agent/main.go`, `cmd/host-agent-real/main.go`, `internal/hostagent/fake.go` — wire the publisher `HostSuffix` from `protocol.AppHostSuffix`.

The reserved-slug guard already included `molma`, so an app can't take the box's own name.

### Tests / dev

- `internal/hostagent/avahipublisher/helpers_test.go` — `TestSanitizeBoxLabel` (host→label reduction: first label, lowercased, `[a-z0-9-]` only, `molma` fallback).
- Updated asserted names to `*.local` across `agent_test.go`, `avahipublisher` tests, `lifecycle/fakes_test.go`, `api/health_test.go`, and `dev/test-caddy-routing.sh` / `dev/test-avahi-publisher.sh`.
- `make check` green.

## What's next

- **Live multi-device verification.** `getent hosts whoami.local` + `curl http://whoami.local/` on the Linux dev box (closes the resolution gap that motivated this); ideally also confirm resolution from a Mac/iPhone on the LAN.
- **Async collision detection.** The fallback fires on the *synchronous* `AddAddress` collision error. Avahi can also report a conflict *after* `Commit` via `EntryGroup.StateChanged → COLLISION`; that signal isn't watched yet, so a post-commit collision wouldn't trigger the box-qualified retry. Watching `StateChanged` (also the readiness gate `APP_LIFECYCLE.md` wants) is the proper fix. Carried over from `avahi-dbus-publisher.md`.
- **`MOLMA_APP_URL` on collision.** The env is written at `generating_override` (before publish), so it always carries the primary `<slug>.local`, not the box-qualified fallback. Cosmetic; only wrong in the rare collision case. Threading the published name into the env would require writing the override after publish.

Builds on [avahi-dbus-publisher.md](avahi-dbus-publisher.md) and the dev-loop Avahi wiring; see [caddy-routing-verified.md](caddy-routing-verified.md) for the routing path.
