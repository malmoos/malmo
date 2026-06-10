# Real /proc sampler for host-agent (live system resources)

- **Status:** done
- **Date:** 2026-06-10
- **Specs touched:** `LOCAL_ANALYTICS.md` # Real-time system resources and `BRAIN_HOST_PROTOCOL.md` # Pattern A — one source-file reconciliation each (`/sys/block/<dev>/stat` → `/proc/diskstats`, same counters in one read); everything else realized, not changed.

Closes issue #115. The live system-resources panel (the Activity icon in the top bar) was wired end-to-end — UI SSE → brain `systemlive` hub → host-agent `GET /v1/system/resources` — but the leaf returned *synthesized* counters in both binaries, so even a booted box showed invented numbers. This slice adds the real `/proc` sampler and injects it into the real binary only; the fake's synthetic counters stay as the inner loop's stand-in.

## What was done

### `internal/hostagent/procsource` (new, Linux-only)

A stateless sampler reading six kernel files under an injectable root (`/` in production, a fixture tree in tests) into one `protocol.SystemResources` of **raw cumulative counters** — the brain keeps all rate derivation (`BRAIN_HOST_PROTOCOL.md` # Pattern A):

- `/proc/stat` aggregate `cpu` line → `CPUCounters`. **TotalJiffies sums user..steal (the first eight fields), excluding guest/guest_nice** — the kernel already accounts guest time inside user/nice, so the issue's "sum of all fields" would double-count it and understate CPU% on a VM-hosting box. IdleJiffies = idle + iowait, per the issue.
- `/proc/meminfo` → `MemCounters` (KiB → bytes; Used = Total − Available, the same meaning the synthetic fields had).
- `/proc/loadavg` → `LoadAvg [3]float64`.
- `/proc/net/dev` → `[]NetCounters`, **filtered**: everything except `lo` and the container-side virtual interfaces (`docker*`, `br-*`, `veth*`). Physical NICs and the future mesh interface pass through — `LOCAL_ANALYTICS.md` reports "physical LAN NICs + the mesh interface only" (the issue's sketch said to skip mesh; the spec says report it, and the spec wins — `DISCOVERY.md`'s LAN-only rule is about *publishing*, not throughput reporting). Sorted by name for a stable wire order.
- `/proc/diskstats` → `[]DiskCounters` for **whole-disk devices only** (`sd*/hd*/vd*/xvd*` letters-only, `nvme<n>n<n>`, `mmcblk<n>`): partitions would double-count their parent disk; loop/dm/md/zram/sr are views over the same physical IO. Sectors × 512 (the kernel's `/proc/diskstats` sector unit is fixed at 512 regardless of the device's real sector size). Read from `/proc/diskstats` rather than per-device `/sys/block/<dev>/stat` files — same counters, one read; both specs reconciled.
- `/proc/uptime` → `UptimeS`; `ts_ns = time.Now().UnixNano()`.

Any read or parse failure fails the **whole sample** — a partial snapshot is worth less than none, since the brain's poller logs-and-skips a failed tick while keeping its previous rate baseline (`systemlive.poll`).

### Seam on `Agent` + real-binary wiring

- New consumer-side `hostagent.SystemSampler` interface (`Sample() (protocol.SystemResources, error)`) and `System` field on `Agent`, following the existing `Health`/`Disk`/`Logs`/`Resources` seam pattern. **When nil (the fake binary, tests), the synthetic monotonically-climbing counters serve unchanged**; when set, the handler serves the sampler's snapshot verbatim, and a sampler error is a 500 `sample-failed` — never a silent fall-through to synthetic, which would corrupt the brain's rate diff with a fake baseline.
- `cmd/host-agent-real/main.go`: `a.System = procsource.New()` next to the other reporters.
- The stale aspirational comment on the handler ("The real binary reads /proc and /sys") now describes the actual seam.
- `internal/systemlive`, the API layer, and the UI are untouched, per the issue.

## Verification

- **Parser tests** (`procsource_test.go`, fixture `/proc` tree, never touches the real one): full coherent fixture (jiffy sums incl. guest exclusion, KiB→bytes, loadavg, allowlist filtering with rx/tx column positions, whole-disk filtering with sector conversion, uptime truncation); `includeIface`/`wholeDisk` tables (`tailscale0` in, `docker_gwbridge`/`nvme0n1p2`/`zram0`/… out); short pre-2.6.33 cpu line; six loud-error cases.
- **Real-`/proc` tests** (the package is Linux-only, so `/proc` is always there): double-sample monotonicity (jiffies and `ts_ns` never go backwards — the property the brain's rate derivation needs) and allowlist enforcement against live interfaces/devices.
- **Seam tests** (`agent_test.go`, cross-platform stub): delegation serves the snapshot verbatim; sampler error → 500 `sample-failed`; the two pre-existing synthetic-fallback tests pass unchanged (fake behavior unmodified).
- **End-to-end smoke** (`systemresources_linux_test.go`): the exact `cmd/host-agent-real` wiring (`a.System = procsource.New()`) served over the real HTTP handler returns this box's actual counters and none of the synthetic signature values — the inner-loop equivalent of the issue's "open the panel on a booted box".
- Full non-PAM Go suite + gofmt + vet green locally; the real binary's compile is CI's job (no `libpam0g-dev` here).

## How it maps to the specs

- `LOCAL_ANALYTICS.md` # Real-time system resources — host-agent owns the host reads, applies the interface/device selection at the source, stays stateless, returns raw counters. # Interface and device selection realized as written (mesh included).
- `BRAIN_HOST_PROTOCOL.md` # Pattern A — the documented `GET /v1/system/resources` payload now carries real data on a real box; cold-start null-rate contract untouched because the brain still owns derivation.
- `CLAUDE.md` # Developing — `procsource` sits behind the Linux build tag on the host-integrated side of the inner/outer boundary; the cross-platform surface (fake agent, brain, UI) compiles and behaves exactly as before.

## Known gaps & deviations

- **`ts_ns` is wall-clock** (`time.Now().UnixNano()`, as the issue specified), not CLOCK_MONOTONIC — an NTP step between two polls could distort or null one tick's rates. The brain already tolerates a bad tick (skip/derive-next); a monotonic source would need a protocol-comment change and isn't worth it for a 1 Hz gauge.
- **The interface filter is name-pattern-based**, not `/sys/class/net/<if>/device`-backed physical detection. Exotic virtual interfaces outside the excluded prefixes (`tun*`, `wg*`) would be reported — acceptable, since a WireGuard-style interface *is* the kind of mesh the spec wants shown.
- **Jiffies and "sum of all fields" deviation from the issue text** — deliberate, documented above and in the package doc.
- **The real binary still can't be compile-checked on this dev box** (PAM); CI covers it.
- The deeper **Settings → System page** (full graphs, all interfaces/drives broken out) stays deferred per `LOCAL_ANALYTICS.md` # UI surfaces.

## What's next

- Nothing new from this slice. With #115 done, the live-resources chain is real end-to-end on a booted box; the deferred admin System page and the per-container "Activity Monitor" view remain tracked in `NEXT.md`.
