# malmo Environment Profiles

> Working spec for the two environments malmo runs in — the bare-metal home appliance and the malmo-operated cloud VM — and everything that differs between them. Companion to `SPEC.md`, `CONTROL_PLANE.md`, `BUILD.md`, `FIRST_RUN.md`, `STORAGE.md`, `MALMO_NETWORK.md`, `DISCOVERY.md`, `BOOT.md`, `THREAT_MODEL.md`, `AUTH.md`.

## Why this doc exists

Every other spec in `docs/specs/` was written for one environment: a bring-your-own x86 box sitting on the user's LAN — the "old laptop in the pantry" (`SPEC.md`). malmo is also offered as a **hosted** product: the same OS running inside a cloud VM that malmo operates, where the customer pays for the resources their OS and apps consume. The cloud VM is a fundamentally different environment — no LAN, no physical disks, no TPM, no USB installer, a public endpoint, and malmo's own infrastructure inside the trust boundary.

Rather than fork malmo into two products, we treat the environment as a **profile** the OS is built and configured for. This doc owns the profile concept and every hosted-profile delta. **It is the single home for hosted-specific design** — the other specs describe the appliance profile and carry a short pointer here. When this doc and another spec appear to disagree, this doc wins *for the hosted profile only*; the other spec remains authoritative for the appliance profile.

**Target customer for hosted: small and medium businesses** running open-source SaaS apps as Docker containers, who want those apps reachable over the public internet. Not the household/family audience the appliance optimizes for. This sharpens several of the cuts below — the family-photos, shared-folder, and cross-device-file-sharing motivations behind a number of appliance features simply do not apply to a hosted SMB box.

## The two profiles

| | `appliance` | `hosted` |
|---|---|---|
| **What it is** | BYO x86 box on the user's LAN | malmo-operated cloud VM, one per tenant |
| **Install** | USB → kiosk installer → wipe disk (`FIRST_RUN.md` Phase 1) | provisioned from a cloud image; cloud-init-style first boot |
| **Reachability** | `.local` on the LAN; `.malmo.network` HTTPS opt-in | `<slug>.<box-id>.malmo.network` public HTTPS, always on |
| **Storage** | physical OS + data drives, LUKS+TPM, mergerfs | virtual block volume(s), provider/KMS encryption |
| **Network stack** | NetworkManager (ethernet + WiFi), Avahi/mDNS | single virtual NIC from cloud metadata; no mDNS |
| **File sharing** | Samba/SMB over the LAN | none (in-dashboard file manager only) |
| **Trust boundary** | the user owns the hardware | malmo-operated infra is inside the boundary |
| **Security posture** | closed by default, nothing publicly exposed | public-by-default, authentication is the gate |

`appliance` is the default and is what every existing spec already describes. It is not re-litigated here. `hosted` is the new profile this doc defines.

(Names are working labels. Alternatives considered: `self-hosted`/`managed`, `box`/`cloud`. `appliance`/`hosted` is used throughout this spec set.)

## Two layers, treated differently

The single most important framing: malmo is two layers, and the profile split touches them very differently.

**Layer 1 — the control plane.** `malmo-brain`, `web-ui`, Caddy, the `docker-socket-proxy`, the brain↔host-agent protocol, the catalog, manifests, the app lifecycle, and the auth model. This is ~95% of malmo's logic (`CONTROL_PLANE.md` # Layer 2) and it is **identical across both profiles**. Forking it would mean maintaining two products and would break the migration-portability guarantee below. The brain is profile-aware only at a handful of narrow seams (it consults the profile marker, e.g. to skip mDNS publish), never branched wholesale.

