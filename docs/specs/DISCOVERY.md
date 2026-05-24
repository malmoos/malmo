# malmo LAN Discovery & mDNS

> Working spec for how malmo boxes and their apps are *found* on the local network — hostname resolution, service advertisement, and the publisher daemon. Touches `MALMO_NETWORK.md` (URL schemes, "Use secure URLs" toggle), `APP_LIFECYCLE.md` (reconciler owns Avahi state alongside Caddy), `STORAGE.md` (Samba/SMB advertisement), `FIRST_RUN.md` (Android-household nudge toward secure URLs), `BOOT.md` (Avahi unit ordering).

## Stance

Discovery is the bridge between "the box is on the LAN" and "the user can type a URL and reach an app." For the `.local` URL scheme committed in `MALMO_NETWORK.md` (`photos.malmo.local`, `notes.malmo.local`, …) to work, every app slug must be a name the LAN can resolve *before* HTTP routing happens. There is no shortcut: routers don't host our zones, browsers don't synthesize subdomains, and TLS termination at Caddy is moot if the name never resolves.

malmo's posture is **Avahi as the LAN nameserver, per-app records published by the reconciler, no client-side magic required.** Discoverability is owned by the brain in the same lifecycle as Caddy site config — install an app, two things get published; uninstall, two things get torn down.

We also accept up-front that **`.local` is a desktop story.** Android browsers do not resolve `.local` at the OS level (see below). The `<box-id>.malmo.network` HTTPS path in `MALMO_NETWORK.md` exists primarily to serve that audience; it is the compatibility path, not a premium feature.

## Locked: Avahi as the publisher

`avahi-daemon`, not `systemd-resolved`'s mDNS responder. The reasons:

- Avahi publishes service records (`_http._tcp`, `_smb._tcp`, `_device-info._tcp`) — the things Finder, Windows Explorer, and Linux file managers key off when populating "Network" or "Locations" sidebars. systemd-resolved's mDNS is hostname-only.
- Samba (SMB shares from `STORAGE.md`) integrates with Avahi out of the box for share advertisement, including TimeMachine. Adopting a second responder for hostnames would mean two implementations multicasting on the same socket — at best wasteful, at worst they fight over claim/defend semantics.
- It's what every neighbor in `CLAUDE.md` (Umbrel, CasaOS, Synology under the hood, TrueNAS) runs, for the same reasons. The interop surface is well-trodden.
- ~5 MB resident. Not a footprint concern.

`systemd-resolved` keeps its DNS-stub role; its mDNS responder is disabled at image-build time.

## What we publish

Three categories of records, all driven by the brain via host-agent:

### 1. The host record

`malmo.local A <lan-ip>` — the box itself. Published once at boot, re-announced on link-up and IP change. The dashboard at `https://malmo.local` (or `http://malmo.local` pre-toggle) resolves through this.

### 2. Per-app A records

For each installed app with slug `<slug>`: `<slug>.malmo.local A <lan-ip>`.

**Mechanism: Avahi DBus `EntryGroup.AddAddress`.** The install reconciler calls
`org.freedesktop.Avahi.Server.EntryGroupNew`, then
`org.freedesktop.Avahi.EntryGroup.AddAddress` with the slug hostname and the
box's local IPv4, then `Commit`. Uninstall calls `EntryGroup.Free`, which
withdraws the announcement.

Static service files (`/etc/avahi/services/*.service`) were the original plan
but were verified not to work for this use case on 2026-05-24: Avahi static
files announce *services*, not bare A-record aliases. Even with the corrected
XML (`<host-name>` inside `<service>`), `avahi-resolve -n <slug>.malmo.local`
timed out — Avahi will not publish a standalone A record from a static file.
DBus `EntryGroup.AddAddress` is the only programmatic path. See
`DECISIONS.md` entry 2026-05-24 and `docs/progress/0013-avahi-dbus-publisher.md`.

**Restart durability.** DBus entry groups are process-local: they are lost when
host-agent restarts. The brain re-publishes all running instances at startup via
the existing startup reconcile (`lifecycle.Reconcile`), which already calls
`host.Publish` for every instance in the `running` state. This covers both
"brain restart while host-agent was running" and "both restart together."

**Known gap:** mid-life host-agent restart while the brain is running is not
covered. The brain does not currently detect that host-agent restarted (only
that it is reachable). A future mitigation is to poll `GET /v1/system/status`
for `uptime_s` decreasing and replay on detection. Tracked in
`docs/progress/0013-avahi-dbus-publisher.md`.

### 3. Service records (Bonjour browsing)

