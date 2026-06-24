# Widen the cloud-image lean check to a full expected-package-set diff

- **Status:** done
- **Date:** 2026-06-24
- **Specs touched:** none — `ENVIRONMENT.md` # How the profile is realized (the lean image's package posture) is the governing spec and is unchanged; this tightens the *enforcement*, not the policy.

Closes #238. Builds directly on [mkosi-userns-ci-lane.md](mkosi-userns-ci-lane.md) (#189), which delivered #238's **option 1** — `make build-cloud-image` (and therefore its lean check) now runs automatically in CI on every `dev/cloud/**` change. This change delivers #238's **option 2**: it replaces the lean check's hardcoded appliance cut list with an exact diff against a checked-in expected package set, so *any* off-list package — not just a named handful — fails the build. The two compose, which is what #238 asked for ("a CI lane is only as good as the assertion it runs. Do both.").

## Why option 1 alone wasn't enough

The pre-existing check (`dev/cloud/bootstrap.sh` step 5) only failed if one of eight named appliance packages (`network-manager`, `avahi-daemon`/`avahi-utils`, `samba`, `mergerfs`, `cryptsetup`, `tpm2-tools`, `openssh-server`) was present. That is a denylist: it is blind to bloat it doesn't name. The #237 regression is the proof — apt recommends drifted back on and pulled in `nftables`, and the only reason the check caught it is that `nftables` happened to be on the named list. A different recommend (or any new transitive dependency) would have sailed through. Worse, #241 accepted `nftables` permanently (docker-ce now hard-Depends it), so the old denylist no longer even covered that case. A denylist cannot guard leanness; an allowlist can.

## What was done

1. **`dev/cloud/expected-packages.txt`** — a new checked-in, lockfile-style allowlist: the exact set of package names the lean hosted image is expected to contain, one per line, `#` comments allowed. Generated from the first green CI build (the authoritative manifest), so it is the real package set, not a hand-guess.
2. **`dev/cloud/bootstrap.sh` step 5 rewritten** — the lean check now parses the JSON manifest's package names and diffs them against `expected-packages.txt`: it fails if anything is in the image but not expected (**bloat**) *or* expected but not in the image (a silent removal / rename). On failure it prints the offending names one per line, so regenerating the file is a copy-paste. Names only are compared — versions are ignored — so a patch/minor bump never churns the file; only a genuine dependency-graph change does, and that change is then forced through review. The one package whose *name* is not version-stable — the concrete kernel image `linux-image-<abi>-amd64`, whose ABI is encoded in its name and so changes on every kernel bump — is normalized out of the diff on both sides; the stable meta-package `linux-image-amd64` still guards kernel presence, so a kernel bump isn't a false bloat failure. The eight-name cut list and its `nftables`/#241 caveat move into the allowlist's header comment as intent.
3. **Comment sync** — `bootstrap.sh`'s sequence header and two `mkosi.conf` comments (the `ManifestFormat` line and the `Packages=` block's "NOT here (the cuts — …)" note), all of which described the old "cut list" assertion, now describe the expected-set diff. The pre-existing `nftables`-as-a-cut wording in that block is left for #241 to reconcile (it still matches `ENVIRONMENT.md`'s cut list).

No spec change: `ENVIRONMENT.md` already defines the lean posture; this only makes the enforcement total instead of partial. No `nftables`/#241 decision is pre-empted — `nftables` is simply listed in the expected set (it is in the image today), with a header note pointing at #241 for its eventual fix.

## How it was verified

- **Lean-check logic — locally:** the new Python diff was exercised against synthetic manifests — an off-list package fails listing it under `UNEXPECTED`; an exact match passes; an expected-but-absent package fails listing it under `MISSING`; `#` comments and blank lines in the expected file are ignored. `bash -n dev/cloud/bootstrap.sh` clean.
- **End-to-end — the CI cloud-image lane (PR #249), green on a real `ubuntu-24.04` runner:** `make build-cloud-image` built `malmo-cloud.raw` and the widened check reported `lean check passed — manifest matches expected-packages.txt exactly (133 packages)`. The expected set was seeded from that same build's authoritative manifest (the build's 134 packages minus the version-normalized kernel image), so the assertion is proven against the real image, not a guess.

## Known gaps & deviations

- **Verified on the PR #248 stack before #248 merged.** The CI lane (`ci-cloud-image.yml`) was introduced by PR #248; this PR was branched on top of it so it could build and verify before #248 landed. #248 is now on `main`; this PR's diff is just the lean-check widening.
- **The expected set is a maintenance surface.** A legitimate dependency change now fails the build until `expected-packages.txt` is updated — by design (a lockfile makes every package-set change explicit and reviewed), but it is a real cost: a careless update could re-widen the hole this closes. The failure output is formatted to make the update mechanical and the diff reviewable.
- **The C2 boot-proof lane (`dev/cloud/test/`) still has no lean check.** It builds an instrumented *superset* of this image, so the allowlist does not apply to it; guarding its leanness is out of #238's scope.

## What's next

1. **`nftables` in the expected set** — `nftables` is listed because docker-ce hard-Depends it (#241, now resolved as accepted permanently). The header comment in `expected-packages.txt` notes this; no further action needed unless Docker drops the dep.
