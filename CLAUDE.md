# malmo

Home server OS in the Umbrel / ZimaOS / CasaOS category. You install it on an old laptop or PC, leave it running, and run the apps you use daily — photos, notes, files, a shared grocery list — on hardware you own, with data you own. **North star: simplicity for non-technical users.** Every privileged operation has a UI path; SSH is rescue-only. Tinkerers are the early adopters, not the destination.

Two phrases that constrain a lot of design:

- *"We are not a NAS."* Storage is plumbing in service of apps. The user-visible model is "OS drive" and "data drive" — no pools, vdevs, or parity as first-class concepts, and no NAS vocabulary in the UI. See `STORAGE.md`.
- *"Files are first-class, apps are windows."* User content lives in `/home/<user>/Photos/`, not inside an app's opaque library. Uninstalling Immich never deletes the photos. See `STORAGE.md`.

## Architecture

A running malmo is five processes/artifacts. Three are Go, one is JavaScript, one is a container we don't write.

- **`malmo-brain`** (`cmd/brain/`, `internal/`) — the control-plane daemon. One Go binary: owns SQLite state, the REST+SSE API, the app lifecycle, and the Caddy config. Drives Docker via the `docker compose` CLI.
- **`host-agent`** (`cmd/host-agent/`) — the privileged side. Today it's a **fake**: real `BRAIN_HOST_PROTOCOL.md` wire format over a real UNIX socket, but the host ops themselves (Avahi, LUKS, PAM, apt) are stubbed in memory. The real one is `cmd/host-agent-real/` (PAM verify is real; the rest is still being built).
- **`web-ui`** (`web-ui/`) — Vue 3 + Vite + TanStack Query dashboard. Talks only to the brain.
- **Caddy** (`dev/`) — reverse proxy. Terminates `*.local` and routes to app containers + the brain, configured live by the brain via Caddy's admin API. Subdomain routing per app, never path-based (browser same-origin policy is the reason).
- **SQLite** — the brain's only persistent store (`internal/store/`).

Wire: `browser → web-ui → brain`, and the brain fans out to `docker compose`, Caddy's admin API, and the host-agent (HTTP/JSON over a UNIX socket).

**`docs/architecture.md` is the live, as-built map** — components, the wiring diagram, the per-package table for `internal/`, and what is *not* built yet. It is kept current in the same PR as the code, so trust it over any package list pasted elsewhere (including this file). Read it first.

## Where the important files are

- **`cmd/brain/main.go`** — ~100 lines; names every package and how they wire. Best single starting point for the code.
- **`internal/`** — the brain's packages (`api`, `lifecycle`, `store`, `catalog`, `manifest`, `admission`, `caddy`, `hostclient`, `protocol`, `auth`, `audit`, `events`, plus host-integration and health packages). What each owns and the import rules are in `docs/architecture.md` # Inside the brain.
- **`cmd/`** — entry points: `brain`, `host-agent` (fake), `host-agent-real`, plus small tools (`malmo`, `malmo-storage-verify`, `openapi-gen`).
- **`web-ui/`** — the dashboard. Internal code architecture in `docs/dev/web-ui.md`.
- **`catalog/`** — hand-written sample manifests (currently `whoami`); door-1 install source. **Adding an app starts with a Catalog app issue** (`.github/ISSUE_TEMPLATE/catalog-app.md` — it gates against duplicate/already-rejected apps); authoring works from that issue per `docs/dev/authoring-apps-with-an-agent.md`.
- **`Makefile` + `dev/`** — dev orchestration (`make help`).
- **`docs/`** — `architecture.md` (as-built), `README.md` (spec map), `specs/` (design source of truth), `progress/` (per-change ADR log), `dev/` (how-to).

## Developing

**When starting work on any issue, read `docs/dev/contributing.md` first** — it is the authoritative contribution loop (orient → pick → branch → build → test → document → self-review → PR) and includes rules that supplement those in this file. The commands below are a quick reference; contributing.md is the binding guide.

The dev model is **two loops** (full detail in `docs/dev/running-locally.md`):

- **Inner loop (seconds) — all native, no VM.** The brain and dashboard run directly on your machine against the local Docker socket; the host-agent is the fake that speaks the real protocol but stubs host ops. ~90% of development happens here, and it works on macOS, Windows (WSL2), or Linux with no platform-specific setup.
- **Outer loop (minutes) — a VM.** Only the host-integrated parts (boot ordering, LUKS/TPM, systemd, NetworkManager, Avahi) need a booted Debian target. QEMU + swtpm per `TESTING.md`; not fully wired into CI yet.

The inner/outer boundary is also the **cross-platform / Linux-only** boundary. The Go code is build-tagged so the cross-platform surface compiles everywhere; the real host integration only exists on Linux. If your change is in the brain, the UI, the catalog, or the protocol contracts, your OS doesn't matter.

