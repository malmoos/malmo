# Container image reclaim on uninstall

- **Status:** done
- **Date:** 2026-06-03
- **Specs touched:** `docs/specs/APP_LIFECYCLE.md` (# stop, start, uninstall — documented the uninstall-time image reclaim under the Delete teardown sequence). No `DECISIONS.md`/`NEXT.md` change — the post-uninstall reclaim was already scoped into the locked plan; `NEXT.md` # Container image cleanup keeps only the deferred periodic/update-orphaned sweep.

Closes issue #9. Image bytes accumulated on the OS drive after every uninstall: `compose down -v` removes volumes but never images, and malmo pins images by digest and keeps them **tagged** (`repo@sha256:…`), so they are not *dangling* — a plain `docker image prune` reclaims nothing. This adds a precise, targeted reclaim at uninstall time.

## What was done

**New `DockerDriver.RemoveImage(ctx, ref)`** (`internal/lifecycle/docker.go`) — the production `cliDocker` shells out to `docker rmi <ref>`, un-forced, where `ref` is the pinned `repo@sha256:…`. Un-forced is deliberate: if the image is still held (another tag, a stopped container), docker refuses and we treat it as best-effort rather than yanking bytes something else references.

**Reclaim on uninstall** (`internal/lifecycle/lifecycle.go`):
- `Uninstall` now captures the instance's pinned images (`store.GetInstanceImages`) **before** `store.Delete` cascades the `instance_images` rows away, then calls `reclaimImages` **after** the row is deleted.
- `reclaimImages` removes each captured `repoOf(image)@digest` **iff** no remaining installed instance references it. The "in use" set (`inUseImageRefs`) is built by walking `store.List()` + `GetInstanceImages` — because the reclaim runs after the row is gone, "referenced by another instance" is simply "present in any remaining row," so the self-exclusion falls out for free and the shared-image guard is exact.
- Both the image capture and the reclaim are **best-effort**: a failed read or a refused `rmi` is logged (`slog.Warn`) and never fails the uninstall, matching the existing "each teardown step is best-effort" philosophy. The pinned ref (not the bare digest) is the dedup/compare key, which is exactly what `docker rmi` removes — so two instances sharing `nginx@sha256:X` keep the image until the last one is uninstalled.

**Tests** (`internal/lifecycle/lifecycle_test.go`, with the existing `fakeDocker`): `TestUninstallReclaimsUnreferencedImage` (single instance → `RemoveImage` called with the pinned ref) and `TestUninstallKeepsImageReferencedByAnotherInstance` (two distinct catalog apps sharing one image → uninstalling the first does **not** reclaim; uninstalling the last referent does). `fakeDocker` gained a recording `RemoveImage`.

## How it maps to the specs

- `APP_LIFECYCLE.md` # stop, start, uninstall — the Delete teardown sequence now ends with the image reclaim, documented inline (the targeted-`rmi`-by-digest rationale and the best-effort/door-agnostic framing).
- `APP_LIFECYCLE.md` # image digest pinning — installs persist `instance_images` (service/image/digest); this is the first reader of that table at teardown.
- `NEXT.md` # Container image cleanup — unchanged: that entry was already scoped (when #9 was filed) to the deferred *periodic / update-orphaned* sweep, explicitly delegating the post-uninstall case to this issue. Left in place.
- `CLAUDE.md` # Go discipline — consumer-side interface (`DockerDriver` lives in `internal/lifecycle`); `slog` structured fields (`instance_id`, `image`, `err`); no new store surface (reclaim composes existing `List`/`GetInstanceImages`, avoiding a premature aggregate query for a handful of apps).

## Known gaps & deviations

- **Update-orphaned images are out of scope.** When an app updates, the previous image is kept 7 days then GC'd (`UPDATES.md`); that sweep — and any recurring `prune -a` — needs a scheduler/timer seam the brain doesn't have yet. Deliberately deferred (`NEXT.md` # Container image cleanup). `UPDATES.md` itself is unchanged: uninstall reclaim doesn't alter the update/rollback retention story, so editing that deferred area would be scope creep.
- **`UNIQUE`-collision aside.** Two instances of the *same* catalog app installed in the same wall-clock second collide on the timestamp-derived instance ID (pre-existing behavior); the shared-image test sidesteps it with two distinct catalog apps that share an image, which is also the more realistic shared-digest case.
- **N+1 store reads.** `inUseImageRefs` issues one `GetInstanceImages` per remaining instance. Fine for a home box's handful of apps; if instance counts ever grow, collapse to a single `SELECT DISTINCT image, digest FROM instance_images` store method.
- **"Keep data" uninstall** still isn't implemented (tombstone + archive is a follow-up); reclaim only runs on the implemented Delete path.

## Tests

- `go test ./internal/lifecycle/` green (the two new cases + the existing suite); `go vet ./...` and `gofmt` clean. The `DockerDriver` interface addition ripples to the single production impl (`cliDocker`) and the single fake (`fakeDocker`); `cmd/brain`'s narrow `fakeRestartReader` is unaffected.

## What's next

- **Periodic / update-orphaned image sweep** — the deferred `NEXT.md` # Container image cleanup item: a scheduled `prune`-style pass (cadence + retention) for images orphaned by updates, once the brain grows a timer/scheduler seam.
- **Single-query in-use lookup** — swap the per-instance loop for a `store` aggregate if instance counts ever make N+1 matter.
