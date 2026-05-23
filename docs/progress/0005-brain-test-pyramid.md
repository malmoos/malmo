# 0005 — Brain test pyramid: DockerDriver refactor + Layers 1–3

- **Status:** done
- **Date:** 2026-05-23
- **Specs touched:** docs/dev/testing-brain.md

## What was done

Realized the high-leverage subset of the plan in
[`docs/dev/testing-brain.md`](../dev/testing-brain.md): the enabling
`DockerDriver` refactor plus the first three test layers. The brain now has a
real pyramid; per-PR `go test ./...` runs in <1s with no Docker daemon.

**Refactor (build order #1).**
- New `internal/lifecycle/docker.go` defines `DockerDriver` (Pull,
  ImageInspect, ComposeUp/Down/Stop, Inspect, NetworkCreate/Remove, PSManaged,
  RemoveContainersByInstance). `cliDocker` is the production impl — the
  previous inline `exec.Command("docker", …)` calls, moved verbatim.
- Same file promotes `caddy.Client` and `hostclient.Client` to the local
  interfaces `CaddyDriver` and `HostDriver`; concrete clients satisfy them
  unchanged. `Manager` now holds interface-typed fields.
- `Manager` gains a swappable `Admitter` (default `admission.Check`) and
  overridable `healthWait`/`healthPoll`. Lifecycle tests inject
  `admission.CheckStructure` and 200ms/20ms timings.
- `admission.CheckStructure` extracted from `Check` — same structural rules,
  no `docker compose config -q` shell-out. Iteration is sorted so rejection
  messages are deterministic for table-driven tests.

**Layer 2 — store tests (`internal/store/store_test.go`).** Real SQLite on
`t.TempDir()`. Covers migration idempotency across reopen, CRUD,
`SetInstanceImages` atomic replace, `GetInstanceImages` ordering, FK cascade
on delete, `SlugTaken`, `SetMDNSName`, and `SetState` on missing rows.

**Layer 1 — admission + manifest + lifecycle helpers.**
- `internal/admission/admission_test.go`: table-driven rejection rows for
  ports / privileged / cap_add / build / extends / network_mode / pid / ipc /
  userns_mode / absolute bind path / named volume (short + long form), plus
  the happy + relative-bind cases. Each row asserts the message names the
  service and the offending field.
- `internal/manifest/manifest_test.go`: `Parse` happy + per-required-field
  missing + unsupported `manifest_version`; `Synthesize` covers
  single-service inference, ambiguous services, bad main service, empty name,
  missing port, unusable slug, empty compose.
- `internal/lifecycle/helpers_test.go`: `repoOf` (with registry-port edge
  case, digest form), `digestOf`, `serviceImages`, `servicePin.PinnedRef`,
  and `Manager.allocateSlug` against a real store (reserved-skip, conflict
  fallback to `-2`, exhaustion, preferred-slugs order).

**Layer 3 — lifecycle scenarios with fakes
(`internal/lifecycle/{fakes,lifecycle}_test.go`).** A `newTestEnv` helper
wires `Manager` against `fakeDocker` + `fakeCaddy` + `fakeHost` + real temp
store/state dir + temp catalog. Scenarios:

1. Install happy Door-1 — state=`running`, override pins
   `traefik/whoami@sha256:…`, route flipped from splash → upstream, ordered
   `Pull → ImageInspect → ComposeUp` calls.
2. Install happy Door-2 — synthesized manifest, no `images:` catalog map.
3. Admission rejection — no SQLite row, no instance dir, no Docker/Caddy
   work attempted.
4. Digest mismatch — rollback clean, `ComposeUp` never called.
5. Unpullable image — rollback before per-app network / route work.
6. `ComposeUp` failure — full rollback including per-app `NetworkRemove`.
7. Health timeout — state=`failed`, instance dir kept, splash flipped to
   `failed`.
8. Uninstall when `ComposeDown` fails — every other teardown step still
   runs; SQLite row, route, mDNS, network, dir all gone.
9. Reconcile drift — running-no-containers → `ComposeUp` + route
   re-asserted; stopped-with-containers → `ComposeStop`; orphan → torn down
   via `RemoveContainersByInstance` + `NetworkRemove`.

Each scenario asserts end state + the one or two driver calls that actually
matter, per the doc's assertion-discipline note.

## How it maps to the specs

- Realizes [`docs/dev/testing-brain.md`](../dev/testing-brain.md) build-order
  steps 1–4 (the doc's recommended high-leverage subset).
- Exercises `APP_LIFECYCLE.md` install transaction shape, rollback semantics,
  the register-early-with-splash → flip pattern, and the reconciler.
- Exercises `APP_MANIFEST.md` admission rules (Tier-3 closed-by-default).
- No spec decisions flipped → no `DECISIONS.md` entry.

## Known gaps & deviations

- Layer 4 (HTTP API via httptest) — deferred per doc's "pay-for-itself"
  guidance.
- Layer 5 (real Docker integration, `//go:build integration`) — deferred.
- Layer 6 (bash + curl e2e canary) — deferred; manual verification stays
  the user-visible regression net for now.
- `cliDocker.RemoveContainersByInstance` is the one untyped escape hatch:
  it exists only for orphan reconciliation when the instance dir is gone.

## What's next

1. Wire Layer 6 (`dev/e2e/*.sh`) — codifies the verifications we keep redoing
   slice after slice, independent of any future refactor.
2. Layer 4 + Layer 5 when concrete pain (an API regression we'd have caught,
   or a Docker CLI surface drift) justifies them — not on principle.
3. `lifecycletest` extraction if a future package needs the helper outside
   `internal/lifecycle`; today the same-package test file is enough.
