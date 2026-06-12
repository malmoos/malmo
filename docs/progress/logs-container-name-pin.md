# Per-app logs — pin the main service's container_name so journald matches real containers

- **Status:** done
- **Date:** 2026-06-12
- **Specs touched:** `APP_LIFECYCLE.md` (# Locked: override file contents — new `container_name` bullet)

Closes #83 and the `CONTAINER_NAME` replica-suffix gap recorded in `per-app-logs.md` # Known gaps & deviations. The brain computed `malmo-<id>-<service>` (`MainContainerName`) and handed it to host-agent's `journalctl CONTAINER_NAME=<name>` follow, but that string was only a Docker network alias — compose named the *running* container `malmo-<id>-<service>-1`, and Docker's journald driver tags lines with the real, replica-suffixed name. Caddy kept working (it resolves the alias via Docker DNS), the dev inner loop kept working (the fake host-agent's ticker doesn't consult names), and the Logs tab was silently a no-op on real hardware.

## What was done

- **`writeOverride` pins `container_name` on the main service** (`internal/lifecycle/lifecycle.go`): `container_name: malmo-<id>-<main_service>` in the main service's override entry, the same stem as the ingress alias and Caddy upstream. The running container's name now *is* the string `MainContainerName` returns, so journald's `CONTAINER_NAME=` tag matches exactly. Sidecars stay unpinned — an explicit `container_name` forbids `compose scale`, and only the main service is single-replica by design (the pin makes that assumption structural rather than conventional). Same pattern as the managed services' fixed `container_name` exec handle (`internal/lifecycle/services.go`).
- **No journalsource change, no wire-contract change** — `BRAIN_HOST_PROTOCOL.md` # `journal_follow` and `LOGGING.md` # Per-app logs already described the exact-match contract as if it held; the pin makes them true. The two "Known gap" comments (`MainContainerName`'s doc, the `journalsource` package doc) are replaced with one line each stating the pin.
- **`APP_LIFECYCLE.md` # Locked: override file contents** gains the `container_name` bullet (the section enumerates exactly what the override stamps, so the pin had to land there in the same change).

Rejected alternatives are recorded on the issue: hardcoding the `-1` suffix in `MainContainerName` (fragile, silently drops logs from any scaled service) and a journald `tag` log-opt + `CONTAINER_TAG=` match (forces `driver: journald` into every override and redefines the name as a tag; `journalctl` has no field glob, so a pure journalsource-side fix was never viable).

## Verification

- New `TestOverridePinsMainContainerName` (`internal/lifecycle/lifecycle_test.go`), on the existing multi-service `migrateJobCompose` fixture: the main service's override entry carries `container_name: malmo-<id>-web`, both sidecars (`migrate`, `seed`) carry none, and `MainContainerName` returns the identical string — the pinned name and the queried name can't drift apart.
- `make check` green (gofmt, vet, OpenAPI freshness, full Go suite).

## Known gaps & deviations

- **Issue text vs. doc discipline:** #83 says to "update `docs/progress/per-app-logs.md` # Known gaps" — progress entries are frozen snapshots, so that entry is untouched and this one closes the gap by reference instead.
- **Pre-existing containers keep their suffixed name until recreated.** The pin lands in the override at install/update time; an instance installed before this change keeps `…-1` (and a broken Logs tail) until its next override regeneration recreates the container. Acceptable: no real-hardware deployments predate this fix.
- The outer-loop proof (journald lines actually matching on a booted Debian target) remains with the per-app-logs real-hardware verification — this was the first blocker for that, not the whole of it.

## What's next

- Real-host verification of the full Logs path (`journalctl CONTAINER_NAME=malmo-<id>-<service>` returning live lines) when the outer-loop QEMU flow runs next.
- The remaining `per-app-logs.md` gaps (member-visible household logs, empty-state hint, Logs surface moving to an app detail card) are unchanged.
