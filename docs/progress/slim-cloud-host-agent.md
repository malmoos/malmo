# Slim cloud host-agent (`-tags hosted`)

- **Status:** done
- **Date:** 2026-06-19
- **Specs realized (no spec change):** `ENVIRONMENT.md` # How the profile is realized ("A build-tagged slim cloud `host-agent`"), # Two layers ("Layer 2 is where the profiles diverge"), # Networking & discovery; `CONTROL_PLANE.md` # Layer 1
- **Closes:** #204 (C1c — last of the C1 split, after #202/C1a and alongside #203/C1b; part of #196). Follows #202/C1a (the `/etc/malmo/profile` marker the brain reads to skip mDNS-publish in `hosted`).

## What was done

`cmd/host-agent-real` is now built in two profiles from one source tree, selected by the `hosted` Go build tag — the same mechanism the repo already uses to split the cross-platform surface from the Linux-only host integration (`CLAUDE.md` # Developing). The `host-agent-real/main.go` boilerplate (socket bind/serve, the first-boot brain bootstrap, the `brainLaunchConfig`/`env`/`envBool` helpers) is now **shared, untagged** code; the host-op **seam composition** moved into two build-tagged files:

- **`wiring_appliance.go`** (`//go:build !hosted`) — today's full appliance wiring, byte-for-behavior unchanged: real PAM, `usermgr`, the health/system/log reporters, plus the LAN discovery stack (`netstate.NMProvider` as the LAN-set source, per-interface `avahipublisher` announcements, and the NetworkManager watcher that replays them on IP/interface change). The default (untagged) build wires exactly what it wired before.
- **`wiring_hosted.go`** (`//go:build hosted`) — the slim cloud variant. **Kept** seams (a cloud VM still needs all of these, none touch the LAN/NM/DBus): real `pamverifier`, `usermgr.LinuxUserManager`, the storage/services/time/resources/reboot/system-resources reporters, `diskusage` (both disk seams), and the per-app journal tail. **Dropped**: `avahipublisher` (mDNS publish), `netstate.NMProvider` + the watcher (NetworkManager), and — by simply not being in the hosted image at all (#203/C1b) — LUKS/TPM unlock, the Samba allowlist, and nftables LAN-scoping, so there is nothing here to wire for them.

Both profiles keep real PAM, so both remain Linux + CGO + libpam0g-dev and must run as root — the hosted build has the **same** CGO/libpam requirements as `host-agent-real` today (`verify_password` is kept). The brain↔host-agent protocol, the mux, and all seam interfaces are untouched: this is consumer-side wiring only.

Two dropped-seam no-ops keep the shared mux safe:

- **No-op `Publisher`.** `hostagent.New` requires a non-nil `Publisher`, and the publish/unpublish routes stay mounted. The hosted build wires a 3-line `noopPublisher` (Publish/Unpublish do nothing). The brain already skips `POST /v1/discovery/publish` in `hosted` via the C1a marker, so this is belt-and-suspenders against a stray call nil-panicking — not a live code path.
- **`a.Net` left nil.** With no NetworkManager there is no LAN set to report; the existing `GET /v1/discovery/state` handler already treats a nil `Net` as "not measured" (empty `interfaces`). That field is a diagnostic read and is moot without mDNS. **Net-state seam choice:** a nil stub (the smaller of the two options the issue offered) rather than a kernel single-NIC reader — we deliberately do **not** pull NetworkManager into the hosted build to populate it. A minimal non-NM kernel reader is a follow-up if a hosted consumer ever needs the interface name.

A `make host-agent-real-hosted` target (`go build -tags hosted`) builds the cloud binary; the cloud image build (#203/C1b, #205/C2) consumes it. Added to `.PHONY` and `make help`.

## Verification

- `go build -tags hosted ./cmd/host-agent-real` produces a binary wiring only the kept seams with the no-op publisher for the dropped discovery seam. It is genuinely slimmer: **`avahipublisher` is absent from the import graph** (`go list -deps -tags hosted` confirms), godbus symbols drop 378 → 182, and the binary is ~410 KB smaller than the appliance build.
- The default (untagged) appliance build is unchanged — `make check` green.
- vet/test green **both** ways: `make check` (default) plus `go vet -tags hosted ./...` and `go build -tags hosted ./...` across the whole module (no ripple to other consumers, e.g. `cmd/malmo-network-verify`).

## Known gaps / what's next

- **`netstate` is still imported by the hosted binary (godbus rides along).** The shared `hostagent` mux's `NetState` seam returns `netstate.LANInterface`, so any binary using the mux imports the `netstate` package; on Linux `netstate/nm_linux.go` (`//go:build linux`) drags `godbus` in even though nothing in the hosted build references `NMProvider`. Fully removing it would require either moving `LANInterface` out of `netstate` (a change to the `hostagent` seam, which #204 forbids) or build-tagging `netstate`'s NM provider behind `!hosted` (provider-side, outside this slice's "consumer-side wiring only" scope). The headline drop — the Avahi/mDNS publish machinery — **is** achieved; the godbus residual is inert (`a.Net` is nil). Filed as #223 to fully strip it.
- **No VM-boot proof here.** Building + linking the slim binary is verified; booting it inside the hosted cloud image (control plane up, brain launched, first-boot seed) is **#205/C2's** job, which bakes the image (#203/C1b) and asserts it under QEMU. The slim agent is the binary C2 boots.
- The slim cloud `host-agent` completes the **C1 split** (#202 marker + brain read, #203 lean image, #204 slim agent). C2 (#205) emits and boots the assembled image.
