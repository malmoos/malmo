# M1a — host-agent-real launches the brain container

- **Status:** done
- **Date:** 2026-06-13
- **Specs touched:** `CONTROL_PLANE.md` (# Locked: host-agent launches the brain container — added the OCI-label lockstep mechanism); realizes `BUILD.md` # First-boot brain bootstrap (steps 2 + 4)

Closes #164 (M1a, part of #161), the next link after [m0-control-plane-images.md](m0-control-plane-images.md) (#163, which built the brain image + baked the offline bundle into the QEMU lane but wired no launch). M0's bar was "the images build and load"; M1a's is "host-agent starts the brain." `CONTROL_PLANE.md` and `BUILD.md` already described the five-step first-boot bootstrap but no code implemented it — `cmd/host-agent-real` did PAM / Avahi / users / health and never launched a container, and the QEMU medium lane symlinked `host-agent-real` to `/bin/true`.

## What was done

**New package `internal/hostagent/brainlaunch`.** A small, testable bootstrap (`Launch`) over a consumer-side `Docker` interface:

1. **docker-load if absent.** `ImagePresent` first; if the image isn't already in Docker, `docker load` the bundled tarball (offline-first — the box boots with zero internet, `BUILD.md` # 5 Distribution C).
2. **Lockstep major-version check before launch.** Read the brain image's declared wire-protocol major from the `malmo.protocol.major` OCI label and refuse (`ErrProtocolMismatch`) if it isn't the major this host-agent speaks. A botched partial update (brain image newer than host-agent) is caught here with a clear error instead of as opaque first-request failures. A missing label renders empty and is also refused — host-agent will not launch a brain it can't verify.
3. **Run the brain with `--restart unless-stopped`.** The container mounts the host-agent socket directory (so the brain reaches host-agent) and `/var/lib/malmo` (SQLite state lives under it), and reads its config from `MALMO_*` env. After this first launch Docker keeps the brain alive across host-agent restarts; host-agent does **not** supervise it in steady state — so a brain container that already exists is a no-op (`Launch` is idempotent on the container name, the recovery key being `docker rm` + a host-agent restart).

The brain is deliberately **not** given the Docker socket — it reaches Docker only through the socket-proxy the control-plane stack brings up later (`CONTROL_PLANE.md` # Docker socket exposure). Until M1b wires that stack, the brain comes up **degraded**: its startup `EnsureIngress` / `Reconcile` / Caddy calls fail-soft (all already best-effort in `cmd/brain/main.go`) and it still binds its listener and logs `malmo-brain listening` — which is exactly M1a's "Done when: brain logs show it came up." It does reach host-agent over the mounted socket, so `host-agent ready` logs too.

**Protocol constant.** `internal/protocol` gains `Major = 1` and `ImageProtocolMajorLabel = "malmo.protocol.major"` (dotted prefix matching the runtime `malmo.instance_id` convention). `cmd/brain/Dockerfile` stamps `LABEL malmo.protocol.major="1"`; the value must track `protocol.Major` and the `/v<N>` URL prefix — bumped together, the same conservative "lockstep pair" posture as `cmd/brain`'s `expectedAgentVersion`, until the release-manifest model lands.

**Wiring (`cmd/host-agent-real/main.go`).** After the socket is bound and the agent is mounted — Docker is ready by here, the unit is `After=docker.service` — `Launch` runs with a 2-minute context. It is **best-effort**: a failure (including a refused mismatch) logs at Error and host-agent keeps serving its socket, so the box stays diagnosable rather than being torn into recovery. `brainLaunchConfig` reads `MALMO_BRAIN_IMAGE` / `MALMO_BRAIN_IMAGE_TAR` (production defaults `malmo-brain:latest` + `/var/lib/malmo/brain-image.tar`), fixing the data root at `/var/lib/malmo` and dialing the same socket host-agent just bound.

**QEMU medium lane un-stub (`dev/test-qemu/`).** `bootstrap.sh` now builds `host-agent-real` (CGO on, `CGO_CFLAGS=-D_GNU_SOURCE`, as the Makefile sets — a dynamic binary linking the host's libpam/glibc, run on the Debian VM which carries `libpam0g`) and stages the real binary at `/usr/lib/malmo/host-agent-real` in place of the `/bin/true` symlink, with a preflight check for the libpam headers. A medium-lane drop-in (`host-agent.service.d/10-malmo-brain-image.conf`) points the bootstrap at the bundle's `malmo-brain:dev` tag + tarball path and orders host-agent `After=malmo-load-images.service` so the image is loaded first. `mkosi.postinst.chroot` enables `host-agent.service` (preset + manual `.wants` symlink): the `/bin/true`-exits hazard that kept it disabled — `RuntimeDirectory=malmo` tearing down `/run/malmo/` (with `storage.json`) on exit — is gone now that the unit runs the real, long-lived binary.

## How it maps to specs

- `CONTROL_PLANE.md` # Locked: host-agent launches the brain container — realized: load-if-absent, `unless-stopped`, launch-time lockstep check as a function call. Added one sentence pinning the *mechanism* (the OCI label) so spec and reality agree.
- `BUILD.md` # First-boot brain bootstrap — steps 2 (docker-load) and 4 (start the container) implemented. Step 3 (pull a newer tag from the registry) is deferred with the rest of the real update path per epic #161; only the bundled image is used.

## Verification

- **`internal/hostagent/brainlaunch` unit tests** (no Docker daemon — `Launch` is exercised against a recording fake): image-absent → load → run; image-present → skip load; the run spec (name, image, `unless-stopped`, socket-dir + data-dir mounts, `MALMO_STATE_DIR`/`MALMO_AGENT_SOCK` env); **lockstep mismatch refuses** (`errors.Is(ErrProtocolMismatch)`, no run) and **missing label refuses**; existing-container no-op; load/image-check/container-check/run errors propagate. `Launch` 95.7% covered; the `cli.go` `docker`-exec wrapper is the integration seam (0% by unit tests, exercised by the VM lane), the same boundary as `lifecycle`'s `cliDocker`.
- **`make check` green** — gofmt + vet + OpenAPI-fresh (no contract change — the guard is runtime, the generated client is untouched) + the full Go suite, including the CGO/PAM `cmd/host-agent-real` compile of the new wiring.
- **VM-boot acceptance is *not* verified here** — like M0, the medium lane needs `sudo make test-medium-qemu` on an mkosi/swtpm/QEMU host, which this environment lacks. The lane is authored to the "Done when" (a running `malmo-brain` launched by host-agent; brain logs show it came up) but a real boot pass is outstanding.

## What's next

- **Run the medium lane on a real QEMU host** to confirm host-agent launches the brain and the brain logs `malmo-brain listening` (the M0 VM-boot acceptance is still outstanding too; both ride the same `sudo make test-medium-qemu`). A medium-assertion that greps for the running `malmo-brain` container + the listening log is the natural M2 (#167) addition.
- **M1b (#165): the brain brings up the control-plane stack** (Caddy + `malmo-ui` + socket-proxy) and gets `DOCKER_HOST` pointed at the proxy — until then the brain runs degraded with no Docker reach, as described above.
- **Registry pull (BUILD.md step 3)** and a distinct production image pin / release manifest stay deferred (#161). The lockstep label value is hand-kept in lockstep with `protocol.Major`; the release-manifest model will replace the hand-keeping.
- **glibc skew of the test-lane host-agent build** — the medium lane builds `host-agent-real` dynamically on the build host; a much newer build host than the Debian VM could produce a binary the VM's glibc won't load. Not hit in practice (production ships a `.deb` built for the target), noted for whoever runs the lane on an exotic host.
