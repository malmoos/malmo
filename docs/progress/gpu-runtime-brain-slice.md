# GPU runtime wiring (brain slice) — `gpu: true` emits the platform override + hard capacity gate

- **Status:** done
- **Date:** 2026-06-12
- **Specs touched:** none here — the spec half landed separately (`7d2d0e6`: `BRAIN_HOST_PROTOCOL.md` # GPU capability query, `APP_ISOLATION.md` # GPU hard-gate reconciliation); this change implements it verbatim.

Closes the implementation half of #67. `permissions.gpu: true` was parsed (`internal/manifest`) and surfaced in the install plan, then silently dropped: `writeOverride` emitted no GPU stanza and there was no host GPU-capability query, so a GPU app ran on CPU and a no-GPU box failed late at `docker compose up`. This is the all-native inner-loop slice — brain + protocol + fake host-agent, fully unit-tested with no VM; the OS-image media stack, real `/dev/dri` detection in `cmd/host-agent-real`, and on-hardware VA-API verification are the outer-loop half, tracked in #125.

## What was done

- **`GET /v1/system/gpu` across the wire stack.** `protocol.SystemGPU` (`present` + `vendor` + `render_gid`), `hostclient.Client.SystemGPU`, and the matching method on lifecycle's `HostDriver` seam. One Pattern A probe answers both install-time questions: is there a usable GPU (the gate), and which render group opens it (the override stanza).
- **host-agent: new `GPUReporter` seam + handler.** Consumer-side interface on `Agent` (same shape as `DiskReporter`); when nil the endpoint reports `present: false` rather than erroring — "no detector wired" means "no usable GPU to offer", so the brain refuses instead of emitting an override against unknown hardware. The real `/dev/dri` scanner plugs in here in #125.
- **Fake host-agent reports a synthetic Intel iGPU.** `FakeGPUReporter` (settable, zero value = no GPU); `cmd/host-agent` wires `{present: true, vendor: intel, render_gid: 104}` so the whole path runs under `make dev`, with `MOLMA_FAKE_NO_GPU=1` flipping it to "no usable GPU" so the refusal path is exercisable in dev too.
- **Install capacity gate (`internal/lifecycle`, step 2b).** For a `gpu: true` manifest the brain queries the host right after admission — before the instance row, any Docker work, or the override exist, so a refusal has nothing to roll back. `present: false` → the new typed `ErrNoGPU`, whose message is what the failed install job shows ("this app needs a GPU, and no usable GPU was detected on this box"); a host error fails the install as a host fault rather than silently falling back to CPU. A `present: true` report carrying no render group (`render_gid: 0`, a malformed host answer) is rejected as a host fault at the same gate, so the override never `group_add`s GID 0 (the root group) onto the `cap_drop: ALL` container. The gate reads the manifest, so Door-2 custom installs are covered by the same line.
- **Override stanza (`writeOverride`), main service only.** `devices: /dev/dri:/dev/dri` + `group_add: <render_gid>` on `main_service`, merged with the existing shared-folder `group_add` and declared-devices lists (the `entry["group_add"]`/`entry["devices"]` assignments became append-then-assign so the three sources compose). Sidecars get nothing. v1 is the Intel iGPU / VA-API path; the identical stanza serves AMD later, NVIDIA (Container Toolkit + `deploy.resources.reservations.devices`) is a structurally different follow-on.

## Verification

- Seven new tests: lifecycle — stanza on `web` only across the multi-service `migrateJobCompose` fixture (`/dev/dri` bind + render GID 104, sidecars clean), no-GPU refusal is `errors.Is(err, ErrNoGPU)` with zero instance rows and zero Docker calls, a GPU-query host error fails the install without masquerading as the refusal, and a no-`gpu` app gets no stanza and triggers no host query; hostagent — nil reporter reports `present: false`, wired reporter round-trips and toggles; hostclient — the wire seam decodes the report.
- `make check` green (gofmt, vet, OpenAPI freshness — no brain API surface changed — full Go suite).
- Live shape check against real Docker on a box with an Intel iGPU: a throwaway compose project carrying the exact generated override shape (`cap_drop: ALL`, `user:` non-root, `devices: /dev/dri:/dev/dri`, `group_add: "992"` — the box's real render GID) boots under `docker compose up`; inside the container the supplementary groups include 992 and opening `/dev/dri/renderD128` for read succeeds, proving the `group_add` is what grants a cap-dropped container the render node.

## Known gaps & deviations

- **Dev loop on a box without `/dev/dri`:** the fake defaults to reporting a GPU, so installing a `gpu: true` app under `make dev` on a machine with no `/dev/dri` (e.g. some Docker Desktop VMs) passes the gate and then fails at `compose up` on the missing device path. Set `MOLMA_FAKE_NO_GPU=1` there to get the refusal path instead. The real agent never has this skew — it reports what actually exists.
- **No catalog app declares `gpu: true` yet**, so the path is exercised by tests and hand-written manifests only.
- The install-plan's advisory GPU-availability figure (`APP_ISOLATION.md` # GPU: "may additionally report") is not in this slice — the refusal is the contract; the pre-commit dashboard warning is a UI nicety that can ride a later install-plan change.
- `vendor` is reported but not yet dispatched on — v1 hosts only ever emit `intel`, and the DRI stanza is vendor-independent until the NVIDIA follow-on.

## What's next

- #125: real `/dev/dri/renderD*` + PCI-vendor detection and render-GID lookup in `cmd/host-agent-real`, the OS-image media stack (drivers, `render` group/udev), and on-hardware VA-API verification behind the same op.
- A first real `gpu: true` catalog app (hardware-transcoding media server is the natural candidate) to exercise the path end-to-end.
- AMD vendor reporting, then the NVIDIA runtime path, as the spec's explicit follow-ons.
