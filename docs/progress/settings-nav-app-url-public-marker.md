# Settings nav, the app's URL, and a public marker on tiles

- **Status:** done
- **Date:** 2026-09-03
- **Specs touched:** `DASHBOARD.md` (# Tile — the public marker; # Settings — the nav grouping and the app URL line)

Three small dashboard changes in one pass, plus the one brain change the third of them needed.

## The problem

**The Settings nav grouped by the wrong thing.** "You" held Account and Notifications; "System" opened with Installed apps. But Installed apps lists the apps *this user* has, which is a "You" fact, and Notifications is a record of what happened on the box, which sits next to Activity.

**A per-app settings page never showed the app's address.** The Open button is a one-click path, but it hides the URL and only renders while the app runs. There was no way to read the address, copy it, or send it to another device from that page.

**The launcher could not say an app was open to the world.** On hosted, an app is either owner-only or public (#306), and a manifest can also keep single paths open on an otherwise closed app (#415). The home grid showed neither. The toggle and the words live on the settings page, one click away from where people actually look at their apps.

## What was done

**Nav regrouped** (`SettingsLayout.vue`). **You** is Account, Installed apps. **System** is Users, Outgoing email, Notifications, Activity, About — Notifications moved down to sit directly above Activity, since both answer "what happened on this box".

**The app's URL is written out as a link** (`InstalledAppDetailSection.vue`), under the app name, in every state — a stopped app has an address too. It truncates rather than wrapping, and it is a real anchor, so it opens in a new tab and right-click-copy works. **No copy button, on purpose:** `navigator.clipboard` is unavailable on the HTTP-only `.local` origin, so a copy button would be the affordance that fails on the appliance while the anchor works everywhere.

**Tiles carry a globe when the app is open** (`AppTile.vue`), in two forms:

- **whole globe** — `exposure: "public"`, the whole app answers anonymously;
- **the same globe at 30% opacity** — `exposure: "restricted"` with `access.public_paths` declared, so part of the app is still open;
- **no marker** — closed end to end. The quiet default needs no mark.

Both are gated on `isHosted()`. The appliance has no public app subdomains and stores `"public"` for every row, so an ungated badge would mark every tile and mean nothing.

**One drawing, two states.** The globe is one PNG (`web-ui/src/assets/globe.png`) used as a **mask filled with `currentColor`**, not drawn as an `<img>`: the source is black line art, which would vanish on a dark tile. The partly-open state is the same globe at 30% opacity, so it reads as a fainter version of an open app rather than as a second symbol to learn. An earlier version cut a circle out of the globe's right side with a second `radial-gradient` mask, an eclipse. It was dropped: at 16 px the crescent read as a smudge rather than as a globe, and a shape nobody recognises carries no meaning.

**The copy names the state, not the paths.** The tile tooltip is "Partly public. Some app paths are open to the public." and the settings page says the same thing in its access line. The path list is manifest detail nobody acts on from either surface, and printing it turned one sentence into a wall. Both surfaces still *derive* the state from the instance's own manifest copy, so the label cannot claim a narrower app than Caddy is serving.

**The Access card lists the open paths.** The summary line says *that* some paths are open; under it, when the app is Only me and its manifest declares any, each path is a mono badge. Badge shape ported from Tailwind Plus (`elements/badges/13-small-with-border`) with the gray palette mapped to malmo's tokens. Hidden while the app is Public, where every path is open and naming four would read as a limit that is not there.

**The app's email account is a listbox, not a native `<select>`.** The choice is "which of my accounts", and an account is recognised by its provider's logo faster than by a name someone typed. Shape ported from Tailwind Plus (`forms/select-menus/05-custom-with-avatar`), with the Headless UI `Listbox*` primitives mapped to **reka-ui** `Select*` (this project ships reka-ui; Headless UI is not a dependency) and the popup styled like `SplitButton`'s menu so the two dropdowns in the app look like one thing. The logo comes from `MailProviderLogo` keyed on `provider_type`, which `MailProviderOption` already carries, so there is no brain change. One wrinkle: reka-ui reserves the empty string as "cleared", so "None" cannot use it — the unbound state travels as a `__none__` sentinel and turns back into `""` on the way to the brain. The wire contract is unchanged.

**Buttons that are links, and links that are buttons.** `Button.vue` gained an `as` prop (a tag name or a component), which fixed three real defects. The app page's **Open** was a hand-built pill on an `<a>` at `px-4 py-2`, so it rendered a size larger than the `size="sm"` buttons beside it — copying the pill by hand is what let them drift. Both **Add account** call-to-actions in Outgoing email were a `<Button>` wrapped in a `<RouterLink>`, which nests a button inside an anchor: invalid HTML, two focus stops for one control, and a screen reader announcing a link that contains a button. The add form's **Cancel** was the reverse, a `<Button @click="router.push(…)">`, so it had no middle-click, no ctrl-click and no target on hover. All three are now `<Button as="a">` or `<Button :as="RouterLink">`. Every other link under `/settings` was checked and is correct.

**No em dashes in the copy these two surfaces show.** Rephrased into separate sentences, not just swapped for another mark: "Applying. The app restarts briefly.", "None (email features off)", "Only you can open it. Visitors sign in to your box first."

**`GET /api/v1/apps` now carries `public_paths`** (`internal/api/api.go`). It was a detail-page-only enrichment, so the list DTO said `restricted` for an app that was in fact partly open, and the tile could not tell the two apart. The DTO's own rule already said this: every response carrying an app's exposure must carry the paths too, or it claims a narrower app than Caddy is serving. Costs one instance-manifest read per app per list call, best-effort — a missing copy leaves the field empty and the tile falls back to no marker. No schema change: the field was already on `InstanceDTO`, so `api/openapi.json` and the generated TypeScript are untouched.

## What was tested

- `make check-web` green (typecheck + production build), and `vue-tsc` clean after the later edits. The masked globe is inlined as a `data:` URI in the built CSS (1 KB, under Vite's inline limit), so the mask is same-origin and no extra request is made.
- `inset-ring` / `inset-ring-border` were checked in the built CSS rather than assumed, since the badge's hairline depends on them.
- `go vet` and `go test ./internal/api/` green after the list change.
- Both marker forms were rendered in headless Chrome at 16 px and 64 px, on light and dark tiles. That is what killed the eclipse: it only read as an eclipse at 64 px.
- The hosted profile was exercised in the inner loop by pointing `MALMO_PROFILE_PATH` at a marker file holding `hosted`, which is how any of this is visible on a dev box at all.

## Known gaps

- Not clicked in a browser as part of the dashboard yet — `web-ui` has no test runner, so the evidence is a build plus the isolated render.
- The marker overlays the logo square. Safe for today's centered glyph icons, not for a future edge-to-edge one.
- The Installed apps **list** still marks nothing; only the home tiles and the detail page say how open an app is.
- `HomeView.vue`'s "Browse the Store" call to action is the same hand-built-pill defect the `as` prop exists to remove, and it is not even the same pill (`rounded-lg`, no hover). Left alone: this pass was scoped to `/settings`.
- The new listbox was not exercised with a real bound account on a hosted box, only with the provider list the dev box serves.
- `web-ui/src/assets/globe.png` comes from Icons8, whose free tier asks for a visible link back. The dashboard gives it none. Either an attribution line lands somewhere, a license is bought, or the icon is replaced before this ships.
