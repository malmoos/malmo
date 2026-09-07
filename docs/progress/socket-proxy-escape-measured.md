# Socket-proxy escape measured; allowlist confirmed minimal; docs corrected

- **Status:** done
- **Date:** 2026-09-07
- **Specs touched:** docs/specs/CONTROL_PLANE.md, docs/specs/THREAT_MODEL.md

Closes the investigation half of #430. The network fix stays with #187 (see What's next). Follows the control-plane container hardening in [control-plane-container-hardening.md](control-plane-container-hardening.md) (#441), which hardened the containers themselves; this entry is about what the socket-proxy *lets through*, which capability dropping does not touch.

## What was done

**Measured the severity on a real box (step 1 — the highest-value output).** Ran `tecnativa/docker-socket-proxy:v0.4.2` with the exact `proxyAllowlist()` set, raw socket mounted read-only, `cap_drop: ALL` + `no-new-privileges` (the post-#441 posture). From a `curl`-only container on the proxy's network — an app `main_service` stand-in — `POST /containers/create` with `Privileged:true` + `Binds:["/:/host"]` was accepted (201), `POST /containers/<id>/start` was accepted (204), and the container wrote to the **host** `/tmp` as **uid 0**. Full write-up and the exact responses are on #430.

Conclusion: this is the worse of the two branches the issue named — a container escape to host root, reachable from any compromised app with nothing but `curl`. `v0.4.2` has no separate `ALLOW_START`; `CONTAINERS=1` covers `/start`. The proxy filters by URL prefix + method only and never reads bodies, so `Privileged`/`Binds` pass straight through.

**Confirmed the reachability premise.** The proxy runs on `cfg.Network` = `malmo-ingress` (`cmd/host-agent-real/main.go:202`); app `main_service` containers join `malmo-ingress` (`internal/lifecycle/lifecycle.go:1692`). So the exposure is live on the current tree.

**Confirmed the allowlist is already minimal (step 4).** Checked each family against real brain paths: `IMAGES`/`NETWORKS` (app + control-plane lifecycle, plus #187's own `docker network connect`), `VOLUMES` (the caddy `caddy_data` volume and managed-DB data volumes are created by `docker compose up` through the proxy), `CONTAINERS`/`POST` (everything). Nothing is safely droppable, and the escape needs only `POST`+`CONTAINERS`+`IMAGES` — so narrowing cannot close it.

**Corrected the two false claims (step 5).**
- `internal/hostagent/brainlaunch/brainlaunch.go` — the `proxyAllowlist` comment said "host-bind mounts stay denied (the proxy defaults them off)". That control does not exist; the proxy never parses bodies. Rewritten to state the real posture: `CONTAINERS`+`POST` is an escape for anyone who can reach `:2375`, and the only control is network reachability (#187). `EXEC` genuinely denied by omission — kept and explained.
- `docs/specs/CONTROL_PLANE.md` # Locked: Docker socket exposure — the "arbitrary host mounts on `POST /containers/create` are denied" line carried the same wrong claim. Rewritten with the body-blind reality, the measured escape, and #187 as the fix.

**Updated `THREAT_MODEL.md` B2 (step 6).** The "Container escape to host root" row no longer reads as absolute — it notes admission covers an app's *own* compose only. The "Brain↔Docker control-plane abuse" residual, previously "—", now records the live gap and its fix. The blast-radius summary carries the one caveat that holds it open until #187.

**Added a guard test.** `TestProxyAllowlistIsMinimal` (`internal/hostagent/brainlaunch/brainlaunch_test.go`) pins the exact allowlist and fails if `EXEC` ever appears, sending anyone who changes it back to #430. Passing.

**Re-triaged #187.** Commented with the measurement and removed its stale `blocked` label (its only named dep, #165, is closed).

## How it maps to the specs

Exercises `CONTROL_PLANE.md` # Locked: Docker socket exposure and `THREAT_MODEL.md` B2 — bringing both in line with what the proxy actually enforces (URL/method, not body). No locked decision flips: the proxy stays the brain's only Docker path; what changes is an honest statement of what it does and does not bound. No `DECISIONS.md` entry.

## Known gaps & deviations

- **The escape is not closed here — by design.** The only structural control on a body-blind proxy is who can reach it, and that is #187's network change (apps off `malmo-ingress`, Caddy connects outward). This entry measures and documents; it does not fix.
- **No "app cannot reach `docker-proxy:2375`" regression test yet.** It cannot pass until #187 closes the network — asserting non-reachability while apps share `malmo-ingress` would fail. It belongs with #187's medium-lane assertions, alongside the existing `malmo-caddy:2019` check, as a separate assertion so removing one exposure cannot silently leave the other. Noted on both #430 and #187.
- The measurement was run against real Docker (28.1.1) with the pinned proxy image, not on a booted malmo box. The setup replicated the production proxy args exactly; the missing piece a real box would add is the app container arriving on `malmo-ingress` through the brain, which the code paths above confirm.

## What's next

1. **#187** — apps off `malmo-ingress`; Caddy connects outward into per-app networks. This is the fix for the measured escape. When it lands, add the `docker-proxy:2375` non-reachability assertion to its medium-lane checks.
