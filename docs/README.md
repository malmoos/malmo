# malmo docs

The map of all documentation. Three homes:

- **[`specs/`](specs/)** — design source of truth. What malmo *is* and the
  locked decisions behind it. Read the relevant spec end-to-end before changing
  behavior in that area.
- **[`progress/`](progress/)** — implementation log. Numbered, ADR-style entries
  recording **what was done** and **what's next** for each unit of work.
- **[`dev/`](dev/)** — developer how-to. Running locally, code-level
  architecture, tooling.

Every change ships with documentation (see [`../CLAUDE.md`](../CLAUDE.md) #
Documentation discipline).

## Specs

`specs/` holds the design docs. [`specs/SPEC.md`](specs/SPEC.md) is the entry
point; the full annotated list (what each doc owns and its headline decisions)
is the **Documents** section of [`../CLAUDE.md`](../CLAUDE.md). Cross-references
inside the specs are bare filenames, relative to `specs/`.

Orientation:

- **Start here:** `SPEC.md`, `CONTROL_PLANE.md`.
- **Apps:** `APP_LIFECYCLE.md`, `APP_MANIFEST.md`, `APP_STORE.md`, `APP_ISOLATION.md`, `SERVICE_PROVISIONING.md`.
- **Protocols:** `BRAIN_UI_PROTOCOL.md`, `BRAIN_HOST_PROTOCOL.md`.
- **Frontend:** `WEB_UI.md`.
- **System:** `STORAGE.md`, `BOOT.md`, `DISCOVERY.md`, `MALMO_NETWORK.md`, `TIME.md`, `USERS_AND_GROUPS.md`, `AUTH.md`.
- **Operations:** `UPDATES.md`, `RELEASE_MANIFEST.md`, `BUILD.md`, `TESTING.md`, `HEALTH.md`, `LOGGING.md`, `TELEMETRY.md`, `LOCAL_ANALYTICS.md`, `NOTIFICATIONS.md`, `FIRST_RUN.md`.
- **Cross-cutting:** `THREAT_MODEL.md`, `DECISIONS.md` (decision log), `NEXT.md` (open questions).

## Progress

See [`progress/README.md`](progress/README.md) for the index. Latest:

- [`0006-auth-and-users.md`](progress/0006-auth-and-users.md)
  — first-admin bootstrap, password login, opaque cookie sessions, auth
  middleware gating all mutations, and a UI router that picks setup / login /
  dashboard from auth state.
- [`0005-brain-test-pyramid.md`](progress/0005-brain-test-pyramid.md)
  — DockerDriver refactor + Layers 1–3 of the brain test plan; per-PR
  `go test ./...` runs in <1s with no Docker daemon.
- [`0004-image-digest-pinning.md`](progress/0004-image-digest-pinning.md)
  — TOFU digest pinning + Door-1 catalog verify; installs are now byte-
  deterministic from second `up` onward.
- [`0003-door-2-and-admission.md`](progress/0003-door-2-and-admission.md)
  — paste-a-compose (Door-2) installs + the shared compose admission policy.
- [`0002-reconcile-and-health-wait.md`](progress/0002-reconcile-and-health-wait.md)
  — startup reconcile pass + health-wait and splash→real route flip.
- [`0001-walking-skeleton.md`](progress/0001-walking-skeleton.md) — first
  vertical slice: install/uninstall an app end-to-end through the real
  architecture spine.

## Dev guides

- [`dev/running-locally.md`](dev/running-locally.md) — run the whole stack
  natively (no VM), and the two-loop dev model.
- [`dev/testing-brain.md`](dev/testing-brain.md) — six-layer test plan for
  `malmo-brain` (unit → store → lifecycle-with-fakes → API → integration
  → e2e). Companion to `specs/TESTING.md`, which covers boot-level lanes.
