# Admission — reject `deploy.replicas` > 1 (single-replica appliance)

- **Status:** done
- **Date:** 2026-06-12
- **Specs touched:** `APP_LIFECYCLE.md` (# Locked: admission policy — new `deploy.replicas` reject bullet)

Surfaced reviewing #146 (`fix/83-main-container-name`), which pins `container_name: molma-<id>-<main-service>` on the main service so the per-app Logs tail's exact `journalctl CONTAINER_NAME=` match holds on real hardware. Compose refuses to scale a service with an explicit `container_name`, so an author manifest declaring `deploy.replicas: 3` on the main service now fails at `docker compose up` with a raw, opaque compose error deep in the install transaction. This change makes the implicit "single-replica appliance" assumption an explicit admission rejection that names the field — caught at catalog-CI publish time as well as install, the same as every other admission rule.

## What was done

- **`internal/admission/admission.go`**: `rawService` gains a `Deploy.Replicas *int`; `checkService` rejects any service whose `deploy.replicas` exceeds 1 with a field-naming message (`service "web" sets deploy.replicas: 3 — molma is a single-node appliance and runs one replica per service; remove deploy.replicas`). Applied to **every** service, not just the main one: on a single node a second replica buys no availability and Caddy routes to one upstream alias per instance, so multi-replica is unsupported across the board — not only where the `container_name` pin forbids it. Door-symmetric via the existing `CheckStructure` path (no daemon needed).
- **`APP_LIFECYCLE.md` # Locked: admission policy**: new bullet in the reject list, cross-referencing # Locked: override file contents for the `container_name` pin that makes the main service structurally unscalable.
- **Test**: two table cases in `TestCheckStructure` — `replicas: 3` rejected (message names the service and `deploy.replicas`), `replicas: 1` allowed (the single-replica default is never spuriously rejected).

## Verification

- `make check` green (gofmt, vet, OpenAPI freshness, full Go suite).

## Known gaps & deviations

- Only `deploy.replicas` is inspected. The legacy top-level service `scale:` key is not a Compose Spec field (`docker compose config` does not honor it as a replica count), so it is intentionally not checked — adding a guard for it would be speculative.
- A `deploy.mode: replicated` with no explicit `replicas` defaults to 1 replica, so it passes — correct, that is a single replica.

## What's next

- Nothing required. If molma ever grows multi-node (not on any roadmap), this rule is the single place that the appliance assumption is enforced.
