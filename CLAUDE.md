# malmo

Home server OS — same category as Umbrel / ZimaOS / CasaOS. North star: **simplicity for non-technical users**. Tinkerers are early adopters, not the destination.

## Audience & positioning

**Long-term target: the tech-curious adult.** Comfortable with software but not infrastructure — owns a laptop, uses cloud apps (Google Photos, Dropbox, Notion), can follow a guided setup, but does not want to learn Docker, SSH, or filesystem layouts to use their box. Reference points for that level of comfort: a Plex user, a Synology DSM user, someone who installs apps from the App Store but doesn't enable Developer Tools. They want to **own their data and apps** on hardware they control, without becoming a sysadmin to do it.

**Early adopters: tinkerers.** Patient with rough edges, bring the app ecosystem with them, write manifests, file good bug reports. v1 ships *tolerable* for tinkerers and *delightful* for the long-term audience — when those tensions appear, the long-term audience wins. The "admins-get-sudo, UI is the path, SSH is rescue" stance (`DECISIONS.md` 2026-05-15) is exactly this shape: tinkerers can SSH if they want, but every privileged operation has a UI path so the long-term user never needs to.

**The pitch.** Install on an old laptop or PC, leave it running in the pantry, run the apps you use daily — a grocery list shared with your partner, photos, notes, files — on hardware you own, with data you own. If the original developer disappears, your app keeps working.

**Monetization shape.** Core OS is free + open. Money comes from paid cloud add-ons (off-site backup, DDNS, possibly remote access) and app-store revenue from paid apps. We do not sell hardware in v1; reference hardware is a possible future SKU but never a requirement.

## How we differ from neighbors

malmo's positioning is a *combination*, not a single axis: branding, hardware openness (BYO, broad compatibility), ease of install and daily use, **the app ecosystem (the strongest pillar)**, and developer-friendliness for app authors. The per-competitor view:

