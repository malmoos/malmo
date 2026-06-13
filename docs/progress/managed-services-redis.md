# Managed services Tier 1 grows Redis (#159)

- **Status:** done
- **Date:** 2026-06-13
- **Specs touched:** `SERVICE_PROVISIONING.md` (# Implementation status, # Per-app isolation in shared instances), `NEXT.md` (# Managed-service lifecycle gaps — Redis item resolved), `DECISIONS.md` 2026-06-13, `docs/dev/catalog-import-gaps.md` (`managed-redis — postiz` → implemented)

Closes #159. The manifest schema already accepted `services: {cache: {type: redis, version: "7"}}`, but the brain provisioned only the SQL families (`provisionedKinds = {postgres, mysql, mariadb}`), so a redis declaration passed `manifest check` and then failed at install — a check/install asymmetry the catalog-import ledger kept hitting (`managed-redis — postiz`: Postiz needs Redis for BullMQ queues). This slice provisions Redis the same way Postgres/MySQL are, closing the asymmetry. Follows the MySQL-family slice (`docs/progress/managed-services-mysql.md`).

## What was decided

The per-app isolation model — an open question in `NEXT.md` — is resolved (`DECISIONS.md` 2026-06-13): **a per-app ACL user with full keyspace**, not a logical-DB-number split. Redis has no database to scope a role to, so the per-app unit is an ACL user (`ACL SETUSER <app> on >pw ~* &* +@all -@admin`): every app authenticates as its own revocable credential and unauthenticated access is refused (a real auth boundary the DB-number split lacks), while the keyspace stays shared. `-@admin` keeps a compromised app off the ACL system / `CONFIG` / `SHUTDOWN` / replication, so it can't subvert the shared instance or the control plane. The credential — not a key namespace — is the boundary; tighter per-app key/command ACLs are a future hardening.

## What was done

### `internal/lifecycle`

- **Maps:** added `redis` to `servicePort` (6379), `serviceImageRepo` (`redis`), `serviceDSNScheme` (`redis`), and `provisionedKinds` — the data-only deltas the kind-keyed maps already model.
- **`redisServiceCompose(version)`** — same shape as the SQL service composes (external `--internal` network, versioned `redis-7.malmo.internal` DNS alias, fixed `malmo-svc-redis-7` exec handle, `restart: unless-stopped`). Runs `redis-server --aclfile /data/users.acl` so per-app ACL users **persist across a restart** (Redis ACLs are server config, not keyspace — they aren't in the RDB/AOF). `REDISCLI_AUTH` carries the superuser password into the container env so the brain's exec'd `redis-cli` and the `redis-cli ping` healthcheck authenticate without it ever reaching argv.
- **`writeServiceDir`** bootstraps `data/users.acl` with the default (superuser) account (`user default on >pw ~* &* +@all`) so redis can start; the redis entrypoint chowns the data dir to the `redis` user, so it can rewrite the file on `ACL SAVE`.
- **`provisionRedisACL`** — `ACL SETUSER` the per-app user then `ACL SAVE` (persist to the aclfile), both via `docker exec redis-cli`. The superuser auth rides `REDISCLI_AUTH` from the container env; the per-app password rides argv as the ACL `>password` token (base64url, no shell hazards), the same place the SQL families already carry per-app passwords — only the superuser password is kept out of argv.
- **`dropServiceGrants`** generalized to a per-kind command list (one `docker exec` per command); Redis runs `ACL DELUSER` + `ACL SAVE`. Best-effort, unchanged for the SQL engines.
- **`provisionServices`** persists a Redis grant with an **empty `DBName`** (the ACL user is the boundary; SQL grants keep db == role). **`serviceReadyProbe`** gains a `redis-cli ping | grep -q PONG` branch.
- **`writeEnv`** (`internal/lifecycle/lifecycle.go`) assembles the DSN without the `/dbname` suffix when the grant has no database, so Redis injects `redis://user:pw@redis-7.malmo.internal:6379` (clients default to logical DB 0); Postgres/MySQL DSNs are unchanged.

### Tests (`internal/lifecycle`)

- **Hermetic** (`lifecycle_services_test.go`, fake docker): `TestInstallProvisionsRedis` (service instance recorded + `ServiceUp`; grant with empty `DBName`; `ACL SETUSER … +@all -@admin` + `ACL SAVE` issued; injected family has host/6379/empty-NAME and a `redis://…:6379` DSN with **no** db path; app attached to the svc network), `TestUninstallDropsRedisACL` (`ACL DELUSER`), and `TestRedisProvisioningExecFailures` (forces the SETUSER / SAVE execs to fail → install errors, covering `provisionRedisACL`'s error paths). `provisionRedisACL` + `redisServiceCompose` at 100%.
- **Live** (`dockerlive_test.go`, real `redis:7`): `TestLiveRedisProvisioning` — lazy spinup with the external aclfile, the per-app ACL user is present in `ACL LIST`, authenticates with the injected password and reads/writes the keyspace (SET/GET via the `redis://` URL form), and is gone from `ACL LIST` after `ACL DELUSER` on uninstall. Run and PASS against real redis:7.
- `make check` green.

## What's next

- **Postiz (#128) is still blocked** on its image-internal-path gaps (`nonroot-data-ownership — postiz`), not on Redis — its `type: redis` dependency is now first-class for any redis-needing app, but the app itself waits on userns-remap or a non-root upstream image.
- **The deferred Tier-1 pieces remain** (`NEXT.md` # Managed-service lifecycle gaps): grace-shutdown after the last consumer uninstalls (services stay running today, Redis included), cross-version migration, and at-rest encryption of the stored superuser + per-app passwords (plaintext today — the Redis superuser password and per-app ACL passwords fold into the same `NEXT.md` # App-secret injection hardening gap).
