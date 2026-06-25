# Strip godbus from the slim `-tags hosted` host-agent

- **Status:** done
- **Date:** 2026-06-25
- **Specs realized (no spec change):** none — `ENVIRONMENT.md` # How the profile is realized already frames the hosted `host-agent` as the build-tagged slim variant with the LAN/NetworkManager/DBus machinery cut; this finishes that cut at the link level. The specs make no `godbus`/DBus-dependency claim, so there is nothing to reconcile.
- **Closes:** #217 (C1c follow-up, part of #196). Finishes the "no DBus deps" done-when of [slim-cloud-host-agent.md](slim-cloud-host-agent.md)/#216, whose Known gaps filed this: the slim build dropped `avahipublisher` but `netstate` still dragged `godbus` in.

## What was done

#216 build-tagged the host-op **wiring** so the hosted `host-agent-real` drops `avahipublisher` and leaves `a.Net` nil, but it was consumer-side only — by #204's scope it could not touch the `netstate` **provider**. The shared `hostagent` mux's `NetState` seam returns `netstate.LANInterface`, so every binary using the mux imports the `netstate` package; on Linux `netstate/nm_linux.go` was `//go:build linux`, so its `godbus`-backed `NMProvider` compiled into the hosted build even though nothing wires it (`a.Net` is nil). ~182 `godbus` symbols rode along, inert.

This lands issue #217's **Option B** — build-tag the NM provider behind `!hosted`, provider-side, two lines and no new file:

- `netstate/nm_linux.go` (the real `NMProvider`, the only `godbus` importer in the package): `//go:build linux` → `//go:build linux && !hosted`.
- `netstate/nm_other.go` (the `godbus`-free `NMProvider` stub, already the non-Linux mirror): `//go:build !linux` → `//go:build !linux || (linux && hosted)`, so the Linux hosted build takes the same stub. Its method set and `Debounce` field are identical to the real provider, so `cmd/host-agent-real` (hosted wiring, `a.Net` nil) and `cmd/malmo-network-verify` (constructs `netstate.NMProvider{}`, and is built by `-tags hosted ./...`) still compile.

The two tags partition cleanly: every (GOOS, hosted) combination resolves to exactly one `NMProvider` — real on `linux && !hosted`, the stub everywhere else (`!linux`, or `linux && hosted`) — with no overlap and full coverage. `LANInterface` (the seam type) stays in the untagged `netstate.go`, so the `hostagent` seam interface is unchanged; Option A (moving `LANInterface` off the seam, which #204 forbade) was not needed.

## Verification

- **`go list -deps -tags hosted ./cmd/host-agent-real`** shows no `godbus` and no `avahipublisher` (baseline: `github.com/godbus/dbus/v5` present). The built hosted binary carries **0** `godbus` symbols (`go tool nm`), down from ~182.
- **Appliance build unchanged:** default `go list -deps ./cmd/host-agent-real` still pulls `godbus` (real `NMProvider` on `linux && !hosted`). netstate `GoFiles` is `[netstate.go nm_linux.go probe.go]` for the appliance and `[netstate.go nm_other.go probe.go]` for hosted.
- **`make check` green** — gofmt + vet + OpenAPI freshness + the full Go test suite (default tags).
- **`go vet -tags hosted ./...` and `go build -tags hosted ./...` green** across the whole module, `cmd/malmo-network-verify` included.
- The `nmtest` real-provider integration test still compiles (`go test -tags nmtest` build; it is gated `linux && nmtest`, so `!hosted` leaves it on the real provider).

## Known gaps / what's next

- `cmd/malmo-network-verify` still links `godbus` under `-tags hosted` — but via `avahipublisher` (a LAN-verification dependency it genuinely uses), not `netstate`. It is an appliance-only diagnostic tool that only has to *compile* under `-tags hosted ./...` (it does); slimming it is out of #217's scope, which targets `cmd/host-agent-real`.
- This completes the C1 split's "no DBus deps" goal; no follow-up planned.