**Layer 2 — the base image and host integration.** Which Debian packages ship, the installer, the boot chain, and `host-agent`. This is where the two profiles diverge, and the divergence is **cheap and healthy** because the architecture already treats Layer 2 as the small, boring, swappable layer: `host-agent` is "a few hundred lines of Go… deliberately boring" (`CONTROL_PLANE.md` # Layer 1) and the Tier-2 services (Avahi, Samba, NetworkManager) are systemd drop-ins, not core machinery. **Almost everything the hosted profile "rips out" lives entirely in Layer 2.** That the large cuts concentrate in the cheap-to-diverge layer is evidence the layering is right, not a warning sign.

### How the profile is realized

- **Same substrate.** Debian + systemd + Docker for both profiles. The brain hard-depends on it (`docker compose` CLI, systemd units, PAM as identity). A different base OS for hosted is explicitly rejected — see # Rejected: a different base OS.
- **A lean cloud image profile.** The hosted image is **not** the appliance rootfs with services disabled. It is its own `mkosi` image profile that installs only what a cloud VM needs. Avahi, Samba, NetworkManager, `cryptsetup`/TPM tooling, mergerfs, `openssh-server`, and `nftables` are **not installed** — not installed-and-disabled. (SSH is off in hosted v1 — there is no LAN to scope it to; `nftables`'s only job on the appliance is LAN-scoping SSH/SMB, which is moot here — public exposure is the cloud provider's security-group concern, # Public-by-default.) mkosi is already the single image builder for every target (`BUILD.md` # 2, `DECISIONS.md` 2026-06-16), so this is a second image definition, not a second builder.
- **A build-tagged slim cloud `host-agent`.** It **keeps** PAM verify_password + user create/delete/set-role/set-password, OS update and reboot, and the DNS/cert posture; it **drops** LUKS/TPM unlock, NetworkManager/WiFi, Avahi/mDNS publish, the Samba allowlist, and nftables LAN-scoping. It uses the same Go build-tag mechanism already in the repo that splits the cross-platform surface from the Linux-only host integration (`CLAUDE.md` # Developing), and slots beside the existing `cmd/host-agent` (fake) and `cmd/host-agent-real`.
- **A runtime marker.** The image carries its profile at a well-known path (e.g. `/etc/malmo/profile`, contents `appliance` | `hosted`). The brain reads it at startup; host-agent's behavior is determined by its build tag. `appliance` is the no-op default, so a box with no marker behaves exactly as today.

### Rejected: a different base OS

We considered building hosted on a leaner, container-optimized base (Flatcar/Talos-style) since a hosted box is "just run Docker plus the brain." Rejected: the brain assumes a Debian-ish host with the `docker` CLI, systemd units, and PAM; a different base forces a full host-agent rewrite and re-validation of the boot/identity assumptions, for no win on a single-VM-per-tenant model. Staying on Debian keeps the divergence contained to "which packages + which host-agent build," which is the cheap Layer-2 seam by design.

### Rejected: one rootfs, services disabled at runtime

We also considered shipping the appliance image unchanged into the cloud and disabling Avahi/Samba/NetworkManager at runtime. Rejected: it carries dead appliance baggage (and its attack surface and update burden) into every tenant VM, and "disabled" is a weaker guarantee than "absent." A distinct lean image profile is barely more work and is structurally cleaner.

## Assumption inventory — what the hosted environment breaks

Every load-bearing appliance assumption that does not hold in a cloud VM, the owning spec, and the hosted delta. This is the at-a-glance map; the sections that follow expand each.

| Appliance assumption | Owning spec | Hosted delta |
|---|---|---|
| TPM 2.0 present; LUKS auto-unlock seals to PCR 7 | `STORAGE.md`, `BOOT.md` | No TPM dependency; volume encryption is provider/KMS-keyed (# Storage) |
| Physical OS drive + data drive(s), mergerfs union, add/eject, canary | `STORAGE.md` | Virtual block volume(s); resize, not add/eject; no mergerfs/canary (# Storage) |
| USB-boot kiosk installer, disk-select, confirm-wipe | `FIRST_RUN.md`, `BUILD.md` | Image is provisioned; cloud-init-style first boot (# Provisioning) |
| TPM/UEFI/RAM hardware floor checked by installer | `FIRST_RUN.md` | No hardware-check gate; the VM shape is fixed at provision (# Provisioning) |
| NetworkManager owns interfaces; WiFi is first-class | `BOOT.md`, `FIRST_RUN.md` | Single virtual NIC from cloud metadata; no NM, no WiFi (# Networking) |
| Avahi/mDNS publishes `<slug>.local`; `.local` is the foundation URL | `DISCOVERY.md`, `MALMO_NETWORK.md` | No mDNS; public DNS is the resolver (# Networking) |
| `.malmo.network` HTTPS is an opt-in toggle | `MALMO_NETWORK.md` | Always on; the only scheme; enrolled at provision (# Networking) |
| Closed by default — nothing publicly exposed | `THREAT_MODEL.md`, `MALMO_NETWORK.md` | Public-by-default, auth-gated (# Public-by-default) |
| Samba/SMB cross-device file access over the LAN | `STORAGE.md` | Not shipped; in-dashboard file manager only (# Access & files) |
| SSH enabled, scoped to LAN + mesh via nftables | `BUILD.md`, `AUTH.md` | Off in v1 (no LAN, no mesh) (# Access & files) |
| The user owns the hardware ("data you own, hardware you own") | `SPEC.md`, `THREAT_MODEL.md` | malmo-operated infra is in the trust boundary; honest convenience tier (# Threat model) |
| Migration is deferred; restore is off-box backup | `STORAGE.md`, `NEXT.md` | Logical export/restore bundle promoted to load-bearing (# Export bundle) |

PAM-as-identity, the Docker/app lifecycle, the catalog/manifest contract, Caddy subdomain routing, the brain↔UI protocol, and the auth/session model are **unchanged** — they are Layer 1.

## Provisioning & first-boot (hosted)

Replaces `FIRST_RUN.md` Phase 1 (the kiosk installer) entirely. There is no installer in hosted — the cloud image *is* the installed system.

- **Provisioning.** A tenant VM is created from the hosted cloud image (`BUILD.md` emits it from the same mkosi config). The VM shape (vCPU/RAM/disk) is fixed at provision time by the control plane, so there is no hardware-check gate and no disk-selection or confirm-wipe step. The TPM/UEFI/RAM floor of `FIRST_RUN.md` # Hardware floor does not apply.
- **First-boot configuration.** Configuration is injected at provision time (cloud-init-style seed data): the assigned `<box-id>` and its `*.<box-id>.malmo.network` enrollment credentials, and a one-time admin-bootstrap secret. The box does not generate a LUKS recovery passphrase or enroll a TPM (# Storage).
- **Setup wizard, trimmed.** The `FIRST_RUN.md` Phase 2 wizard shrinks: the **network step is gone** (the NIC is configured from cloud metadata, there is no WiFi), the **storage/disk step is gone**, and the **enrollment step is gone** (enrollment happened at provision; secure URLs are not a toggle). What survives is **the first admin account** (`FIRST_RUN.md` # Step 2, including the recovery code) and **time zone**. Telemetry consent stays as specced.
- **Admin bootstrap.** The first admin is created against PAM exactly as on the appliance (identity stays PAM-sourced — # Two layers). The bootstrap secret gates who gets to create that first account, replacing the appliance's "whoever is physically at the box during first boot" trust.

## Networking & discovery (hosted v1)

- **No mDNS, no `.local`.** Avahi is not installed. There is no LAN to multicast on. Everything in `DISCOVERY.md` is appliance-only.
- **Public DNS is the resolver.** Every app is reachable at **`<slug>.<box-id>.malmo.network` over public HTTPS**, resolved through malmo.network's authoritative DNS to the VM's public address. This is the **default and only** URL scheme — there is no `.local` fallback because there is no LAN. The `<slug>.<box-id>` shape is exactly the appliance secure-URL scheme (`MALMO_NETWORK.md`), so the brain's per-app route + URL-surfacing logic is unchanged; what changes is that it is the sole scheme rather than a toggle-gated overlay.
- **Enrollment is automatic and always-on.** The box enrolls with malmo.network at provision time. There is no "Use secure URLs" toggle (`MALMO_NETWORK.md` # The toggle) — secure URLs are the only URLs. The toggle, its off-state, and the Android-compatibility framing are all appliance-only concepts.
- **Certs.** Caddy obtains a real Let's Encrypt **wildcard cert for `*.<box-id>.malmo.network`** via ACME DNS-01 — the same mechanism the appliance uses when enrolled (`MALMO_NETWORK.md` # Enrollment flow). Per-box Caddy ACME therefore survives essentially unchanged; it is simply always-on instead of toggle-gated. The DNS-01 credential is part of the provisioned enrollment data.
- **One virtual NIC.** Addressing comes from the cloud's DHCP/metadata. NetworkManager is not installed; there are no WiFi flows, no primary-connection pinning, no `network-online`-scoped-to-primary logic (`BOOT.md` # NetworkManager). The single interface is brought up by the minimal cloud-native path.

## Public-by-default, auth-gated — an honest inversion of "closed by default"

The appliance is **closed by default**: no app or service is publicly exposed, access is identity-mesh only, and public-internet exposure is not even a default option (`THREAT_MODEL.md`, `MALMO_NETWORK.md` # Security posture). Hosted v1 **intentionally inverts this**, and the spec is honest about it rather than pretending otherwise:

- The apps **are** publicly reachable. Reaching them over the internet is the entire reason an SMB chooses hosted.
- The gate is **authentication**, not network-unreachability. Apps that ship their own login carry it; the malmo dashboard sits behind the malmo login. The reverse proxy serves the public endpoint; the app or the dashboard decides who gets in.
- **Network filtering is the cloud provider's, not the box's.** Hosted ships no host firewall — `nftables` is in the cut list (# How the profile is realized), because its appliance job is LAN-scoping SSH/SMB and hosted drops both. L3/L4 filtering is the **provider's security group / VPC firewall**, and provisioning every tenant behind one that admits only the intended public surface (443, plus 80 for the ACME/redirect path) is an **explicit operator requirement**: without it, Docker's default publish-to-`0.0.0.0` would expose an app's ports directly. A minimal in-guest `nftables` backstop for provider postures that lack security groups is deferred (`DECISIONS.md` 2026-06-19, `NEXT.md`).
- This is a deliberate product position for the hosted profile, not a regression of the appliance's posture. The appliance stays closed-by-default; the hosted box is open-but-authenticated.

Per-app "expose to anonymous users vs require a malmo session in front" controls are a later refinement (# Deferred). v1 is "the endpoint exists and is auth-gated."

## Storage (hosted)

- **Virtual block volumes, not physical disks.** The "OS drive / data drive" user-facing model (`STORAGE.md`) collapses: there is one (or a small number of) cloud block volume(s), not removable physical media. The user-visible storage vocabulary stays minimal, but the physical-disk framing is gone.
- **No mergerfs, no add/eject, no canary.** mergerfs unions physical drives for zero-downtime expansion (`STORAGE.md` # Data drives); a cloud volume is **resized** instead, so the add-drive/eject-drive flows, the data-drive enrollment marker, and the storage canary (which exist to detect a removed or wrong *physical* drive) do not apply. Capacity change is a volume resize, orchestrated by the control plane.
- **Encryption-at-rest under a custodian model.** The appliance's LUKS+TPM auto-unlock defends against **drive theft** (`STORAGE.md` # Threat model) — a threat that does not exist for a cloud volume the customer never physically holds. Hosted relies on **provider volume encryption, or LUKS keyed from a hosted KMS** (key custody is an open question, # Open questions). What this defends: a co-tenant, a stolen/leaked disk image, an idle volume. What it explicitly does **not** defend against: the malmo-operated infrastructure itself — see # Threat model. There is no TPM seal, no PCR policy, and no user-held recovery passphrase, because none of those map to a VM the operator runs.

## Access & files (hosted)

- **SSH: off in v1.** The appliance enables sshd-but-allows-no-account and scopes it to RFC1918 + mesh via nftables (`BUILD.md` # SSH, `AUTH.md`). In hosted v1 there is no LAN to scope to and no mesh, so SSH is **off**. Operator/console rescue is a hosting concern, not a user-facing feature. (Re-opening user SSH is a candidate for the later mesh pass.)
- **No Samba/SMB.** Cross-device file sharing over SMB assumes a LAN with first-class native clients (`STORAGE.md` # Cross-device access). Over the public internet it is a non-starter and is **not shipped**. File access is the **in-dashboard file manager** (`FILES.md`), with a WebDAV-over-HTTPS path as a possible later addition.
- **Identity stays PAM-sourced.** Even though no SSH or SMB surface consumes it in v1, PAM remains the source of truth for accounts and passwords (the decision in # Two layers). This keeps Layer 1 — including the auth model and the migration bundle — identical to the appliance.

## Boot (hosted)

The boot chain (`BOOT.md`) simplifies in Layer 2:

- **No TPM unseal step.** The root volume is not LUKS-sealed-to-TPM (# Storage), so the initramfs TPM-unlock stage and its `malmo-recovery.target` trigger (`BOOT.md` # Failure → recovery target) do not exist in the same form. Volume decryption, where present, is provider/KMS-mediated before the OS sees the disk.
- **No NetworkManager / storage-assembly-of-physical-disks.** The single NIC comes up via the cloud-native path; `malmo-storage-ready.target` has no mergerfs/data-drive/canary work to do (# Storage), though the bind-mount layout for `/home` and `/var/lib/malmo` onto the data volume is retained.
- **Recovery target, reconsidered.** The appliance recovery target serves a static page on port 80 for a user standing at a console (`BOOT.md` # What recovery target serves). A headless cloud VM has no such user; the host-agent-crashloop case routes to an operator-visible signal instead. The precise hosted recovery surface is an open question (# Open questions).

## Threat model (hosted)

The appliance threat model rests on a sentence hosted cannot honor: the user owns the hardware (`SPEC.md`, `THREAT_MODEL.md`). Hosted keeps the data-ownership half of the pitch (the user's data is theirs, exportable and portable — # Logical export / restore bundle) but **gives up the hardware-ownership half**: malmo runs the machine. The spec is explicit about this rather than papering over it. This is the **honest convenience tier** — hosted trades "hardware you own" for "you don't have to run it."

- **malmo-operated infrastructure is inside the trust boundary.** Once a volume is unlocked, the operator can read it; the operator runs the hypervisor and the host. At-rest encryption (# Storage) defends against a co-tenant, a leaked disk image, and an idle volume — **not** against the operator. We say so plainly; we do not claim operator-blind or confidential-compute properties in this pass.
- **Tenant isolation is VM-level.** One VM per tenant (the whole-machine assumption that makes malmo malmo holds — `CONTROL_PLANE.md`), so isolation between customers is the hypervisor's VM boundary, not in-process multi-tenancy. There is no shared brain across tenants.
- **The exposure posture is public-by-default, auth-gated** (# Public-by-default, auth-gated). The defended perimeter is authentication at the edge and per-app login, not network-unreachability.
- **What does not change.** App isolation between apps on one box (`APP_ISOLATION.md`), the brain's own attack surface and the socket-proxy mitigation (`CONTROL_PLANE.md`), and the auth/session model (`AUTH.md`) are all Layer 1 and carry over unchanged.

A future operator-blind posture (customer-held keys, confidential VMs, remote attestation) is **not** promised here and is noted as a direction, not a commitment — it constrains backups, password reset, and support in ways this pass does not take on.

## Logical export / restore bundle

The migration-portability promise — "it's your OS, move it home whenever you want" — is what makes hosted strategically coherent with the appliance. Making it real promotes the currently-deferred cross-box migration / restore-from-backup paths (`NEXT.md`) to load-bearing.

The portable unit is **not the disk image.** The two profiles' images differ (LUKS/LAN/mDNS present on one, absent on the other), so a byte-for-byte clone of a hosted VM will not boot on a laptop and vice versa. The portable unit is a **logical bundle**:

- the user content under `/home/<user>/` and the shared tree,
- the installed-app set as their manifests (plus per-instance config/data),
- the brain's SQLite state.

Restoring the bundle onto a fresh malmo install of **either** profile reproduces the user's apps and data. Designing the export path as this bundle (rather than an image clone) keeps the promise honest in both directions and is what a hosted customer is handed when they graduate to their own box.

This section is the one candidate to split into its own spec if it outgrows this doc — the bundle format and the restore transaction are a real surface in their own right. Call that at write time; for now it is specified here as an OS capability, distinct from the deferred commercial layer.

## Per-instance resource limits

A hosted tenant pays for the resources their apps consume, which requires the lifecycle owner to be able to **bound** an app's consumption. The mechanism is in scope; the pricing/metering pipeline is not.

- **Mechanism (in scope).** The app lifecycle (`APP_LIFECYCLE.md`) can apply per-app cgroup limits (`--memory` / `--cpus`) and a disk quota as a policy on the generated compose project, sourced from the manifest's declared resources or a control-plane-supplied policy. This is a seam on the existing install/reconcile transaction, available to both profiles but exercised by hosted.
- **Deferred.** Pricing tiers, the metering pipeline (sampling `/proc` + disk into a billing system), and the billing surface itself are the commercial layer (# Deferred). The brain already samples resource usage; wiring that to billing is out of scope for this pass.

## Deferred

Named so they are not relitigated, and pushed to `NEXT.md`:

- **The identity-based WireGuard mesh** and per-device pairing for hosted (the appliance mesh design in `MALMO_NETWORK.md` # Deferred remains the reference). Hosted v1 is a plain public endpoint.
- **A central shared ingress** with "no per-VM public IP" routing into the tenant fleet. v1 gives each VM its own public reachability.
- **Per-app public-vs-login exposure controls.** v1 is "the endpoint exists and is auth-gated."
- **The commercial control plane outside the tenant:** the provisioning/control API, resource metering → billing, pricing tiers, fleet management, suspend/restore, and abuse handling. This is net-new infrastructure with no analogue in the appliance product and is where the operational weight of being a data custodian lives.

## Open questions

Tracked centrally in `NEXT.md`. The notable ones this pass surfaces:

- **Encryption key custody.** Provider volume encryption vs LUKS keyed from a hosted KMS. A vTPM is available on some hypervisors but is pointless under a custodian model. Picks the exact at-rest mechanism for # Storage.
- **Hosted recovery surface.** What replaces the appliance's console-served `malmo-recovery.target` page for a headless VM (# Boot).
- **Export/restore bundle home.** Whether # Logical export / restore bundle stays in this doc or graduates to its own spec, and the bundle's concrete format.
- **Profile names.** `appliance`/`hosted` vs alternatives.
