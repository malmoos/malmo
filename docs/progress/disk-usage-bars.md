# Per-disk storage usage bars in the system-resources panel

- **Status:** done
- **Date:** 2026-06-13
- **Specs touched:** `LOCAL_ANALYTICS.md`, `BRAIN_HOST_PROTOCOL.md`, `DECISIONS.md`

Closes #149. The system-resources panel (the top-bar Activity dropdown, `LiveResources.vue`) showed CPU, RAM, network I/O, and disk I/O *throughput* — but never how full the disks are. This adds a **Storage** section: a collapsed aggregate used/total bar that expands to one bar per volume (System, Data). It extends the disk reporting that [install-footprint-brain.md](install-footprint-brain.md) (#70) started — that change added `data_disk_free_bytes`/`_total_bytes` + the `hostagent.DiskReporter`/`diskusage` statfs seam for the install plan's single-drive free figure; this carves out the per-volume *display* view on top of it.

## The decision (spec-grounded)

The issue flagged the ownership of the `DataDisk*` fields as a "keep or deprecate" call. **Kept, untouched.** They back the install-plan footprint (`internal/lifecycle/footprint.go`) with specific semantics — the *data* drive only, `Bavail` net of the root reserve — and the new `Disks []DiskSpace` is a display superset that also covers the OS drive. Two consumers, two needs; folding them would churn the footprint path for no behavioural gain (`DECISIONS.md` 2026-06-13). The "Data" entry duplicates `DataDisk*` by design — a small redundancy cheaper than rewiring the install plan.

Two sub-decisions:

- **A new brain endpoint, not a reuse.** There was no brain→UI `system/status` proxy; the only consumers of host `SystemStatus` were the install-plan footprint and the boot check. The panel needs a **one-time poll** (storage fullness doesn't move at the live gauges' 1 Hz cadence), so the brain exposes `GET /api/v1/system/storage` (huma, all signed-in users, no role gate — host-level storage isn't per-user data, same posture as the live stream). The live SSE channel is untouched.
- **Level-0 detection by device id, not statfs success.** A box with no data drive still has `/srv/malmo` as a *directory on the OS drive* (`STORAGE.md`), so a successful statfs there is not enough to call it a separate volume — it would render a duplicate "Data" bar equal to "System". `diskusage` includes the Data entry only when `/srv/malmo`'s backing filesystem (st_dev) differs from `/`'s. A missing path or a same-device result fails open to "absent".

## What was done

- **Protocol (`internal/protocol/host.go`):** new `DiskSpace{Label, FreeBytes, TotalBytes}` + `Disks []DiskSpace` on `SystemStatus`. `DataDisk*` unchanged.
- **Reporter (`internal/hostagent/diskusage`):** `Reporter` now measures both `/` ("System") and `/srv/malmo` ("Data"); new `Disks()` returns one entry per present volume, omitting a volume whose statfs fails or that shares the OS drive's device. `DataDisk()` keeps its exact behaviour. The mount paths + an st_dev lookup are injectable fields so the present/absent/fail-open branches are testable without two real filesystems.
- **Agent seam (`internal/hostagent`):** new consumer-side `DiskSpaceReporter` interface + `Agent.DiskSpace` field; the `systemStatus` handler populates `Disks` (empty slice when no reporter is wired). New `FakeDiskSpaceReporter`. `cmd/host-agent-real` wires one `diskusage.New()` to both `Disk` and `DiskSpace`; `cmd/host-agent` (fake) wires two canned volumes (System 18/64 GiB, Data 412 GiB/1 TiB) so `make dev` shows both bars without a second drive.
- **Brain API (`internal/api/system.go`):** `GET /api/v1/system/storage` proxies `SystemStatus.Disks` to `SystemStorageDTO`; host read failure → 502. Regenerated `api/openapi.{json,yaml}` + `web-ui/src/generated/openapi.ts`.
- **UI (`web-ui/src/LiveResources.vue`):** a one-time `api.get("/system/storage")` on panel open (guarded so a fetch resolving after close doesn't leave stale bars); a Storage section below Memory — collapsed aggregate bar (Σused/Σtotal) with an expand chevron, expanding to one labelled bar per disk ("412 GiB free of 1 TiB"). `humanBytes` gained a TiB tier.

## Verification

- **`diskusage` (100% of statements):** both-present, Level-0 same-device (only System), data-path-missing, OS-device-error fail-open, data-statfs-fails-after-present (omitted not zeroed), OS-statfs-fails (only Data), and the real-`stat(2)` seam via `New()`.
- **`hostagent`:** `systemStatus` returns a non-nil empty `Disks` with no reporter; returns the reporter's two entries in order when wired.
- **`api`:** `system/storage` requires a session (401), maps the host's disks through for an admin, and is reachable by a member (no role gate).
- `make check` green (gofmt, vet, OpenAPI freshness, full Go suite); `make check-web` green (typecheck + build + generated-types freshness).

## Known gaps & deviations

- **Drive-by fix in `LiveResources.vue`:** pristine `main` already failed `vue-tsc` at the load-average line — an inline `.map` closure in the template defeats the `v-else` null-narrowing of `sample` (a latent error the web gate would reject). Hoisted into a `loadLine` computed so `make check-web` is green. Same file as the feature; called out here for honesty.
- **The euid-0 prod path is not exercised hermetically.** Like the install-footprint statfs, the real `/`+`/srv/malmo` reading on hardware is an on-box check; the unit tests cover the logic against temp dirs with an injected device id.
- **No "OS drive" / "data drive" wording in the bars** — the issue specified the labels "System" and "Data", which read fine in the panel; the user-facing `STORAGE.md` vocabulary ("OS drive"/"data drive") is not surfaced here.
- **Mergerfs aggregate only.** A multi-data-drive box reports the union's `/srv/malmo` total as one "Data" bar (mergerfs aggregates the statfs), not per physical disk — matching the user-visible "one data drive" model.

## What's next

- The deferred **Settings → System** admin page (`LOCAL_ANALYTICS.md` # UI surfaces) can reuse `GET /api/v1/system/storage` for a fuller per-volume view.
- A real per-physical-disk breakdown (SMART, per-drive bars) if the "we are not a NAS" line ever loosens — explicitly out of scope today.
