# Hosted host-agent ships version-stamped

Fixes the first bug found on a real hosted box running a tagged release: a freshly-provisioned `v0.4.0` Hetzner box raised `version-mismatch` on the dashboard ("The system agent is running an older version (dev) than this malmo needs (0.4.0 or newer)") and blocked app installs, on a box that was otherwise entirely healthy. Follow-up to `repo-version-and-compat-range.md`, which introduced the stamping contract and the range-based detector this tripped over.

## What was done

`dev/cloud/stage-control-plane.sh` built the slim hosted `host-agent` with a bare `go build` and no `-ldflags`. It is the only `go build` in the cloud-image staging path — the brain and UI go through Docker, and `cmd/brain/Dockerfile` reads `cat VERSION` itself, so both were stamped correctly. The agent was not. It shipped `internal/version`'s unstamped `Version = "dev"` default, `hostagent.AgentVersion` is `version.Version` verbatim, and the systemStatus handler reported `"dev"` to the brain on every handshake.

The brain's `agentVersionAcceptable` then handed `"dev"` to `semver.Compare` via `versionCore`, which produced the invalid `"vdev"`. Invalid versions sort before every valid one — deliberately, as the safe default for a garbled version string — so the check read `"dev"` as older than `0.4.0` and raised. The comparison is `>= 0` and was never the problem: a correctly-stamped `0.4.0` agent satisfies a `0.4.0` minimum by equality. The bug was entirely in the build.

`stage_build_go` now applies the same `-ldflags` the Makefile's `LDFLAGS` do, via a new `stage_version_ldflags`. They are recomputed rather than inherited because `bootstrap.sh` is invoked as `sudo -E ./dev/cloud/bootstrap.sh`, so make's `MALMO_VERSION`/`MALMO_COMMIT` are plain make variables that never reach it. `git rev-parse` runs as `CALLER`: under sudo the repo is owned by the invoking user and git refuses a dubious-ownership repo as root. The commit falls back to `unknown` as the Makefile's does rather than failing a ~40min image build; only `Commit` can degrade that way, since `Version` comes from the file.

`dev/cloud/cloud-assertions.sh` gains step 1c: the baked `/usr/lib/malmo/host-agent-real --version` must report `malmo X.Y.Z (g<sha>)`. Verified it fails against the actual unstamped `v0.4.0` binary and passes against the stamped one, so it is a real regression test rather than a tautology.

No spec change. `BUILD.md` # Versioning already required that every build stamp both fields — the spec was right and the cloud build silently violated it. `minimumAgentVersion` is untouched, per its own comment: it moves only on a real protocol break, not per release.

## Why this got through

Nothing that ran could have caught it. The stamp is applied by the build command, so a `go test` binary is unstamped by construction — a unit test asserting anything about `version.Version` only ever pins the default. The release workflow's tag-vs-`VERSION` assert checks the file, not what lands in the binary. It is observable only on a built image, which is why the assertion went in the boot-proof lane and not next to the detector's unit tests.

## What's next

- `v0.4.0` images are structurally broken: every box provisioned from one raises `version-mismatch` and cannot install apps. The banner's "It will update automatically" is canned copy — the unstamped binary is baked in, so a running box cannot self-heal. Shipping this needs a `dev`->`main` PR bumping `VERSION` to `0.4.1` (the bump is the release trigger) and a re-provision.
- The detector's user-facing text promises an automatic update that no mechanism currently performs (`UPDATES.md`'s apt cadence is not wired). Worth revisiting when the release manifest lands and `minimumAgentVersion` stops being a hand-bumped constant.

## Known gaps

- `stage_version_ldflags` is the second place the ldflags string is spelled out (the Makefile has the first). A shared source would be better, but the sudo boundary makes sharing awkward and two call sites do not yet justify the indirection (CLAUDE.md # No premature abstraction). The new boot assertion is what catches a future drift between them.
