# Network-state slice: per-LAN-interface announcement, Avahi allowlist, IP-change replay

- **Status:** done
- **Date:** 2026-06-11
- **Specs touched:** `DISCOVERY.md` # Per-app A records (mechanism is now per-interface), # Interface scoping (no conf.d fragment — the main conf's key is rewritten in place; restart + republish semantics), # Failure modes ("pokes Avahi (`--reload`)" corrected — reload fixes neither the conf nor committed entry groups); `BRAIN_HOST_PROTOCOL.md` # Discovery (stale static-file implementation note replaced with the entry-group reality); `TESTING.md` # Medium lane (network-state coverage realized); `docs/architecture.md` host-agent-real row (discovery now real).

Closes issue #130 and the mixed-announcement window from [avahi-lan-ip-detection.md](avahi-lan-ip-detection.md) (frozen — its gap list stands as written). Discovery is now correct on multi-interface boxes and across IP changes: instead of one route-probed IPv4 announced on `interface=-1` (all interfaces, including mesh and Docker bridges), every app name is announced **per LAN interface with that interface's own address**, the LAN set coming from NetworkManager over DBus. The same set drives `allow-interfaces` in `/etc/avahi/avahi-daemon.conf`, and a NetworkManager watcher replays all announcements on every network change — committed Avahi entry groups hold literal addresses, so nothing else can fix them after a DHCP change.

## What was done

### `internal/hostagent/netstate` (new package — the shared network-state surface)

- **`LANInterface{Name, Index, IPv4}`** and **`NMProvider`**: enumerates NetworkManager's `ActiveConnections` over the system DBus and keeps those with `Type ∈ {802-3-ethernet, 802-11-wireless}` and `State == ACTIVATED` — the issue's open question resolved toward **enumerating active connections by type**, not the primary-connection pin (a box with ethernet + WiFi has two LAN interfaces; the pin names one). Docker bridges (`bridge` type), the mesh (`tun`), and veths are excluded structurally by the type filter — no name blacklist. Interface index from the kernel (`net.InterfaceByName`), IPv4 from `Ip4Config.AddressData`, result sorted by name, deduped, races-with-disappearing-connections skipped.
- **`Watch(ctx, onChange)`**: subscribes to `PropertiesChanged` under the NM path namespace, filters to the four NM interfaces (drops WiFi signal-strength noise), debounces 2s, and calls `onChange` — the IP-change replay trigger.
- **`DetectIPv4()` / `ProbeIPv4()`**: the #129 route-probe moved here from `avahipublisher` (the issue's "promote IP detection now that a second consumer exists"); `ProbeIPv4` stays exported for the #129 regression test.
- `nm_other.go` keeps the cross-platform build green (same idiom as `pamverifier`).

### `internal/hostagent/avahipublisher` (per-interface announce + replay)

- **`Publish` announces per interface:** one entry group per slug, `AddAddress(ifindex, …, ip)` once per LAN interface. The `LAN` field is optional — nil (the dev inner loop, no NM) falls back to the #129 single route-probed address on `interface=-1`. Zero LAN interfaces is a publish **error** (install surfaces it; the issue's second open question resolved toward fail-don't-defer, because the watcher retries as soon as a link comes back).
- **`RepublishAll()`**: frees and re-adds every stored group with current addresses, re-announcing the **stored** names verbatim — the collision fallback never re-runs, so a name the brain persisted can't silently drift. Per-slug failures keep the old mapping and surface the first error.
- **`Sync`** (`conf.go`): rewrites `allow-interfaces` inside `/etc/avahi/avahi-daemon.conf` (atomic temp + rename; Avahi has **no conf.d mechanism**, and the daemon reads its conf only at startup — `--reload` re-reads only static service files). On a conf change it restarts avahi-daemon (which destroys every committed entry group) and **always** ends with `RepublishAll` — an IP-only change is invisible at conf level but still invalidates the groups. Zero LAN interfaces → warn + no-op (boot race; next NM event retries).

### Wiring

