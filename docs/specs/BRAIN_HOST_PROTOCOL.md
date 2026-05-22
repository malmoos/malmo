# Brain ↔ host-agent protocol

> The wire-level contract between the malmo brain (in a container) and `host-agent` (running on the host with root). Companion to `CONTROL_PLANE.md`, `AUTH.md`, `SERVICE_PROVISIONING.md`.
>
> Covers transport, wire format, patterns, auth, versioning, and failure semantics. The reconciler pattern (desired-vs-actual state) that goes with this protocol is in `APP_LIFECYCLE.md` # "same reconciler pattern extends to all host-managed state."

## Scope

`host-agent` exists because the brain runs in a container and can't safely touch the host. host-agent does the host-side work the brain asks for:

- **mDNS / Avahi** publishing (per-app hostnames on the LAN).
- **Tier-2 native ops** — `systemctl` toggles, write `/etc/samba/smb.conf`, run `tailscale up`, edit `authorized_keys`, run `passwd`.
- **Disk / LUKS / TPM** — mount, format, smartctl probe, recovery-passphrase operations.
- **System updates** — `apt`.
- **Network configuration** — NetworkManager-backed: list/scan/connect/forget WiFi networks, DHCP vs. static IP per connection, primary-connection pinning, active-interface state. host-agent talks to NetworkManager over DBus; the brain talks to host-agent over this protocol.
- **Power** — shutdown, reboot.
- **Misc host state** — time zone, hostname, system summary.

Things that **don't** cross this protocol:

- **Docker daemon.** Brain talks to Docker directly via `docker-socket-proxy` (a separate sidecar that restricts the API surface). host-agent is not in the Docker path.
- **Caddy.** Runs as a container; brain manages it like any other container.
- **App-facing services** (Postgres, Redis, future background-job runner). Those are Tier-1 services apps consume; orthogonal to host-agent.

If a host capability isn't in the list above, it doesn't live behind host-agent. The boundary is *"touches the host's root filesystem, systemd, or apt."*

## Transport: UNIX socket

host-agent listens on a UNIX socket:

| Path        | `/var/run/malmo/agent.sock`          |
|-------------|--------------------------------------|
| Owner       | `root`                               |
| Group       | `malmo`                              |
| Mode        | `0660`                               |

The brain's container UID is a member of the `malmo` group. The socket is mounted into the brain's container; nothing else on the host can connect. The `malmo` group is **unrelated to `malmo-shared`** (the household-content group) — see `USERS_AND_GROUPS.md` # Group reference.

**Why UNIX socket over loopback TCP:**
- File-permission access control is kernel-enforced and stronger than any app-level token.
- No port allocation, no "what if something else binds to it first," no firewall config.
- Same Go HTTP-server stack works on it — `net.Listen("unix", ...)`.

## Wire format: HTTP/1.1 + JSON

Plain HTTP over the socket, with JSON request and response bodies. Versioned URL prefix: `/v1/...`.

**Why HTTP/JSON, not gRPC or custom binary:**

- **Debuggability is a first-class goal.** From any shell on the host:
  ```
  curl --unix-socket /var/run/malmo/agent.sock http://localhost/v1/system/status
  curl --unix-socket /var/run/malmo/agent.sock -X POST http://localhost/v1/system/reboot
  ```
  This is invaluable during spec/early-implementation iteration, for incident response, and for any future tinkerer-facing tooling.
- Brain already runs an HTTP server (for the dashboard); reusing the HTTP client stack on the brain side is trivial.
- No code-generation step; iterate on schemas at the speed of editing a struct.
- Stream-friendly via SSE (below) and upgradeable to WebSocket where bidirectional is needed (future — see "Web terminal").
- Performance at home-server scale is a non-issue. host-agent is not a hot path.

The tradeoff we accept: no automatic schema enforcement at the wire. We hand-write request/response Go structs and JSON-tagged fields. Acceptable for two binaries shipped together.

## API patterns

The protocol uses **two patterns**, with a clear rule for when each applies.

### Pattern A — Sync request/response

For anything that typically completes in **under ~5 seconds** and doesn't need progress reporting:

```
POST /v1/services/tailscale/enable
→ 200 OK
  { "enabled": true, "running": true }
```

```
GET /v1/system/status
→ 200 OK
  { "hostname": "cindy-zx9", "uptime_s": 84021, "disk_pressure": false, ... }
```