For the box itself: `_device-info._tcp` with `model=malmo` so it appears with a recognizable icon in Finder. Samba publishes its own `_smb._tcp` and TimeMachine records (`STORAGE.md`).

For individual apps: **not in v1.** We do not publish per-app `_http._tcp` service records. The browse-the-network experience for apps is the dashboard, not the OS file manager. Revisit if compelling use cases appear.

## The reconciler owns Avahi state

This is the load-bearing rule. Install transaction (`APP_LIFECYCLE.md`):

1. Write compose project.
2. Write Caddy site block.
3. Call Avahi DBus `EntryGroup.AddAddress` + `Commit`.
4. Reload Caddy; Avahi multicasts immediately on Commit.
5. Wait for both to be live (Caddy reload returns; Avahi announcement multicasts).
6. Mark app `ready`.

Uninstall does the inverse. Slug rename touches both. If either step fails, the install rolls back both — never leave a Caddy block routing for a name nobody announces, or an Avahi record for a Caddy block that doesn't exist. The two are siblings, lockstep.

**Install latency note.** Avahi announcement takes <1 second on a healthy LAN, but it's a real step — other devices need to multicast-cache the answer before resolution succeeds. The dashboard should not mark an app `ready` until the announcement has been emitted (Avahi's DBus `EntryGroup.StateChanged` → `ESTABLISHED`). Spec'd in `APP_LIFECYCLE.md`'s install-transaction section.

## Interface scoping

By default Avahi advertises on every interface. We restrict it to **LAN interfaces only** — ethernet and WiFi managed by NetworkManager, not the Headscale mesh interface (where MagicDNS handles naming) and not Docker bridge networks.

Configured in `/etc/avahi/avahi-daemon.conf`:

```
allow-interfaces=eth0,wlan0  # actual names resolved at boot
deny-interfaces=tailscale0,docker0,br-*
```

host-agent computes the allow-list at boot from NetworkManager's connection state and writes the config fragment. Re-computed on interface add/remove. Spec'd in `BRAIN_HOST_PROTOCOL.md` as part of the network-state surface.

## Why subdomains can't be wildcarded — and what we do instead

mDNS (RFC 6762) has no central server and no zone file. Each device announces *exactly* the names it owns; resolvers ask "who has this exact name?" and accept the first answer. There is no wildcard synthesis. RFC 6762 §12 doesn't define wildcard semantics, and no major resolver implements them.

Three options were considered:

- **Per-app A records, published on install** (option A, chosen). Works on every mDNS-capable client. Symmetric with Caddy reconciliation. Scales easily to ~100 apps; multicast announcement traffic is negligible at that scale.
- **CNAME `<slug>.malmo.local` → `malmo.local`** (option B, rejected). CNAME-following over mDNS is under-specified. Apple's mDNSResponder follows them, Windows Bonjour mostly does, systemd-resolved has had bugs, and several Linux NSS implementations don't. Compatibility loss with no upside — the reconciler still has to publish each CNAME, identical work to publishing A records.
- **Single A record + Host-header routing only** (option C, rejected). Requires the browser to resolve the subdomain before it can send a `Host:` header. mDNS doesn't resolve names that weren't announced, so the request never leaves the client. The model assumes resolution is solved; on the LAN, it isn't.

## Client compatibility

| Client | `.local` resolution | Notes |
|---|---|---|
| macOS (Safari, Chrome, Firefox, Finder) | ✅ Native | mDNSResponder built-in; this is the gold path. |
| iOS (Safari, Chrome, Files.app) | ✅ Native | Same stack as macOS. |
| Windows 10/11 (Edge, Chrome, Explorer) | ⚠️ With Bonjour | Native support is partial and version-dependent. Bonjour Print Services (free Apple download, also shipped by iTunes/Adobe) is the reliable path. **First-run dashboard should detect Windows without Bonjour and link to the installer.** |
| Linux desktop | ✅ With `nss-mdns` | Almost universally installed; we don't need to do anything. |
| **Android (any browser)** | ❌ Not at OS level | NSD is an app-API, not a system resolver. Browsers don't use it. `.local` URLs return NXDOMAIN. **No workaround at malmo's layer.** |
| Chromecast / smart TVs / IoT | Varies | Out of scope for v1 user-facing URLs. |

## The Android problem, explicitly

Android does not wire mDNS into `getaddrinfo`. A browser query for `photos.malmo.local` is sent to the configured unicast DNS server (the router), which returns NXDOMAIN. There is no fallback. This is by design — Google has battery, multicast-on-WiFi-cost, and security reasons, and their preferred discovery model is cloud-mediated (Cast, Nearby).

