# Oatmeal composition — Home + Settings onto serif headings + olive-50 card panels

- **Status:** done
- **Date:** 2026-07-09
- **Specs touched:** none (composition realizes the Oatmeal theme already owned by `WEB_UI.md` # Styling; no IA or visual-language rule changed — see # How it maps to the specs)
- **Closes:** #290
- **Builds on:** #260 (`oatmeal-theme.md`, palette + fonts + token remap), #261 (`oatmeal-element-idioms.md`, the shared `components/ui/Heading.vue` display idiom this slice rolls out), #262 (`oatmeal-token-sweep.md`).

## What was done

#260 recolored the whole dashboard onto the olive tokens and restructured the **auth** surfaces to the theme's composition; #261 added the shared `<Heading>` / `<Button>` idioms and applied them to chrome + auth, explicitly deferring the Home/Settings section-heading rollout to #290. This slice finishes the job for the two remaining signed-in surfaces so "our UI structure follows the theme" holds across the whole dashboard, not just its colors.

Two signature Oatmeal moves, applied uniformly to Home and every Settings section:

- **Serif `font-display` section headings.** Every section title switched from the old tiny uppercase-muted label (`text-xs font-medium uppercase tracking-wide text-muted-foreground`) to the shared `components/ui/Heading.vue` (Instrument Serif, `tracking-tight`). Top-level section/group titles use `:level="2"` (`text-2xl`); the app-detail page's per-app sub-sections (Outgoing email / Setup secrets / Settings) use `:level="3"` (`text-xl`) so they read as sub-headings under the app name. The app-detail page title (`app.name`) moved from a bespoke `<h1 class="text-xl font-semibold">` to `<Heading :level="2">` — aligning it with the rest of the signed-in shell (which is uniformly `h2`-based; there is no `h1` in the signed-in app, only the auth wordmark uses `:level="1"`).
- **Olive-50 card panels with cloud spacing.** Panels were already `bg-card` (olive-50); they moved from the compact `rounded-xl … px-4 py-3` to the calmer `rounded-2xl … p-5` (the auth cards' `1rem` radius + generous padding). Applied to every card/row/table container across the sections and the Settings desktop nav panel.

Per-surface notes:

- **Home** (`HomeView.vue`) — "System health", "Household", "Yours" are now serif headings; the health-issue rows are `rounded-2xl p-5`. The **launcher grids are deliberately left open** (tiles on the `background`, not wrapped in a panel) — see # Known gaps.
- **Settings sections** — Account, Notifications, About, Users (Add user + People), Outgoing email (Add + Email accounts), Installed apps, and the installed-app **detail** page all take the serif heading + `rounded-2xl p-5` panel treatment. **Activity** was the one section with no in-page heading (its title came only from the nav); it gains a serif "Activity" heading on the header row (Export CSV/JSON stay to its right), with the admin/member intro line moved just below — so all sections now open with a consistent serif title. The audit table card is `rounded-2xl`.
- **Settings nav** (`SettingsLayout.vue`) — the desktop nav panel bumped to `rounded-2xl` to match the content panels; the grouped-nav labels stay small (they're navigation chrome, not page headings).

No behavior, data flow, routing, or IA changed — this is a pure composition pass. No literal `bg-olive-*` and no raw hex introduced (colors keep flowing from the semantic tokens).

## How it maps to the specs

- **`WEB_UI.md` # Styling** already owns the Oatmeal theme (olive `@theme` + Inter/Instrument Serif); this slice *realizes* the serif-heading + card-panel composition it describes, so no edit was needed there.
- **`DASHBOARD.md`** (calm-launcher north star, the tile model) and **`SETTINGS.md`** (the My-account / Box-settings IA, panel inventory) describe *information architecture and behavior*, not heading pixels or panel radii. The IA is unchanged and neither doc pins the old heading style, so — matching the #261 precedent ("No visual-language rule changed, so DASHBOARD.md needed no edit") — no spec edit was warranted. The `docs/README.md` spec map is unaffected (no spec added/removed).

## Known gaps & deviations

- **Home launcher grids are left open, not wrapped in a panel — a deliberate reconciliation of #290 with `DASHBOARD.md`.** An earlier pass wrapped each Household/Yours grid in an `bg-card` (olive-50) panel to read literally as "card panels". But the app tiles' own logo squares are *also* `bg-card` (olive-50), so nesting them inside an olive-50 panel washed out their fill contrast, and boxing the apps leans toward the "control panel" look `DASHBOARD.md`'s north star argues against ("a calm launcher, not a control panel … breathing room"). On the launcher **the tiles are the cards**; re-wrapping them isn't the theme's intent. So the grids stay open (tiles as cards) with serif group headings — the composition change for Home is the headings + tiles-as-cards, and Settings carries the explicit card-panel composition. Flagged because it's a judgement call a reviewer should eyeball.
- **Form element-idiom (pill `<Button>` + pill inputs) deferred to a focused follow-up.** #261's ADR anticipated #290 also consuming `<Button>` for action buttons, but #290's own "Do" list scopes to *card/panel composition + font-display headings + structural utilities*, and the button question is coupled to inputs: the theme's pill button (`rounded-full`) is paired with a pill input idiom (the auth surfaces make **both** `rounded-full`). There is no shared `<Input>` component yet, and pillifying inputs across the dense admin forms (Users, Outgoing email) is out of #290's stated scope. Converting **only** the buttons would leave pill buttons beside square (`rounded-lg`) inputs in every form — an internally inconsistent half-conversion that reads worse than today's uniform rounded controls. So all action buttons/inputs stay on the current token-based `rounded-lg` shape for this slice; unifying them onto the pill idiom (buttons + an `<Input>` component together) is the natural next step and wants a visual pass.
- **Pre-existing non-olive accents left untouched (surgical scope).** The Activity "OK" result badge still uses `bg-emerald-500/10 text-emerald-600` and the failed-tile tint uses `amber-*` — both predate this change and are unrelated to the composition. They weren't introduced here and weren't swept (out of scope for a composition pass); the `success`/`warning` tokens exist if a future sweep wants them.
- **Verification is `make check-web` (typecheck + production build) — green.** A live full-stack visual pass wasn't run in this environment (the signed-in Home/Settings sit behind the authenticated SPA, and no browser-automation lib is available to drive it headlessly). The change is presentational and built on the already-proven `<Heading>` idiom and semantic tokens, but the two judgement calls above (open launcher grids; the calmer heading/panel scale) are worth a reviewer's eyeball.

## What's next

1. **Form element-idiom rollout** — a shared `<Input>` (pill) idiom, then move Home/Settings action buttons to `<Button>` and inputs to the pill input together, so the control layer matches the auth surfaces without a transitional button/input shape mismatch.
2. **Dark mode** — still the deferred downstream decision from #260.
