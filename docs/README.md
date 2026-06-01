# malmo docs

The map of all documentation. Three homes:

- **[`specs/`](specs/)** — design source of truth. What malmo *is* and the
  locked decisions behind it. Read the relevant spec end-to-end before changing
  behavior in that area.
- **[`progress/`](progress/)** — implementation log. ADR-style entries recording **what was done** and **what's next** for each unit of work.
- **[`dev/`](dev/)** — developer how-to. Running locally, code-level
  architecture, tooling.

Every change ships with documentation (see [`../CLAUDE.md`](../CLAUDE.md) #
Documentation discipline).

**New contributor?** Start with [`dev/contributing.md`](dev/contributing.md) — the
end-to-end loop (orient → pick a task → branch → build → test → document → PR).
Actionable parallel work lives in [GitHub Issues](https://github.com/onel/malmo/issues)
(`gh issue list --label P1`).

## Specs

`specs/` holds the design docs. [`specs/SPEC.md`](specs/SPEC.md) is the entry
point; the full annotated list (what each doc owns and its headline decisions)
is the **Documents** section of [`../CLAUDE.md`](../CLAUDE.md). Cross-references
inside the specs are bare filenames, relative to `specs/`.

Orientation:

- **Start here:** `SPEC.md`, `CONTROL_PLANE.md`.
- **Apps:** `APP_LIFECYCLE.md`, `APP_MANIFEST.md`, `APP_STORE.md`, `APP_ISOLATION.md`, `SERVICE_PROVISIONING.md`.
- **Protocols:** `BRAIN_UI_PROTOCOL.md`, `BRAIN_HOST_PROTOCOL.md`.
- **Frontend:** `WEB_UI.md` (stack/deploy), `DASHBOARD.md` (logged-in IA + the owner-scoped apps model), `FILES.md` (in-dashboard file manager).
- **System:** `STORAGE.md`, `BOOT.md`, `DISCOVERY.md`, `MALMO_NETWORK.md`, `TIME.md`, `USERS_AND_GROUPS.md`, `AUTH.md`.
- **Operations:** `UPDATES.md`, `RELEASE_MANIFEST.md`, `BUILD.md`, `TESTING.md`, `HEALTH.md`, `LOGGING.md`, `TELEMETRY.md`, `LOCAL_ANALYTICS.md`, `NOTIFICATIONS.md`, `FIRST_RUN.md`.
- **Cross-cutting:** `THREAT_MODEL.md`, `DECISIONS.md` (decision log), `NEXT.md` (open questions).

## Progress

[`progress/README.md`](progress/README.md) is the canonical record: the full
build-ordered index of every entry plus the **Up next** queue (next
implementation slices). It's the single source of order — this map deliberately
doesn't duplicate the list.

## Dev guides

- [`dev/contributing.md`](dev/contributing.md) — the contributor loop: orient,
  pick a task from [GitHub Issues](https://github.com/onel/malmo/issues), branch
  off `main`, build, test, document, PR into `main`. Read this first if you're new.
- [`dev/running-locally.md`](dev/running-locally.md) — run the whole stack
  natively (no VM), and the two-loop dev model.
- [`dev/testing-brain.md`](dev/testing-brain.md) — six-layer test plan for
  `malmo-brain` (unit → store → lifecycle-with-fakes → API → integration
  → e2e). Companion to `specs/TESTING.md`, which covers boot-level lanes.
- [`dev/code-review.md`](dev/code-review.md) — the review standard: what to read before reviewing, 12 lenses (correctness, security, spec fidelity, Go discipline, audit completeness, test coverage, documentation honesty, scope, migration safety, error quality, dependencies, commit hygiene), severity levels, and output format. Used by the pre-PR self-review step and the dedicated review agent.
