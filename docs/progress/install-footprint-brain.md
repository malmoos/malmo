# Install footprint: catalog `Entry` + install-plan `footprint`

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** No design change — `APP_STORE.md` # Catalog schema already promises the coarse per-app `footprint`, and `BRAIN_UI_PROTOCOL.md` # install-plan already specs the box-specific `footprint` block; this realizes both in the skeleton. Two doc reconciliations: `BRAIN_HOST_PROTOCOL.md`'s `GET /v1/system/status` example now shows the `data_disk_free_bytes` / `data_disk_total_bytes` fields the footprint reads, and `BRAIN_UI_PROTOCOL.md`'s install-plan "makes no host calls" line is corrected to "no *mutating* host calls" (the footprint does read-only host + Docker queries).

Closes issue #70 (the middle link of #68, "on-disk footprint before install"). #69 landed the data model — object-form `images` with per-image sizes, `storage.estimated_size`, and the derived `(*Manifest).Footprint()`. This issue serves that data on two surfaces: the **coarse footprint** on every catalog browse `Entry` (the store grid's upper-bound number), and the **box-specific install-plan `footprint`** (sharper — images already pulled on this box are subtracted, plus the live free-space reading). #71 (the UI that renders both) was blocked on this.

## What was done

### Coarse footprint on `catalog.Entry` (`internal/catalog`)

- **`Entry.Footprint manifest.Footprint`** — `List()` now sets it from `man.Footprint()` (the derived sum from #69). Pure data hoist; the catalog list already parses each manifest, so there is no extra I/O. This is the same number for every box — the catalog-grid upper bound, before any local-cache subtraction.

### `estimated_size` byte parser (`internal/manifest`)

- **`(Storage) EstimatedSizeBytes() (int64, bool, error)`** — parses `storage.estimated_size` ("10GB", "1.5GB", "512MB", "4096") to bytes with a **tri-state** return: unset → `(0, false, nil)`, valid → `(n, true, nil)`, malformed → `(0, false, err)`. The `bool` is what lets the install plan *omit* `estimated_state_bytes` (rather than report a misleading 0) when the manifest declares no estimate. Binary units throughout (`GB = 2³⁰`), matching the spec example `estimated_size: "10GB"` → 10 737 418 240.

### Free-disk bytes on the host protocol (`internal/protocol`, `internal/hostagent`)

Free space on the data drive is a genuine host concern (`statfs` on `/srv/molma`) — the one piece of the footprint the brain can't compute itself. Rather than a new endpoint, the existing disk-summary carrier grew two fields:

- **`SystemStatus.DataDiskFreeBytes` / `DataDiskTotalBytes`** — advisory; `0` means "not measured". Doc comment ties them to `Bavail × Bsize` / `Blocks × Bsize`.
- **`hostagent.DiskReporter` seam + `Agent.Disk` field** — consumer-side interface (CLAUDE.md), mirroring the `RAMReporter` pattern from #38. The system-status handler reads it nil-guarded.
- **`internal/hostagent/diskusage`** — the real `linux`-tagged `syscall.Statfs` reporter on `/srv/molma`, **fails open** to `(0, 0)` + `slog.Error` so a missing data drive never bricks status. `FakeDiskReporter` (canned 412 GiB free / 1 TiB) wires the fake agent.

### `lifecycle.InstallFootprint` (`internal/lifecycle`)

The transaction owner computes the box-specific estimate (it owns both the Docker driver and the host client; the API layer must not reach past it):

- **`InstallFootprint{DownloadBytes, ImageDiskBytes, EstimatedStateBytes, HasEstimate, FreeBytes}`** + `(*Manager) InstallFootprint(ctx, *manifest.Manifest)`. It walks `man.Images`, and for each **subtracts images already present on this box**: `imagePresent` inspects the digest-pinned ref (`repo@sha256:…`) via `DockerDriver.ImageInspect` — present (no error) ⇒ skip, absent or blank-digest ⇒ count. So an app that reuses a base image you already have reads as cheaper.
- **Granularity is image-level, not layer-level** — forced by the data shape: the catalog carries per-image sizes, not per-layer. A safe over-estimate (shared base layers across two *new* images are double-counted), documented in the function comment.
- **Degrades, never fails** — a malformed estimate is swallowed (→ `HasEstimate false`), a host-status error swallows to `FreeBytes 0`, and the image figures survive either. The footprint is advisory; it must never fail an install plan.
- **`HostDriver` interface** grew `SystemStatus` (the `hostclient.Client` already satisfied it).

### Wired into the install-plan DTO (`internal/api`)

- **`InstallPlanDTO.Footprint InstallPlanFootprint`** — `{download_bytes, image_disk_bytes, estimated_state_bytes?, free_bytes}`. `estimated_state_bytes` is a `*int64` with `omitempty`, set only when `HasEstimate`, so it is absent (not 0) when the manifest declares no estimate.
- **`buildInstallPlan` stays pure** (permissions + scope menus, trivially unit-testable); the box-specific footprint is attached in the `installPlan` handler via `s.life.InstallFootprint(ctx, man)`, because it queries Docker + the host.
- **Regenerated `api/openapi.{json,yaml}` + `web-ui/src/generated/openapi.ts`** — the wire types are committed artifacts; `make openapi` + `npm run gen:api` (no `npm install`) keep CI's freshness gates green. Mechanical; the UI that consumes them is #71.

## How it maps to the specs

- **`APP_STORE.md` # Catalog schema** — the coarse per-app `footprint` now rides every browse `Entry`.
- **`BRAIN_UI_PROTOCOL.md` # install-plan** — the box-specific, incremental `footprint` block (image bytes minus what's already pulled, the parsed state estimate, live free space) is realized; the "advisory" framing held (read-only, degrades to zeros).
- **`BRAIN_HOST_PROTOCOL.md` # system/status** — `data_disk_free_bytes` / `data_disk_total_bytes` added to the example.
- **`APP_MANIFEST.md` # Storage** — `storage.estimated_size` is now parsed to bytes.

## Known gaps & deviations

- **Image-level, not layer-level subtraction.** Two *new* images sharing a base layer count that layer twice — a safe over-estimate. Layer-level would need per-layer catalog data (`docker manifest inspect` blob sizes), which #69 deliberately didn't carry.
- **`free_bytes` from the fake agent is canned** (412 GiB / 1 TiB). The real `diskusage` reporter does `statfs` on `/srv/molma`, but that path only exists once the real host-agent + data-drive assembly land (still VM-deferred).
- **Footprint sub-failures are silent to the user.** A Docker or host hiccup degrades the numbers to zeros (logged at the brain) rather than surfacing "couldn't estimate" — the consent screen just shows a conservative figure. Whether the UI should distinguish "0 because cached" from "0 because we couldn't measure" is a #71 question.
- **No caching.** Each install-plan fetch re-inspects every image against Docker. Fine at this scale (install-plan is a deliberate user action, not a poll); revisit only if it shows up.

## What's next

1. **#71 — done in PR #95** (`install-footprint-ui.md`). The store-card `~size` and the consent-dialog Storage block (download line, space line, not-enough-space warning) landed on this branch before this PR merged.
2. **Real `diskusage` exercise** — once the VM host-agent + `/srv/molma` assembly exist, confirm the `statfs` reporter against a real data drive.
