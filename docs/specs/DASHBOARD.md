# Dashboard — logged-in product surface

> Working spec for what the malmo dashboard **is** once you're logged in: the home screen, the app model the home screen renders, global navigation, and the top bar. Companion to `WEB_UI.md` (which owns the stack, container, and deploy model — *not* the information architecture), `AUTH.md` (role-gated nav), `APP_LIFECYCLE.md` (instances, slugs, routing), `STORAGE.md` (per-user folders), `DISCOVERY.md` (the `.local` names this scheme publishes), and `NOTIFICATIONS.md` (the bell).
>
> `WEB_UI.md` answers "how is the dashboard built and shipped." This doc answers "what does the user see and touch." Every other subsystem *feeds* this surface; nothing else owns it.

## North star for this surface

A **calm launcher**, not a control panel. The home screen is the apps the household runs, with breathing room — not a wall of gauges and widgets. This is a deliberate position *against* the Umbrel/CasaOS "dashboard of widgets" shape and toward the Synology/ZimaOS "the apps are the product, the chrome gets out of the way" shape. Problems surface when they exist and stay invisible when they don't.

---

## Locked: the apps model — instances are owner-scoped

This is the load-bearing decision, and it's the one that reshaped `SPEC.md` (see `DECISIONS.md` 2026-05-29 # App instances are owner-scoped). It is also malmo's clearest app-layer differentiator — see # Why this is a differentiator below.

Every app instance has an **owner**. There are two kinds:

- **Household (shared) instance** — owned by an admin, on behalf of the whole household. One running instance; every household member who has permission sees and opens the *same* instance. The app's own internal multi-user (Jellyfin profiles, Immich accounts, Home Assistant users) handles per-person separation *inside* the one instance. This is the right shape for genuinely-shared apps: Home Assistant, a VPN, a household media library, a shared grocery list.
- **Personal (per-user) instance** — owned by a single user. Its own instance id, data dir, slug, route, managed-service database, and folder bindings (it binds the *owner's* `~/Photos`, `~/Documents`, etc.). This is the right shape for personal-data apps: Immich (my photo backup ≠ my partner's), a password vault, personal notes.

Mechanically, a personal instance is exactly the Tier-3 shape already locked in `APP_LIFECYCLE.md` # "an app instance is a Docker Compose project": *N independent compose projects pointing at the same manifest+compose, each with its own instance id, data dir, and slug.* The control plane was already built for this — per-instance databases (`SERVICE_PROVISIONING.md`: each instance gets its own DB + role inside the shared Postgres *server*), per-instance on-disk layout, and a reconciler that publishes one name per instance. Owner-scoping is the model that uses machinery we already have, not new machinery.

### Install authorization

| Actor | Can install | Result |
|---|---|---|
| **Admin** | Yes | Chooses **Household** (shared, admin-owned) or **Just for me** (a personal instance owned by the admin). |
| **Member** | Yes | Always a **personal** instance, owned by that member. Members cannot create household instances. |

A member installing an app binds *their own* user folders into *their own* instance — which is why per-user instances **resolve** the "files are first-class" tension rather than create it. A single shared Immich would have to read every user's `~/Photos`, violating the per-user `0750` isolation in `STORAGE.md`. A personal Immich reads only its owner's `~/Photos`. Owner-scoping and per-user folders fit each other.

