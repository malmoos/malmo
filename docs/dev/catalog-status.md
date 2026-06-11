# Catalog app status

The **per-app roster** of every app molma has evaluated and what state it's in. One row per app. This is the "look it up by name" view: *given an app, does it work, and if not, why and what unblocks it?*

It is the companion to [`catalog-import-gaps.md`](catalog-import-gaps.md), and the two are kept deliberately distinct:

- **This file is keyed by app.** It answers *"what's the state of `<app>`?"* and includes the apps that are **fine** — `Full` is a tracked claim, not an absence.
- **The gaps ledger is keyed by mechanism** (`secret-injection`, `nonroot-data-ownership`, …). It answers *"what platform gap caused this, and where does the fix stand?"*

So the limitation cell here is always a **one-line, user-visible summary plus a link into the ledger** — never a copy of the ledger's mechanism prose. One canonical statement per fact: *what* lives here, *why / where-it-stands* lives in the ledger. When they'd disagree, the ledger wins on mechanism and this file wins on the app roster.

## States

Four states, in increasing order of "can the user have this app":

| State | Meaning | Maps to ledger `Severity` |
|-------|---------|---------------------------|
| **Full** | Shipped; no limitation recorded. | — (no entry) |
| **Degraded** | Shipped and runs; a real feature is broken, but core use works. | `degraded` |
| **Blocked** | *Temporarily* rejected. Can't run today, but ships the moment a named OS feature or upstream change lands. The row names what we're waiting on. | `blocks-start` (with an unblock) |
| **Rejected** | *Permanently* not shipping. Fundamentally can't work under molma's model; recorded so we don't keep rediscovering why. | `blocks-start` (won't-do) |

The **Blocked vs. Rejected** split is the load-bearing one. `Blocked` says *"re-check this when X ships."* `Rejected` says *"don't bother — here's the wall."* Both keep an app out of a normal user's catalog, but only one has a future.

**`Blocked` and `Rejected` are enforced, not just documented.** Both carry `listed: false` in their manifest (`APP_MANIFEST.md` # A, `APP_STORE.md` # Listed apps): the entry stays in the catalog directory — manifest, adapted compose, resolved digests all preserved — but the store hides it from browse and refuses to install it. This roster says *why* an app is pulled; the flag is *how*. A `Degraded` app stays listed (it runs); a `Full` app is listed by default.

**`Full` is a claim, not silence.** It means no limitation has been *recorded* — it is not a promise that someone re-ran the app today. A `Full` row with no detail link means "nothing known," not "audited clean." When an app is smoke-tested or re-verified, link the progress entry in **Detail** so the claim is backed.

## Roster

| App | State | Limitation (user-visible) | Detail |
|-----|-------|---------------------------|--------|
| files-demo | Full | None known | — |
| hermes-agent | Full | None known | — |
| kan | Full | None known | [gaps][kan] |
| mealie | Full | None known | — |
| memos | Full | None known | — |
| whoami | Full | None known (sample app) | — |
| gitea | Degraded | git-over-SSH is disabled (HTTP clone/push only); outgoing email (notifications, password resets) is silently dropped until an SMTP relay ships. | [gaps][gitea] |
| docuseal | Degraded | Email signing links only resolve on the LAN until remote access ships; outgoing mail needs an SMTP relay. | [gaps][docuseal] |
| jotty | Degraded | Uploads over ~1 MB fail (images, drawio, avatars); text notes and checklists are unaffected. | [gaps][jotty] |
| kimai | Degraded | Outgoing email (password-reset links, reports) is silently dropped until an admin configures SMTP. | [gaps][kimai] |
| open-webui | Degraded | No on-device inference; the user must point it at an external model API (OpenAI-compatible or a LAN Ollama). | [gaps][openwebui] |
| calibre-web | Blocked | Crash-loops on every boot — never serves a page. Currently pulled / not installable. | [gaps][calibre] |

[gitea]: catalog-import-gaps.md#smtp-relay--gitea-2026-06-11
[kan]: catalog-import-gaps.md#secret-injection--kan-2026-06-05
[docuseal]: catalog-import-gaps.md#app-url-injection--docuseal-2026-06-05
[jotty]: catalog-import-gaps.md#runtime-self-patch--jotty-2026-06-07
[kimai]: catalog-import-gaps.md#smtp-relay--kimai-2026-06-09
[openwebui]: catalog-import-gaps.md#gpu-local-inference--open-webui-2026-06-07
[calibre]: catalog-import-gaps.md#nonroot-data-ownership--calibre-web

### Blocked / Rejected — what we're waiting on

The unblock for every non-`Full` app that isn't merely `Degraded`. Keep this in sync with the roster.

- **calibre-web** — *Blocked.* The linuxserver.io image runs s6-overlay, which must start as uid 0 to set up `/run` before dropping to `PUID:PGID`; molma forces `user: 3276:3276` and strips `CAP_SETUID`/`CAP_SETGID`, so preinit aborts (`/run belongs to uid 0 instead of 3276`). This is a curation-reject per `APP_ISOLATION.md` # Runtime identity & data ownership, gap-class `nonroot-data-ownership`. **Two independent unblocks, either suffices:** (1) user-namespace remap (a topic in `NEXT.md`, not yet an implementation issue — a feasibility spike comes first because Docker's userns-remap is daemon-global, not per-app); or (2) swapping to the upstream `janeczku/calibre-web` image (no s6-overlay, may honor the runtime `user:`) — unverified under the Tier-3 sandbox, needs a boot test. Re-check this row when either lands.

## How it stays current

A roster that isn't wired into the add-an-app flow rots. The discipline, mirroring the gaps ledger:

- **Import agents / app authors:** every catalog import adds or updates the app's row here — `Full` / `None known` if it shipped clean. If the import surfaced a gap, the ledger entry and this row land in the same change, and the row links the ledger entry.
- **When a platform gap or upstream fix ships:** flip the affected rows (`Blocked` → `Full`/`Degraded`, `Degraded` → `Full`) in the same change that flips the ledger entry's `Status:`, and **remove `listed: false`** from the now-shippable manifest so it returns to the store. The ledger is the worklist (`grep "Status: open"`); this file is the user-facing scoreboard those flips feed.
- **When an app is pulled:** leave its row (don't delete it), set the state to `Blocked` or `Rejected` with the reason, and add **`listed: false`** to its manifest so the store actually withdraws it. A removed app that vanishes from the roster is exactly the knowledge this file exists to keep; the flag is what makes the roster's verdict real instead of advisory.
