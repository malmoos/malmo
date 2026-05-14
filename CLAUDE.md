# malmo

Home server OS — same category as Umbrel / ZimaOS / CasaOS. North star: **simplicity for non-technical users**. Tinkerers are early adopters, not the destination.

## Repo state

**Spec-phase only.** No code, no build, no tests. Everything here is markdown design docs. Do not scaffold code, package layouts, or tooling unless explicitly asked.

## Documents

- `SPEC.md` — top-level vision, distribution, local access model, monetization. Start here for context.
- `CONTROL_PLANE.md` — `host-agent` + `malmo-brain` (Go, single binary, SQLite) + Caddy + managed sidecars. Brain runs as a container, talks to Docker via socket-proxy. Architectural overview; topical specs split out into the docs below.
- `APP_LIFECYCLE.md` — how the brain installs / runs / updates / uninstalls apps on top of Docker. Compose-project unit, on-disk layout, reconciler pattern, install transaction, crash handling, slug allocation, Caddy timing.
- `BRAIN_UI_PROTOCOL.md` — wire-level contract between the dashboard and the brain. REST + SSE + future WebSocket. Sibling to `BRAIN_HOST_PROTOCOL.md`; same four-pattern shape.
- `WEB_UI.md` — dashboard codebase, stack (Vue 3 + Tailwind 4 + shadcn-vue + TanStack Query), deploy-model options.
- `APP_MANIFEST.md` — `manifest.yml` schema. One model, two doors (store apps vs. user-pasted compose). Compose file is held verbatim; malmo never rewrites it.
- `SERVICE_PROVISIONING.md` — three tiers: managed data services (Postgres/Redis), OS integrations (Tailscale/SMB), regular apps.
- `APP_ISOLATION.md` — runtime enforcement of manifest permissions; per-user Tier-3 instances; network model.
- `STORAGE.md` — ext4 + LUKS + TPM2 auto-unlock. v1 ships Levels 0–1 only (single OS drive, optional data drive). Mergerfs/SnapRAID are deliberate later additions.
- `FIRST_RUN.md` — installer → setup wizard → dashboard.
- `MALMO_NETWORK.md` — cloud-side surface. MVP slice: one URL scheme at a time via a global "Use secure URLs" toggle (`.local` HTTP by default, opt-in `<box-id>.malmo.network` HTTPS via Let's Encrypt). Deferred: mesh / remote access via Headscale + DERP, device pairing, sharing.
- `AUTH.md` — dashboard auth, sessions, roles, recovery, and where Tier-2 admin UIs live. Password-only in v1; server-side opaque cookies; admin recovery code; SSH separate and opt-in.
- `BRAIN_HOST_PROTOCOL.md` — wire-level contract between the brain (in a container) and `host-agent` (on the host with root). HTTP/JSON over UNIX socket, two API patterns (sync + jobs), SSE for streams, lockstep versioning. Happy-path only — failure semantics deliberately deferred.
- `DECISIONS.md` — evolution-of-thinking log. Captures what we changed our mind about and *why*. Read this before relitigating; add an entry when a load-bearing decision flips.
- `NEXT.md` — prioritized list of open design topics, with pointers back to the relevant doc for context. The "Open questions" section in each doc is now just a pointer to this file; never add open items to individual docs, add them here.

When working on a topic, read the relevant doc(s) end-to-end before proposing changes. The docs cross-reference each other heavily and decisions in one constrain the others.

## Load-bearing decisions (don't relitigate without cause)

- **Debian base, single-node, BYO x86, Docker apps, custom YAML manifest, ISO install.**
- **Subdomain routing** (`photos.malmo.local`), explicitly *not* path-based — browser same-origin policy is the reason. See `SPEC.md`.
- **Headscale + DERP (BSD-3)** for the mesh. Tailscale's coordinator is proprietary; NetBird's server is AGPLv3. Both rejected.
- **ext4 + LUKS, not ZFS.** ZFS forecloses Level 2/3 storage upgrades and adds CDDL/kernel licensing pain.
- **Brain is one Go binary in a container**, not microservices. SQLite for malmo's own state; managed Postgres is for *apps*.
- **Closed by default for remote access** — no public exposure toggle in v1; identity-based mesh only.
- **Manifest schema versioned from day one**, public, two-major back-compat. Required fields are minimal (~7-line manifests valid).
- **Permissions are declared and enforced**, not metadata.

## Working style

- This is a spec; precision matters. When proposing a change, name the doc and section.
- Push back on tradeoffs; defer to product calls once made (per user preference).
- Open questions are tracked at the bottom of each doc — that's where genuinely unresolved items live. Don't invent answers; surface them.
- Don't add "future-proofing" abstractions to the spec. The docs are already explicit about what's deferred (e.g., fscrypt, ARM, snapshots, paid-app mechanics).
- Keep the "no NAS vocabulary in the UI" rule (`STORAGE.md`) in mind for any user-facing language.
