# Control-plane container hardening

- **Status:** done
- **Date:** 2026-09-04
- **Specs touched:** docs/specs/CONTROL_PLANE.md, docs/specs/THREAT_MODEL.md

## What was done

**Closes #431.** Every **app** container gets `cap_drop: ALL` + `no-new-privileges` + a pinned `user:`, written by the brain into its override with no opt-out (`APP_LIFECYCLE.md` # Locked: override file contents). The control plane's own containers had none of it. The container that terminates TLS, holds the hosted wildcard's private key and carries an unauthenticated admin API ran with Docker's full default capability set.

Step 1 of the issue was to re-check the claim, since it was written from outside a working copy. It held: the committed `dev/control-plane/compose.yml` `caddy` service had no `cap_drop`, no `security_opt`, no `read_only`, and nothing adds them at runtime — the brain's `EnsureControlPlane` runs `docker compose up -d` on that file verbatim and writes no override for it, and neither the hosted profile nor `RewriteUIImage` touches anything but the `malmo-ui` `image:` line.

What each container runs now:

- **`caddy`** — `cap_drop: [ALL]`, `cap_add: [NET_BIND_SERVICE]`, `security_opt: [no-new-privileges:true]`, `read_only: true`. `NET_BIND_SERVICE` is kept because Caddy binds `:80` and `:443`; nothing else in the default set is used. `read_only` became affordable when #433 gave both write paths somewhere to go — `/data` is the `malmo-caddy-data` volume, `/config` a `tmpfs`.
- **`malmo-ui`** — the same three capability/privilege lines. It already had `read_only: true`. It is also a Caddy, listening on `:80` inside the container, so it needs the same one capability back.
- **`malmo-docker-proxy`** — `cap_drop: ALL` + `no-new-privileges`, added in `brainlaunch.proxyRunSpec` (it is launched by host-agent with `docker run`, not from the compose). It needs **no** capability at all: haproxy binds `:2375`, above the privileged range. This is the container holding the raw Docker socket, so it is the one where a bug is worth the most.
- **`malmo-brain`** — unchanged, deliberately. See Known gaps.
- **`dev/docker-compose.yml`** — the inner loop's standalone Caddy gets the same posture, so `make dev` runs Caddy the way a box does. Its `/data` is a `tmpfs` rather than the named volume: dev Caddy is plain-HTTP `.local` and issues no certificate.

`RunSpec` grew two fields (`CapDrop`, `SecurityOpt`) and `CLIDocker.Run` translates them to `--cap-drop` / `--security-opt`. No `CapAdd` field: nothing host-agent launches needs one, and an unused field is a second thing to keep true.

**Verified against real Docker, not only the fake.** Built `malmo-ui:dev` and brought the committed control-plane compose up on the `malmo-ingress` network:

- `docker inspect` on both containers: `CapDrop=["ALL"]`, `CapAdd=["CAP_NET_BIND_SERVICE"]`, `SecurityOpt=["no-new-privileges:true"]`, `ReadonlyRootfs=true`.
- Caddy started clean, served the catch-all 404 on `:80`, and accepted a live admin-API route add; through that route the dashboard bundle came back from the hardened `malmo-ui` (`<!doctype html> … <title>malmo</title>`).
- **The certificate path works under a read-only root**, which is the part the issue flagged as the risk. Posting a `tls` app with an `internal` issuer made Caddy obtain a certificate and write the key, the cert and its local CA under `/data/caddy/…` on the volume. A real ACME order is not reachable here (no public DNS), but the write path a wildcard order needs is the same one.
- The socket proxy was run under the new flags against the real image: healthy, and `GET /v1.41/containers/json` returned 200 through it.

**The hosted boot proof asserts it on a real box.** `dev/cloud/cloud-assertions.sh` gains step 5c: after the control plane comes up, `docker inspect` must show `cap_drop: ["ALL"]` + `no-new-privileges` on `malmo-caddy`, `malmo-ui` and `malmo-docker-proxy`, and a read-only root + only `NET_BIND_SERVICE` on the two Caddies. It runs in every boot mode of the `CI / Cloud image` lane, so a box that boots without the sandbox turns that job red. Both image canaries are bumped (`dev/cloud/test/bootstrap.sh` v20 → v21, `dev/test-qemu/bootstrap.sh` v28 → v29) because the compose, the assertions script and `host-agent-real` are all baked into the image.

A new guard test (`internal/hostagent/controlplane`) parses the **committed** compose and fails if any service loses `read_only`, `cap_drop: [ALL]`, `cap_add: [NET_BIND_SERVICE]` or `no-new-privileges`. For apps the brain enforces the sandbox in code; the control plane declares it by hand in YAML, where a later edit can quietly drop a line and the stack still starts — just without the sandbox. `TestEnsureTransportSeedsNetworkAndProxy` gained the same assertion for the proxy spec.

## How it maps to the specs

`CONTROL_PLANE.md` gains **# Locked: control-plane container hardening**, naming what each of the four containers runs and why the two exceptions are exceptions (issue step 6 — "if any of these turn out to be deliberately absent, record the reason"). `THREAT_MODEL.md` B2 gains a row for a compromised control-plane container, carrying the unhardened brain and the writable-root proxy as the residual. The posture itself is the one `APP_ISOLATION.md` # Capabilities & privilege already locks for apps; nothing new was decided, so no `DECISIONS.md` entry.

## Known gaps & deviations

- **The brain container is not hardened.** It `chown`s app data directories to the uid it elects for an app (`internal/lifecycle`), so `CAP_CHOWN` is load-bearing and `cap_drop: ALL` would break an install. Doing it properly means naming the set it does need and proving that set on a booted box — the QEMU lane, not a unit test. Recorded in the spec and B2, and filed as #442.
- **The socket proxy keeps a writable root.** `read_only` was tried against the real image and it exits 1: the entrypoint writes `/tmp/haproxy.cfg`, and haproxy wants `/run` and `/var/lib/haproxy`. Three `tmpfs` mounts pinned to another project's internal paths would break silently on an image bump, so only the capability half was taken.
- **This applies to containers as they are created.** `EnsureTransport` leaves an existing proxy container to Docker rather than recreating it, so a box that already has one keeps the old, unsandboxed container until something recreates it. Compose does recreate `caddy` and `malmo-ui` when their config changes, so those two convert on the next `up -d`.
- **No QEMU lane run here**, appliance or hosted. The evidence above is real Docker on the dev host with the committed compose; the new cloud-lane assertion is written but has only been run by CI, not locally, and a booted box was not exercised here.
- **The appliance medium lane got no equivalent assertion.** It is local-only (swtpm + LUKS), so an assertion added there could not be run before pushing; the cloud lane boots the same compose and covers the same three containers.
- **Nothing was checked for the hosted `acmedns` Caddy image specifically** — it is the same Caddy binary with one DNS module compiled in, and the tested write path (`/data`) is the one it uses, but the hosted image itself was not run.
- `user:` is untouched. The issue named it as present-but-absent; it is the one item that needs a real decision rather than a line of YAML (Caddy binds privileged ports, and both images run as root today), so it is not smuggled in here.

## What's next

1. **Harden the brain container** (#442) — name the capability set it actually needs (`CAP_CHOWN` at minimum), add it to `runSpec`, and prove it on a booted box.
2. **Decide whether the control-plane containers should run as a non-root `user:`.** Needs `NET_BIND_SERVICE` on a non-root process to keep working, and an owner for the volume's files.
