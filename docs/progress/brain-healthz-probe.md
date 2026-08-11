# The brain answers `/healthz` so the updater can tell live from reverted

- **Status:** done
- **Date:** 2026-08-11
- **Specs touched:** docs/specs/BRAIN_UI_PROTOCOL.md

Third implementation slice of the update work designed in [#369](https://github.com/malmoos/malmo/pull/369), after [ghcr-control-plane-images.md](ghcr-control-plane-images.md) gave stream B a registry and [system-version-whole-box.md](system-version-whole-box.md) made the box report what it runs. Closes #378.

## What was done

`UPDATES.md` # 3 step 3d has host-agent waiting up to 60s for `/healthz` on a freshly recreated brain, and reverting **both** control-plane images if the wait times out. The endpoint did not exist. The brain's whole HTTP surface was `/api/v1/*` behind session auth plus two hosted-only raw routes, so there was nothing an out-of-band caller could ask "are you serving?".

`GET /healthz` now answers 200 with `ok`, and three properties are load-bearing rather than incidental:

- **Outside `/api/v1`.** It is an operational probe, not a product surface: no versioning promise, absent from `api/openapi.yaml`, absent from the generated TS client. Registered on the raw mux next to the other non-huma routes.
- **Public.** The caller is host-agent, which drives the box and has no session to present. The answer discloses nothing — "this process is serving HTTP" is what the TCP accept already told the caller. Reach is narrow anyway: Caddy proxies only `/api/*` and `/_malmo/*` to the brain, so the path is not routable from the LAN.
- **Exempt from the per-IP throttle.** Plane 2 allows 30 req/min/IP and the updater polls once a second for up to 60s. Without the exemption the wait would `429` at the halfway mark, the updater would read that as "the new brain is unhealthy", and a **working update would be reverted by the rate limiter**. The handler writes a fixed body and touches nothing, so serving it costs about what refusing it would.

**Liveness, not readiness, and that is the whole design.** The handler checks nothing — not SQLite, not Docker, not Caddy, not host-agent. Its only consumer reverts an update when it fails, so a dependency check would roll back a healthy new brain because some unrelated subsystem was sick, and roll it back to an old brain facing the same sick subsystem. Dependency health is a different question with an existing answer: `GET /api/v1/health` (`HEALTH.md`), admin-gated and structured.

## How it maps to the specs

- `UPDATES.md` # 3 step 3d / # 8.4 — this is the probe the update transaction turns on.
- `BRAIN_UI_PROTOCOL.md` — new # API discipline paragraph "Operational probes live outside the API", the plane-2 exemption noted in # Rate limiting & abuse, and a locked-decision bullet. The # Public-API posture rule ("no internal routes outside `/api/v1/`") is addressed head-on rather than quietly bent: that rule bans routes the *dashboard* uses that an external caller cannot, and the dashboard never calls this one.

## Known gaps & deviations

- **No consumer yet.** Nothing polls it until the update transaction (#380) lands. This slice only makes the probe exist.
- **Not wired as a Docker `healthcheck:`.** The brain is launched by `docker run` from `brainlaunch`, and changing that run spec belongs with the updater slices that already rewrite it. So Docker still considers the brain healthy the moment the process starts.
- **Liveness only, deliberately.** A caller that wants "is this brain *usable*" — store open, Docker reachable — does not get it here and should read `GET /api/v1/health`. If the updater ever needs a stronger signal than "serving", that is a new endpoint with a new name, not a quietly stricter `/healthz`.
- **Not exercised on a real box.** The tests drive the real middleware chain (auth allowlist, rate limiter, mux) through `httptest`, and the throttle-exemption test was mutation-checked — with the exemption removed it fails at request 30 with a 429 — but no VM boot ran. #382 is where a real host-agent probes a real recreated brain.
- **The all-nil server in the test is the dependency assertion.** `NewServer(nil × 10)` means a future dependency check in this handler panics the test rather than silently turning the updater's commit signal into a report on an unrelated subsystem. It is a blunt instrument and it is meant to be.

## What's next

1. **#379 — the ledger + staged-compose rewrite.** The declaration half of the updater, and the place where "the brain is not in the compose" gets an answer.
2. **#380 — the update transaction.** Pull by digest, snapshot SQLite, write the declaration, recreate, probe (this endpoint), revert both on failure.
3. **#381 — the trigger**, and **#382 — the QEMU proof**.
