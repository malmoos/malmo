# Trimmed first-run wizard + the steps' thin backend (C4)

- **Status:** done
- **Date:** 2026-06-19
- **Specs touched:** `FIRST_RUN.md` (per-profile Phase 2 step set; Step 3 as-built browser-`Intl` detection), `TIME.md` (# System TZ first-run mechanism)

Part of the cloud-VM track in #196, closing #208. Follows [hosted-setup-gate.md](hosted-setup-gate.md) (C3a #206 — the seed + the `/setup` admin-bootstrap gate this wizard's admin step forwards to) and [hosted-profile-marker.md](hosted-profile-marker.md) (C1a #202 — the `profile` the wizard reads to pick its step set). This is **C4**: it grows the M1c placeholder `Setup.vue` (password-only, hardcoded `admin`, recovery code) into the real multi-step first-run wizard and adds the thin backend the new steps need. The wizard is the **shared step shell** bare-metal **B4** reuses — B4 prepends the appliance-only Network/Enrollment steps without reworking the framework.

## What was done

### Backend — the steps' thin seams

- **Time-zone host-agent seam (`internal/protocol` + `internal/hostagent` + `internal/hostclient` + `cmd/host-agent-real`).** New `protocol.SetTimezoneRequest{timezone}`; a `timezonemgr.LinuxTimezoneManager` provider that shells `timedatectl set-timezone` (no build tag — `os/exec`, like `usermgr`); `hostagent.Agent` gains a consumer-side `TimezoneManager` interface + `Timezone` field + the `POST /v1/system/set-timezone` route. The handler **shape-validates** the zone (≤64 chars, no `..`, no leading/trailing `/`, `[A-Za-z0-9_+/-]` only) before delegating, and keeps the fake's nil-guard fallback (accept + log when no manager is wired). `hostclient.SetTimezone` is the brain-side caller. Wired in the **shared** `cmd/host-agent-real/main.go` (not behind `-tags hosted`) — timezone applies to both profiles.
- **Box config in `internal/store` (`box_meta`).** Two typed accessors over the existing KV table: `TelemetryConsent`/`SetTelemetryConsent` (bool via `strconv`, **defaults off** — TELEMETRY.md locked) and `FirstRunComplete`/`SetFirstRunComplete` (defaults false, latches true). A shared `getBoxMetaBool` treats an unset key as false rather than an error.
- **API (`internal/api`).** `GET /auth/state` (public, pre-auth) now also returns `profile` and `first_run_complete` so the wizard can route before any session exists; an unset profile reports `appliance` (mirrors `profile.Read()`'s default). Three new admin-only endpoints: `POST /system/timezone` (422 on blank, 502 on host failure), `POST /telemetry/consent {enabled}` (204), `POST /first-run/complete` (204, idempotent). These are **box config, not elevation-class** — no audit, no 5-minute re-elevation; the wizard's fresh admin session reaches them directly. `/setup` gained an optional `skip_recovery_code` (default false ⇒ appliance path byte-unchanged; true ⇒ empty recovery hash and `recover()` fails closed — an empty hash never matches a supplied code).
- **OpenAPI** regenerated (`make openapi`); the web-ui client regenerated (`npm run gen:api`). The `/auth/state` response is now a named `AuthStateBody` schema (was the anonymous `Auth-stateResponse`), so `api.ts`'s `AuthState` alias was repointed.

### Frontend — the wizard (`web-ui/src/`)

- **Shell (`Setup.vue`).** Holds the step cursor and renders one step at a time; each step emits `next`. The step list is **profile-aware** and structured for B4 to prepend appliance steps: today both profiles run {Admin (+recovery), Time zone, Telemetry, Done}. On a **resumed** wizard (admin already exists — a prior session, or an upgraded box predating the marker) the admin step is skipped, since `/setup` would 409.
- **Step components (`web-ui/src/setup/`).** `AdminStep` — real username + password (drops the hardcoded `admin`), the recovery toggle (on by default; turning it off forces the Step 2a confirmation copy + an explicit acknowledgment) with the once-only recovery-code reveal + "I have saved this" gate, and on **hosted** a setup-secret field forwarded to the C3a-gated `/setup`. `TimezoneStep` — browser-`Intl` detected zone pre-selected in a full IANA picker (`Intl.supportedValuesOf`), POSTed to the timezone seam. `TelemetryStep` — one off-by-default checkbox + the locked PostHog "What does this collect?" disclosure (TELEMETRY.md # Backend choice). `DoneStep` — latches the marker via `finishFirstRun`.
- **Gate (`App.vue` + `auth.ts`).** `auth.ts` reads `profile` + `first_run_complete` at bootstrap, and `finishFirstRun()` posts the marker then flips into the dashboard (centralizing the flip keeps the gate honest across every step). App.vue shows the wizard while **first-run is incomplete** (Phase 3) — not merely while no admin exists — with a session guard so an already-provisioned but logged-out box still lands on Login, not the wizard's admin-gated steps: `!firstRunComplete && (!hasUsers || currentUser)`.

### Tests

- `internal/api`: `firstrun_test.go` (auth-state `profile` + `first_run_complete` progression; telemetry-consent persist/admin-gate/401; first-run-complete latch-idempotent/admin-gate/401), timezone cases in `system_test.go` (204 + value reaches the host, 422 blank, 403 member, 401 unauthenticated, 502 host failure), `skip_recovery_code` in `auth_test.go` (empty code + empty hash + `recover` fails closed). The shared harness gained a `set-timezone` recorder. `internal/hostclient`: `TestSetTimezone`. `internal/store` + `internal/hostagent`: consent/marker round-trips + the `timedatectl` handler matrix (done in the backend slice). New-code coverage is >90% (the few uncovered lines are defensive store-500 / malformed-value error returns). `make check` and `make check-web` green.

## How it maps to the specs

- Realizes `FIRST_RUN.md` Phase 2 for the hosted profile (`ENVIRONMENT.md` # Provisioning — "Setup wizard, trimmed"): the surviving steps {first admin + recovery, time zone, telemetry, done}, the dropped {network, storage, secure-URLs/enrollment}. The per-profile step set and the as-built Step 3 detection were written into `FIRST_RUN.md` in this change; `TIME.md` # System TZ updated to match.
- The first-run-complete marker realizes `FIRST_RUN.md` # Phase 3 ("the wizard never reappears") as a flag distinct from `has_users` — the admin is created mid-wizard, so "an admin exists" is not "first-run is done". This is the bootstrap-complete marker the cloud e2e lane (C5) asserts.
- Telemetry follows `TELEMETRY.md`: one off-by-default toggle covering both streams, the mandated PostHog disclosure copy.

## Known gaps & deviations

- **Time-zone detection diverges from the original spec.** `FIRST_RUN.md` / `TIME.md` specced IP-geolocation auto-detect; as built it's browser-`Intl` (product decision — no geo-IP backend dependency, works offline, the setup device shares the box's locale on the LAN). Both spec docs were updated to the as-built mechanism in this PR. No NTP/geo-IP backend work was added.
- **Identity is username-direct, not the display-name→slug model.** `FIRST_RUN.md` # Identity describes a display name mapped to a slug; the codebase's `/setup` + `createUser` take a username directly (a pre-existing divergence from M1c). C4 matches the codebase — real username, hardcoded `admin` dropped — and does not build the display-name model.
- **Telemetry persists consent only.** The transmission pipeline (batching, the `telemetry.malmo.network` client, the rotating install ID) is out of scope; this writes the consent the future client gates on.
- **Appliance now shows Time zone + Telemetry.** Both-profiles steps run on appliance too (they always did per spec); the appliance-only Network/Enrollment steps that bracket them are **B4**, not built here. A pre-marker box with an existing admin will, on upgrade, click once through {Time zone, Telemetry, Done} (admin step skipped) before the marker latches — in practice only dev boxes, reset via `make clean`; no production appliance boxes predate the marker.
- **Not yet exercised end-to-end on a booted VM.** The cloud QEMU drive of the full wizard is C5; the outer loop is #189-blocked on this box.

## What's next

- **C5 — cloud e2e.** Drive the wizard end-to-end in the QEMU cloud lane and assert the first-run-complete marker.
- **B4 — bare-metal wizard steps.** Reuse this shell; prepend the appliance Network/WiFi + storage + secure-URLs/enrollment steps ahead of the admin step.
- **Settings → Privacy / System → Time.** Surface the telemetry toggle and timezone override in Settings (the wizard's choices are the founding admin's one-time defaults; both are "always overridable later" per spec).
