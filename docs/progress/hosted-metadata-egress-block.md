# Hosted: block app-container egress to the cloud metadata endpoint (#251)

- **Status:** done (implementation + QEMU-lane assertion written; neither the assertion nor a live boot was run locally — first execution at CL6)
- **Date:** 2026-06-25
- **Closes:** #251 (epic #196)
- **Specs touched:** `ENVIRONMENT.md`, `DECISIONS.md`

Closes the SSRF residual risk that [cloud-image-real-cloud-seed-channel.md](cloud-image-real-cloud-seed-channel.md) (#246) surfaced and deferred. On a hosted (cloud) box the first-boot seed — the **admin-bootstrap secret** and the **acme-dns password** — stays retrievable from the cloud metadata endpoint (`http://169.254.169.254/...`) for the life of the server. Docker NATs container egress out the host NIC, so without a host rule any local process — including an **untrusted third-party app container** — can read those secrets: the classic cloud-metadata SSRF (cf. the 2019 Capital One breach). Leaking the acme-dns password is the worst case — it is the long-lived credential to rewrite `_acme-challenge.<box-id>` and so mint/MITM the box's `*.<box-id>.malmo.network` wildcard cert.

## What was done

Tooling-only (`dev/cloud/` + specs); no Go, no brain/host-agent change.

- **Standing nftables rule — `dev/cloud/mkosi.extra/etc/malmo/metadata-firewall.nft`.** Its own `inet malmo_metadata` table with one chain hooking **`forward`** at a pre-Docker priority (`-300`), `policy accept`, dropping `ip daddr 169.254.169.254` (with a counter). A separate table means loading it never flushes Docker's own nat/filter rules; `policy accept` means all non-metadata forwarded traffic passes through untouched. The `add table` / `delete table` / re-create idiom makes re-running idempotent.
- **A dedicated oneshot — `dev/cloud/mkosi.extra/etc/systemd/system/malmo-metadata-firewall.service`.** `Type=oneshot`, `RemainAfterExit=yes`, `ExecStart=/usr/sbin/nft -f /etc/malmo/metadata-firewall.nft`, ordered `Before=docker.service host-agent.service` so the rule is in place before docker or the brain can start any container.
- **Enabled for both lanes — `dev/cloud/mkosi.postinst.chroot`.** One `enable_unit` line drops the `multi-user.target.wants` symlink. The production postinst is auto-run for the lean `make build-cloud-image` image **and** the boot-proof test lane (which `Include=..`s it), so the rule ships in production and is exercised in QEMU.
- **A QEMU-lane proof — `dev/cloud/cloud-assertions.sh`.** New step 6b asserts the rule's **shape** (a `forward` hook, never an `output` hook, matching the metadata IP — so the host-root seed fetch is structurally safe) and that a **real container packet hits the drop**: it reads the drop counter before/after a `timeout`-bounded `/dev/tcp` connect to `169.254.169.254:80` issued from inside the brain's netns (a genuine forward-path source over `malmo-ingress`) and requires the counter to increment.

### Why a forward-hook DROP (the design call #251 delegated)

The seed materializer fetches the endpoint **as host root in the host netns — the OUTPUT path** — once at first boot. App/control-plane containers reach it **over a Docker bridge — the FORWARD path** (the destination IP survives the POSTROUTING masquerade). Dropping only in `forward` blocks **every** container — apps *and* the brain, which reads the seed from disk, never from metadata — while leaving the host-root first-boot fetch untouched. So it is a **standing** policy applied every boot, with no "unblock until seeded" timing or state, and it resolves #251's "does a hosted box need metadata after first boot?" question: no container ever does.

Owner: a static image-baked oneshot, not a host-agent reconciler. The rule is one fixed link-local IP that never changes, so it does not need the deferred dynamic firewall reconciler (`NEXT.md`); a future host-agent firewall posture subsumes it. Putting it in the seed materializer script was rejected (firewall logic is the wrong owner there and would be clobbered by that future reconciler).

## How it maps to the specs

- **`ENVIRONMENT.md` # Provisioning & first-boot** — the #246 residual-risk note is closed; a new "Metadata-endpoint egress block (realized, #251)" as-built bullet records the mechanism.
- **`ENVIRONMENT.md` # How the profile is realized / # Public-by-default** — "malmo manages no firewall ruleset of its own in hosted" now carries its one exception; the metadata block is named as the **first** malmo-owned in-guest rule, with the general default-deny backstop still deferred.
- **`DECISIONS.md` 2026-06-25** — the decision entry: one in-guest rule for a malmo-specific SSRF, distinct from the still-deferred provider-substitute backstop; the 2026-06-19 "filtering is the provider's" substance holds (link-local source never reaches the provider edge).

## Known gaps & deviations

- **Live verification deferred to CL6.** The "Done when" bar — a container `GET` to `169.254.169.254` is refused on a real booted box while seed ingestion still works — is covered by the QEMU-lane assertion (`cloud-assertions.sh` step 6b), but that assertion has **not itself been run**: the QEMU lane needs root + the cleared apparmor-userns sysctl (the same blocker), so the first real execution is the joint cloud#6 / CL6 live run (the same deferral as C3a/C4/#246) — or a local `sudo -E make test-cloud-qemu` once `kernel.apparmor_restrict_unprivileged_userns=0` is set. Although #189 (the mkosi `PR_CAPBSET_DROP` blocker) is now closed, the local box still has that sysctl at `1`.
- **Host-network containers bypass this rule.** A `--network=host` container shares the host netns, so its traffic to `169.254.169.254` would take the OUTPUT path, not FORWARD, and this rule would not intercept it. That is structurally defended elsewhere — admission rejects `network_mode: host`/`none`/`container:*` at install (`internal/admission`), so a catalog/Door-2 app cannot reach host networking — but the future general default-deny backstop should account for it (an OUTPUT-side uid/owner match) if host-network workloads ever become reachable.
- **nft ruleset not syntax-checked locally.** `nft -c -f` needs root (netlink cache) and rootless `unshare -rn` is blocked by the apparmor sysctl above; the file is standard nft and validated by inspection, with the real parse happening at first boot (and the assertion failing loudly if the table is absent).
- **IPv4 only / single address.** The rule targets `169.254.169.254` exactly (Hetzner's endpoint, `MALMO_SEED_METADATA_URL`'s default), not the whole `169.254.0.0/16` link-local block. Surgical per the issue; a broader block is a trivial future tightening if another provider's endpoint is added.
- **No general in-guest backstop.** This is the metadata SSRF control only; the deferred provider-substitute default-deny ruleset (`NEXT.md`, `DECISIONS.md` 2026-06-19) is untouched.
- **Fail-open, deliberately.** The oneshot is *ordered* `Before=docker.service`, not a hard `Requires=`, so a (near-impossible, static-ruleset) nft load failure leaves the box booting and serving with the SSRF window open rather than bricking the whole control plane on a bad rule. The boot-proof assertion fails loudly (table absent) so the lane catches it; a hard fail-closed `Requires=` is rejected for v1 because a down box is worse than a transient window for a static, validated rule.

## What's next

1. **CL6 live acceptance** — confirm on a real Hetzner provision that an app container cannot reach `169.254.169.254` while first-boot seed ingestion still works (the issue's "Done when").
2. **General in-guest `nftables` backstop** (deferred, `NEXT.md`) — if/when a provider posture without security groups needs an in-guest default-deny, the `inet malmo_metadata` table and this oneshot are the natural seam for a host-agent firewall reconciler to grow into and subsume.
