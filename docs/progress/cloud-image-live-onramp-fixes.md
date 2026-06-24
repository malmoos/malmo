# Cloud image: live on-ramp fixes — seed-fetch keep-alive + box DNS resolver

- **Status:** done
- **Date:** 2026-06-24
- **Specs touched:** `docs/specs/ENVIRONMENT.md` # Networking & discovery (the box's name resolution is a static `/etc/resolv.conf`).

Two box-side bugs that only surfaced when the production cloud image was driven through a real Hetzner provision → wildcard-cert → admin-claim cycle (the `malmoos/cloud` #6 / CL6 live on-ramp, the joint acceptance for the hosted profile). Both follow `cloud-image-real-cloud-seed-channel.md` (#246): that entry shipped the real-cloud seed channel and noted "the live Hetzner endpoint + DHCP timing is the cloud#6 / CL6 acceptance" — running it is what exposed these.

Before the fixes, a provisioned box booted, networked, and served the control-plane 404 on `:80`, but never obtained its `*.<box-id>.malmo.network` wildcard cert (`:443` certless), so the on-ramp could not go green.

## 1. The seed-fetch dropped a good 200 when the metadata socket stayed open

`malmo-seed-materialize.sh`'s `http_get` keyed success on the exit code of `timeout … bash -c '… exec 3<>/dev/tcp/…; cat <&3'`. Hetzner's metadata server **ignores the HTTP/1.0 `Connection: close`** and holds the socket open, so `cat` never sees EOF and `timeout` kills it (exit 124) **after** the full `200 OK` + body were already captured. The trailing `|| return 2` then discarded that perfectly good response as "transient," every retry hit the same wall, and after the deadline the script logged "no seed" and exited 0 — leaving the box unprovisioned (`/setup` 503, no enrollment creds for Caddy, no cert).

Fix: decide on the **captured bytes**, not the subshell exit code — `[ -n "$resp" ] || return 2`, then parse the status line as before. An empty `resp` is the only genuine transient (a refused connect during the DHCP race, or a true hang); a non-empty `resp` carries the answer regardless of how the read ended. Confirmed against the real endpoint on a throwaway Debian box (writes a valid `seed.json`).

A regression test (`seed_materialize_test.go` `TestFetchSeed200KeepAliveSocketStillLandsSeed`) drives a **raw listener that writes the 200 and then holds the connection open** until the client is killed — reproducing the Hetzner behavior. It fails on the old code (exit 1, no seed) and passes on the fix. The pre-existing `TestFetchSeed200WritesBodyVerbatim` used a polite `httptest` server that closes the socket, so it never exercised this path — that test gap is exactly why the bug reached a live box.

## 2. The lean image had no DNS resolver, so Caddy could not reach the ACME endpoints

With the seed now landing, the brain configured Caddy's wildcard `tls` app (`:443` listening) but Caddy served **no certificate** and **no DNS-01 challenge TXT ever reached acme-dns** — the ACME order failed at its first call. Root cause: the image enables `systemd-networkd` + DHCP (so the NIC gets an IP) but ships **no `systemd-resolved` (not in the 133-package set) and no `/etc/resolv.conf`**, and networkd does not write that file itself. The box could route by IP — it reached the metadata service and served `:80` — but could resolve **no name**. So the brain's Caddy (a `malmo-ingress` bridge container, which derives its resolver from the host `/etc/resolv.conf`) could not resolve `acme-v02.api.letsencrypt.org` or `auth.malmo.network`, and issuance never started. Invisible until now because nothing on the box had ever needed outbound name resolution.

Fix (`mkosi.postinst.chroot`): write a static `/etc/resolv.conf` (public resolvers) in the first-boot postinst. No new package (the lean manifest stays at 133), and the box only ever resolves public names — control-plane images are baked, no registry pulls. After this the box obtained its wildcard cert in ~1 min; the acme-dns debug log showed the box's `TXT updated` followed by Let's Encrypt's 0x20-cased validation queries, and `:443` served a real, publicly-trusted cert. `ENVIRONMENT.md` # Networking & discovery documents the static-resolver fact.

## How it maps to the specs

- Unblocks `ENVIRONMENT.md` # Provisioning & first-boot (real-cloud seed channel, #246) and # Networking & discovery (the always-on wildcard cert via acme-dns DNS-01, C3b/#207) on real Hetzner — both are now exercised end to end, not just in the QEMU/test lane.
- `malmoos/cloud` #6 / CL6 (`TestOnRampLive`) passed green with the image carrying both fixes.

## Known gaps & deviations

- **Static resolver, not DHCP-provided.** `/etc/resolv.conf` hardcodes public resolvers rather than honoring the DHCP-offered DNS, which would need `systemd-resolved` — a package the lean image deliberately omits. Fine for a box that only resolves public names; revisit if the hosted box ever needs split-horizon or private resolution.
- **acme-dns metadata-secret exposure (#251) is unchanged** — the user-data (admin-bootstrap secret + acme-dns password) stays retrievable from the metadata endpoint for the box's life; blocking egress to `169.254.169.254` is host-agent's hosted firewall posture, not this change.

## What's next

- `#251` — host-agent hosted firewall posture (block app-container egress to the metadata endpoint).
- The cloud half (the `/api/v1/setup` path correction in the live e2e and the green-run record) lives in `malmoos/cloud` (`docs/progress/cl6-green-live-onramp.md`); nothing further is owed on the OS side for CL6.
