# Store detail page: extra costs and full-screen screenshots

- **Status:** done
- **Date:** 2026-08-27
- **Specs touched:** none — `APP_MANIFEST.md` already describes `external_costs`, and `APP_STORE.md` already describes the catalog fields this renders

Closes the "No UI yet" gap left by [catalog-wire-external-costs.md](catalog-wire-external-costs.md): `Detail.ExternalCosts` reached the box API, and nothing on the box rendered it. Both halves here are ported from the marketing store's app page in `../cloud` (`internal/web/templates/pages/app.html`, `internal/web/static/app-gallery.js`), so the two surfaces that show the same catalog show it the same way.

## The problem

Two things a person needs before they install were missing from `/store/:id`.

**What a third party charges.** Ten catalog apps declare `external_costs` today — listmonk needs a paid mail provider before it sends anything, Open WebUI needs a model provider key to be more than a shell. The field is on the wire and on `GET /api/v1/catalog/{id}`, but the page did not show it, so the bill was a surprise after the install.

**What the app looks like.** The screenshot strip was inert. A click on a thumbnail did nothing, so the only view of the app was a 224-pixel-tall crop.

## What was done

Both changes are in `web-ui/src/views/AppDetailView.vue`. No brain change, no wire change, no new dependency.

**A pricing panel above the Information panel.** A `Price` row, then each third-party cost as a `<details>` disclosure, marked Required or Optional and showing the manifest's rate estimate and prose. A required cost renders open — it is the one someone has to read to decide — and an optional one stays collapsed. The "you pay the provider, not malmo" note sits behind an info tooltip, so the panel stays quiet when nobody asks. That tooltip is CSS only (`group-hover` / `group-focus-within`), so it opens on hover for a pointer and on focus for a keyboard or a tap — no popover element, no script.

**Price is a constant in the view, not a wire field.** Every catalog app is free today and what malmo charges is authored in the curation source, not published (`APP_STORE.md` # Catalog schema). The cloud page states it in one place for the same reason (`cmd/control-plane/main.go` # `appPrice`); this page now has the same single line to change when a price does reach the wire.

**A full-screen screenshot viewer.** A click on a thumbnail opens that shot fit to the viewport — the whole image, not a zoom of the thumbnail in place — with arrow keys, prev/next buttons, and a live "2 of 3" counter. The index wraps both ways, so the arrows never dead-end and there is no disabled state to keep in sync.

**It is a native `<dialog>`, not the fixed-overlay idiom `AppMenuDialog` uses.** The browser then supplies the top layer, the backdrop, Esc, the focus trap, and focus restore to the thumbnail that opened it, which is most of what that other component hand-writes. What is left here is an index and a `src` swap. `showModal()` blocks interaction behind the dialog but not every browser's wheel scroll, so the document is pinned while the viewer is open.

**Lifting that pin is where the port needed more than a translation, and review caught it.** The first version released it on the dialog's `close` event only. A dialog can stop being on screen without closing: taking an open `<dialog>` out of the DOM fires no `close` event. The cloud page is one server-rendered document, so it could only ever be left by a full load; this is a router route, so the Back button unmounts the component mid-viewer. The pin then outlived the dialog and every other page in the dashboard was unscrollable until a reload. The pin is now tracked with its own flag and lifted from three places: the dialog's `close`, `onUnmounted`, and a watcher on the shot list — a background refetch that returns the same app with no screenshots removes the open dialog through the `v-if`, which is the same hole again. That watcher also clamps the index, so a shorter list cannot render a broken image under a "6 of 3" counter.

**One thing was deliberately not ported.** The cloud thumbnails are links to the full-size asset, so a blocked script still opens the image. That page is server-rendered HTML; this one is a Vue view that needs script to exist at all, so the thumbnails are buttons here.

**Hover lifts with a shadow.** The cloud change also fixed the hover affordance and the same reasoning applies: darkening the hairline border is the card idiom, but it is wrong over arbitrary image content — a pale line vanishes into a light screenshot and frames a dark one, and edge brightness across the catalog runs the whole range. `hover:shadow-md`, matching `StoreAppCard`'s `group-hover:shadow-md`. The static border stays, since it is what gives a white screenshot an edge.

## What was tested

- `make check-web` green (typecheck + production build).
- The three new Tailwind variants were checked in the built CSS rather than assumed: `group-open:rotate-90`, `backdrop:bg-black/90`, and `pl-4.5` all emit rules.
- The data was checked against the real store checkout, not a fixture: 10 apps declare `external_costs`, `listmonk` has a required one and `postiz` an optional one with three screenshots, so both branches of the panel and the viewer's prev/next have real content to render.

## Known gaps

- **The stuck-scroll bug above was found by review, not by running it.** It is fixed, but the fix is typechecked rather than clicked — the Back-button path in particular has not been exercised by hand.
- **Not yet looked at in a browser.** The change is typechecked and built, and the CSS was verified, but nobody has clicked it. `make dev-app APPS="postiz listmonk"` against a store checkout is the run that shows both panels with real data.
- **No automated test.** `web-ui` has no test runner, so this is covered by the type checker and the build only — the same standing gap every view in this directory has.
- **The install dialog does not repeat the costs.** Someone who lands straight on Install from a card never passes this panel. Whether a required cost belongs in the consent screen too is a design question, not a wire one.
