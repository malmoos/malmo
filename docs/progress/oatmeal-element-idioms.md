# Oatmeal element idioms — shared pill Button + display Heading, applied to chrome + auth

- **Status:** done
- **Date:** 2026-07-01
- **Specs touched:** docs/specs/WEB_UI.md, docs/specs/DASHBOARD.md
- **Closes:** #261
- **Stacked on:** #260 (`oatmeal-theme.md`) — this branch builds on `feat/260-oatmeal-theme`; its PR targets that branch.

## What was done

With #260's palette + fonts in place, this slice applies Oatmeal's signature **element idioms** so the dashboard reads as the same product as cloud — not just the same colors.

- **Two shared components, written fresh from Tailwind utilities** (not transcribed from Oatmeal's `.tsx` — the licence permits the *pattern* in our End Product, not the *source*):
  - `components/ui/Button.vue` — the pill idiom (`rounded-full`, olive ink fill). Variants `primary` (`bg-accent text-accent-foreground hover:bg-olive-800`), `secondary` (outline on `bg-card`), `ghost`; sizes `sm` / `md` / `icon`. Colors flow from the olive semantic tokens; native button attrs (`type`, `disabled`, `@click`) fall through. Uses the existing `cn()` helper.
  - `components/ui/Heading.vue` — the display idiom (`font-display` = Instrument Serif, `tracking-tight`). `level` prop sets both the semantic tag (`h1`/`h2`/`h3`) and the size.
- **Auth unified onto the components.** #260 had made the auth buttons/wordmark pill + serif via a central `.auth` CSS block; that CSS is now the source of *drift*, so it was removed and the auth screens refactored onto `<Button>` / `<Heading>`: every submit button across `Login.vue`, `RecoverView.vue`, and the four setup steps (`AdminStep`, `TimezoneStep`, `TelemetryStep`, `DoneStep`) is now `<Button type="submit">`; the two "Copy" buttons are `<Button variant="secondary" size="sm">`; the "malmo" wordmark on Login/Setup/Recover is `<Heading :level="1">`. The `.auth button[type="submit"]`, `.auth button.copy`, and `.auth h1` rules were deleted from `style.css` — one source of truth for the pill button + display heading.
- **Chrome.** `TopBar.vue`'s account avatar is now `<Button variant="primary" size="icon">` (the round dark-olive pill). `Dock.vue`, `AppShell.vue`, and `SettingsLayout.vue` are RouterLink navigation rails with no buttons/headings to restyle — they already inherited the olive tokens in #260 and need no idiom change; link-styled toggles (`.auth button.link`, "Forgot password?", "← Back to sign in") stay links, distinct from the pill button.

## How it maps to the specs

- **WEB_UI.md # Styling / # Components** — realizes the "shadcn-vue, copy-paste components owned in our repo" model: the first two owned components land under `components/ui/`, hand-written from utilities (not the shadcn CLI, not Oatmeal source), consuming the olive `@theme` tokens.
- **DASHBOARD.md** (calm-launcher posture) — the chrome stays quiet: the idioms are shape/typography, not new decoration. No visual-language rule changed, so DASHBOARD.md needed no edit (the issue's "note any touch-ups" — there were none).

## Known gaps & deviations

- **`primary` hover uses a literal `olive-800`, not a token.** There is no accent-hover token; the fill lightens `olive-900 → olive-800` to match cloud's `bg-olive-950 hover:bg-olive-800`. It's the same hover #260's `.auth` submit button used — contained to the one component.
- **In-card auth section titles (`.auth h2`) stay sans, not `<Heading>` serif.** The display idiom is applied to the wordmark (the primary display heading) and the chrome; making every small form-section title serif reads heavy against the calm posture. Section-heading rollout across Home/Settings pages is part of the composition restructure (#290), which will consume `<Heading>`.
- **Idiom rollout is bounded to the issue's named chrome + auth.** Prominent buttons elsewhere (Home app actions, Store install, dialogs, `SplitButton`) are **not** converted here — they're outside #261's Touch list and belong to #290 (Home/Settings composition), which now has `<Button>`/`<Heading>` to build on. Called out so the partial coverage isn't mistaken for completeness.
- **Overlap with #290.** #261 delivers the reusable idiom components + applies them to chrome/auth; #290 restructures Home/Settings composition and will consume them. The two are complementary, not conflicting.
- **Visual verification.** The components were rendered headlessly against the production CSS (round dark-olive icon Button, Instrument Serif wordmark, secondary + primary pill buttons, muted-red error) and `make check-web` passes. Home stays token-recolored from #260; its idiom pass is #290.

## What's next

1. **#290 — restructure Home + Settings composition**, consuming `<Button>` / `<Heading>` for their action buttons and section headings.
2. **A link component/idiom** if inline text links proliferate (currently 3 occurrences — below the abstraction threshold).
3. **Dark mode** — unchanged from #260; still a deferred downstream decision.
