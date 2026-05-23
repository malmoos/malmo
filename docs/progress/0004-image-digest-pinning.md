# 0004 — Image digest pinning (TOFU + catalog verify)

- **Status:** done
- **Date:** 2026-05-23
- **Specs touched:** `APP_LIFECYCLE.md`, `APP_MANIFEST.md`, `APP_STORE.md`

Makes installs byte-deterministic: every service ends up running against an
override that pins `image: name@sha256:…`. Door-1 (catalog) verifies the
resolved digest against the catalog's promise; Door-2 (user-pasted compose)
falls back to pure TOFU, since there is no external authority to compare
against.

## What was done

### Manifest schema (`internal/manifest`)

New optional `Manifest.Images map[string]string` — the catalog-promised
`image:tag → sha256:…` map (`APP_STORE.md` # Trust model). `Synthesize`
leaves it empty for Door-2 apps. Absent ⇒ TOFU.

### Resolver (`internal/lifecycle/pinning.go`, new)

`resolveImages(ctx, man, composeBytes)` parses each service's `image:` from
the verbatim compose, `docker pull`s each unique image, reads
`RepoDigests` via `docker image inspect`, and picks the digest whose repo
matches. For images already pinned by digest (`name@sha256:…`) it short-
circuits to that digest after a pull. The returned `[]servicePin` carries
both the original `image:tag` (kept for the SQLite row and human display)
and the resolved `sha256:…`.

**Door-1 verify** lives in the same pass: any image present in
`man.Images` must match the resolved digest exactly. Mismatch is a typed
error, surfaced as a failed job whose `step=resolving_digests` and
message names the image plus both digests.

### Install transaction (`internal/lifecycle/lifecycle.go`)

Resolution runs **before** override generation, not after. The spec
(`APP_LIFECYCLE.md` # install transaction) describes a write-then-rewrite
sequence ("5. generate override / 6. pull, resolve, rewrite"); we collapse
that to write-once-with-pins, which is functionally identical and avoids
emitting a transient unpinned override. The full new ordering:

```
4. writeInstanceDir
5. resolveImages + Door-1 verify + SetInstanceImages   ← new step
6. writeOverride(with pins) + writeEnv
7-12. network / route / up / health / flip            (unchanged)
```

Failure at step 5 (unpullable image, registry error, digest mismatch)
hits the existing rollback path: instance row deleted (FK cascade drops
the `instance_images` rows), instance dir removed, no Caddy/mDNS state.

`writeOverride` now takes a `[]servicePin` and emits
`image: <repo>@<sha256:…>` per service alongside the existing
`cap_drop`/`security_opt`/`networks`/`labels`. Override `image:` wins over
the base compose's `image: name:tag` via compose merge, which is exactly
the spec's contract.

### Persistence (`internal/store`)

New table:

```sql
CREATE TABLE instance_images (
    instance_id TEXT NOT NULL,
    service     TEXT NOT NULL,
    image       TEXT NOT NULL,   -- original image:tag from author compose
    digest      TEXT NOT NULL,   -- sha256:…
    PRIMARY KEY (instance_id, service),
    FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE
);
```

`Store.SetInstanceImages` writes the set transactionally (delete + insert)
so it's idempotent and ready to receive the next generation on update.
`GetInstanceImages` returns the rows ordered by service. FK + the existing
`PRAGMA foreign_keys=ON` mean uninstall and rollback both clean these up
without explicit deletes.

### Catalog (`catalog/whoami/manifest.yml`)

Added the real `traefik/whoami:v1.10.3` digest to the manifest's `images`
map so Door-1 verification actually runs through the live catalog. CI
resolution + the signed-JSON catalog stay deferred (`APP_STORE.md`).

## How it maps to the specs

- `APP_LIFECYCLE.md` # image digest pinning — realized: every install ends
  up running against a `compose.override.yml` that pins each service by
  digest. From the second `up` onward the compose is byte-deterministic.
- `APP_LIFECYCLE.md` # install transaction — step 5/6 collapsed into one
  write; see "What was done" above.
- `APP_STORE.md` # Trust model — the verify path against the catalog's
  `images` map is now enforced at install. The catalog itself is still
  hand-curated; CI + signing is the follow-up.
- `APP_MANIFEST.md` # one model, two doors — both doors converge on the
  same resolver; Door-2 just provides an empty `Images` map.

## Verified

Live, against the running dev stack:

- **Door-1 happy path** — uninstall + reinstall `whoami` through the new
  flow. Override contains
  `image: traefik/whoami@sha256:43a68d10…`; SQLite row in
  `instance_images` carries the same digest; app routes through Caddy as
  before.
- **Door-1 catalog mismatch** — hand-tampered the `images` map to a bogus
  `sha256:deadbeef…`. Install failed with `step=resolving_digests` and
  message "catalog promised … registry served …". No instance row, no
  instance dir.
- **Door-2 TOFU** — pasted compose with `traefik/whoami:v1.10.3` →
  resolved, pinned, and persisted under a `tofu-demo` slug. Runs healthy.
- **Unpullable image** — pasted compose with `nope-nope-nope/...`. Failed
  at `resolving_digests` with the underlying `pull access denied` error;
  no instance dir.
- **Migration on existing DB** — the running brain's pre-existing SQLite
  picked up the new table on startup without complaint; existing
  un-pinned `whoami` install kept running.

## Known gaps & deviations

- **One-generation rollback storage** is not yet wired. The spec calls
  for a "previous digest" kept for rollback during update; that lands
  with the update slice, which doesn't exist yet. The schema can grow a
  `generation` column or sibling `instance_images_prev` table when we
  get there.
- **Signed-JSON catalog + CI digest resolution** are still TODO. The
  `images` map is currently hand-edited in `manifest.yml` rather than
  resolved by CI at catalog-build time (`APP_STORE.md`).
- **No `docker compose pull` reuse**: we shell out to `docker pull`
  per unique image rather than using `compose pull`. The per-image
  loop is simpler given we also need each image's `RepoDigests`
  inspected.
- **Override is rewritten when a manifest lists no `images` map**: the
  pin still lands, but there is no external attestation. By definition —
  that's the TOFU semantics.

## What's next

1. **Update + rollback** — fetch new manifest/compose, save current
   override → `.prev`, snapshot data, re-resolve, swap on failure
   (`APP_LIFECYCLE.md` # update + rollback).
2. **`WEB_UI.md` component stack** — Tailwind 4 + shadcn-vue; surface
   splash/failed states, the digest-mismatch reason, and a
   `main_service` picker for multi-service Door-2 apps.
3. **Signed-JSON catalog + CI digest resolution** — replace the hand-
   edited `images` map with the publish pipeline from `APP_STORE.md`.
4. **VM outer loop** — begin the real host-agent.
