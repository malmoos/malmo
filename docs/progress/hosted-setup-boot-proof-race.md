# Fix the hosted `/setup` boot-proof race; confirm the wildcard-TLS path is green

- **Status:** done
- **Date:** 2026-07-08
- **Specs touched:** `docs/specs/TESTING.md` (# Hosted cloud variant — the gate description brought up to the as-built SSO/403 reality); new `docs/dev/hosted-boot-proof.md` (runbook)

Closes the "pre-existing unrelated `/setup` 503 boot-proof failure reproduces on clean `main`" that `hosted-grow-root-disk.md` flagged as tracked separately. That failure — `make test-cloud-qemu` deterministically failing the `unseeded` scenario with `hosted /setup not disabled: … 503 (want 403)` — turned out to be a **boot-proof assertion race, not a box bug**. A second, louder-sounding symptom (a hosted box "never binds `:443` / gets no wildcard cert") was investigated in the same pass and **does not reproduce from current `main`**: it was a broken-build artifact, not a live regression.

## What was done

### The `/setup` assertion now rides through a transient 503 (`dev/cloud/cloud-assertions.sh`)

The `unseeded` boot polls `POST /api/v1/setup` expecting **403** (hosted disables `/setup`; the owner bootstraps via portal-to-box SSO, #275). The poll broke on any of `403|503|409|200`, so a **503 ended the loop and failed the proof**. But the serial diag proved the box was correct: the brain resolved `profile=hosted`, brought the stack up, installed the catch-all, bound its listener, and installed the dashboard route — and the `/setup` handler returns 403 unconditionally on hosted (`internal/api/auth.go`, no 503 path). The 503 was Caddy answering "no ready upstream for `/api`" during the first second after the four control-plane containers came up (the diag showed them all "Up <1–2 seconds"), before the brain's listener/route landed. The step was racing startup.

Fix: break only on a **definitive** brain answer (`403`, or the appliance-mode `409|200` the check is designed to catch and reject) and ride through a transient `502|503` — exactly as the `/api/v1/me` poll just above already does. A genuinely stuck `/setup` still fails: the loop exhausts its 30s window on the last 503 and the 403 assertion rejects it. One-line regex change plus an explaining comment; no box behavior touched.

### Verified the wildcard-TLS path is green (no code change)

The `:443`/wildcard symptom was disambiguated by running the build-and-boot CI job (`CI / Cloud image`, `publish=false`) on this branch: **both the `unseeded` and `seeded` boots passed** (`cloud end-to-end: PASS (boots: unseeded seeded)`). The `seeded` scenario is a hard assertion that `:443` binds *and* the brain logs `caddy: wildcard TLS configured`, so its pass is positive proof the apply-path works from current `main`. Cross-checked in the tree that every previously-known real-box root cause is already fixed: the `certificates.automate` naming (`hosted-wildcard-cert-automate.md`, #301), the seed-fetch keep-alive decision, and the static resolver (`cloud-image-live-onramp-fixes.md`). No product-code change was warranted.

### Documented the process so the next agent doesn't chase the ghost

- New `docs/dev/hosted-boot-proof.md` — the "where we are now" runbook for hosted HTTPS bring-up and the boot-proof: the happy-path flow (seed → `EnsureWildcardTLS` → `:443` bind, two-path cert model), the brain-log milestones to grep, a symptom → where-to-look table, and how to run the lane (CI vs local). Consolidates knowledge that was only in frozen progress snapshots.
- `docs/specs/TESTING.md` # Hosted cloud variant: replaced the stale `/setup` secret-gate description (`un-seeded ⇒ 503; seeded ⇒ wrong secret 401, correct secret 200`) — superseded by portal-to-box SSO in #275 and knowingly left stale by `cloud-wildcard-tls-assertion.md` — with the as-built gate the lane actually asserts (`/setup` ⇒ 403 on hosted; `/_malmo/sso` ⇒ 503 unseeded / 401 seeded-bad-token).

## How it maps to the specs

- `TESTING.md` # Hosted cloud variant now matches what `cloud-assertions.sh` asserts (SSO gate + `/setup` 403), and the wildcard-TLS application + `:443`-bind assertion it already described.
- Reinforces `ENVIRONMENT.md` # Networking & discovery (always-on wildcard HTTPS applied by the brain at boot) and # Admin bootstrap — as built (hosted `/setup` disabled; SSO is the bootstrap path).

## Known gaps & deviations

- **Real cert issuance is still not proven air-gapped.** The boot-proof green means config-applied + `:443` bound, not a live cert — that acceptance is a real provider box, unchanged by this work. The runbook states this explicitly so a green lane isn't misread as "HTTPS end-to-end proven."
- **The startup transient itself is unchanged** — the brief `/api` 503 window right after the stack comes up is normal startup, self-healing in ~1s; the fix hardens the *assertion*, it does not (and need not) remove the window.
- **No new box/brain/UI behavior.** This is a test-lane + docs change; the only executable edit is the assertion's break condition.

## What's next

- When a real provider box is next provisioned from a current image, fold its live-cert acceptance status into the runbook's real-box row so the air-gapped/live split has a single up-to-date record.
