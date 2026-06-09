# Managed services — MySQL + MariaDB Tier-1 types

- **Status:** done
- **Date:** 2026-06-09
- **Specs touched:** `SERVICE_PROVISIONING.md`, `APP_MANIFEST.md`, `DECISIONS.md`

Extends the Tier-1 managed-services path built by `managed-services-postgres.md` to the MySQL family (#108): `mysql` (8.0, 8.4) and `mariadb` (10.11, 11.4) land as two `type` values backed by the one existing code path — lazy spinup, per-app DB+user, `docker exec`-the-client provisioning, `MOLMA_SERVICE_<NAME>_*` injection, dedicated `--internal` network, drop-on-uninstall all carry over unchanged. This unblocks the two `blocks-start` apps bailed in the import-gap ledger under `managed-mysql`: Ghost (#85, requires MySQL 8 specifically) and Kimai (#89, MySQL/MariaDB only) — both ledger entries flipped to `implemented`. Design rationale in `DECISIONS.md` 2026-06-09 (Tier-1 managed services grow MySQL + MariaDB types).

## What was done

### Manifest — `internal/manifest`
- `serviceVersions` allowlist gains `mysql: {8.0, 8.4}` and `mariadb: {10.11, 11.4}` — the upstream LTS series (mysql 8.0 is past Oracle EOL but kept because Ghost pins it specifically). The `validateServices` unknown-type error now names all four types. No new fields: a MySQL declaration is the same `services: {<name>: {type, version}}` shape.

### Lifecycle — `internal/lifecycle/services.go`
- **Per-engine deltas are data, not code paths:** `servicePort` (3306), `serviceImageRepo` (`mysql`/`mariadb`), `serviceDSNScheme` (`mysql://` for both engines — one wire protocol), `provisionedKinds` (the gate redis still fails), and `mysqlTools` (client binary names + root-password env var: `mysql`/`mysqladmin`/`MYSQL_ROOT_PASSWORD` vs `mariadb`/`mariadb-admin`/`MARIADB_ROOT_PASSWORD` — the mariadb image's mysql-named binaries are deprecated).
- **`serviceName` folds version dots to dashes** (`mysql-8-0`): compose project names reject dots, and the container name / network / DNS alias / project all share the stem — so `molma-svc-mysql-8-0` and `mysql-8-0.molma.internal`. Postgres names are unchanged (no dots).
- **Provisioning** (`provisionDB` dispatcher → `provisionMySQLDB`): `CREATE DATABASE`; `CREATE USER '<x>'@'%' IDENTIFIED BY …`; `GRANT ALL PRIVILEGES ON <db>.* TO …` via docker-exec of the image's own client. The SQL rides as a positional `$1` so the shell never parses it (backticks), and the root password is expanded from the *container's* env (`MYSQL_PWD="$MYSQL_ROOT_PASSWORD"`) — it never appears in host-side argv (unlike Postgres, the MySQL images don't trust the local socket). The db/user stem is bounded to 26 chars so `<stem>_xxxx` fits MySQL's 32-char user-name cap.
- **Readiness** (`serviceReadyProbe`): `mysqladmin`/`mariadb-admin ping` over **TCP only** (`-h127.0.0.1 --protocol=TCP`) — during first-boot init the entrypoint runs a temporary socket-only server (`--skip-networking`) that a socket ping would mistake for ready. The generated compose healthcheck mirrors it.
- **Uninstall** (`dropServiceGrants`): `DROP DATABASE IF EXISTS`; `DROP USER IF EXISTS '<x>'@'%'`, same best-effort semantics as Postgres.
- **`writeServiceDir`/`mysqlServiceCompose`**: same shape as the Postgres compose — external `--internal` network, versioned DNS alias, fixed `container_name`, `molma.service` label, data at `/var/lib/mysql`, root password via `.env`.
- `writeEnv` emits the family with `PORT=3306` and `DSN=mysql://user:pw@<alias>:3306/db`; variable names unchanged.
- No store, API, or override changes — `service_instances`/`service_grants` are kind-agnostic and `writeOverride`'s network attachment already keys off the declaration.

### Tests
- `manifest`: MySQL-family parse happy-path (all four type/version combos); rejections extended (bad mysql/mariadb versions, major-only `"8"`); the old "unknown type: mysql" case became `mongodb`.
- `lifecycle`: `TestInstallProvisionsMySQLFamily` (both engines: instance recorded, engine client + CREATE USER/GRANT exec'd, dot-folded HOST, PORT 3306, `mysql://` DSN, override network) and `TestUninstallDropsMySQLDB`; the Postgres fixtures gained a kind/version-parameterized installer.
- `TestLiveMySQLProvisioning` (`dockerlive` tag) mirrors the Postgres live test against a real `mysql:8.0`: shared container lazily spun up, DB present, provisioned user connects over TCP with the injected password, DB dropped on uninstall — **run and passing locally** (~17s). The mariadb deltas (binary names, `MARIADB_ROOT_PASSWORD`, the `$1` SQL shape, TCP connect as the provisioned user) were verified one-off against a live `mariadb:11.4` container; one live engine in the suite suffices since the code path is shared.

## What's next

- **Re-import Ghost (#85) and Kimai (#89)** against the new types — the reason this exists. Kimai's secondary finding (entrypoint `pwconv`/setuid-drops to www-data under `cap_drop: ALL`) is untested under the sandbox and may still bail it.
- **Inherited deferrals, unchanged scope:** backup/restore (`mysqldump`, gated on the backup design), grace-shutdown, at-rest encryption of stored passwords (`NEXT.md` # App-secret injection hardening), cross-version migration, and Redis provisioning.
- **Service images are tag-pinned** (`mysql:8.0`), not digest-pinned — same as Postgres (`NEXT.md`).
- **mysql 8.0 EOL watch:** when Ghost moves its supported floor past 8.0, drop it from the allowlist.
