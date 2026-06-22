# Pin apt recommends off for the hosted cloud image

- **Status:** done
- **Date:** 2026-06-22
- **Specs touched:** none — `ENVIRONMENT.md` # How the profile is realized (the cut list, already correct) is the governing spec; no change.

Closes #237. A maintenance fix on the C1b cloud-image profile ([hosted-cloud-image.md](hosted-cloud-image.md), #218): `make build-cloud-image` started failing its own lean check —

    LEAN CHECK FAILED — appliance packages present in cloud image: ['nftables']

even though neither the check nor the spec had changed. The lean check (`dev/cloud/bootstrap.sh`) and `ENVIRONMENT.md` are both right — hosted ships no host firewall and `nftables` is on the cut list. The image stopped being lean underneath an unchanged check.

## Root cause — recommends drift

`nftables` is present in the manifest **only** as `iptables`' Debian *Recommends* (`docker-ce` hard-**Depends** `iptables`; trixie `iptables` only *Recommends* `nftables`; nothing else in the 133-package image hard-depends it). So `nftables` is in the image only because apt recommends were being installed during the build — and they were not when the profile landed (#218). The proof recommends were off at #218 is in the config itself: `systemd-boot-efi` (a *Recommends* of `systemd-boot`) and `tzdata` (a *Recommends* of `systemd`) are listed **explicitly** "or the build fails" — that is only necessary with recommends off.

The cloud `mkosi.conf` never set the recommends knob; it rode on mkosi's default. mkosi here is a moving dev build, and the default re-enabled recommends underneath us — pure tooling drift, no malmo code change between #218 and now (verified: `docker-ce` + the `nftables` cut have coexisted since the profile's first commit).

## What was done

One mkosi-native knob in `dev/cloud/mkosi.conf`, in `[Content]`:

    WithRecommends=no

mkosi maps `WithRecommends` straight to apt: `installer/apt.py` always emits `-o APT::Install-Recommends=<with_recommends>`. Pinning it `no` guarantees `APT::Install-Recommends=false` for every build of this profile regardless of the mkosi default, dropping `nftables` (and any other recommend-only bloat) while keeping `iptables` — `docker-ce`'s hard dependency, which Docker bridge networking needs at runtime. The boot-proof test lane (`dev/cloud/test/mkosi.conf`) inherits the setting through its `Include=..`, so it builds equally lean.

Two adjacent comments that attributed recommends-off to mkosi's *default* ("mkosi builds with Install-Recommends=false", explaining why `systemd-boot-efi` and `tzdata` must be listed explicitly) were updated to point at the new pin, so the file stays internally consistent and the reason those packages are explicit is now guaranteed, not coincidental.

No spec change: `ENVIRONMENT.md` already lists `nftables` as a cut, so pinning recommends off makes reality match the spec rather than relaxing the cut list.

## How it was verified

- **`WithRecommends` is a recognized mkosi setting**, not silently ignored: `mkosi summary` accepts `WithRecommends=no` (exit 0), while a typo control `WithRecommendz=no` is rejected with `Unknown setting WithRecommendz`. The cloud config parses clean under mkosi 26.
- **The knob reaches apt:** `mkosi/installer/apt.py` unconditionally passes `-o APT::Install-Recommends={with_recommends}` — so `WithRecommends=no` ⇒ `APT::Install-Recommends=false` ⇒ `nftables` (the sole recommend-source) not installed ⇒ the lean check's `nftables` assertion passes.

## Known gaps & deviations

- **Full `mkosi build` / `make test-cloud-qemu` not run here** — same #189 blocker the whole cloud track carries: mkosi 26 on this Ubuntu 24.04 box hits `PR_CAPBSET_DROP` EPERM (`apparmor_restrict_unprivileged_userns=1`), confirmed independent of the harness sandbox. Verification is the config-layer chain above; the green `make build-cloud-image` (no `nftables` in the manifest) and Docker-still-serves boot check land at the maintainer's joint cloud CL6 live run, per the issue's "Done when".
- **No leanness regression guard added.** The issue's "Also (separate, smaller)" — wiring the lean check into a maintainer CI lane so an unpinned-dependency regression can't pass unnoticed — is filed as a follow-up (#238; out of this fix's "Done when", and a non-trivial image-build-in-CI piece). The C2 boot-proof lane still has no lean check; only `make build-cloud-image` asserts it.

## What's next

1. **Joint CL6 verification** — a real `MALMO_HETZNER_IMAGE` built from a clean lean image (`malmoos/cloud` #6), confirming the manifest carries no `nftables` and Docker comes up on a booted cloud VM.
2. **Leanness regression guard** (#238) — wire `build-cloud-image`'s lean check into a maintainer CI lane, or widen the assertion, so recommends/dependency drift can't silently re-bloat the image again.
