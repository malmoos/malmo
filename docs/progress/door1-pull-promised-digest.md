# Door-1 installs pull the catalog-promised digest, not the tag

- **Status:** done
- **Date:** 2026-07-16
- **Specs touched:** docs/specs/APP_LIFECYCLE.md (# Locked: image digest pinning — rewritten to match APP_STORE.md), docs/specs/DECISIONS.md (2026-07-16 entry)
- **Issue:** #331 (closes).

## What was done

A Door-1 install of Nextcloud failed in the field with:

```
resolve digests: catalog digest mismatch for nextcloud:34.0.1-apache:
catalog promised sha256:6444fc54..., registry served sha256:e15c14f8...
```

Both digests were real, valid images. Docker Hub had rebuilt `library/nextcloud:34.0.1-apache` two days earlier to absorb base-image package updates: same Nextcloud version, same Dockerfile steps, same Debian base layer, 21 of 22 layers new. Nothing was wrong with the catalog or the registry, and the install was refused anyway.

The cause was a **contradiction between two specs**, with the implementation following the wrong one. `APP_STORE.md` # Trust model says the box pulls by digest and an upstream tag move "doesn't affect it"; `APP_LIFECYCLE.md` said the brain "refuses to install if the locally-resolved digest after `docker pull` doesn't match the catalog's promise". `resolveImages` implemented the latter: pull the tag, read the RepoDigest, compare, fail on difference.

`APP_STORE.md` has the better semantics and now wins in both the code and the prose.

Changes:
- **`internal/lifecycle/pinning.go`**
  - `pullAndResolve` — when a digest is held (the catalog promise, or an author who pinned `name@sha256:…` in the compose), it is now the *address*: the pull is `repoOf(image)@<digest>` and the tag is never consulted. The digest-ref and promise paths were two branches doing the same job and are now one.
  - The mismatch comparison loop in `resolveImages` is **gone**, not relocated: a content-addressed pull cannot return other bytes, so there was nothing left to compare. `ImageInspect` is no longer called on the Door-1 path at all.
  - New, narrow failure: a compose pinned to `name@sha256:…` whose digest disagrees with the `images` promise for the same ref. That is the catalog contradicting itself — a curation bug with no safe pick — as distinct from upstream drift, which is now a non-event.
  - Door-2 / TOFU is untouched: an empty `Images` map means no promise, so the tag is pulled and its resolved digest is trusted on first use.
  - The offline path is untouched in behavior and got simpler in premise: it already trusted the promise, and the online path now agrees with it instead of diverging.

- **`internal/lifecycle/lifecycle_test.go`**
  - `TestInstallDigestMismatchRollsBack` **deleted** — it asserted a state that is now unreachable.
  - `TestInstallDoor1IgnoresUpstreamTagMove` — the regression test for this issue: the tag resolves to different bytes than the catalog pinned, and the install must succeed, pull the promised digest, pin it, and record it. Verified to fail against the pre-fix `pinning.go` with the exact production error above.
  - `TestInstallDigestPromiseContradictionRollsBack` — covers the new curation-bug failure, including clean rollback.
  - `TestInstallHappyDoor1` — now asserts `Pull → ComposeUp` (no `ImageInspect`) and that the *only* ref pulled is the digest.

- **`internal/lifecycle/fakes_test.go`** — added `fakeDocker.pullErrAll` and `pulled()`. The offline tests expressed "registry unreachable" as `pullErr[testImage]`, keyed by the **tag**; once Door-1 pulls a digest ref, a by-ref key silently lets the pull succeed against a registry the test says is gone. An unreachable registry is a property of the box, so it is now expressed as one.

## How it maps to the specs

- APP_STORE.md # Trust model ("The brain pulls by `@sha256:...` derived from the catalog"): now true of the code. ✓
- APP_STORE.md # Failure modes ("Image digest changes upstream between catalog publish and box pull: the box pulls by digest, so the upstream's new bytes don't affect it"): the scenario this issue hit, now behaving as specified. ✓
- APP_LIFECYCLE.md # Locked: image digest pinning: rewritten. The refuse-on-mismatch sentence is replaced with the promise-as-address model, plus why tags are mutable in practice and why curation, not the box or the clock, decides when a promise moves. The stale "pulls and verifies against it" phrasing in the offline paragraph went with it. ✓
- APP_LIFECYCLE.md # Locked: install transaction step 6 ("Pull images, resolve digests, rewrite override"): unchanged and still accurate — Door-2 still resolves. ✓

## Notes

- **`DECISIONS.md` entry added (2026-07-16).** The first draft of this change argued none was needed — that it merely restored what `APP_STORE.md` had always locked, so nothing flipped. That was wrong. The code matched the 2026-06-16 (#167) entry exactly, whose `Now` clause reads "a box *with* a registry is unchanged: it pulls and verifies as before"; this change falsifies that sentence, and the section rewritten here is titled `Locked:`. A decision flipped. The `APP_STORE.md`-vs-`APP_LIFECYCLE.md` contradiction explains how the code got there; it does not make the flip a non-event.
- **Byte-identical installs are a new guarantee in practice.** Before this, two boxes installing the same catalog version could run different images depending on when they installed. They can no longer diverge.
- **Pinning is only as fresh as curation makes it**, and that is now the whole of the mechanism: nothing upstream reaches a box unless a promise is deliberately updated. Noticing that upstream moved is a catalog-build concern and is deliberately not the box's job.
- The registry keeping untagged manifests addressable is now load-bearing. `APP_STORE.md` # Failure modes already calls this out ("If the digest was *deleted* from the upstream registry (rare — most registries keep digests addressable), the install fails with a registry-side error"), and that remains the accepted trade.
