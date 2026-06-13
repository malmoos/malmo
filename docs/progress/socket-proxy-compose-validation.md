# M1b spike — socket-proxy ↔ compose-CLI validation (escalated)

- **Status:** findings — M1b implementation is blocked on an architecture decision now owned by onel (#165 reassigned)
- **Date:** 2026-06-13
- **Specs touched:** none yet — this records the `CONTROL_PLANE.md` # Docker socket exposure validation #165 requires; the spec + `DECISIONS.md` edits land with the implementing PR

This is the de-risking spike for #165 (M1b — the brain brings up the control-plane stack), the next link after [host-agent-launches-brain.md](host-agent-launches-brain.md) (#164/M1a, which left the brain running degraded with no Docker reach). #165 carries an explicit "second spec-collision risk (socket-proxy vs. compose CLI)" and asks the implementer to "validate the socket-proxy ↔ compose-CLI integration ... widen the allowlist or escalate." That validation is done. It surfaced two collisions; one of them (managed-DB `docker exec`) is an architecture call above this issue's scope, so #165 is reassigned to onel with these findings rather than implemented blind.

## Decision already taken (product): host-agent seeds the proxy

The chicken-and-egg that gates the whole issue: the brain cannot `docker compose up` the socket-proxy because the proxy is its *only* Docker path, and a process cannot bootstrap its own sole transport. Resolution chosen: **host-agent** (which holds the raw socket) brings up the `malmo-ingress` network + the `docker-proxy` container alongside the brain and points the brain at `DOCKER_HOST=tcp://docker-proxy:2375`; the brain owns Caddy + `malmo-ui` and reaches Docker only through the proxy (the raw socket is never mounted on the brain). This refines `CONTROL_PLANE.md` # the dashboard UI is a brain-launched container ("the brain launches ... the docker-socket-proxy") — the proxy is brain *transport infrastructure* seeded by host-agent, distinct from the Caddy/UI services the brain serves and owns. The `DECISIONS.md` entry + spec edit land with the implementing PR.

## What was validated, and how

Run against this box's local Docker (29.5.3, compose v2.40.3 in the client), the production topology was reproduced: a `tecnativa/docker-socket-proxy` started on `malmo-ingress` with M0's exact allowlist (`POST PING VERSION INFO CONTAINERS IMAGES NETWORKS VOLUMES`; EXEC + host-bind denied), and a containerized `docker:28-cli` client on that network drove `docker compose` with `DOCKER_HOST=tcp://<proxy>:2375` — i.e. exactly how the containerized brain reaches Docker.

## Finding 1 — the compose path works through the proxy (allowlist sufficient)

`docker compose up -d` **and** `docker compose down -v` both succeeded end-to-end through the proxy: container create/start/stop/**delete**, an external `malmo-ingress` attach, published host ports, `restart: unless-stopped`, a bind-mounted config file, and a `read_only` + `tmpfs` service. The DELETE path matters beyond the control-plane stack — the brain's app uninstall is `docker compose down -v` (`internal/lifecycle/docker.go`) — and the proxy's `POST=1` covers DELETE, so it is fine. **M0's allowlist needs no widening for the compose-CLI path.**

One load-bearing constraint confirmed: a containerized client driving compose with bind mounts only works when the project directory is at an **identical path on the host and inside the brain container** — the Docker daemon resolves bind sources as *host* paths, so a mismatched path silently mounts a non-existent source. The brain already mounts `/var/lib/malmo` at the same path (#164 run spec), so staging the control-plane compose + `caddy.json` under `/var/lib/malmo/control-plane/` satisfies this for free (and `caddy.json` must live on the host there so the daemon can bind-mount it into the Caddy container).

## Finding 2 — `docker exec` is denied, and that breaks managed-DB provisioning

The proxy denies the `EXEC` family by design (`CONTROL_PLANE.md` # Docker socket exposure: "Dangerous endpoints (EXEC, ...) are denied"). But `internal/lifecycle/services.go` provisions managed databases by `docker exec`'ing the engine's own client inside the shared service container — `provisionPostgresDB` (`psql` CREATE ROLE/DATABASE), `provisionMySQLDB` (`mysql -e`), `dropServiceGrants` on uninstall, and the `waitServiceReady` readiness probes (`pg_isready` / mysqladmin ping). **The moment M1b sets `DOCKER_HOST=tcp://docker-proxy:2375`, every one of these fails.** That breaks managed Postgres/MySQL/MariaDB, and managed Redis (#159) is in flight on the same provisioning seam.

Dev is unaffected — the natively-run dev brain keeps the raw Docker socket and never points `DOCKER_HOST` at the proxy; only the containerized/production brain switches. But the production posture #165 establishes is structurally incompatible with exec-based provisioning.

The three resolutions (and why this is onel's call, not a unilateral one):

- **Widen to `EXEC=1`** — flips a Locked security decision; a compromised brain could then exec arbitrary commands in any container, the exact threat the proxy exists to stop. Rejected here, not unilaterally.
- **Re-architect provisioning off `docker exec`** — connect to the managed engine over TCP with a Go client, or run a one-shot provisioning container with only that engine's port reachable. Correct end state, but it spans `SERVICE_PROVISIONING.md` and is its own L/XL issue, not part of M1b.
- **Defer** — ship the proxy switch (correct production security), keep EXEC denied, and gate managed-DB-in-production on the re-architecture. Managed DB is pre-production regardless.

## Caddy admin-endpoint audit (#165 "audit internal/caddy/ for the hardcoded endpoint")

No hardcoding to fix: `internal/caddy/caddy.go`'s `New(adminAddr)` takes the endpoint as a parameter, fed from `cfg.caddyAdmin` (`MALMO_CADDY_ADMIN`, default `http://localhost:2019`) in `cmd/brain/main.go`. The containerized brain reaches Caddy by service name purely by setting `MALMO_CADDY_ADMIN=http://malmo-caddy:2019` in its launch env — the same way `MALMO_CADDY_PROBE_URL` is already overridden for the app-health probe. No `internal/caddy` change is required; the wiring is host-agent's brain run-spec env.

## What's next

- **onel decides Finding 2** (the EXEC / managed-DB resolution). Until then M1b implementation is blocked — switching to the proxy without a plan silently breaks managed DB.
- **Then implement M1b** on the settled shape: host-agent seeds `malmo-ingress` + `docker-proxy` and sets the brain's `DOCKER_HOST` + `MALMO_CADDY_ADMIN`; the brain reconciles Caddy + `malmo-ui` from the control-plane compose (staged at `/var/lib/malmo/control-plane/`, same-path); VM-lane staging of the compose + `caddy.json`; the `DECISIONS.md` entry + `CONTROL_PLANE.md` edit for the host-agent-seeds-proxy refinement. VM-boot acceptance ("Caddy + malmo-ui + proxy up, dashboard loads through Caddy, raw socket not mounted") rides `sudo make test-medium-qemu`, not verifiable in the inner loop — the same caveat as M0/M1a.
- **File the managed-DB-vs-proxy follow-up** once onel picks the direction (or onel files it as part of owning #165).