- `Agent` gains a consumer-side **`NetState`** interface + `Net` field; `GET /v1/discovery/state` now reports the live LAN interface names (empty = "not measured"). `FakeNetState` for the fake binary (fixed plausible `eth0`) and tests.
- **`cmd/host-agent-real`**: `NMProvider` → publisher `LAN` + `Sync`; `Sync.Apply()` once at startup (non-fatal — unprivileged dev runs can't write `/etc/avahi`), then a `Watch` goroutine applying on every NM change. The fake binary's `MOLMA_DEV_AVAHI=1` path deliberately keeps `LAN` nil (no NM dependency in the inner loop).
- **`cmd/molma-network-verify`** (new, CGO-free): drives the same packages minus PAM for the QEMU lane — `lan` prints the NM LAN set, `serve` syncs + publishes + watches until killed.

### Medium QEMU lane (`dev/test-qemu/`)

The VM now has **three NICs with pinned MACs**: NIC1 carries SSH exactly as before (systemd-networkd DHCP, NM-unmanaged via MAC — the LUKS/TPM boot phases never depend on NM), NIC2/NIC3 are NetworkManager-managed on isolated usernets with distinct subnets. Image gains `network-manager`, `avahi-daemon`, `avahi-utils` (canary v16 → v17). The second boot's assertions then drive `molma-network-verify`: NM LAN set == exactly the two NM NICs (SSH NIC excluded), `allow-interfaces=<nic_a>,<nic_b>` written + daemon restarted + the published name resolves to a LAN address (conf-change → restart → republish path), `nmcli device disconnect` of one NIC rewrites the allowlist (interface-removal path), and a static-IP change on the survivor must make `avahi-resolve` return the **new** address (the IP-change replay, end to end through the real watcher).

## Verification

- **Unit tests** (no tags, everywhere): probe composition (moved), `writeAllowInterfaces` insert/replace/no-op/no-section/missing-file, `Sync.Apply` restart-and-republish/zero-iface-no-op/restart-failure-ordering/LAN-error, `discoveryState` interfaces with and without a provider.
- **`make test-netstate`** (new target, `nmtest` tag, real NetworkManager): LAN set on this box is exactly `{eno1, index 2, 192.168.2.160}` with six Docker bridges up — none leak; `Watch` subscribes and honors ctx cancel. Debounce-on-real-change deliberately not asserted here (flipping links on a developer box is not acceptable test behavior) — that's the QEMU lane's job.
- **`make test-avahi`** (`avahitest` tag, real avahi-daemon + real NM): per-interface announce resolves to a LAN interface address; zero-LAN publish errors; `RepublishAll` flips a stub address 198.51.100.7 → .8 and resolution follows while the stored name stays put. 9/9 pass on this box.
- **`dev/test-avahi-publisher.sh`** end to end with the real binary: startup `Sync.Apply` warns (unprivileged, expected), publish announces `targets=[if2=192.168.2.160]`, resolves, withdraws.
- **`make check` green** (gofmt + vet + OpenAPI freshness + full suite, with libpam0g-dev).
- **Medium QEMU lane: extended but not yet executed** — the lane needs interactive sudo, which this session didn't have. Run `sudo make test-medium-qemu` (first run rebuilds the image, ~5 min) to execute the network-state phase; everything up to the lane was verified as above.

## Known gaps & deviations

- **Multi-interface `AddAddress` in one entry group is proven only in the QEMU lane** (this box has a single LAN interface). mDNS multi-homing this way is believed-legal and mirrors avahi-daemon's native host record; the lane's two-NIC assertions are the proof.
- **Host-agent mid-life restart replay** (`uptime_s` regression detection, carried from [avahi-dbus-publisher.md](avahi-dbus-publisher.md)) did **not** ride along: it is brain-side (lifecycle reconcile), not cheap, and orthogonal to the host-agent-internal watcher this slice added.
- **`GET /v1/discovery/state` still hardcodes `publisher: "avahi-fake"` and `host_name: "molma"`** in both binaries — pre-existing debt, untouched; only `interfaces` went live this slice.
- **The brain stays out of network state** (the issue's third open question): no brain consumer of `DiscoveryState` exists, and nothing in this slice gave it one. The dashboard network page will revisit.
- The `nmtest`/`avahitest` tagged suites still don't run in CI (carried) — run by hand here, and by the QEMU lane once executed.
- `deny-interfaces` from the old `DISCOVERY.md` example is not written — an allowlist already denies everything else.

## What's next

- **Run the extended medium lane** (`sudo make test-medium-qemu`) and fix anything the two-NIC environment surfaces — done locally everywhere else, but the lane is the issue's done-when for the host-integrated parts.
- Brain-side host-agent-restart replay (`uptime_s` regression → reconcile republish) — still the standing gap from [avahi-dbus-publisher.md](avahi-dbus-publisher.md).
- WiFi setup UI on the same `netstate` surface (`BRAIN_HOST_PROTOCOL.md` # Network configuration) — separate slice.
