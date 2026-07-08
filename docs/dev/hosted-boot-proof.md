# Hosted HTTPS bring-up & the cloud boot-proof — how it works, how to debug

The hosted box gets its `*.<box-id>.malmo.network` wildcard HTTPS with **no toggle**: it happens on every boot, driven by the brain. This page is the "where we are now" view for that path and its air-gapped test lane — read it before chasing a red cloud boot-proof or a "`:443` doesn't bind" report. **As of the entry that added this doc, the path works and the lane is green**; the notes below are the map for when it isn't.

The design source of truth is `../specs/ENVIRONMENT.md` # Networking & discovery and # Provisioning & first-boot; the lane's shape is `../specs/TESTING.md` # Hosted cloud variant. This doc is the operational companion — the flow, the log lines, and the failure-mode → where-to-look table.

## The happy path (what a healthy boot does)

1. **Seed lands.** `malmo-seed.service` materializes `/var/lib/malmo/seed.json` (delivered by the provider's cloud-init on a real box, or by an SMBIOS credential in the QEMU lane) *before* host-agent launches the brain. The seed is `{box_id, admin_bootstrap_secret, enrollment}`; `enrollment` is the per-box acme-dns account `{subdomain, username, password}`.
2. **Brain ingests it.** On the first hosted boot `cmd/brain`'s `loadHostedEnvironment` reads the seed, persists the assertion key + enrollment + box-id (box-id last, as the commit marker), and logs **`hosted: provisioning seed ingested`**. On every later boot it loads the *persisted* identity and ignores any re-delivered seed (the identity is frozen in SQLite). An absent/unreadable seed logs `hosted box has no provisioning seed …` and the box stays pre-provisioned (no box-id, no wildcard pass) rather than crashing.
3. **Wildcard TLS is applied.** If `profile == hosted` **and** `enrollment.Complete()`, the brain calls `caddy.EnsureWildcardTLS`, which:
   - PUTs Caddy's `tls` app: the wildcard `*.<box-id>.malmo.network` in **`certificates.automate`** (the "what to obtain") plus an automation policy pinning it to the acme-dns **DNS-01** issuer (the "how"). Both are required — a policy without an automate entry never places the order (this was the #278/#301 bug; see `../progress/hosted-wildcard-cert-automate.md`).
   - PATCHes the server's `listen` to `[":80", ":443"]`, binding `:443`.
   - Logs **`caddy: wildcard TLS configured`**. This is synchronous config only — Caddy obtains the cert in the background on its own schedule, so a slow/unreachable ACME never blocks startup and `:443` binds regardless of whether a cert exists yet.
4. **Two-path cert model.** The **wildcard** `*.<box-id>` is obtained via acme-dns DNS-01 (challenge at the delegated `_acme-challenge.<box-id>`). The **apex** `<box-id>.malmo.network` (the dashboard host) is *not* routed through acme-dns — Caddy's default issuer gets it over tls-alpn-01/http-01 once the dashboard route names it. So exactly one name touches acme-dns; there is no order-vs-order race to manage.

Net: a provisioned box logs both milestones, binds `:443`, and serves every `<slug>.<box-id>.malmo.network` from the one wildcard cert.

## The boot-proof lane

`dev/cloud/run-cloud-tests.sh` (`make test-cloud-qemu`) boots the real production image under QEMU, **air-gapped** (`restrict=on` — the seed arrives over SMBIOS, never the network), and greps a serial-console verdict written by `dev/cloud/cloud-assertions.sh`. Three scenarios over one persisted overlay:

| Scenario | What it asserts (box-facing) |
|----------|------------------------------|
| `unseeded` | No seed → `GET /_malmo/sso` ⇒ 503 (gate armed, closed); `POST /setup` ⇒ 403 (disabled on hosted — the owner bootstraps via SSO, not a secret). |
| `seeded` | Seed with a **complete** enrollment → brain logs `provisioning seed ingested`; a bad token on `/_malmo/sso` ⇒ 401 (verifier armed); **`:443` binds** and the brain logs `caddy: wildcard TLS configured`. |
| `frozen` | A *different* seed re-delivered on a later boot is ignored; the box still serves under the original box-id and does **not** re-ingest. |

**Air-gapped means config-apply, not a real cert.** The lane cannot reach acme-dns or Let's Encrypt, so the `seeded` scenario proves the brain *applies* the issuer + binds `:443`, not that a cert was obtained. Real DNS-01 issuance + a live `*.<box-id>` cert are verified on a real provider box on a real network — deliberately outside this lane (the whole "config that looks right but never issues" class is invisible air-gapped; the `certificates.automate` unit test in `internal/caddy/caddy_test.go` is the closest static guard).

## When it's red: where to look

The serial log is the primary artifact: `.dev/cloud-boot/last-serial.log` locally, or the "Seeded-boot test" step log in CI. On failure `cloud-assertions.sh` dumps a `=== MALMO_CLOUD_DIAG ===` block (docker ps, networks, iptables, and **`malmo-brain` log tail**). Grep it for the milestone lines above.

| Symptom | Most likely cause | Where to look |
|---------|-------------------|---------------|
| `:443` never binds / connection-refused | `EnsureWildcardTLS` was **skipped** — this is nearly always `boxID == ""` (seed not ingested) or an incomplete enrollment, *not* a bug in the bind/apply path. | Is `provisioning seed ingested` in the brain log? Is `/var/lib/malmo/seed.json` present and does it carry an `enrollment` block? If both are present and `:443` still doesn't bind, then it's a genuine apply-path regression — check `caddy: wildcard TLS configured` is absent and read the Caddy admin PUT/PATCH errors. |
| `:443` binds but no cert (real box) | DNS/ACME reachability — acme-dns delegation, the box's resolver, or Let's Encrypt reach. Not reproducible air-gapped. | A real provider box only; read the box's Caddy log (via provider rescue mode — hosted ships no SSH). Not a boot-proof concern. |
| `hosted /setup not disabled: … 503` (unseeded) | **Startup race, not a bug.** A 503 there is Caddy answering "no ready upstream for `/api`" in the first second after the stack comes up. | The assertion now rides through transient 502/503 and only fails on a stuck window (`cloud-assertions.sh`, the `/setup` poll). If it recurs, confirm the brain reached `caddy: dashboard route installed` and `malmo-brain listening`. |
| Brain baked old but image labeled new | Stale `.dev/` control-plane cache reused across a build. | Build on a fresh checkout (the CI job does — no `.dev/` cache), which rebuilds the control-plane images from source. |

A **broken image build** presents as several of these at once (e.g. `:443` refused *and* seed-ingest missing) — check the build step succeeded and the control-plane images were rebuilt before diagnosing the box logic.

## How to run it

- **CI (preferred — no local root/KVM, no image push):** `gh workflow run "CI / Cloud image" --ref <branch> -f publish=false`. Builds the image, then runs the `unseeded seeded` boots under QEMU. `publish=true` (the default) additionally uploads the built image to the provider — only do that deliberately. Runtime ~40–120 min.
- **Local:** `sudo make test-cloud-qemu` (needs root + `/dev/kvm`). Scope boots with `MALMO_CLOUD_BOOTS="seeded"` to reproduce the wildcard path alone, or the default `"unseeded seeded frozen"` for the full run.

## Related history (frozen snapshots — background, not the current view)

- `../progress/hosted-wildcard-cert-automate.md` — the `certificates.automate` fix (the real #278/#301 root cause).
- `../progress/cloud-wildcard-tls-assertion.md` — how the seeded lane came to assert the `:443` bind + apply.
- `../progress/cloud-image-live-onramp-fixes.md` — the two real-box fixes (seed-fetch keep-alive, static resolver) that a green real box depends on.
