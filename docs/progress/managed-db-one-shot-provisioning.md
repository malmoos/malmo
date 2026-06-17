# Managed-DB provisioning moves off `docker exec` to a one-shot client container (#185)

- **Status:** done
- **Date:** 2026-06-15
- **Specs touched:** `DECISIONS.md` 2026-06-15 (flips the 2026-06-05 `docker exec` call; resolves the 2026-06-14 gate), `SERVICE_PROVISIONING.md` (# Implementation status, # Locked: provisioning), `CONTROL_PLANE.md` (# Locked: Docker socket exposure — managed-DB gate lifted)

Closes #185. The M1b spike (`socket-proxy-compose-validation.md`, Finding 2) and the 2026-06-14 decision left managed-DB-in-production gated: the containerized brain's only Docker path is the `docker-socket-proxy`, which denies the `EXEC` family by design, but `internal/lifecycle/services.go` provisioned per-app databases/roles by `docker exec`'ing the engine's own client (`psql` / `mysql` / `mariadb` / `valkey-cli`) inside the long-running service container. So the instant the brain points `DOCKER_HOST` at the proxy, provisioning breaks. This slice re-architects provisioning off `docker exec` while keeping `EXEC` denied — the production posture the proxy exists to enforce. (Dev — the natively-run brain on the raw socket — was never affected.)

## What was decided

**A one-shot client container, not engine-over-TCP-from-the-brain** (`DECISIONS.md` 2026-06-15). The 2026-06-14 entry named two candidate shapes. The one-shot container is decisive because it preserves *both* reasons the original 2026-06-05 decision gave against a Go SQL client: (a) **no new Go driver dependency** — it reuses the service's own image client, and (b) **the brain stays off the app-reachable `malmo-svc-*` network** — only the ephemeral `--rm` container joins it (honoring `DECISIONS.md` 2026-06-02, control plane off app-reachable networks). The engine-over-TCP shape would have violated (b). It is also minimal-change: the SQL/ACL command shapes and quoting are byte-identical to the exec path — only the Docker transport changes.

**De-risked empirically before building.** The spike only validated detached `compose up -d`/`down -v` through the proxy. A foreground `docker run --rm` captures output via a container **attach**, which is a different endpoint — so it was verified against the exact M0 allowlist (`POST PING VERSION INFO CONTAINERS IMAGES NETWORKS VOLUMES`, `EXEC` absent): `docker exec` → 403 (the breakage baseline), but a foreground `docker run --rm` with output capture → rc=0 with stdout captured, and `--rm` teardown (DELETE) works. The reason is structural and version-stable: `/containers/{id}/attach` is gated by `CONTAINERS=1` (allowed) while `/exec/{id}/start` is gated by the absent `EXEC` — both do the same TCP upgrade, only the path family differs. A full end-to-end provision (env-file remap + TCP-over-alias + entrypoint passthrough, then a verify query) was then run through the proxy against a real Postgres before any code was written.

## What was done

### `internal/lifecycle/docker.go`

- **Dropped `DockerDriver.Exec`** (its only consumer was `services.go`).
- **Added `RunOneOff(ctx, image, network, envFile, args)`** — `docker run --rm --network <network> --env-file <envFile> <image> <args…>`, combined output. The throwaway container joins the service network; the brain does not.
- **Added `ContainerHealth(ctx, container)`** — reads `docker inspect -f {{.State.Health.Status}}` by container name (a `CONTAINERS` read the proxy allows), returning `none` when no healthcheck is declared. Replaces the exec'd readiness probe.

### `internal/lifecycle/services.go`

- **`clientWrapper(kind, version)`** — the per-engine `sh -c` script the one-shot runs: it remaps the service superuser password (delivered by `--env-file` under the `.env` var name, never argv) to the client's expected env var (`PGPASSWORD` / `MYSQL_PWD` / `REDISCLI_AUTH`), then execs the client with an explicit `-h <kind>-<version>.malmo.internal` (TCP — there is no shared local socket in a separate container). The per-command tokens ride positional `"$@"`, so the wrapper shell never reparses the SQL/ACL args.
- **`runServiceClient(ctx, kind, version, args…)`** — builds the one-shot from the service's own image (`serviceImageRepo[kind]:version`), its `--internal` network, and its `.env`, then calls `RunOneOff`. Used by all four provisioning/drop functions.
- **`provisionPostgresDB` / `provisionMySQLDB` / `provisionValkeyACL` / `dropServiceGrants`** rewritten to call `runServiceClient` with their existing SQL/ACL argument shapes (postgres multi-`-c`; mysql `-e <sql>`; valkey `ACL SETUSER …` then `ACL SAVE`; per-kind drops). The superuser password remains out of host argv; the per-app credential stays in argv as before.
- **`waitServiceReady`** polls `ContainerHealth` until `"healthy"` (the per-engine compose healthcheck — `pg_isready` / a TCP admin ping / `valkey-cli ping` — runs *inside* the container via Docker's healthcheck mechanism, not an API exec, so it works under the proxy; the brain only reads the status). An inspect error is treated as transient (keep polling to the deadline). **`serviceReadyProbe` deleted** (its only caller was `waitServiceReady`).

### Tests

- **Hermetic** (`fakes_test.go`, `lifecycle_services_test.go`): `fakeDocker` drops `Exec`, gains `RunOneOff` (recording, with a `runOneOff` failure hook) and `ContainerHealth` (defaults `"healthy"`). The existing provisioning/uninstall assertions match the same SQL/ACL substrings on the recorded one-shot calls; `TestRedisProvisioningExecFailures` drives failures through the `runOneOff` hook.
- **Live** (`dockerlive_test.go`, build tag `dockerlive`, not in `make check`): unchanged test bodies now exercise the one-shot path for real — `TestLivePostgresProvisioning`, `TestLiveMySQLProvisioning`, `TestLiveRedisProvisioning` all provision → connect with the injected credential → drop on uninstall (PASS against `postgres:15` / `mysql:8.0` / `valkey/valkey:8`). The verification helpers keep their own `docker exec` (they run against the test's raw socket, not the proxy).
- `make check` green.

## What's next

- **Outer-loop VM acceptance.** `dev/test-qemu/medium-assertions.sh` gained a managed-DB **transport-capability** block — from the brain's own vantage (its `DOCKER_HOST` is the proxy), a one-shot `docker run --rm` with output capture must succeed and a `docker exec` must still be refused — the exact capability this slice turns on, against the real booted socket-proxy. (The full provision/drop/readiness round-trip is covered by the `dockerlive` suite and belongs to the M2 app-install lane in the VM; this is not that.) It needs a run under `sudo make test-medium-qemu` on an mkosi/swtpm/QEMU host.
- The deferred managed-service lifecycle gaps are unchanged by this slice: grace-shutdown timer, backup/restore + cross-version migration, and at-rest encryption of the stored superuser + per-app passwords (`NEXT.md` # Managed-service lifecycle gaps, # App-secret injection hardening).
