# Accept `nftables` in the hosted cloud image (docker-ce hard-dep)

- **Status:** done
- **Date:** 2026-06-23
- **Specs touched:** `ENVIRONMENT.md` (# How the profile is realized, # Public-by-default), `DECISIONS.md` (new 2026-06-23 entry), `NEXT.md` (in-guest backstop item).

Closes #241. The permanent fix for the C1b cloud-image lean-check break that #243 unblocked temporarily ([cloud-image-recommends-pin.md](cloud-image-recommends-pin.md), #237, is the recommends-path history this supersedes for `nftables`). That frozen entry closes with "no spec change (`ENVIRONMENT.md` already lists `nftables` as a cut)" — **this entry supersedes that claim**: `ENVIRONMENT.md` no longer lists `nftables` as a cut, because `docker-ce` now hard-Depends on it. `make build-cloud-image` was failing its own lean check —

    LEAN CHECK FAILED — appliance packages present in cloud image: ['nftables']

— but this is **not** the #237 regression. #237 was `nftables` arriving as `iptables`' Debian *Recommends*, fixed by pinning `WithRecommends=no`; that fix is still in place and still correct for the recommends path.

## Root cause — docker-ce now hard-Depends on nftables

`docker-ce` bumped to `5:29.6.0-1~debian.13~trixie`, whose control file reads:

    Depends: containerd.io (>= 2.1.5), docker-ce-cli, iptables, nftables, libc6 (>= 2.34), libsystemd0

`iptables, nftables` are two **separate hard dependencies** (not an `iptables | nftables` alternative). Docker 28 moved its default firewall backend to nftables; by 29.x the `nftables` package is mandatory. With recommends already pinned off, `nftables` still installs — pulled by `docker-ce` directly. Recommends-off cannot remove a hard dependency, and the hosted image **must** run docker (the four control-plane containers), so `nftables` is unavoidable. #237's rationale for keeping `nftables` on the cut list ("only a recommend, droppable; `iptables` stays for docker") no longer holds.

## What was done

The decision (issue #241, option 1 — **accept `nftables` permanently**, recommended): the package's presence is docker's, not appliance LAN machinery, so it leaves the cut list for good and the docs are reconciled to that reality.

- **`dev/cloud/bootstrap.sh`** — `nftables` was already absent from the lean-check `cuts` set (#243 removed it). The **temporary** comment block #243 left ("TEMPORARILY removed … Tracked for a proper fix/decision in #241 … Re-evaluate before this lands") is replaced with a **permanent** rationale: docker-ce hard-Depends on `nftables` as its firewall backend, the hosted image must run docker, malmo manages no ruleset of its own. The step-4 header comment (`nftables included`, now false) is corrected to `nftables is intentionally NOT cut`. **The executable cut list is byte-for-behavior unchanged** — these are comment-only edits, so the build behaves exactly as the green post-#243 main.
- **`ENVIRONMENT.md`** — # How the profile is realized: `nftables` removed from the "not installed" list and reframed as the one installed exception (docker's hard-dep backend, no malmo ruleset). # Public-by-default: "`nftables` is in the cut list" → "`nftables` *is* present (docker's backend) but malmo installs no ruleset of its own"; the substance (provider security groups own L3/L4 filtering) is unchanged. The slim-host-agent bullet's "drops … nftables LAN-scoping" stays — the host-agent still owns no firewall *function*; only the package's presence changed.
- **`DECISIONS.md`** — new 2026-06-23 entry. The 2026-06-19 decision's substance (no malmo-managed firewall; provider security groups) is unchanged; only its factual premise ("ships no `nftables` package") flipped, so a new entry records the delta rather than editing the frozen one. Pinning `docker-ce < 28` to keep `nftables` a recommend (ship an EOL'd Docker) is recorded as rejected.
- **`NEXT.md`** — the deferred in-guest `nftables` default-deny backstop is now a ruleset + host-agent seam only; the package is already present, no install needed.

## How it was verified

- **Comment-only delta to the build path.** The `cuts` set in `bootstrap.sh` is identical to current `main` (no `nftables` member); only comments changed, so `make build-cloud-image`'s behavior is unchanged from the green post-#243 main. `bash -n dev/cloud/bootstrap.sh` parses clean, and the embedded python lean-check heredoc compiles (`python3 -m py_compile` on the extracted snippet).
- **Docker still hard-Depends `nftables`** confirmed from the issue's `dpkg-deb -f docker-ce_…29.6.0…deb Depends` evidence (`iptables, nftables` as separate deps), so the package is present in the manifest and is *expected* there — the lean check correctly no longer flags it.

## Known gaps & deviations

- **Full `mkosi build` / `make test-cloud-qemu` not run here** — same #189 blocker the whole cloud track carries: mkosi 26 on this Ubuntu 24.04 box hits `PR_CAPBSET_DROP` EPERM (`apparmor_restrict_unprivileged_userns=1`), confirmed independent of the harness sandbox. This change is documentation + comment-only with **no behavioral delta** to the build, so the green `make build-cloud-image` and the "Docker comes up on a booted cloud VM" proof land unchanged at the maintainer's joint cloud CL6 live run, per the issue's "Done when".
- **Leanness regression guard** (#238) — wiring the lean check into a maintainer CI lane (which would have caught the dependency bump earlier) is the separate follow-up the issue notes this composes with; out of scope here.

## What's next

1. **Leanness regression guard** (#238) — wire `build-cloud-image`'s lean check into a maintainer CI lane so a future `docker-ce` (or other) dependency bump that re-bloats the image can't pass unnoticed.
2. **In-guest `nftables` default-deny backstop** (`NEXT.md`) — if a hosted target ever lacks a provider security-group layer; now a ruleset + host-agent seam only, since the package is already baked in.
