# Avahi publisher: route-probed LAN address (no more Docker-bridge announcements)

- **Status:** done
- **Date:** 2026-06-11
- **Specs touched:** `BOOT.md` # What downstream services do (stale static-file mechanism + wrong restart-durability claim corrected), `DISCOVERY.md` # What we publish (host-record attribution corrected); everything else realized, not changed.

Closes issue #129. Picks up the "local-IP detection" known gap from [avahi-dbus-publisher.md](avahi-dbus-publisher.md) (frozen — its gap list stands as written). App `.local` records could announce a Docker bridge address instead of the LAN address (verified live: `avahi-resolve memos.local` → `172.25.0.1`, a `br-*` bridge, while the LAN IP was `192.168.1.126`), making apps unreachable from every other device — iOS resolved the name fine but got an unroutable address. Root cause: `detectLocalIPv4()` returned the first non-loopback, non-link-local IPv4 from `net.InterfaceAddrs()`, and on any box running Docker (every malmo box) a bridge can win by enumeration order. Not dev-only: `cmd/host-agent-real` uses the same `DBusPublisher`, so the bug was latent in production.

## What was done

### Route-based source-address detection (`internal/hostagent/avahipublisher/localip.go`, new)

- **`probeLANIPv4()`** — connect a UDP socket to `192.0.2.1:9` (TEST-NET-1; UDP connect performs only the kernel route lookup, no packet is sent) and read the chosen source address from `LocalAddr`. The route lookup structurally excludes Docker bridges and other virtual interfaces — they are never the route toward a non-local address — with **no RFC1918 blacklist** (172.16/12 is a valid home-LAN block, so blacklisting would be wrong).
- **`enumerateLocalIPv4()`** — the old enumeration heuristic, kept verbatim as the **fallback when the probe errors** (a box with no default route — e.g. static IP without a gateway — must still publish so app installs don't fail), with a `slog.Warn` that the announced address may be wrong.
- **`detectIPv4(probe, enumerate)`** — the composition, split out so the fallback branches are unit-testable without touching the routing table.
- The file carries no build tag (pure `net` code) so the tests run on every platform, same as `slug.go`.

### Publisher changes (`publisher.go`)

- **Test override split from the cache:** the `localIP` field doubled as both; it's replaced by a `detectIP func() (string, error)` field tests can stub. Production leaves it nil and gets the probe.
- **Process-lifetime cache dropped:** the IP is re-detected on every `Publish`, so apps installed (or stopped/started — start re-publishes via the lifecycle start path) after a DHCP change announce the current address. **Honest limit:** entry groups committed *before* the change keep their old literal IP until the brain replays them at restart (mixed-announcement window); live IP-change replay is slice 2 (#130 carries the network-state surface).
- Package comment documents the probe caveat: a full-tunnel VPN on a dev machine routes the probe through the tunnel and announces the VPN address. Malmo installs don't run client VPNs.

### Spec corrections (same PR, per the issue)

- `BOOT.md` ~L95 still described the dead `/etc/avahi/services/` static-file mechanism (the 0012 false start) **and** claimed restart durability came "for free" from Avahi watching the directory. Replaced with the real mechanism: DBus entry groups, process-local, lost on host-agent restart, replayed by `lifecycle.Reconcile` at brain startup (`DISCOVERY.md` # Restart durability).
- `DISCOVERY.md` # What we publish said all three record categories are "driven by the brain via host-agent" — the host record (`malmo.local`) is avahi-daemon's native per-interface host record driven by the system hostname, not published by our code.

## Verification

- **Unit tests** (`localip_test.go`, no build tag): probe-wins / falls-back / both-fail composition with stubs; `probeLANIPv4` against the real routing table (skips if no default route).
- **Integration tests** (`dbus_linux_test.go`, `avahitest` tag, run locally against the real avahi-daemon): `TestDBusPublisher_RedetectsIPPerPublish` (stub counts two detections for two publishes — no cache); `TestDBusPublisher_AnnouncesRouteProbedAddress` — the **#129 regression check**: publish, then `avahi-resolve -4 -n`, asserting the resolved address equals the route-probed one.
- **Real-system done-when, on a box with six Docker bridges up** (`docker0` + five `br-*`, LAN on `eno1` 192.168.2.160): built `cmd/host-agent-real`, published `malmotest129` through `POST /v1/discovery/publish` over the UNIX socket, and `avahi-resolve -4 -n malmotest129.local` returned **`192.168.2.160`** — the LAN address, not `172.x`; unpublish withdrew the name (resolve times out after). Same steps as `dev/test-avahi-publisher.sh` (the script itself couldn't run unmodified because `.dev/` on this machine is root-owned from a past QEMU run — `make host-agent-real` can't write the binary there; built to `/tmp` instead, identical flow).
- `make check` green.

## Known gaps & deviations

- **Mixed-announcement window after an IP change:** already-committed entry groups keep the literal IP they were committed with; only a brain restart (reconcile replay) or per-app republish refreshes them. Full IP-change replay is the network-state slice (#130).
- **The probe follows the default route**, so a full-tunnel VPN on a *dev* machine announces the tunnel address (documented in the package comment). Not a production concern.
- **Fallback enumeration is still order-dependent** — by design, it only runs when the probe fails (no default route), and it warns.
- The `avahitest`-tagged integration tests still don't run in CI (no nspawn lane yet — carried from [avahi-dbus-publisher.md](avahi-dbus-publisher.md)); they were run by hand on this box.

## What's next

- Nothing new from this slice. The network-state slice (#130: per-LAN-interface announcement, Avahi interface allowlist, IP-change replay) is the follow-up that subsumes the mixed-announcement window.
