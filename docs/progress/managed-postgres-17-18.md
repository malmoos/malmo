# Managed Postgres grows 17 and 18, and the data bind stops depending on the image default (#354)

Closes #354. `serviceVersions` offered only Postgres 15 and 16, and the catalog hit the first app that cannot run on either: importing uptimepage (store #59 / store PR #60) onto managed Postgres 16 failed at boot with `function uuidv7() does not exist`. `uuidv7()` is an 18 built-in, used by ten of that app's migrations as a column `DEFAULT` rather than one-time DDL — so a shim function would have to live for the life of the instance, not just get the migration through. That app shipped with a bundled `postgres:18-alpine` sidecar instead (the "not yet provisioned" routing case), so nothing was blocked; this entry is about the platform not offering only EOL-adjacent majors as the catalog grows.

## What was decided

- **Add both 17 and 18.** 18 is where the demand is; 17 comes along as the conservative middle so a manifest author isn't forced to jump two majors.
- **Keep 15, mark it deprecated in the docs.** Removing it from the allowlist is a one-line change, but backup/restore and cross-version migration for managed services are both deferred (`SERVICE_PROVISIONING.md`), so there is no supported path off 15 for a box already running it. Deprecating now sets up removal for when migration exists.
- **Extensions are documented, not implemented.** No `extensions:` manifest key. The honest position is that trusted contrib extensions already work — malmo's per-app role *owns* its database (`CREATE DATABASE <db> OWNER <role>`), and since PG13 a database owner may `CREATE EXTENSION` a trusted one — while untrusted contrib (superuser-only) and third-party (`pgvector`, `postgis`: not in the official image at all) are the bundled-sidecar path. A declared-extensions key is a permissions-and-image design question, not a rider on a version bump; `pgvector`-as-a-managed-type is filed separately (#355) and subject to the usual "3+ store apps want it" bar.

## What was done

### `internal/manifest`

`serviceVersions["postgres"]` becomes `{15, 16, 17, 18}`, with the 15-deprecation rationale in the comment beside it.

### `internal/lifecycle`

`postgresServiceCompose` now pins `PGDATA: /var/lib/postgresql/data` in the service environment. The image default moved: 15/16/17 default to `/var/lib/postgresql/data`, but 18 defaults to `/var/lib/postgresql/18/docker` (major-version-specific directories, docker-library/postgres#1259) — verified directly against `postgres:15`, `:16`, `:17` and `:18`.

**The issue's stated failure mode is not what the images actually do, and its proposed fix does not work.** Both were checked against real containers rather than reasoned about:

- The issue predicted that adding `"18": true` alone yields a Postgres that boots, passes `pg_isready`, serves traffic and then **silently** loses the cluster on restart. It does not: the 18 entrypoint detects the mount at the old path and **refuses to start**, with `Error: in 18+, these Docker images are configured to store database data in a format which is compatible with "pg_ctlcluster" … there appears to be PostgreSQL data in: /var/lib/postgresql/data (unused mount/volume)`. Under the brain that surfaces as lazy spinup failing the readiness wait (`malmo-svc-postgres-18 not ready after 1m30s (last: unhealthy)`) and the install rolling back. Loud, not silent — the image guards this exact trap. The fix is still needed and unchanged in shape; only the consequence of skipping it is different.
- The issue proposed binding the **parent** `/var/lib/postgresql` for 18. That fails: the 18 entrypoint drops to uid 999 before creating `$PGDATA`, so it cannot `mkdir` inside the `0700` directory `writeServiceDir` creates — `mkdir: cannot create directory '/var/lib/postgresql': Permission denied`, container exited. Reproduced with the directory owned by the invoking user *and* by root, so it is not an artifact of the dev machine's uid; making it work would mean the brain chowning service data to a hardcoded in-image uid.

Pinning `PGDATA` instead is the smaller change and the better one: no version branch in the template, and one on-disk layout across every major — `services/postgres-<version>/data` is the cluster directory whatever the image would have chosen. The pin is a no-op for 15/16/17, whose default is already that path.

### Tests

- `TestLivePostgres18Persistence` (`internal/lifecycle/dockerlive_test.go`, `dockerlive` tag) — the durability proof the issue asked for. It installs an app on managed Postgres 18, creates a table whose primary key defaults to `uuidv7()` (so the 18-only built-in that motivated the major is exercised through the provisioned engine, not just the allowlist), inserts a row, then **destroys the container** and brings the service back through the brain's own boot path, `reconcileServices`, before asserting the database, the role's credential and the row all survive. Destroying rather than restarting is load-bearing: `docker restart` keeps the container filesystem, so a restart-based test would pass against a broken bind. **Mutation-checked** — with the `PGDATA` pin removed the test fails, and it passes with it. The existing `pgDatabaseExists` / `pgRoleCanConnect` helpers were parameterized by version (they hardcoded 15) and a small `pgExec` helper added.
- `TestPostgresComposePinsPGDATA` (`internal/lifecycle`) — always-on regression guard that the rendered template pins `PGDATA` and binds `./data` at that same path, for every offered major.
- `TestParseServicesPostgresMajors` (`internal/manifest`) — 15/16/17/18 all parse; a `19` rejection case joins the existing `13` one so the allowlist stays bounded at both ends.

### Docs

`SERVICE_PROVISIONING.md` # Catalog (v1) carries the new version list, the "new manifests should declare 18" steer and the 15 deprecation with its reason. A new # Database extensions subsection states the three-case split (trusted contrib works today; untrusted contrib unavailable; third-party is a different image and therefore a different managed type) and notes the same rule holds for the MySQL family and Valkey. `APP_MANIFEST.md` # D updates the version list, points at the extensions section for the absent `extensions:` key, and moves its examples off 15 onto 18.

The extensions section is not a reasoned-about claim — the three cases were checked against a real `postgres:18` with the brain's exact provisioning shape (`CREATE ROLE app_a4f7 LOGIN`; `CREATE DATABASE app_a4f7 OWNER app_a4f7`), connecting as that role: `pgcrypto`, `citext`, `hstore`, `pg_trgm` and `uuid-ossp` all return `CREATE EXTENSION`; `pg_stat_statements` returns `ERROR: permission denied to create extension`; `vector` returns `ERROR: extension "vector" is not available`.

`make check` green; the `dockerlive` Postgres tests were run against a real Docker daemon.

## What's next

- **uptimepage moves off its bundled sidecar.** Now that 18 is offered, the app can route Postgres to a managed `services:` block — noted already in `store/apps/uptimepage/notes.md`. That move is the first real consumer of this change and the thing that would catch anything this entry missed.
- **`pgvector` as a managed type** is filed as #355. It is a distinct image (`pgvector/pgvector:pg18`), not a version, and waits on the "3+ store apps actually want it" bar.
- **Retiring 15** stays blocked on cross-version migration for managed services, which is itself gated on the backup design (`NEXT.md`). Deprecated in the docs is as far as this change goes; nothing enforces it at parse time.
- **Nothing migrates an existing instance between majors.** An app pinned to 15 or 16 keeps its own container and data dir; this change only widens what a new manifest may declare. Three or four shared Postgres containers on one box is now possible, which is the cost of the wider allowlist.
