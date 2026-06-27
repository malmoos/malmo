# Profile-aware service-down allowlist; drop the phantom caddy unit

- **Status:** done
- **Date:** 2026-06-27
- **Specs touched:** docs/specs/HEALTH.md (locus-B service-down row)

## What was done

Fixed two defects in the `service-down` detector (`internal/hostagent/servicehealth`) that made a freshly provisioned **hosted** box report four false `service-down` errors on the dashboard (`avahi-daemon`, `caddy`, `chrony`, `smbd` — all "inactive") while the box was actually healthy. Observed live on `pine-peak.malmo.network`.

The detector watched a single hardcoded allowlist (`CoreUnits`) on both profiles:

```go
docker, caddy, avahi-daemon, chrony, smbd
```

Two problems:

- **Phantom caddy unit (all profiles).** Caddy is a brain-managed container (`malmo-caddy`), not a host systemd unit, on every profile (`dev/control-plane/compose.yml`; `CONTROL_PLANE.md` # "Caddy is malmo substrate, runs as a container"; `HEALTH.md` # Locus C — "there is no `caddy.service`"). `systemctl is-active caddy.service` can never be active, so this raised a permanent false positive. The spec's locus-B allowlist (`HEALTH.md` line 172) already omitted caddy — the code had diverged. Caddy liveness is a locus-C check (deferred until the brain owns Caddy's container lifecycle).
- **Not profile-aware (hosted only).** The lean cloud image cuts Avahi, Samba, and chrony (`ENVIRONMENT.md` # How the profile is realized; `dev/cloud/expected-packages.txt`), but `wiring_hosted.go` and `wiring_appliance.go` both called the same `servicehealth.New()`. A hosted box therefore watched three appliance units that never exist.

Changes:

- **`internal/hostagent/servicehealth/servicehealth.go`** — replaced `CoreUnits` with two exported allowlists: `ApplianceUnits` (`docker`, `avahi-daemon`, `chrony`, `smbd` — caddy and host-agent intentionally absent) and `HostedUnits` (`docker` only). `New()` → `New(units []string)` so each profile's wiring passes its own set. Package + symbol doc comments explain the caddy/host-agent omissions.
- **`cmd/host-agent-real/wiring_hosted.go`** — `servicehealth.New(servicehealth.HostedUnits)`.
- **`cmd/host-agent-real/wiring_appliance.go`** — `servicehealth.New(servicehealth.ApplianceUnits)`.
- **`docs/specs/HEALTH.md`** — locus-B `service-down` row now records the allowlist as profile-specific (appliance set vs. hosted = docker-only) and states caddy is never in either set (it is a locus-C container check).

## How it maps to the specs

- `HEALTH.md` # Detector catalog (locus-B service-down) — code now matches the spec's host-unit allowlist (caddy excluded) and the spec records the hosted reduction. ✓
- `HEALTH.md` # Locus C — caddy liveness stays a locus-C concern (deferred), not a phantom locus-B systemctl check. ✓
- `ENVIRONMENT.md` # How the profile is realized — the hosted allowlist reflects the lean image's cuts (no Avahi/Samba/chrony). ✓

## Known gaps & deviations

- **Caddy has no liveness check on hosted (or appliance) until locus-C lands.** Removing the phantom unit removes a false positive, not real coverage — there was none (the check could never pass). Locus-C Caddy self-heal remains deferred (`NEXT.md` # Caddy liveness self-heal).
- **`cmd/host-agent-real` not linked locally** — both builds stop at the pre-existing PAM cgo dep (`C.RTLD_NEXT`, no `libpam0g-dev` on this box); the changed lines resolve. Full link verifies on a PAM box or the nspawn/qemu lane (same limitation as `health-system-report.md`).

## How it was verified

- `go test ./internal/hostagent/servicehealth/` — pass, incl. new `TestAllowlists_NoPhantomOrSelfUnits` (no caddy/host-agent in either list; hosted = docker-only) and `TestNew_WatchesGivenUnits`.
- `go vet ./internal/hostagent/servicehealth/` clean; all changed Go files gofmt-clean.

## What's next

- Rebuild + reupload the hosted image (`make deploy-image` in `malmoos/cloud`) and redeploy so provisioned boxes pick up the slim host-agent; re-check `pine-peak.malmo.network`.
- Locus-C Caddy liveness/self-heal once the brain owns Caddy's container lifecycle.
