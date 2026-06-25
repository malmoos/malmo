# MongoDB-compatible managed service — FerretDB v2 compatibility spike (Phase 0 gate)

- **Status:** done — **verdict: NO-GO for v1.** Phase 1 (the provider implementation) is **not** entered.
- **Date:** 2026-06-25
- **Issue:** #253 (Tier-1 managed service: MongoDB-compatible provider, FerretDB engine). This is the Phase 0 compatibility-spike *gate* the issue mandates before any provider code.
- **Specs touched:** `SERVICE_PROVISIONING.md` (post-v1 candidate back-reference), `NEXT.md` (the open-item home for the deferral).

## Why this exists

#253 proposes a managed `type: mongodb` service provisioned exactly like managed Postgres/MySQL/Valkey, with [FerretDB](https://www.ferretdb.com/) (Apache-2.0) as the engine underneath — mirroring the redis→valkey license substitution, because MongoDB Community Server is SSPL (on malmo's avoid-list). The issue gates the build behind a Phase 0 spike: *prove the engine actually runs the apps we want before building the provider.* The promotion bar (`SERVICE_PROVISIONING.md` # Catalog) is **3+ store apps that genuinely want it and actually work**, on a **shared instance with per-app isolation** (the contract every other Tier-1 service upholds). The issue is explicit: "If <3 apps work, this issue stops here … and we note it in `NEXT.md` rather than shipping a half-working type."

Two independent findings below each fail that bar. The verdict is **NO-GO**.

## What was done