| OS | Category | What we copy | Where we differ | Notes |
|---|---|---|---|---|
| **Umbrel / umbrelOS** | Same category | One-click Docker apps, app-store-as-product, friendly install. | Multi-user from day one. Multi-disk + mergerfs from day one. Managed services (one shared Postgres, not one per app). UI is the path for privileged operations (Umbrel punts to SSH-rescue more often). Subdomain routing — Umbrel uses path-based, which we explicitly rejected for browser-isolation reasons (`SPEC.md`). | Closest competitor on category and audience; we differ on architectural bets. |
| **CasaOS** | Same category | Container-first, simple UX, browser-friendly. | Same as Umbrel — multi-user, managed services, subdomain routing. CasaOS leans more "Docker dashboard"; we're more "appliance OS." | Less integrated than Umbrel but in the same shape. |
| **ZimaOS** | Same category | Install-time storage presets (radio buttons, plain-language guidance) is directly inspired by their UX. Container-first. | Multi-user, managed services, subdomain routing. ZimaOS bundles with Zima hardware; we're explicitly BYO. | We copy ZimaOS's storage-setup language ("Maximize storage" / "Protect my data") because it's the right plain-English framing. |
| **Runtipi** | Same category, different vibe | App-catalog model. | Same architectural bets. | Acknowledge, don't actively study. |
| **Yunohost** | Same category, different vibe | App-catalog model. Two concrete borrowings: (a) the **`yunohost diagnosis` check taxonomy** as prior art for the v1 health-check set in `HEALTH.md`; (b) the **app integration-level (0–9) rating** as a model for catalog badges (`Files-first-class`, `Backup-aware`, `Multi-instance`). Their per-app `backup`/`restore` script convention is also a useful reference for our manifest backup hints. | Native packages (no containers), LDAP+SSOwat SSO across apps, path-based routing as default (works because nginx + package-time path patching — won't translate to Docker apps), public-facing assumptions baked in (email stack, DynDNS), sysadmin/activist audience. We're closed-by-default, Docker-app, no cross-app SSO. | Path-based routing is right *for them* because of native apps; doesn't change our subdomain-routing call. |
| **TrueNAS (CORE/SCALE) / HexOS** | **Different category** | Failure-mode philosophy: explicit boot states, "pool missing is a first-class state," graceful degraded UI (`DECISIONS.md` 2026-05-16). System dataset vs. data pool split (we have the same split via `/var/lib/malmo-state` vs. `/var/lib/malmo`). | We are explicitly NOT a NAS. No ZFS, no pools/vdevs/parity-as-first-class. No NAS vocabulary in the UI. Apps are the product, storage is plumbing. HexOS is "TrueNAS made friendly" + cloud-pairing for recovery; we're closed-by-default with no cloud recovery in v1. | Closest model for failure-mode UX; furthest from us in target audience. Study how they handle storage-anomaly states; do not adopt their NAS-first framing. |
| **Synology DSM** | Different category (closed) | **The gold-standard reference for failure-mode UX.** UI always reachable, drive issues surface as banners with guided walkthroughs, the box never bricks. Recovery is one-click from the dashboard, not "SSH in and read logs." | Closed-source, hardware-locked, proprietary. We're open and BYO. Synology is also weak on third-party app ecosystem (Docker is bolted on, not first-class); ours is the opposite. | When designing UX for failure modes, "what would Synology do?" is the right question. (See `HEALTH.md`.) |
| **Unraid** | Different category | Nothing direct. | Power-user storage features, paid OS, NAS vocabulary throughout. | Acknowledge as the "Unraid land" we are not entering. |
| **Proxmox** | Different category | Nothing. | Virtualization / cluster orchestration, sysadmin tool. | Out of category. |

**Pattern across the differences:** malmo sits in the Umbrel/ZimaOS/CasaOS category by *audience and pitch*, and borrows Synology's *failure-mode philosophy* (which TrueNAS also approximates). The combination is the position — "Umbrel-category product with Synology-grade graceful degradation."

**Two phrases worth internalizing:**

- *"We are not a NAS."* Storage is plumbing in service of apps. The user-visible model is "OS drive" and "data drive" — no pools, no vdevs, no parity-as-first-class concepts. See `STORAGE.md`.
- *"Files are first-class, apps are windows."* User content lives in `/home/<user>/Photos/`, not inside an app's opaque library. Uninstalling Immich never deletes the photos. This is the differentiator vs. Nextcloud/Photoprism-style apps that own their content. See `STORAGE.md`.

## Repo state

**Spec + early implementation.** The markdown design docs remain the source of truth; a walking-skeleton implementation now lives alongside them. The skeleton proves the spine end-to-end (UI → brain → docker compose → Caddy route → SSE → uninstall) and runs natively on a dev box — no VM. The real host-integrated parts (Avahi, LUKS/TPM, boot ordering, auth) are still stubbed or unbuilt.

Implementation layout:
- `cmd/brain/` + `internal/` — `malmo-brain` (Go, huma API + SQLite, `docker compose` CLI driver). Packages: `api`, `lifecycle`, `catalog`, `manifest`, `store`, `caddy`, `hostclient`, `events`, `protocol`, `auth`, `audit`, `admission`.
- `cmd/host-agent/` — **fake** host-agent: real `BRAIN_HOST_PROTOCOL.md` wire format over a real UNIX socket, canned host ops (no Avahi/LUKS/apt yet).
- `web-ui/` — Vue 3 + Vite + TanStack Query dashboard (Tailwind/shadcn-vue deferred; plain CSS for now).
- `catalog/` — hand-written sample manifests (currently `whoami`).
- `dev/` + `Makefile` — dev orchestration: Caddy container + `make` targets. Inner loop is all-native; `make help` lists targets.

When extending the implementation, keep it faithful to the locked specs and update the relevant doc if a decision flips. Do not build out host-integrated subsystems (storage, boot, networking) without the VM outer loop the specs assume.

## Documents

**All spec docs live in `docs/specs/`.** Bare-filename cross-references throughout these docs are relative to that directory. Implementation progress lives in `docs/progress/`, developer how-to in `docs/dev/`. **Contributing guide: [`docs/dev/contributing.md`](docs/dev/contributing.md)** — start here for the branch/build/test/document/PR workflow, including the mandatory `Closes #<N>` rule.

**`docs/README.md` is the canonical, annotated map of every spec** — one line per doc, what it owns, and its headline locked decisions. Read it to orient before touching a topic; read the relevant doc(s) end-to-end before proposing changes (the docs cross-reference each other heavily and decisions in one constrain the others). Start at `SPEC.md` for top-level context.

**Rule: keep `docs/README.md` current.** Whenever a doc is added to `docs/specs/` (or an existing doc's scope changes materially), update its entry in `docs/README.md` in the same change. A doc in `docs/specs/` not listed there is a bug — fix it.

<!-- The per-doc annotated list previously here was removed 2026-05-29: it duplicated docs/README.md (the canonical map) and was reloaded into every session's context. Keep the map in docs/README.md. -->

`DECISIONS.md` (evolution-of-thinking log — read before relitigating) and `NEXT.md` (prioritized open design topics; the only place open items live — never add them to individual docs) are the two cross-cutting docs to know by name.

## Documentation discipline

Every change ships with documentation — a code change is not complete until its docs are written in the same change.

- **Three doc homes.** Design source of truth → `docs/specs/`. Implementation progress → `docs/progress/`. Developer how-to (running locally, code-level architecture) → `docs/dev/`.
- **Project knowledge lives in checked-in docs, never in a coding agent's local/private memory.** Anything worth remembering about malmo — decisions, conventions, workflows, gotchas — must land in the repo (the three doc homes above, `DECISIONS.md`, or `CLAUDE.md`) so the whole team and every future session sees it. Per-tool "memory" features are local to one machine and one person; they are not a substitute for writing it down here.
- **Every unit of work gets a progress entry.** Add a `docs/progress/<slug>.md` (ADR-style, kebab-slug, not numbered) that records **what was done** and **what's next**, following the template in `docs/progress/walking-skeleton.md`. **Progress entries are frozen ADR-style snapshots** — once written, do not retroactively edit a prior entry's "what's next" or "known gaps" when a follow-up lands (no strikethroughs, no "done in X" annotations). A new entry references the one it closes in its opening paragraph; the index below plus chronological reading is the "where we are now" view. Filenames carry no order — **append the new entry to the bottom of the index in `docs/progress/README.md`** (oldest-first); that index is the only record of build order now that the `NNNN-` prefix is gone.
- **Keep specs and reality in sync.** When the implementation realizes or diverges from a spec, update the matching `docs/specs/` doc in the same change (and add a `DECISIONS.md` entry if a locked decision flips).
- **The doc map is load-bearing.** Keep `docs/README.md` and `docs/progress/README.md` (the indexes) current in the same change. A doc not linked from the map is a bug.
- **Root `README.md`** is the front door (pitch + quickstart); keep its quickstart accurate when the dev workflow changes.
- **No line wrapping in markdown.** Use continuous lines of text, not ~70 character breaks. Markdown viewers handle reflowing; hard-wrapped lines make diffs harder to read and are unnecessary.

## Go code discipline

Small set of rules. Codified now so we don't have to back them out later.

- **Consumer-side interfaces.** Interfaces live in the package that *uses* them, not the package that implements them. `lifecycle.DockerDriver` lives in `internal/lifecycle/`, not in a hypothetical `internal/docker/`. Provider packages export concrete types only. Exception: a single interface shared by three or more consumers can move to the provider, but default to consumer-side until that's true.
- **Layer boundaries.** `internal/lifecycle` is the transaction owner; only `cmd/brain` and `internal/api` may import it. `internal/store` is the persistence boundary; only `internal/lifecycle`, `internal/api`, and `cmd/brain` may import it. Anything else reaching in is breaking the model — push the call through the right seam instead.
- **`log/slog` is the only logger.** No `"log"` imports, no `fmt.Println` for diagnostics. Structured fields, not interpolated strings: `slog.Info("app installed", "instance_id", id)`, not `slog.Info(fmt.Sprintf("installed %s", id))`. The default handler is set in `cmd/brain/main.go`; use `slog.Default()` (i.e. the package-level functions) — don't thread `*slog.Logger` through constructors.
- **Standard structured fields.** Use these key names so journalctl/jq filters stay stable: `instance_id`, `manifest_id`, `slug`, `service`, `image`, `host`, `upstream`, `step`, `err`, `output`, `user_id`, `username`, `role`, `action`, `actor_user_id`, `target_kind`, `target_id`, `retry_after`. Adding a new recurring field? Add it here.
- **Typed errors at boundaries, not everywhere.** Define a sentinel/typed error only when a *consumer* needs to discriminate (HTTP status, retry decision, UI text). `store.ErrNotFound` exists because the API maps it to 404. Don't pre-declare error types speculatively.
- **No premature abstraction.** Don't introduce an interface, factory, or DI container until at least two concrete consumers exist. CLAUDE.md says this generally; it bites hardest in Go where every extra interface is import-graph weight.
- **`internal/` for everything except `cmd/`.** No `pkg/`. Anything inside `internal/` is private to this module by Go's own rules — no public API surface to maintain.
- **Tests in the same package by default.** Use `package foo_test` only when the test genuinely needs to exercise the public surface (catches accidental privacy regressions). Fakes live in `*_test.go` until a second package needs them; then promote to `internal/foo/footest`.
- **Elevation-class mutations audit success *and* failure.** Any handler that creates / deletes / role-changes / password-changes a principal (or installs / uninstalls / changes permissions on an app) emits an `audit.Record(..., success=false)` on every observable failure path — host 502, store 500, conflict 409, guard rejection (last-admin, self-delete) — in addition to the success case. Mirrors `login.failure`; lets the Activity view answer "did someone unauthorized try to mutate X?" symmetrically with login attempts. Pure reads and validation 422s don't audit.
- **Brain commits first, host is reconstructible.** State mutations that span brain SQLite + host-agent commit to brain first, then call host. On host failure, the brain row is rolled back so the two sides stay aligned (`USERS_AND_GROUPS.md` # Roles: "if either side fails, both roll back"). The reverse order leaves orphan host state with no brain row to clean it up from. Established by `/setup`, `createUser`, `updateUserRole`, `deleteUser` — keep the pattern.

## Load-bearing decisions (don't relitigate without cause)

- **Debian base, single-node, BYO x86, Docker apps, custom YAML manifest, ISO install.**
- **Subdomain routing** (`photos.malmo.local`), explicitly *not* path-based — browser same-origin policy is the reason. See `SPEC.md`.
- **Headscale + DERP (BSD-3)** for the mesh. Tailscale's coordinator is proprietary; NetBird's server is AGPLv3. Both rejected.
- **ext4 + LUKS, not ZFS.** ZFS forecloses mergerfs/SnapRAID upgrades and adds CDDL/kernel licensing pain.
- **Mergerfs from day 1** when a data drive is present (pool of one with one drive; `epmfs` placement). Enables zero-downtime drive addition. SnapRAID parity stays deferred.
- **User content at `/home/<user>/`** with macOS-style capitalized use-case folders (`Photos/`, `Music/`, `Movies/`, `Documents/`, `Notes/`, `Downloads/`). Data drive mounts at `/srv/malmo/` with bind mounts to `/home/` and `/var/lib/malmo/`.
- **Files are first-class, apps are windows.** User content lives in use-case folders; app state in `/var/lib/malmo/instances/<id>/`. Uninstalling an app never deletes user content. Manifests bind-mount use-case folders by declaration.
- **SMB shares via Samba** for cross-device access (Windows, macOS, iOS, Android, Linux). mDNS-advertised. TimeMachine-compatible.
- **Avahi as the LAN publisher; per-app `.local` records owned by the reconciler.** No mDNS wildcards exist; each app slug is a real announced name, published by host-agent via Avahi DBus `EntryGroup.AddAddress` alongside the Caddy site block. LAN interfaces only (not mesh, not Docker bridges). `.local` is HTTP-only by definition (no public DNS → no Let's Encrypt) and Android browsers don't resolve it — secure URLs are the compatibility path. See `DISCOVERY.md`.
- **One malmo password per user, PAM is the source of truth.** Dashboard, SSH, and SMB all authenticate against the same `/etc/shadow` entry. Brain has no password hash; it calls host-agent's `verify_password` on every login. Per-protocol opt-in (SSH and SMB off-by-account-by-default) is done via service allowlists, not separate credentials.
- **SSH and SMB scoped to LAN + mesh via nftables** — RFC1918 + the mesh interface. Public internet blocked structurally, not per-account.
- **NetworkManager owns every network interface** (ethernet, WiFi, future bridges/VPN); not systemd-networkd. WiFi is first-class in first-run because the "old laptop in the pantry" install includes WiFi-only machines. host-agent drives NM over DBus; the primary connection is the one with `connection.required-for-network-online=true`. See `BOOT.md`, `BRAIN_HOST_PROTOCOL.md`, `FIRST_RUN.md`.
- **Brain is one Go binary in a container**, not microservices. SQLite for malmo's own state; managed Postgres is for *apps*.
- **Closed by default for remote access** — no public exposure toggle in v1; identity-based mesh only.
- **Manifest schema versioned from day one**, public, two-major back-compat. Required fields are minimal (~7-line manifests valid).
- **Permissions are declared and enforced**, not metadata.
- **Dashboard role maps to Linux group membership.** Members unprivileged; admins in `sudo`. UI is the path for every privileged operation; SSH is rescue-only. 5-minute re-prompt window for destructive UI ops. See `USERS_AND_GROUPS.md`.

## Working style

- This is a spec; precision matters. When proposing a change, name the doc and section.
- Push back on tradeoffs; defer to product calls once made (per user preference).
- Open questions are tracked at the bottom of each doc — that's where genuinely unresolved items live. Don't invent answers; surface them.
- Don't add "future-proofing" abstractions to the spec. The docs are already explicit about what's deferred (e.g., fscrypt, ARM, snapshots, paid-app mechanics).
- Keep the "no NAS vocabulary in the UI" rule (`STORAGE.md`) in mind for any user-facing language.