Implication for malmo: **households with Android users need `<box-id>.malmo.network` HTTPS URLs** (`MALMO_NETWORK.md`), where resolution goes through public DNS. The "Use secure URLs" toggle is, in practical user-facing terms, the *"my household has Android devices"* toggle.

Two follow-ups:

- **First-run nudge** (`FIRST_RUN.md`): the wizard asks "Will anyone in your household use this from an Android phone?" If yes, flip the secure-URLs toggle and explain why. Avoids the "works on my laptop, broken on my partner's phone" support ticket.
- **Dashboard hint**: when a member is added and their first dashboard session comes from an Android User-Agent while secure-URLs is off, surface a one-time banner to the admin: "Your household includes an Android device. Enable secure URLs so apps work everywhere."

## Failure modes & known gotchas

These are not bugs we can fix in malmo's code, but support-load realities to anticipate. Each one wants an entry in the diagnostic bundle (`LOGGING.md`) when relevant.

- **AP isolation / client isolation** on consumer routers silently blocks multicast between clients. Common on guest WiFi, occasional on default home configs. Symptom: box pings fine by IP, `.local` doesn't resolve from any client. Diagnostic bundle should include a "multicast probe" — broadcast a known query, count responses; zero responses with the box on the same subnet strongly implies AP isolation.
- **`.local` collision with Active Directory.** Some SOHO/corporate networks use `.local` as an internal AD domain. Their unicast resolver claims `.local` queries and Avahi never gets asked. Rare in our audience but real. No fix; document and let the user switch to secure URLs.
- **VLAN segmentation.** Multicast doesn't cross VLAN boundaries by default. Box on one VLAN, clients on another, no discovery. User-network-design issue; out of scope for v1 to detect.
- **IPv6 link-local.** Avahi publishes both A and AAAA by default. Some older clients get confused. We leave the default on; if reports surface, we have the `use-ipv6=no` knob.
- **Multiple `.local` responders on the LAN.** A second malmo box, a Synology, a printer — all happily coexist (each owns its own names). The only collision case is two devices claiming `malmo.local`; Avahi's RFC 6762 §9 conflict-resolution renames the loser to `malmo-2.local`. This is correct behavior but produces a confusing URL change. The dashboard surfaces a typed health issue (`hostname-conflict`) when Avahi reports a rename; admin can pick a different hostname from Settings → System → Network.
- **IP change without link-down.** Rare (manual DHCP server change, router swap). Avahi's announce-on-link-up doesn't fire. host-agent watches NetworkManager state and pokes Avahi (`avahi-daemon --reload`) on IP change.

## What we explicitly don't do

- **No wildcard records.** Doesn't exist in mDNS; not worth trying to fake.
- **No private CA for `.local` HTTPS.** `.local` + Let's Encrypt is impossible (no public DNS). Installing a private CA on every client device is a non-starter for our audience. `.local` is HTTP-only by definition; HTTPS goes via `<box-id>.malmo.network`. Stated here to forestall the "just ship a private CA" suggestion.
- **No DNS-SD service catalog for apps.** Dashboard is the browse surface, not Finder's "Network" sidebar. We may revisit if a use case emerges.
- **No LLMNR.** Microsoft's old multicast-name protocol. Modern Windows leans on mDNS-with-Bonjour; LLMNR doesn't carry service records and is being phased out for security reasons. Not worth supporting.

## Cross-references

- `MALMO_NETWORK.md` — URL schemes, secure-URL toggle (the Android compatibility path).
- `APP_LIFECYCLE.md` — install/uninstall transaction includes Avahi state alongside Caddy.
- `STORAGE.md` — Samba/SMB advertisement via Avahi; TimeMachine records.
- `FIRST_RUN.md` — Android-household nudge during wizard; Windows-Bonjour link.
- `BOOT.md` — `avahi-daemon.service` ordering; not on the critical path for dashboard availability.
- `HEALTH.md` — `hostname-conflict` typed issue when Avahi renames our host.
- `BRAIN_HOST_PROTOCOL.md` — host-agent computes the Avahi interface allow-list from NetworkManager state.
- `LOGGING.md` — multicast probe in diagnostic bundle.

## Open

Tracked in `NEXT.md`. The notable ones:

- **Multicast probe in the diagnostic bundle** — exact shape, what we measure, how we present the AP-isolation suspicion to admins.
- **Per-app `_http._tcp` service records** — whether listing apps in Finder/Explorer Network sidebars is worth the complexity. Default no; revisit if requests appear.
- **Hostname rename UX** — when Avahi conflict-resolves us to `malmo-2.local`, how aggressively the dashboard prompts for a fix vs. lets it ride.
- **Windows Bonjour detection** in first-run — User-Agent isn't reliable; consider a JS-side mDNS probe instead.
