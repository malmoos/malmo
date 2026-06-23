# Cloud acme-dns public face live — close the cross-repo gap C3b left open

- **Status:** done
- **Date:** 2026-06-23
- **Specs touched:** `ENVIRONMENT.md` (# Networking & discovery — as built), `DECISIONS.md` (2026-06-21 acme-dns endpoint constant — gap bullet closed), `NEXT.md`

Doc-sync only. Closes the known gap that [hosted-wildcard-cert.md](hosted-wildcard-cert.md) (C3b, #207) recorded and [hosted-caddy-image-bake.md](hosted-caddy-image-bake.md) (CL6 prep) carried forward: the box-side wildcard-cert machinery shipped against a box-side constant `MALMO_ACMEDNS_ENDPOINT` (default `https://auth.malmo.network`), but the cloud deploy bound acme-dns's HTTP API to `127.0.0.1:4443` (internal `/register` only) — no public face existed, so the default was a chosen value, not a confirmed endpoint, and no box could renew. The gap was filed as `malmoos/cloud` #14.

## What was done

`malmoos/cloud` #14 (cloud PR #29) deployed the public face, verified cloud-side: the control-plane VM fronts acme-dns with the shared Caddy over real Let's Encrypt TLS for `auth.malmo.network`, exposing only `/update` + `/health` and 404-ing everything else (`/register` stays loopback-only); the authoritative `:53` face is delegated from the apex zone in Google Cloud DNS and answers publicly (confirmed via Google + Cloudflare resolvers). `https://auth.malmo.network` is therefore a **confirmed live endpoint** — the box-side default holds unchanged and needs no re-seed.

OS-side this is a documentation reconciliation, not a code change. The box-side wiring (C3b/#207 + the CL6 image bake) was already complete; nothing in the brain, the seed contract, or the Caddy config moves. Updated, in this PR:

- `ENVIRONMENT.md` # Networking & discovery — as built: the intro now states the public DNS + acme-dns face are deployed and confirmed, and narrows the remaining gap to real end-to-end *issuance*.
- `DECISIONS.md` (2026-06-21 — acme-dns endpoint is a box-side constant): the "open cross-repo gap" bullet is rewritten as closed, citing #14.
- `NEXT.md`: the "Public acme-dns API face" cross-repo item is marked deployed, with the residual narrowed to the joint issuance run.

The frozen C3b/CL6 progress entries ([hosted-wildcard-cert.md](hosted-wildcard-cert.md), [hosted-caddy-image-bake.md](hosted-caddy-image-bake.md)) are **left untouched** — they are snapshots of their moment; this entry is the where-we-are-now record that supersedes their "face not deployed" / "issuance unverified" notes.

## Known gaps & deviations

- **Real issuance still unverified.** A live box actually obtaining and renewing its `*.<box-id>.malmo.network` wildcard against the now-public face is exercised only in the joint cloud on-ramp (cloud #6 / CL6) — there is still no real Let's Encrypt/DNS in the OS inner loop or the air-gapped QEMU cloud lane. The deployment confirmed here is the prerequisite for that run, not the run itself.
- **`allowfrom` / rate-limiting on the public `/update`** is a cloud-side hardening item (tracked in `malmoos/cloud`), not an OS concern.

## What's next

- The joint cloud #6 / CL6 live on-ramp — signup to a live HTTPS box — is the first and only real test of issuance against this face. It is a cloud-side-driven run; the OS box-side machinery is complete.
