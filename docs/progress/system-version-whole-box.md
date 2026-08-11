# `system/version` reports the whole box, not just the brain

- **Status:** done
- **Date:** 2026-08-11
- **Specs touched:** docs/specs/BRAIN_UI_PROTOCOL.md, docs/specs/UPDATES.md (# 8, no text change)

Second implementation slice of the update work designed in [#369](https://github.com/malmoos/malmo/pull/369), after [ghcr-control-plane-images.md](ghcr-control-plane-images.md) gave stream B a registry. Closes #374.

## What was done

The slice was planned as "expose the running versions over the API — nothing does today." That was wrong: `GET /api/v1/system/version` already existed and returned the brain's version and commit, with a comment saying the dashboard surface was a later slice. The earlier survey missed it by grepping only for raw `mux.HandleFunc` routes, which catches 12 handlers and misses every `huma.Register` route — the real API is 48 routes. The gap was narrower than planned: the endpoint answered "what is this **binary**", and the update work needs "what is this **box**".

`SystemVersionDTO` gained two fields, additively — `version` and `commit` keep their meaning and position, since `/api/v1` minors may only add (`BRAIN_UI_PROTOCOL.md` # Versioning), and the regenerated spec keeps both `required` while the new pair is optional:

- **`host_agent_version`** — from the host round trip the brain already made. `SystemStatus.AgentVersion` was read internally to drive the `version-mismatch` health issue but never returned to a caller.
- **`ui_image`** — the image ref pinned for `malmo-ui` in the staged control-plane compose, read by a new `lifecycle.ControlPlaneUIImage`. Today that is `malmo-ui:dev`, because the image is baked and `docker load`ed offline; after the updater lands it will be a real ref.

**Reading the compose rather than asking Docker is the load-bearing choice.** That file is the declaration the brain reconciles to on every startup *and* the one the control-plane updater rewrites before recreating containers (`UPDATES.md` # 8.3). Reading it means the version reported here and the version the box converges to are the same fact, with no second source to drift. The cost is that it reports intent rather than observed reality: if a recreate fails halfway the file has moved and the container has not. That window is exactly what the updater's health-check-then-revert closes, so the two are designed to agree.

**The endpoint degrades instead of failing whole**, which is deliberately not `systemStorage`'s posture (a host read failure there is a 502). The two answer different questions. An empty storage panel is a lie — it renders as "no disks". A version report missing one of three components is self-describing: the field is absent and the caller can see which one could not be read. Failing the whole read would throw away the brain's version, which is compiled in, always true, and the first thing an updater needs. Both new fields are `omitempty`, and **absent means "could not read it", never "not installed"** — a caller distinguishing "unknown" from "old" checks presence, which empty strings could not support.

**The host leg is bounded at 3s**, well under the host client's shared 30s timeout. Without that bound, "degrade instead of failing" degrades only after the caller has waited half a minute on a component that is optional in the answer — the opposite of the point. A healthy host-agent answers off a local unix socket in milliseconds. The timeout is a `var` so a test can shrink it and assert prompt degradation against a socket that accepts and then hangs (a dead socket fails fast and would not exercise it).

`BRAIN_UI_PROTOCOL.md` documents the endpoint for the first time; it had prose for `/system/storage` but never for `/system/version`.

## How it maps to the specs

- `UPDATES.md` # 8.4 step 5 — the box reports what it is running after an update. This is the read that report is built on, and it spans both streams: stream A is the host-agent, stream B is the brain and UI.
- `BRAIN_UI_PROTOCOL.md` # Versioning — additive-minor honoured; no field removed or repurposed.
- `CONTROL_PLANE.md` — the brain owns the control-plane compose, so the reader lives in `internal/lifecycle` rather than in `internal/api`.

## Known gaps & deviations

- **`ui_image` is an image ref, not a version.** Today it reads `malmo-ui:dev` from the staged compose, because the compose pins that literally and the image arrives by `docker load`. It is honest about what is running, but a consumer wanting a semver has to wait until the updater writes real refs. Naming it `ui_image` rather than `ui_version` keeps it from claiming more than it knows.
- **A `${VAR}`-interpolated image ref is rejected, not resolved.** `docker compose` does that interpolation, not the brain. The UI service is pinned literally today and the caddy service is the one using interpolation; if that ever changes, the reader errors loudly rather than reporting `${MALMO_UI_IMAGE:-…}` as a version.
- **No dashboard surface.** Out of scope by design; the endpoint is the read a later slice will render.
- **Not exercised on a real box.** The API tests run the real hostclient against a real `hostagent` over a real unix socket, so the host leg is the production wire rather than a mock, and one test parses the actual committed staged compose so a service rename or a switch to interpolation fails in CI. But no VM boot ran, so the `MALMO_CONTROL_PLANE_DIR`-set path is proven against a temp dir, not against a booted box.
- **The planning error is worth remembering.** "Nothing exposes this" was asserted from an incomplete grep and repeated into the slice plan and a merged progress entry. Route surveys on this repo have to cover `huma.Register`, not just `mux.HandleFunc`.
- **Two tests were destroyed and restored during this slice.** `internal/lifecycle/controlplane_test.go` already existed, holding the only coverage of `EnsureControlPlane`'s directory/project-name forwarding and its error propagation. Creating the new image-reader suite overwrote the file, and the suite still passed — deleted tests do not fail. Automated review caught it and both are restored alongside the new ones. The lesson generalises: a file listing that filters out `_test.go` hides the file you are about to overwrite, and a green suite is no evidence that coverage survived. Check the diff's deletion count, not the test result.

## What's next

1. **The updater** — pull by digest, snapshot the brain's SQLite, rewrite the staged control-plane compose, recreate, health-check, revert both on failure. The expensive half, shared by both profiles, and buildable against a local target with no cloud trigger.
2. **Box → cloud version report and trigger.** This endpoint is the read it sends. Still blocked on the box↔cloud credential design (`NEXT.md` # Box ↔ cloud API authentication, Tier 1).
3. **Surface versions in the dashboard**, the visible half this endpoint's original comment anticipated.