**Day-to-day commands** (`make help` lists all):

- `make dev` — the whole inner-loop stack (Caddy container + fake host-agent + brain + Vite) in one terminal, output prefixed `[agent]`/`[brain]`/`[ui]`. Open <http://localhost:5173>. Ctrl-C stops everything.
- `make build` — compile brain + host-agent.
- `make check` — **the pre-PR gate.** gofmt + vet + OpenAPI freshness + the full Go test suite. Mirrors CI. Run before every PR.
- `make check-web` — pre-PR gate for `web-ui/` changes: typecheck + production build (also mirrors CI).
- `make test-nopam` — full suite minus the PAM package, for when you don't have `libpam0g-dev` (i.e. off Linux).
- `make clean` — stop dev Caddy, remove malmo containers/networks, wipe `.dev/state`.

**Prerequisites for the inner loop:** Docker + `docker compose`, Node 20+, Go 1.23+, host port `:80` free (dev Caddy binds it so `<slug>.local` works portless), and `avahi-daemon` running on Linux (so `.local` names resolve under `make dev`). The full Go test suite additionally needs `libpam0g-dev` on Linux; see `docs/dev/running-locally.md`.

**Start every piece of work from a fresh branch off latest `main`:** `git checkout main && git pull && git checkout -b <branch>`. Never commit straight to `main`.