Stood up FerretDB v2 by hand (the two-container production topology) and measured the engine directly, then cross-checked every measurement against primary sources (FerretDB docs, the `FerretDB/FerretDB` and `microsoft/documentdb` issue trackers, each candidate app's own docs/issues). The empirical run and the research agreed on every load-bearing point.

**Method, and the one deviation from the issue's Phase 0 plan.** The issue's Phase 0 asks to "smoke-test each candidate app: install, first-boot migrations, CRUD, login." I instead measured the **engine's hard ceiling** directly (change streams, oplog, replica set, transactions, and the authorization model — all via mongosh against the real engine) and derived each app's verdict from that ceiling plus the app's documented runtime requirements plus research (the per-app research verdicts were adversarially verified — a second agent tried to refute every "works"/"works-degraded" call). I did **not** boot all five apps. This is sufficient *here* because both NO-GO pillars are engine-level and boot-independent: a measured missing feature (e.g. change streams) makes a dependent app fail *without* booting it, and the isolation finding (Finding 2) is a property of the shared instance, not of any app. Booting an app could only ever upgrade a "blocked" to "works" — which the dispositive ceiling forecloses for the blocked apps — so it cannot change the verdict. The residual exposure is narrow: the two positive calls (LibreChat "works", Wekan "works-degraded") are evidence-derived, not boot-verified, so each *could* still hit an unrelated gap on a real boot. That residual risk is moot for the v1 decision because even both-confirmed they are only ~2 apps and neither can be isolated from the other on the shared instance.

**Engine stood up** (`docker compose`, on the dev box):
- Proxy: `ghcr.io/ferretdb/ferretdb:2` (53.6 MB, **stateless** MongoDB-wire proxy on `:27017`; built-in healthcheck `["CMD","/ferretdb","ping"]`).
- Backing store: `ghcr.io/ferretdb/postgres-documentdb:17` (2.31 GB, PostgreSQL 17 + Microsoft's MIT-licensed DocumentDB extension; the persistent volume + `POSTGRES_PASSWORD` live here; **no** healthcheck of its own).
- Proxy → store wired by one env var: `FERRETDB_POSTGRESQL_URL=postgres://<u>:<pw>@<store>:5432/postgres`.
- Wire version advertised: **MongoDB 7.0.77**, topology **standalone** (`hello.isWritablePrimary=true`, no `setName`).
- Client used for all probes: a self-built **`mongodb-mongosh` 2.8.3** image (Apache-2.0; see "Provisioning client" below).

### Finding 1 — the compatibility ceiling (measured + corroborated)

| MongoDB feature | FerretDB v2 | How verified |
|---|---|---|
| CRUD, single-field + TTL/partial indexes, basic aggregation (`$group`, `$match`, `$lookup`) | ✅ works | `insertOne`/`find`/`createIndex`/`aggregate $group` all `ok` |
| Authentication (SCRAM) | ✅ enforced | anonymous connect → `Unauthorized: Command find requires authentication` |
| **Replica set / `rs.status()`** | ❌ absent | `replSetGetStatus` → `CommandNotFound`. v2 "replication" is **PostgreSQL WAL streaming**, invisible to Mongo drivers ([docs](https://docs.ferretdb.io/guides/replication/)) |
| **Oplog (`local.oplog.rs`)** | ❌ none | empty collection, no tailable data. v1.18 had basic oplog; **v2 regressed** (storage is DocumentDB now) ([documentdb#81](https://github.com/microsoft/documentdb/issues/81), closed not-planned) |
| **Change streams (`watch()` / `$changeStream`)** | ❌ unsupported | `db.coll.watch()` → `CommandNotSupported: Stage $changeStream is not supported yet in native pipeline`. Blocked at the engine ([documentdb#50](https://github.com/microsoft/documentdb/issues/50), open; maintainer comment 2026-06-24: "targeted for **Q3 2026**") |
| **Multi-document transactions** | ❌ unsupported | `commitTransaction` → `CommandNotFound` ([FerretDB#1547](https://github.com/FerretDB/FerretDB/issues/1547)/[#1548](https://github.com/FerretDB/FerretDB/issues/1548)) |

Change streams (and the replica set they presuppose) are the decisive gap: they are unimplemented in the DocumentDB engine FerretDB v2 sits on, with an upstream target of **Q3 2026 at the earliest** on someone else's roadmap. Anything that calls `.watch()` or needs an oplog/replica-set fails.

### Finding 2 — per-app isolation cannot be provided (measured + corroborated) — the decisive one

malmo's managed-service contract guarantees **per-app isolation on a shared instance**: each app sees only its own database, enforced by standard role/grant mechanics (`SERVICE_PROVISIONING.md` # Per-app isolation in shared instances — Postgres roles, MySQL `GRANT ... ON <db>.*`, Valkey ACL users). FerretDB v2 **cannot** provide this.

Measured, reproducibly, across two scoped users on one shared instance:
- `db.createUser({user, pwd, roles:[{role:"dbOwner", db:"app1"}]})` succeeds — but the user is created in **`admin`** (`app1@admin`), not scoped to `app1`. DocumentDB auth is a single global PostgreSQL-role namespace, not MongoDB's per-database `system.users`.
- A user scoped `dbOwner` on `app1` then **read, wrote, and listed collections in a different app's database (`app2`)** — full cross-app access.
- A user created with only the `read` role successfully **wrote**. Roles are accepted by `createUser` but **not enforced**.
- The FerretDB proxy exposes exactly one relevant flag — `--auth` (authentication on/off, confirmed on). There is **no authorization/RBAC flag at all**; the gap is fundamental, not a misconfiguration.

Net: **every authenticated Mongo user has full read/write to every database on the instance.** The per-app credential authenticates but does not isolate. The research corroborates verbatim ("FerretDB v2's auth model is NOT MongoDB's … no per-database user store, no `dbOwner`/`readWrite` enforcement; users are PostgreSQL roles" — FerretDB v2 [authentication docs](https://docs.ferretdb.io/security/authentication/)).

This is a *worse* boundary than managed Valkey: Valkey at least blocks cross-app destruction and documents shared-keyspace reads as deferred hardening; here it's unrestricted cross-app read **and write** to a store apps use for their primary data. The only way to isolate apps on FerretDB is one dedicated instance per app — which defeats the shared-instance model that defines Tier 1, at ~2.3 GB of Postgres+DocumentDB per app.

### Per-app verdict (the demand bar)

The ledger (`docs/dev/catalog-import-gaps.md`) records **no** Mongo `blocks-start` bail yet, so demand is prospective. Assessing the issue's named start-set plus a broader scan of genuinely-Mongo-only, home-relevant apps:

| App | Genuinely needs Mongo? | Home-relevant? | Verdict on FerretDB v2 | Blocking feature |
|---|---|---|---|---|
| **Rocket.Chat** (the marquee app the issue names) | mongo-only | yes | ❌ **blocked** | change streams + replica set (required since v1.0, no single-node/poll fallback) |
| **LibreChat** | mongo-only | yes (high demand) | ✅ **works** | — (its one transaction site is gated behind a `supportsTransactions()` probe with a non-transactional fallback) |
| **Wekan** | mongo-only (Meteor) | yes | ⚠️ **works-degraded** | no oplog/change-streams → Meteor falls back to poll-and-diff (functional, ~10 s reactivity latency, worse CPU under load) |
| **UniFi Network Application** | mongo-only | low (prosumer Ubiquiti) | ❌ **blocked / unverified** | aggregation gaps (`$group`+`$first`) + proven instability on FerretDB; no primary-source report of stable v2 operation; Mongo-version-pinned |
| **NodeBB** | **mongo-optional** (also Postgres/Redis) | yes | ✅ works, but **doesn't count** | — (malmo would serve it managed Postgres; it doesn't establish Mongo demand) |
| Habitica | mongo-only | yes | ❌ blocked | Mongoose multi-document transactions |
| Appsmith | mongo-only | no | ❌ blocked | replica-set startup gate + change streams |
| Mongo-Express | mongo-only | no (admin GUI) | ✅ works | — (not a home app) |

The genuinely-Mongo-only, home-relevant apps that clear "works or works-degraded" come to **LibreChat (solid) + Wekan (degraded)** — and even those two cannot be isolated from each other on the shared instance (Finding 2). UniFi is unstable/unverified on v2, the marquee Rocket.Chat is hard-blocked, and NodeBB doesn't need Mongo at all. The bar (3+ that genuinely want it **and work** **and can be isolated**) is not cleared.

## Recorded for the record (so a future revisit doesn't redo it)

These were resolved during the spike and hold regardless of the go/no-go, for whenever the engine clears the bar:

- **Provisioning-client image + license.** `mongosh` is **Apache-2.0** ([LICENSE](https://github.com/mongodb-js/mongosh/blob/main/LICENSE)), but MongoDB ships it **only inside SSPL server images** (`mongo`, `mongodb/mongodb-community-server`) — none usable. FerretDB ships no production client image (its docs point at the SSPL `mongo` image). The clean path, verified working here, is a **small self-built image installing only the `mongodb-mongosh` apt package** (Apache-2.0; the analog of running `psql`/`valkey-cli` in the one-shot provisioning container). The community `rtsp/mongosh` image is an alternative.
- **Two-container compose topology.** First non-single-container managed service. Current GA is **FerretDB v2.7.0** (2025-11-10). Pin both, lock-stepped: proxy `ghcr.io/ferretdb/ferretdb:2.7.0`, store `ghcr.io/ferretdb/postgres-documentdb:17-0.107.0-ferretdb-2.7.0` (tag scheme `{PG-major}-{DocumentDB}-ferretdb-{FerretDB}`). **Readiness handle = the proxy** (its `/ferretdb ping` healthcheck verifies wire-serving *and* backing-store reachability); the persistent volume + DNS alias map differently (volume on the store, `:27017` + alias on the proxy) — so `serviceContainerName` would target the proxy while `writeServiceDir` provisions both services in one compose project.
- **DSN shape** (corrects the issue's assumed `mongodb://…:27017/<db>`): `mongodb://user:pw@<alias>:27017/<db>?authSource=admin&directConnection=true&retryWrites=false`. `authSource=admin` because the user is a global PG role (not scoped to `<db>`); `directConnection=true` because FerretDB presents as standalone (its own tooling sets it); `retryWrites=false` because retryable writes need transaction machinery FerretDB lacks (drivers default `true` → "Transaction numbers are only allowed on a replica set member" errors).
- **No code was changed.** A nice consequence: `internal/manifest/manifest_test.go`'s canonical *unknown-type rejection* case is `type: mongodb` — and that is now **correct and intentional**, not a placeholder. `mongodb` deliberately remains an unprovisioned type.

## What's next

- **Revisit when *both* upstream gates clear**, tracked in `NEXT.md` # Managed MongoDB (FerretDB engine) — evaluated, deferred:
  1. DocumentDB ships **change streams** ([documentdb#50](https://github.com/microsoft/documentdb/issues/50), targeted Q3 2026) — unblocks the reactive/marquee apps (Rocket.Chat, full-fidelity Wekan) and is the demand driver.
  2. FerretDB/DocumentDB gains an enforced **per-database authorization** model — without it the shared-instance isolation contract can't be met and the type can't ship as Tier 1 regardless of app compat.
- **Until then, no managed `mongodb` type.** Apps that need Mongo stay curation-rejects (none are in the catalog today); the prospective demand is recorded above so the next import that bails on Mongo can grep this entry.
- **If a single high-demand Mongo-only app (LibreChat) ever forces the issue before the gates clear**, the only viable shape is the `isolated: true` dedicated-instance escape hatch (`SERVICE_PROVISIONING.md` # Per-app isolation, "Not in v1") — a separate design, and still subject to the change-streams ceiling. Not recommended for v1.
