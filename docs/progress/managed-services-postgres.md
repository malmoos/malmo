# Managed services — Tier 1 Postgres provisioning

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** `SERVICE_PROVISIONING.md`, `APP_MANIFEST.md`, `DECISIONS.md`, `NEXT.md`

Builds the first managed-data-service path end-to-end, following the manifest-field → store-table → lifecycle-step → env-injection shape established by `app-secret-injection.md`. Until now the `services:` manifest block was parsed-and-ignored: no `Services` field on the manifest struct, no provisioning code, no `MOLMA_SERVICE_*` injection anywhere. `kan` (`postgres:15`) and `docuseal` (`postgres:16`) were both blocked on it (secret + app-url injection landed earlier the same day). Managed Postgres unblocks the *database* dependency; `kan` boot then surfaced a second, unrelated blocker — the override force-restarts its one-shot `migrate` job, so it can't reach the `service_completed_successfully` gate `web` waits on (tracked separately in #92). An app that declares `services.database: {type: postgres, version: "15"}` now gets a real per-app database+role inside a shared, lazily-spun-up Postgres container, with credentials injected as `MOLMA_SERVICE_DATABASE_*`. Design rationale in `DECISIONS.md` 2026-06-05 (managed-service provisioning).

## What was done

### Manifest — `internal/manifest`
- New top-level `services: {<name>: {type, version, name?}}` field (`Services map[string]ServiceDep`). The map key is the app's logical name and becomes the env-var suffix (`database` → `MOLMA_SERVICE_DATABASE_*`). `validateServices` requires a snake_case key (reusing the secret-name rule), a known `type` (`postgres`|`redis`), and a version on the per-type allowlist (`postgres: 15,16`; `redis: 7`). Redis passes *schema* validation (forward-valid manifests) but is not provisioned this slice. `name` is parsed but unused in v1 (the brain generates the real database name).

### Store — `internal/store`
- New `service_instances` table (`kind, version, superuser_password, state, created_at`, PK `(kind, version)`) — the desired-state record of each shared service container. `GetServiceInstance` (→ `ErrNotFound`, the lazy-spinup trigger), `CreateServiceInstance` (→ `ErrConflict` on dup), `ListServiceInstances` (reconcile).
- New `service_grants` table (`instance_id, logical_name, kind, version, db_name, role_name, password`, PK `(instance_id, logical_name)`, `FOREIGN KEY … ON DELETE CASCADE`) — the per-app database+role, reclaimed with the instance like `instance_secrets`. `SetServiceGrants` / `GetServiceGrants`, mirroring the secrets CRUD.

### Docker driver — `internal/lifecycle/docker.go`
- `ServiceUp(dir, project)` — compose up for a service project using only the generated `compose.yml` + `.env` (no per-app override).
- `Exec(container, args)` — `docker exec`, used to provision per-app databases via the service container's own `psql`, so the brain never joins the service network (`DECISIONS.md` 2026-06-02).

### Lifecycle — `internal/lifecycle/services.go` (+ wiring in `lifecycle.go`)
- `ensureServiceInstance` (lazy spinup, idempotent): generates a superuser password, writes `<stateDir>/services/<kind>-<version>/{compose.yml,.env}`, creates the `--internal` `molma-svc-<kind>-<version>` network, `ServiceUp`s the project, polls `pg_isready` via `Exec` until ready, persists the row. The generated compose pins `postgres:<version>`, sets `container_name: molma-svc-postgres-15` (the exec handle) and the in-network DNS alias `postgres-15.molma.internal` (the host apps put in their DSN), a `pg_isready` healthcheck, a data volume, and `restart: unless-stopped`.
- `provisionServices` (install step `5c`, after secrets): per declared service → `ensureServiceInstance`, generate `db_name`/`role_name` (`<sanitized id>_<rand-hex>`) + password, `Exec` psql `CREATE ROLE … LOGIN PASSWORD …; CREATE DATABASE … OWNER …`. Grants persisted via `SetServiceGrants` before override/env.
- `writeOverride` attaches every app service to each required `molma-svc-<kind>-<version>` network (kan's `migrate` job *and* `web` both need the DSN) and lists them external.
- `writeEnv` re-emits each grant as `MOLMA_SERVICE_<NAME>_{HOST,PORT,NAME,USER,PASSWORD,DSN}` (HOST = the DNS alias; DSN = `postgres://role:pw@host:5432/db`), read from the store.
- Uninstall + install-rollback capture grants before the cascade and best-effort `Exec` psql `DROP DATABASE … WITH (FORCE); DROP ROLE …`. The shared service instance is left running — grace-shutdown is deferred.
- `reconcileServices` re-asserts each recorded service instance is up at brain startup (network + `ServiceUp`); service containers carry the `molma.service` label, not `molma.managed=true`, so the app-orphan reaper never touches them.

### Tests
- `manifest`: services parse happy-path (postgres + redis schema-valid) and rejection (unknown type, bad version, missing version, bad key).
- `store`: service-instance CRUD + dup conflict; grants roundtrip + cascade-on-instance-delete.
- `lifecycle`: install records the shared instance + calls `ServiceUp`, persists a grant, issues the provisioning psql, writes `MOLMA_SERVICE_DATABASE_*` into `.env`, attaches the app service to the svc network; a second app reuses the one instance (no second `ServiceUp`); uninstall issues the `DROP DATABASE`.
- `molma manifest check catalog/kan/manifest.yml` passes.

## What's next

- **Real-system verification (done for the Postgres path).** `TestLivePostgresProvisioning` (`dockerlive` build tag) installs a folderless app against real Docker and asserts the shared `molma-svc-postgres-15` container comes up, the per-app DB+role is created, the role connects over TCP with the injected DSN password, and the DB is dropped on uninstall. **`kan` end-to-end boot is not verified** — it's blocked on #92 (override force-restarts the one-shot `migrate` job → `compose up -d` hangs on the completion gate); `TestLiveKanBoot` is committed but `t.Skip`-ped until #92 lands.
- **Redis provisioning** is unbuilt (schema-valid only). A redis declaration currently fails at provisioning with a clear error. ACL-user-vs-logical-DB isolation needs its own pass — `NEXT.md` # Redis managed-service provisioning.
- **Grace-shutdown** (stop a service version 12h after its last consumer uninstalls) is deferred — services stay running. `NEXT.md` # Managed-service grace-shutdown.
- **Backup/restore + cross-version migration** (`pg_dump`/`pg_restore`, auto-migrate on major bump) are deferred, gated on the backup design — `NEXT.md` # Backup architecture shape.
- **At-rest encryption** of the superuser + per-app passwords (plaintext in SQLite + service `.env`) folds into the existing `NEXT.md` # App-secret injection hardening.
- **Service image is tag-pinned** (`postgres:15`), not digest-pinned like app images.
