# CI cloud-image build lane + mkosi unprivileged-userns fix

- **Status:** done
- **Date:** 2026-06-23
- **Specs touched:** none — `docs/specs/TESTING.md` (lane design) is unchanged; the host-setup detail lives in `docs/dev/running-locally.md`.

Closes #189. The issue reported that the QEMU medium lane "can't build under mkosi 26 on Ubuntu 24.04" — `mkosi build` dies with a `PR_CAPBSET_DROP` `EPERM` at sandbox bring-up, before producing any image. The whole cloud-image track has carried this blocker: [cloud-image-recommends-pin.md](cloud-image-recommends-pin.md) (#237) could only verify its fix at the config layer and deferred the green `make build-cloud-image` to "the maintainer's joint cloud live run." This change root-causes the EPERM, fixes it, and wires the **first automated cloud-image build** — which also lands the CI lean-check lane #238 asked for.

## Root cause — Ubuntu 24.04's AppArmor unprivileged-userns restriction, not `sudo`

The issue hypothesized the failure was mkosi running *as root* (under `sudo -E`) and conflicting on the cap-drop. It is not. mkosi 26 builds **rootless**: `acquire_privileges` calls `become_user` (unshares a user namespace), then `drop_capabilities` does `prctl(PR_CAPBSET_DROP)`, which needs `CAP_SETPCAP` in the new namespace.

Ubuntu 24.04 ships `kernel.apparmor_restrict_unprivileged_userns=1`. Under it an unconfined process gets a user namespace with **no** `CAP_SETPCAP`, so `PR_CAPBSET_DROP` returns `EPERM`. Confirmed on the dev box:

- A fully **unprivileged** `mkosi sandbox -- true` reproduces the exact traceback (`acquire_privileges → drop_capabilities → prctl`) — no `sudo` involved.
- `unshare -Ur true` fails with `write failed /proc/self/uid_map: Operation not permitted` — direct proof the restriction blocks the userns this needs.

So it is neither `sudo`- nor version-specific in the way diagnosed: *any* unprivileged mkosi invocation on a restriction-enabled kernel hits it. The medium lane's `bootstrap.sh` already runs `mkosi build` as the non-root caller (`sudo -u "$CALLER"`), so it is exactly this path. mkosi's one escape is holding effective `CAP_SYS_ADMIN` (then `acquire_privileges` returns early and skips the cap-drop) — which an unprivileged build never does.

## What was done

1. **CI lane — `.github/workflows/ci-cloud-image.yml`** (runs-on `ubuntu-24.04`, pinned; path-filtered to `dev/cloud/**` + `Makefile` + the workflow file, same narrow-trigger pattern as `ci-go.yml`). It relaxes `kernel.apparmor_restrict_unprivileged_userns=0` (safe on an ephemeral runner), installs a **pinned** mkosi (`==26`) plus the Debian tools-tree bootstrap tooling, runs a fast `mkosi sandbox -- true` smoke (isolates the userns fix from build-dependency noise), then `make build-cloud-image`. That build also runs the manifest lean check — so this lane is simultaneously the leanness regression guard #238 wanted (its option 1).
2. **Cloud bootstrap preflight (`dev/cloud/bootstrap.sh`)** — before the build, probe mkosi's real sandbox from a config-less temp dir (so a healthy host doesn't provision a tools tree during the probe). On failure, print a precise remedy (the sysctl knob, temporary and persisted) and exit, instead of leaving the caller with mkosi's opaque ctypes traceback.
3. **Medium bootstrap hint (`dev/test-qemu/bootstrap.sh`)** — a non-blocking warning printed above the `mkosi build` when the restriction is detected. It runs mkosi as `$CALLER` under `sudo`, where a config-less hard-probe is awkward and unverifiable from CI (no KVM), so a hint + the shared knob + the doc cover it rather than a probe.
4. **Doc — `docs/dev/running-locally.md` # Ubuntu 24.04: unprivileged user namespaces** — the prerequisite, the sysctl remedy (temporary + persisted), the scoped-AppArmor-profile alternative, and which scripts detect it. The heading is the anchor the two preflight messages point to.

## How it was verified

- **Root cause + cloud preflight — locally on this Ubuntu 24.04 box (mkosi 26):** unprivileged `mkosi sandbox -- true` reproduces the EPERM; `unshare -Ur true` fails at the `uid_map` write; and running `./dev/cloud/bootstrap.sh` now exits 1 with the remedy message at the new preflight, *before* any mkosi build output.
- **CI lane — `ci-cloud-image.yml`, green on a real `ubuntu-24.04` runner (PR #248):** the runner confirmed the restriction was on (`before: 1` → `after: 0`); the `mkosi sandbox` smoke passed once the knob was set; `make build-cloud-image` produced `malmo-cloud.raw` with `lean check passed` and `source-sanity check passed` — in ~2m16s. This is the first automated green cloud-image build, the end-to-end proof the cloud track previously deferred.
- **`bash -n`** clean on both edited bootstrap scripts.

## Known gaps & deviations

- **The medium lane's *boot* half is still not in CI.** #189's failing step was the **build**, which shares the identical mkosi-sandbox fix the cloud lane proves; the medium build is unblocked by the same documented knob. But the 2-boot QEMU + swtpm + LUKS/TPM assertions need nested KVM, which GitHub runners don't provide (TCG would be prohibitively slow/flaky) — so the boot lane stays a developer/maintainer-run lane, consistent with `ci-go.yml`'s standing deferral of the boot lanes.
- **mkosi is pinned to `==26`** (the dev-box version) for a reproducible lane. A future bump could shift sandbox behavior and should be made deliberately.
- **#238's option 2 (widen the lean assertion to a full expected-package-set diff) is not done here.** This lane delivers option 1 — the lean check now runs automatically on every `dev/cloud/**` change — which satisfies #238's "Done when." The assertion-widening remains an optional follow-up.

## What's next

1. **Close #238** — the lean check now runs in CI on every cloud-image change (its option 1 / "Done when" is met by this lane). Optionally widen the assertion (option 2) as a separate, smaller change.
2. **Medium-lane boot in CI** remains blocked on KVM-capable runners — out of this change; only the build half is unblocked here.