```
POST /v1/auth/verify-password
  { "user": "cindy", "password": "..." }
→ 200 OK
  { "valid": true }
```

Plain HTTP. The brain blocks on the response. Errors come back as HTTP status + JSON body with `code` and `message`.

**`/v1/auth/verify-password` is hit on every dashboard login** (brain delegates PAM verification rather than storing a password hash itself — see `AUTH.md` # Identity primitive). Implementation: host-agent runs PAM `authenticate()` with the supplied credentials, returns `valid: true|false`. The endpoint never reveals *why* a verification failed (wrong password vs. unknown user vs. locked account) — only the binary result, mirroring PAM's own posture. Rate-limiting lives in the brain; this endpoint just answers truthfully.

**Network endpoints (NetworkManager-backed).** host-agent exposes Pattern A routes that wrap NetworkManager's DBus surface:

```
GET  /v1/network/state
  → { "primary": "ethernet-1", "ethernet": [...], "wifi": [{ "ssid": "...", "connected": true, "signal": -52, "secured": true, "saved": true }, ...], "ipv4": {...}, "ipv6": {...} }

POST /v1/network/wifi/scan
  → { "networks": [{ "ssid": "...", "signal": -52, "secured": true, "freq_mhz": 5180 }, ...] }

POST /v1/network/wifi/connect
  { "ssid": "...", "password": "...", "hidden": false }
  → { "connection_id": "...", "state": "activated" }    (or error.code = "auth-failed" | "no-signal" | "timeout")

POST /v1/network/wifi/forget               { "connection_id": "..." }
POST /v1/network/connection/set-primary    { "connection_id": "..." }
POST /v1/network/connection/configure-ip
  { "connection_id": "...", "ipv4": { "mode": "dhcp" | "static", "address": "...", "gateway": "...", "dns": [...] } }
```

All network ops are Pattern A (NM DBus calls return promptly). State-change notifications from NM (signal level, connection drops, primary switch) flow as SSE on `GET /v1/network/events` so the dashboard can update live without polling. WiFi credentials are stored by NetworkManager itself (`/etc/NetworkManager/system-connections/`, mode `0600`, root-only) — the brain never sees the password after the `connect` call returns. The primary-connection pin (`connection.required-for-network-online=true`) is owned by `set-primary` — exactly one connection carries it at any time; see `BOOT.md` # NetworkManager.

**Discovery / mDNS endpoints (Avahi-backed).** host-agent owns the publisher; brain registers and unregisters per-app `.local` names. Pattern A:

```
POST /v1/discovery/publish
  { "slug": "photos" }
  → 200 OK  { "name": "photos.malmo.local", "state": "established" }
  (or error.code = "hostname-conflict" | "avahi-down" | "timeout")

POST /v1/discovery/unpublish
  { "slug": "photos" }
  → 200 OK

GET  /v1/discovery/state
  → { "publisher": "avahi", "host_name": "malmo", "renamed_to": null,
      "published": [{ "slug": "photos", "name": "photos.malmo.local", "state": "established" }, ...],
      "interfaces": ["eth0", "wlan0"] }
```

Implementation: `publish` writes `/etc/avahi/services/app-<slug>.service`, waits on Avahi's DBus for `EntryGroup.StateChanged → ESTABLISHED` (typically <1s), returns. `unpublish` removes the file. Both ops are idempotent — duplicate publish is a no-op, unpublish on an unknown slug returns 200. Avahi's RFC 6762 §9 conflict-resolution (host rename to `malmo-2.local`) surfaces in `state.renamed_to` and the brain raises `hostname-conflict` (`HEALTH.md`). The Avahi interface allow-list (`allow-interfaces=` in `/etc/avahi/avahi-daemon.conf`) is computed by host-agent from NetworkManager state at boot and on interface change — eth/wlan in, `tailscale0` / `docker0` / `br-*` out. See `DISCOVERY.md` for the full record model and gotchas.

**`enroll-drive` and `eject-drive` carry credentials inline** because host-agent verifies them via PAM as the first step of the job and uses them to authorize reading `/etc/malmo/secrets/luks-recovery.key`. The brain does not cache or forward the password beyond the single request. On invalid credentials the job fails immediately with `error.code = "auth-failed"`; otherwise host-agent proceeds with format → LUKS → TPM enrollment → mount → mergerfs add (enroll) or stop apps → unmount → marker removal (eject). Declared attributes: `Dangerous: true`, `ResourceClass: "disk"`, `MaxDuration: 10m`. See `STORAGE.md` # Adding a data drive and # Ejecting a data drive for the user-facing flow; `AUTH.md` # Roles for the fresh-password requirement.

### Pattern B — Jobs (long-running ops)

For anything that **may exceed 5 seconds** or that needs progress / cancel:

```
POST /v1/jobs/enroll-drive
  { "device": "/dev/sdb", "admin_user": "andrei", "admin_password": "..." }
→ 202 Accepted
  { "job_id": "j_77c1a3", "status": "running", "kind": "enroll-drive" }

POST /v1/jobs/eject-drive
  { "admin_user": "andrei", "admin_password": "..." }
→ 202 Accepted
  { "job_id": "j_88d2b4", "status": "running", "kind": "eject-drive" }

POST /v1/jobs/system-update
→ 202 Accepted
  { "job_id": "j_a4f7b2", "status": "running", "kind": "system-update" }

GET /v1/jobs/j_a4f7b2
→ 200 OK
  { "job_id": "j_a4f7b2", "status": "running", "progress": 0.42,
    "started_at": "...", "kind": "system-update" }

POST /v1/jobs/j_a4f7b2/cancel
→ 200 OK
  { "job_id": "j_a4f7b2", "status": "cancelling" }
```

Status values: `running`, `completed`, `failed`, `cancelled`, `cancelling`.

When `completed`, the response carries a `result` field with the operation's output. When `failed`, an `error` field with code and message.

**The rule:** *if a route can exceed ~5 seconds, it's a job.* Bias toward "make it a job" when uncertain — the job pattern is a strict superset (a caller can poll once with a long-poll if they want sync-ish semantics).

**Why not "everything is a job":** read-only / fast routes don't need the cognitive overhead of "is this done? where's the result?" and the extra JSON nesting. The dividing line is explicit per route and documented in the API contract.

### Pattern C — SSE (streaming log/progress output)

For one-way streams from host-agent to brain:

```
GET /v1/jobs/j_a4f7b2/log
→ 200 OK
  Content-Type: text/event-stream

  id: 1
  data: {"ts": "...", "stream": "stdout", "line": "Reading package lists..."}

  id: 2
  data: {"ts": "...", "stream": "stderr", "line": "..."}
```

Three primary uses:

1. **App container logs** (`docker logs -f` equivalents, surfaced in the app-details view in the dashboard).
2. **Long-running job output** — apt upgrade progress, image pull progress, install/update logs.
3. **Tier-2 service logs** — `journalctl -u smbd -f` for the SMB admin page, etc.

Browsers speak SSE natively. When the dashboard surfaces these streams, the browser can subscribe through the brain to host-agent's SSE stream end-to-end with no translation.

**Reconnect resilience.** Each emitted event has a monotonic `id`. host-agent keeps a rolling per-job buffer of the last ~256 KB of log output. On reconnect, the client sends `Last-Event-ID: <n>`; host-agent replays from the buffer starting at `n+1`. If the gap exceeds the buffer, host-agent emits a single `data: {"lost": true}` event and resumes from current. This is standard SSE reconnect — uses spec-defined mechanisms only.

### Pattern D — WebSocket (bidirectional, future)

Not in v1. Will be needed when the web terminal lands: an interactive PTY requires bidirectional I/O.

WebSocket is an HTTP upgrade. It runs over the same UNIX socket, the same Go HTTP server, with no new transport. When we build it:

```
POST /v1/terminal/sessions
→ 201 Created
  { "session_id": "t_abc", "ws_url": "/v1/terminal/sessions/t_abc/io" }

GET /v1/terminal/sessions/t_abc/io
→ HTTP/1.1 101 Switching Protocols
  Upgrade: websocket
  ← bidirectional frames carrying terminal I/O
```

The principle: **HTTP/JSON for ops, SSE for one-way streams, WebSocket for bidirectional.** Additive; no pre-design needed in v1.

A web terminal has independent security implications (root PTY = root on the host) that need their own design — tracked in `NEXT.md` and `AUTH.md`.

## Authentication & authorization

**Authentication = socket file permissions. There is no application-layer token.**

The kernel enforces it: anything not in the `malmo` group can't connect. Anything in the `malmo` group can do everything host-agent exposes. There's no per-caller authorization because the only caller is the brain.

**Test invariant (CI must assert):**

> The `malmo` group on the running system contains exactly one member: the brain's container runtime UID. Any additional member is a configuration error and fails the test.

This is the entire authn/authz model for this boundary. If group membership is wrong, the security boundary is broken; the test is the safety net.

If a future tool ever needs host-agent access (a debug CLI, a recovery tool), we either add it explicitly to the test allowlist *and* the `malmo` group, or it talks through the brain.

## Versioning: lockstep with OS release

Brain and host-agent ship as part of the same OS release. Brain version N talks to host-agent version N. There is **no protocol-version negotiation** at connection.

**Why lockstep:**

- The OS update is one atomic unit (per `UPDATES.md` v1 — Debian base + brain + host-agent + Caddy + Tier-2 packages). Both binaries are upgraded together.
- No version-negotiation code to maintain or get wrong.
- A crashed brain ↔ healthy host-agent imbalance is the only transient case; both binaries are tiny and can be upgraded together cheaply.

**Resolves an open item:** `NEXT.md` previously listed "brain ↔ host-agent protocol versioning" as open. Under lockstep, the question dissolves — there is no negotiation surface.

## Failure semantics

Four categories, each with its own mechanism. They're not one problem.

### A. Per-job declared attributes

Every operation host-agent exposes as a job declares static metadata. Not user-visible config — registration-time properties enforced by host-agent uniformly.

```
JobKind {
  Name           "system-update" | "app-install" | "disk-format" | ...
  MaxDuration    e.g. 30m for system-update, 60s for systemctl ops
  Dangerous      bool — crash mid-flight = no auto-resume (see APP_LIFECYCLE)
  ResourceClass  "apt" | "disk" | "systemd" | "network" | "none"
  StallPolicy    optional: "no progress for X = stalled"
}
```

**Timeouts.** host-agent enforces `MaxDuration` uniformly. Exceeded → status flips to `stalled` (distinct from `failed`). Cancellation runs SIGTERM → 10s grace → SIGKILL. Final result wins: if the op completes before SIGKILL, the job ends `completed` regardless of pending cancellation.

**Stalled vs. failed.** Distinct statuses. `stalled` means "we're not sure — it's running too long or producing no progress"; `failed` means "we know it broke." The UI surfaces these with different messaging — important for non-technical users.

**Resource-class serialization.** Two jobs sharing a `ResourceClass` cannot run concurrently. The second queues; job response carries queue position. Two `apt` operations can never race. `ResourceClass: "none"` ops have no serialization.

**Cross-class dangerous lock.** Any job with `Dangerous: true` waits for **all** running jobs (across resource classes) to drain before starting, and blocks any new jobs while it runs. Catches the case where, e.g., a disk format and an apt upgrade are technically different resource classes but you really don't want both at once.

**Registration is required-by-construction.** host-agent's job-kind registration function takes these attributes as required Go-typed parameters. You can't register an op without declaring them.

### B. Reconciler pattern (desired vs. actual state)

The companion mechanism to this protocol, specified in `APP_LIFECYCLE.md` (extends the existing app-lifecycle reconciler to all host-managed state).

Brief shape: brain models desired state in SQLite; host-agent exposes `GET /v1/state/summary` returning actual state; brain reconciles at three triggers — on startup, on a 60-second heartbeat, after every state-changing op. Drift policy: brain auto-reconciles when *it* made the last change (handles crash-mid-step); brain surfaces (doesn't auto-fix) when something *else* changed state (respects manual user changes via SSH).

Dangerous ops are excluded from auto-reconcile — interrupted `mkfs` is not safely retryable.

### C. SSE reconnect

Covered in Pattern C above. Self-contained: monotonic event IDs, ~256 KB per-job rolling buffer, `Last-Event-ID` on reconnect, single `{"lost": true}` event when the gap exceeds buffer. Uses SSE spec mechanisms only.

### D. Orchestration rules

Protocol-shaped rules about *when and how* the protocol is exercised. Not new protocol surface.

**host-agent self-update.** When the OS updater installs a new `malmo-host-agent` package:

1. Brain stops accepting new jobs.
2. Brain waits for running jobs to drain. Hard cap (5 minutes): if a job is still running, the OS update fails with "an operation is still running, retry later."
3. apt installs the new binary; systemd restarts host-agent.
4. Brain reconnects with backoff; resumes.

Brain treats "host-agent unreachable" during this window as expected, not as an error.

**FD limits.** host-agent's systemd unit sets `LimitNOFILE=16384`. Brain enforces ≤16 concurrent SSE streams at a time; host-agent enforces the same as a backstop.

**Concurrent dangerous ops.** Already covered by the cross-class dangerous lock in (A). Spelled out: never run two destructive ops concurrently. Ever. UI shows them as queued.

## Test invariants (CI)

Beyond the malmo-group membership assertion (above), CI asserts:

- Every registered `JobKind` has non-zero `MaxDuration` and an explicit `Dangerous` value (no defaults).
- A round-trip test for SSE reconnect: kill the brain mid-stream, restart, verify resume with the same `Last-Event-ID` recovers continuity (or emits `lost: true` if the buffer was overrun).
- A reconciler test: write a desired state to brain SQLite, simulate brain restart, verify reconciliation converges actual → desired for non-dangerous ops only.

## Locked decisions

- **Transport:** UNIX socket at `/var/run/malmo/agent.sock`, owner `root:malmo`, mode `0660`.
- **Wire format:** HTTP/1.1 + JSON, versioned URL prefix (`/v1/...`).
- **API patterns:** sync request/response (Pattern A) for <5s ops; explicit `Job` objects (Pattern B) for anything that can exceed ~5s or needs progress/cancel; SSE (Pattern C) for one-way streams; WebSocket (Pattern D) reserved for future bidirectional needs (web terminal).
- **Authentication:** socket file permissions only; no app-layer token. CI test asserts `malmo` group has exactly one member (brain's container UID).
- **Versioning:** lockstep with OS release. No protocol-version negotiation.
- **Out of scope for host-agent:** Docker daemon (brain talks to Docker via docker-socket-proxy), Caddy (managed container), Tier-1 app-facing services.
- **Debuggability is a first-class design constraint.** Choices that would make the protocol harder to debug from `curl` need an explicit justification.
- **Per-job declared attributes are mandatory.** Every `JobKind` declares `MaxDuration`, `Dangerous`, `ResourceClass`. Registration-time, type-enforced.
- **Stalled is distinct from failed.** Two job statuses, two UI tones.
- **Cancellation: SIGTERM → 10s grace → SIGKILL. Final result wins.**
- **Cross-class dangerous lock:** any `Dangerous: true` job blocks all other jobs while it runs and waits for all running jobs to drain before starting.
- **SSE reconnect: standard `Last-Event-ID` + ~256 KB rolling per-job buffer. Single `lost: true` event when the gap exceeds buffer.**
- **Reconciler pattern lives in `APP_LIFECYCLE.md`.** Drift policy: brain auto-reconciles when *it* made the last change; surfaces (doesn't auto-fix) when something else did. Dangerous ops excluded from auto-reconcile.
- **Heartbeat: 60 seconds.** Brain polls `GET /v1/state/summary`.
- **host-agent self-update drains all jobs first**; 5-minute hard cap before failing the OS update.
- **Network endpoints wrap NetworkManager over DBus.** host-agent is the only thing on the box that talks to NM. WiFi credentials live in NM's connection store (`/etc/NetworkManager/system-connections/`, root-only); the brain never persists them. See `BOOT.md` # NetworkManager and `DECISIONS.md` 2026-05-18.

## Knock-ons to other docs

- `CONTROL_PLANE.md` — points to this doc as the authoritative spec for the brain↔host-agent boundary.
- `AUTH.md` — the "Brain ↔ host-agent in the auth path" section is consistent with this protocol (private channel, no app-layer token); the malmo-group test invariant is now documented here.
- `SERVICE_PROVISIONING.md` — Tier-2 ops (systemctl, config edits) flow through host-agent via this protocol's Pattern A and Pattern B.
- `UPDATES.md` — apt operations are Pattern B (jobs with SSE log streams). The "brain ↔ host-agent protocol versioning" open item is resolved (lockstep).
- `NEXT.md` — carries the future web-terminal and app-facing-background-jobs items (failure semantics is now closed).