Actionable parallel work lives in [GitHub Issues](https://github.com/malmoos/malmo/issues) (`gh issue list --label P1`).

**After opening a PR, run a self-review** using a fresh sonnet agent with no conversation history — it has no attachment to the implementation choices you made. In Claude Code: `/code-review low Read docs/progress/<your-slug>.md first for context, then review the diff per docs/dev/code-review.md.` Address every Block finding before the PR merges; note any disagreements in the progress entry's Known gaps.

## Documentation discipline

Every change ships with documentation — a code change is not complete until its docs are written in the same change.

- **Three doc homes.** Design source of truth → `docs/specs/`. Implementation progress → `docs/progress/`. Developer how-to (running locally, code-level architecture) → `docs/dev/`. The as-built snapshot is `docs/architecture.md`, updated in the same PR as the code.
- **Project knowledge lives in checked-in docs, never in a coding agent's local/private memory.** Anything worth remembering about malmo — decisions, conventions, workflows, gotchas — must land in the repo (the doc homes above, `DECISIONS.md`, or `CLAUDE.md`) so the whole team and every future session sees it. Per-tool "memory" features are local to one machine and one person; they are not a substitute for writing it down here.
- **Every unit of work gets a progress entry.** Add a `docs/progress/<slug>.md` (ADR-style, kebab-slug, not numbered) recording **what was done** and **what's next**, following the template in `docs/progress/walking-skeleton.md`. **Progress entries are frozen snapshots** — once written, never retroactively edit a prior entry's "what's next" or "known gaps" when a follow-up lands (no strikethroughs, no "done in X" annotations). A new entry references the one it closes in its opening paragraph; chronological reading plus the index is the "where we are now" view. **Append new entries to the bottom of `docs/progress/README.md`** (oldest-first); filenames carry no order.
- **Keep specs and reality in sync.** When the implementation realizes or diverges from a spec, update the matching `docs/specs/` doc in the same change (and add a `DECISIONS.md` entry if a locked decision flips).
- **The doc maps are load-bearing.** Keep `docs/README.md` (spec map) and `docs/progress/README.md` (progress index) current in the same change. A doc in `docs/specs/` not listed in `docs/README.md` is a bug — fix it.
- **Root `README.md`** is the front door (pitch + quickstart); keep its quickstart accurate when the dev workflow changes.
- **No line wrapping in markdown.** Use continuous lines of text, not ~70-character breaks. Markdown viewers reflow; hard-wrapped lines make diffs harder to read.

`DECISIONS.md` (evolution-of-thinking log — read before relitigating) and `NEXT.md` (prioritized open design topics; the only place open items live — never add them to individual docs) are the two cross-cutting docs to know by name.

## Go code discipline

Small set of rules. Codified now so we don't have to back them out later.

- **Consumer-side interfaces.** Interfaces live in the package that *uses* them, not the package that implements them. `lifecycle.DockerDriver` lives in `internal/lifecycle/`, not in a hypothetical `internal/docker/`. Provider packages export concrete types only. Exception: a single interface shared by three or more consumers can move to the provider, but default to consumer-side until that's true.
- **Layer boundaries.** `internal/lifecycle` is the transaction owner; only `cmd/brain` and `internal/api` may import it. `internal/store` is the persistence boundary; only `internal/lifecycle`, `internal/api`, `internal/auth`, `internal/audit`, and `cmd/brain` may import it. Anything else reaching in is breaking the model — push the call through the right seam instead.
- **`log/slog` is the only logger.** No `"log"` imports, no `fmt.Println` for diagnostics. Structured fields, not interpolated strings: `slog.Info("app installed", "instance_id", id)`, not `slog.Info(fmt.Sprintf("installed %s", id))`. The default handler is set in `cmd/brain/main.go`; use `slog.Default()` (the package-level functions) — don't thread `*slog.Logger` through constructors.
- **Standard structured fields.** Use these key names so journalctl/jq filters stay stable: `instance_id`, `manifest_id`, `slug`, `service`, `image`, `host`, `upstream`, `step`, `err`, `output`, `user_id`, `username`, `role`, `action`, `actor_user_id`, `target_kind`, `target_id`, `retry_after`, `iface`, `interfaces`, `src`, `dir`, `profile`, `box_id`, `zone`. `host` is a machine or upstream hostname only — a single network interface name is `iface`, a list of them is `interfaces` (never overload `host` for either). `src` is a source filesystem path (bind-source, folder-source); `dir` is a relative bind dir path. `profile` is the resolved environment profile (`appliance`|`hosted`, `ENVIRONMENT.md`). `box_id` is the hosted box's provisioned identity (`ENVIRONMENT.md` # Provisioning). `zone` is an IANA time-zone name (`TIME.md`, host-agent set-timezone). Adding a new recurring field? Add it here.
- **Typed errors at boundaries, not everywhere.** Define a sentinel/typed error only when a *consumer* needs to discriminate (HTTP status, retry decision, UI text). `store.ErrNotFound` exists because the API maps it to 404. Don't pre-declare error types speculatively.
- **No premature abstraction.** Don't introduce an interface, factory, or DI container until at least two concrete consumers exist. It bites hardest in Go where every extra interface is import-graph weight.
- **`internal/` for everything except `cmd/`.** No `pkg/`. Anything inside `internal/` is private to this module by Go's own rules — no public API surface to maintain.
- **Tests in the same package by default.** Use `package foo_test` only when the test genuinely needs to exercise the public surface (catches accidental privacy regressions). Fakes live in `*_test.go` until a second package needs them; then promote to `internal/foo/footest`.
- **Elevation-class mutations audit success *and* failure.** Any handler that creates / deletes / role-changes / password-changes a principal (or installs / uninstalls / changes permissions on an app) emits an `audit.Record(..., success=false)` on every observable failure path — host 502, store 500, conflict 409, guard rejection (last-admin, self-delete) — in addition to the success case. Mirrors `login.failure`; lets the Activity view answer "did someone unauthorized try to mutate X?" symmetrically with login attempts. Pure reads and validation 422s don't audit.
- **Brain commits first, host is reconstructible.** State mutations that span brain SQLite + host-agent commit to brain first, then call host. On host failure, the brain row is rolled back so the two sides stay aligned (`USERS_AND_GROUPS.md` # Roles: "if either side fails, both roll back"). The reverse order leaves orphan host state with no brain row to clean it up from. Established by `/setup`, `createUser`, `updateUserRole`, `deleteUser` — keep the pattern.

## Load-bearing decisions (don't relitigate without cause)

- **Debian base, single-node, BYO x86, Docker apps, custom YAML manifest, ISO install.**
- **Subdomain routing** (`photos.local`), explicitly *not* path-based — browser same-origin policy is the reason. See `SPEC.md`.
- **Headscale + DERP (BSD-3)** for the malmo mesh — **deferred, not v1.** Locked for when remote access ships; Tailscale's coordinator is proprietary and NetBird's server is AGPLv3, both rejected as the substrate. In v1, remote access is user-opt-in Tailscale (Tier-2 service, `SERVICE_PROVISIONING.md`), which is entirely separate — the user's own Tailscale account, not malmo-brokered.
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

- **Always work in a git worktree for local implementations.** Use the `isolation: "worktree"` option when spawning agents, or manually create a worktree (`git worktree add`) before making changes. Never implement directly on the checked-out branch.
- **When reviewing a PR** (your own or someone else's), read `docs/dev/code-review.md` end-to-end before looking at any diff — it defines the lenses, severity levels, and what "reviewed" means on this project. Use a sonnet agent for code review (`model: "sonnet"`).
- This is a spec-led project; precision matters. When proposing a change, name the doc and section.
- Read the relevant `docs/specs/` doc(s) end-to-end before proposing changes — they cross-reference each other heavily and decisions in one constrain the others. Use `docs/README.md` to find the right one.
- Push back on tradeoffs; defer to product calls once made (per user preference).
- Open questions are tracked at the bottom of each doc and in `NEXT.md` — that's where genuinely unresolved items live. Don't invent answers; surface them.
- Don't add "future-proofing" abstractions to the spec. The docs are already explicit about what's deferred (e.g., fscrypt, ARM, snapshots, paid-app mechanics).
- Keep the "no NAS vocabulary in the UI" rule (`STORAGE.md`) in mind for any user-facing language.
