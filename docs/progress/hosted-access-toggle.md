# Hosted per-app access toggle — dashboard Only-me / Public

- **Status:** done
- **Date:** 2026-07-08
- **Specs touched:** `docs/specs/DASHBOARD.md` (# Locked: global navigation — the Installed-apps detail page now lists the hosted **Access** toggle)

Third slice of the hosted per-app access-restriction epic (#304), closing **#307** and building on the box-side backend of **#306** (`hosted-forward-auth-route.md`). #306 shipped the per-instance `exposure` state (`restricted` / `public`), the `PUT /api/v1/apps/{id}/exposure` endpoint (owner-or-admin, hosted-only, audited), and `InstanceDTO.exposure` on every app response. This slice is the dashboard control that reads that field and calls that endpoint — no new backend.

## What was done

### Web-UI — an Access toggle on the installed-app detail page (`web-ui/src/views/settings/InstalledAppDetailSection.vue`)

- A new **Access** section on the per-app detail page (`/settings/apps/<id>`), a segmented **Only me** / **Public** toggle in a card matching the page's other management sections. The subtitle describes the live effect ("Only you can open it — visitors sign in to your box first." vs "Anyone with the link can open it.").
- **Gated to `isHosted() && canControl`.** The appliance has no public app subdomains and the endpoint 404s there, so the control is hidden off hosted (`isHosted()` from `@/auth`, set at bootstrap from `GET /auth/state`). `canControl` is the existing owner-or-admin gate the page already uses for stop/start/uninstall/config; the brain re-checks via `authorizeAppMutation`.
- The mutation `PUT`s `{ exposure }` and, because the endpoint echoes the updated instance synchronously (no job), `onSettled: invalidate` refreshes the detail + list queries. Buttons disable while the PUT is in flight or another app op is busy; a failure surfaces inline ("Couldn't change access: …").

### Types (`web-ui/src/api.ts`)

- `export type Exposure = Instance["exposure"]` — derived from the generated schema, not hand-narrowed. #306 declares the huma enum on `InstanceDTO.exposure`, so `openapi-typescript` emits the literal union directly and the two values live in exactly one place. (This is *not* the `Scope` situation: `scope` is still a free string in the huma struct, which is why its UI-side union is hand-written.)

## Known gaps & deviations

- **No optimistic update.** The echoed DTO is discarded in favor of an `invalidate()` refetch, matching every other mutation in this view (stop/start/mail/config). One extra GET per toggle; not an N+1 (a single settle refetch). Could `setQueryData` off the echoed body later if the flash is noticed, but consistency won here.
- **Owner-only, single-user.** Inherited from #305/#306: "Only me" is the box *owner*. Restricting to a subset of box users the owner creates is the epic's additive follow-up (`ENVIRONMENT.md` # Deferred).
- **No SSE event on an exposure change.** `PUT /apps/{id}/exposure` emits no event, and `useEvents.ts` listens for none, so a second open tab won't reflect a toggle until its next refetch. Consistent with the other app-mutation endpoints (mail rebind, config save), which are equally event-less — a pre-existing convention, not a regression this slice introduces.
- **Store-page vs installed-page.** The toggle lives on the installed-instance detail page (`InstalledAppDetailSection.vue`), not the pre-install store page (`AppDetailView.vue`, `/store/:id`) — exposure is per-instance state that exists only after install.

## What's next

- **#308** — the hosted e2e lane proving both modes end-to-end through real Caddy + the cookie-leak probe of the strip invariant, which (per #306's product call) proves the already-shipped `restricted` default rather than gating it.
