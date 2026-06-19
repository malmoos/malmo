# Per-instance resource limits (C6)

- **Status:** done
- **Date:** 2026-06-19
- **Specs touched:** `ENVIRONMENT.md` # Per-instance resource limits (wording synced to the locked runtime model), `APP_ISOLATION.md` # Resource limits (governing spec — implemented, not changed), `NEXT.md` (disk-quota deferral)

The **C6** slice of the cloud-VM track (#196, #211) and the first lifecycle seam to actually *branch on* the environment profile, building on [hosted-profile-marker.md](hosted-profile-marker.md) (C1a) — whose "what's next" flagged that the marker was read-and-logged but consumed by nothing yet. This is the first consumer: a per-app cgroup-limit policy on the install/reconcile transaction, with CPU capping gated to `hosted`.

## The spec tension (resolved before coding)

#211 and `ENVIRONMENT.md`'s first-pass wording said per-app limits are "sourced from the manifest's declared resources" and apply `--memory` / `--cpus` to "both profiles." That contradicts the **locked** `APP_ISOLATION.md` # Resource limits:

- **The manifest cannot impose a limit** — there is no `limit` field, by design (an author can't see the user's hardware). It declares only `recommended` specs (advice, never a ceiling). So the limit is **not** manifest-sourced.
- **CPU is never capped on the appliance** — it is time-shared; throttling only makes an app feel sluggish. The appliance's only cap is the **optional, default-off, user-set memory cap**.

Resolution (no locked decision flipped, so no `DECISIONS.md` entry): keep the manifest limit-free, make the mechanism **profile-divergent** — appliance caps **memory only**, hosted caps **memory + CPU** — and sync `ENVIRONMENT.md` to match. The control-plane API and user-cap UI that would *set* a policy do not exist yet; this PR builds the **store-backed policy + the setter seam + the application path**, nothing user-facing.

## What was done

- **`internal/store/` (new `resources.go` + migration)** — a new `instance_resource_limits` table (PK `instance_id` → `instances` `ON DELETE CASCADE`, `memory_bytes` + `nano_cpus`, both `DEFAULT 0`). `ResourceLimits` value type with `IsZero`; `GetResourceLimits` (a **missing row is the zero value, not an error** — the uncapped common case) and `SetResourceLimits` (upsert; the FK rejects an unknown instance). The cascade means uninstall reclaims the row with no extra teardown step.
- **`internal/lifecycle/` (new `resources.go`)** — `Manager.SetResourceLimits(id, lim)` is the **seam** a future control-plane push / user-cap UI calls: it validates non-negative, checks the instance exists (`store.ErrNotFound` surfaces), and persists. No production caller yet, by design. `resourceLimitsStanza(profile, lim)` renders the compose `deploy.resources.limits` block — memory in both profiles, `cpus` only when `profile == hosted` (a stray appliance CPU value is dropped, not rendered), `nil` when nothing applies (the app bursts freely). `reapplyResourceLimits(id)` surgically patches **only** the main service's `deploy` stanza in the already-rendered `compose.override.yml`, reporting whether it changed; `mainService(id)` reads the persisted manifest for the service name.
- **`internal/lifecycle/lifecycle.go`** — `Manager` gains a `profile` field (defaults to `Appliance`) + `SetProfile`; `writeOverride` takes the resolved `ResourceLimits` and stamps the stanza on the main service at install; `Reconcile`'s running branch re-applies the policy — on a **drifted** instance it patches before `compose up`, on a **live** instance it recreates the container **only when the stanza changed**, so a policy edit (or a clear) takes effect without a reinstall and an unchanged policy is a no-op.
- **`cmd/brain/main.go`** — wires `life.SetProfile(prof)` from the startup-resolved profile (the marker C1a reads), so the CPU-cap gate reflects the box's posture.
- **Docs** — `ENVIRONMENT.md` # Per-instance resource limits synced to the locked model (limit is user-/control-plane-sourced, not manifest; memory both profiles, CPU hosted-only; disk quota deferred). `NEXT.md` gains the disk-quota item.

## What's next

- **Disk quota (deferred, in `NEXT.md`).** The third hosted dimension. The locked `ext4` + `overlay2` stack can't enforce a per-container quota portably; the path is XFS project quotas through the host-agent (new protocol verb + `host-agent-real` op + a `disk_bytes` column on the same store seam). Filed as #221.
- **A caller for the seam.** `SetResourceLimits` persists a policy but nothing invokes it yet. Two future consumers: the appliance's user-set memory cap (`APP_ISOLATION.md` # Optional user-set cap → `DASHBOARD.md` / `LOCAL_ANALYTICS.md` app-hog signal) and the hosted control-plane policy push (`ENVIRONMENT.md` # Deferred commercial layer).
- **Immediate application.** Today a set policy lands on the next reconcile (brain restart) or the next drift recovery. A real caller will likely want to apply it inline (call `reapplyResourceLimits` + `compose up` for that instance) rather than wait for reconcile; that belongs with the caller, not this seam.