**Folder source is a per-folder install choice.** For each folder an app declares (`APP_MANIFEST.md` # `folders`), the install screen resolves a *source*: a personal instance offers the owner's folder (default) or the household Shared folder per folder; a household instance always uses Shared. The author declares only the folder + mode, never the source — "my own Jellyfin on my movies" vs "on the family library" is the installer's call, not the author's (`DECISIONS.md` 2026-05-30). This is the only folder-related input on the otherwise all-or-nothing consent screen, alongside the `pick-subfolder` prompt.

**The consent screen is driven by `GET /api/v1/catalog/:id/install-plan`** (`BRAIN_UI_PROTOCOL.md` # GET /api/v1/catalog/:id/install-plan). The brain computes the screen's inputs from the parsed manifest and the caller's role: the role-derived scope options (admin gets Household + Just-for-me, member gets personal only), the declared permissions, and a per-folder per-scope source menu (household → Shared only; personal → owner's folder or Shared). The endpoint is read-only and advisory — it makes no host calls and mutates nothing; the user's elections are validated and stamped into the compose override only at `POST /api/v1/apps` install time.

### Warn, don't block, on duplicate install

When a user goes to install an app that already has an instance on the box (household or another user's personal), we **warn and offer, never block**:

> *Jellyfin is already installed as a household app. You can open it, or install your own copy.*

Rationale: two people may want genuinely different things from the same app — different media libraries, different photo backups, different notes. Blocking the second install assumes the first install serves everyone; it often doesn't. This is the one behavior that requires real multi-instance to be live (you can't fake it by disallowing duplicates), and we accept that cost because it *is* the differentiator.

### What stays deferred (don't scope-creep here)

- **Resource limits / quotas per user.** N personal Immichs = N container stacks on a pantry laptop. This is a real runtime cost, but it's a *product-acceptance* reality, not a structural one — the box owner self-limits. We surface it as a **warning when resources are tight** (ties into `HEALTH.md` `ram-pressure` / `disk-full`), not a hard cap. Quotas remain a `NEXT.md` Tier-4 item.
- **Granular post-install permission revocation.** Install-time consent is all-or-nothing (accept the declared permissions or cancel); changing a grant after install — turning off a folder, downgrading write→read — is the separate per-app permissions screen deferred to `NEXT.md` Tier-3. (Cross-user *shared-folder access* for personal instances is no longer deferred — it's now the install-time source election below; `DECISIONS.md` 2026-05-30.)
- **SSO.** The long-term door (mentioned in `SPEC.md` # Accounts & users): if/when malmo gets SSO, *shared* instances become the *encouraged* path for apps that support internal multi-user, with personal instances as the escape hatch for apps that don't (or for users who want hard data isolation). Warn-don't-block keeps both doors open in the meantime.

---

## Locked: instance naming / routing — `<slug>--<user>`, bare `<slug>` for shared

Every instance needs a stable, unique, routable name — it's the LAN `.local` record (`DISCOVERY.md` # Per-app A records), the `.malmo.network` subdomain (`MALMO_NETWORK.md`), and the Caddy site block, all keyed on the instance slug.

**The scheme:**

| Instance | Slug | LAN | Public |
|---|---|---|---|
| Household | `<slug>` | `immich.malmo.local` | `immich.<box-id>.malmo.network` |
| Personal (owner `alex`) | `<slug>--<user>` | `immich--alex.malmo.local` | `immich--alex.<box-id>.malmo.network` |

- **Bare slug = the household instance; suffixed = personal.** Ownership is legible in the URL itself.
- **Double dash (`--`) as the separator.** App slugs are kebab-case and can contain single hyphens (`home-assistant`), so a single `-` is ambiguous — `home-assistant-alex` can't be parsed into slug + user, but `home-assistant--alex` can.
- **`<slug>` leads, `<user>` trails** (not `<user>--<slug>`) so an app's instances sort together by app identity rather than collide-sorting under each user.

### Why this shape, and not the prettier dotted one

The obvious alternative — `<user>.<slug>.<box-id>.malmo.network` (`alex.immich.…`) — reads better but **breaks the cert architecture**, which is the decisive constraint:

- `MALMO_NETWORK.md` (lines 27, 138–139) locks **one** wildcard DNS record `*.<box-id>.malmo.network` and **one** wildcard Let's Encrypt cert `*.<box-id>.malmo.network`, renewed quietly every ~60 days via ACME DNS-01.
- **A TLS wildcard spans exactly one label — it does not cross dots.** `immich--alex.<box-id>.malmo.network` is one label → covered. `alex.immich.<box-id>.malmo.network` is *two* labels → **not** covered by `*.<box-id>…`. The dotted form would force a *separate* wildcard cert (`*.immich.<box-id>…`) issued per app, a new ACME round and DNS record on every install — destroying the "one cert, renew quietly" model.
- The LAN side agrees: Avahi publishes each instance as a flat A record (`DISCOVERY.md`). A single-label name resolves everywhere; a multi-label `.local` name (`alex.immich.malmo.local`) is handled inconsistently by Android and Windows mDNS stacks — exactly the clients `DISCOVERY.md` already worries about.

So both transports independently force **flat, single-label**. The dotted form was rejected not on taste but on the cert and mDNS constraints. The aesthetic cost is small in practice: these hostnames are clicked from dashboard tiles, rarely typed or read raw.

### Pros / cons of the chosen scheme

**Pros**
- One wildcard cert covers every app and every personal instance, forever — no per-install ACME churn.
- Works on every mDNS client (flat single label).
- Ownership is visible in the name; bare = canonical/household, suffixed = personal.
- Reuses the existing per-instance slug field (`APP_LIFECYCLE.md` # instance is a compose project) — the slug is just *derived* differently for personal instances.

**Cons (accepted)**
- `immich--alex` is less elegant than `alex.immich`. Mitigated: rarely seen raw.
- The `--` separator must be reserved: catalog slugs and usernames may not contain `--`, and neither may produce an `xn--` label prefix (reserved for IDN/punycode). We control both the catalog and username validation, so this is a validation rule, not a real limit. Documented as a constraint on `APP_STORE.md` slug validation and `USERS_AND_GROUPS.md` username rules.
- **The personal-instance hostname leaks the `username ↔ app` mapping to the LAN.** `immich--alex.malmo.local` is a published mDNS/DNS record (`DISCOVERY.md`), so any device on the network can enumerate which user installed which app by passive discovery — usernames are first names (`FIRST_RUN.md`), so this is mildly identifying. Accepted: this is a closed-by-default, single-household LAN (`THREAT_MODEL.md` treats the LAN as semi-trusted), the same record set has to exist for routing regardless, and ownership-in-the-URL is the deliberate legibility choice above. Noted so it's a conscious tradeoff, not a surprise; revisit if malmo ever targets shared/untrusted LANs.

---

## Locked: the home screen is the app launcher

Home = a grid of app tiles. No widgets (see below). The grid is grouped:

- **Household** — shared instances the current user has permission to open.
- **Yours** — the current user's personal instances.

At v1's app counts there's no scale problem, so the groups are simply **rows/sections** on one screen. The longer-term shape is **swipeable pages** (think iOS home screens) once a household accumulates enough apps to justify paging — reserved, not built.

A member sees their **Yours** group plus the **Household** apps they're permitted to open; they never see other members' personal instances. An admin additionally sees management affordances (install-as-household, the gear routes below). The grid itself is the same component; the *contents* are scoped per user.

### Tile

A tile shows: icon, app name, and a category/role label. In the calm default it carries **no status decoration**. State surfaces only when it's not nominal:

- **Down / stopped** — tile goes **grayed**, with a small alert mark in a corner. (Exact treatment for *updating* / *starting* and the rest is implementation-time UX, not spec.)

### Open-app interaction

Clicking a tile **opens the app in a new browser tab** at its own subdomain (`<slug>[--<user>].malmo.local`, or the `.malmo.network` host when the remote toggle is on). The app runs on its own origin — that's the whole point of subdomain routing (`SPEC.md`: browser same-origin isolation). The dashboard is the launcher, not a frame/proxy around apps.

### First arrival / empty state

(Folds in the former `NEXT.md` Tier-2 "Dashboard at first arrival.") A box with no apps yet shows an empty **Your apps** state that points at the Store rather than a wall of suggestions or a forced starter bundle. The calm posture applies from the first second: invite, don't shove. Concrete copy and whether to offer a light "get started" nudge is implementation-time UX.

---

## Locked: global navigation — a four-item dock

A floating bottom dock with exactly four destinations:

| Item | What it is |
|---|---|
| **Home** | The app launcher (above). |
| **Files** | The in-dashboard file browser over the user's use-case folders and `~/Shared/`. Owned by its own spec (the `NEXT.md` Tier-1 `FILES.md`); appears here as a top-level destination because "files are first-class." |
| **Store** | Browse/install apps. Install respects the authorization table above. |
| **Settings** | Box + account settings, and the **home for gated routes**. |

**Activity (audit log) and Users live *under Settings* as role-gated routes**, not as top-level dock items — they're admin-surface, not daily-use. Role gating per `AUTH.md`: members see their own account settings and the app-relevant slices; admins see Users, Activity, and the system/storage/network panels.

**Search** is deferred. At v1 app counts the grid is scannable; search earns its place when a household's app + file corpus outgrows the eye. Reserved, not built.

---

## Locked: the top bar

Three elements, top corners, quiet by default:

- **Storage pill** — a small always-present capacity readout (e.g. `1.2 / 4 TB`). Present but never loud; clicking it goes to Settings → Storage. It turns insistent only under disk pressure (`HEALTH.md` `disk-full`).
- **Avatar / account menu** — the current user; the menu is the path to account settings, sign-out, and (for admins) the gated routes that also live under Settings.
- **Notification bell** — the in-product notification center (`NOTIFICATIONS.md`). A small dot indicates unread count. This is the dashboard-only v1 transport; off-box transports (email, push) are the deferred seam in `NOTIFICATIONS.md`.

The greeting/status line and any ambient "everything's fine" prose are **implementation-time UX**, not spec — explicitly out of scope here.

---

## Locked: no home-screen widgets in v1

Umbrel ships app-contributed home-screen widgets as a first-class `umbreld` module. malmo does **not** have a widget concept in v1, and the home screen shows no cards on a healthy box. Reasons:

- It's the calm-launcher position: apps get the breathing room; the chrome stays quiet.
- Widgets are an app-author contract (a manifest surface, a render sandbox, a security boundary) that we are not opening in v1.
- The information widgets would carry (health, resources, storage) already has homes: the storage pill, the bell, the live-resources surface (`LOCAL_ANALYTICS.md`), and the degraded-state cards that appear *only* when something's wrong.

This is a pin-a-no decision, recorded so a future "add widgets" PR is a deliberate reopening, not a drift. If widgets ever return, they'd be an app-manifest feature designed alongside the security model — out of scope now.

---

## Why this is a differentiator

A scan of the neighbors (May 2026) shows everyone separates *files* per user but **nobody makes app *instances* a per-user, self-service concept**:

- **Umbrel** — explicitly single-user; multi-user is a years-old, still-unbuilt feature request.
- **ZimaOS** — multi-user at the SMB/folder layer only; apps bind to the owner account ("supports owner accounts only"), so a member logging in still hits the main user's app data.
- **TrueNAS SCALE** — per-user separation is ZFS datasets + ACLs; running two instances of an app is a *manual admin* chart deployment with no notion of "this instance belongs to Alex."
- **Synology DSM** — real multi-user OS, and you *can* run multiple Docker instances, but it's manual (separate containers, manual ports + reverse-proxy rules); Package Center packages are single-instance.

The universal fallback is "one shared instance + the app's own internal multi-user (if it has any), layered on shared files" — which is exactly the no-SSO tension in `SPEC.md`, and exactly what breaks for personal-data apps. The reason no one ships identity-driven per-user instances is the plumbing (per-instance routing, certs, databases). malmo already has that plumbing, so it can ship the thing the neighbors punt on. This is squarely on the "app ecosystem is the strongest pillar" thesis.

---

## Relationship to other docs

- `WEB_UI.md` — stack, container, deploy, API-version handshake. Unchanged by this doc; this doc is its IA complement.
- `APP_LIFECYCLE.md` — owns the instance-as-compose-project model and the slug field this scheme derives. The `<slug>--<user>` derivation rule is recorded there too.
- `DISCOVERY.md` — publishes the per-instance `.local` name; "slug" there now means "the (possibly suffixed) instance slug."
- `AUTH.md` — the role gating behind the dock, Settings routes, and install authorization.
- `STORAGE.md` — per-user `~/` folders that personal instances bind; the model owner-scoping is designed to respect.
- `NOTIFICATIONS.md` / `LOCAL_ANALYTICS.md` — the bell and the (non-widget) live-resources surface.
- `FILES.md` (`NEXT.md` Tier-1, not yet written) — the Files destination in the dock.

Open items that touch this surface (per-app tile state vocabulary beyond down/stopped, search design, swipe-paging, first-arrival nudge copy) live in `NEXT.md`, not here.
