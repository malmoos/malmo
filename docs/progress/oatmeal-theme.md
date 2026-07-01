# Oatmeal design system — olive palette + self-hosted fonts + token remap

- **Status:** done
- **Date:** 2026-07-01
- **Specs touched:** docs/specs/WEB_UI.md, docs/specs/DECISIONS.md
- **Closes:** #260

## What was done

Adopted the **Oatmeal** design system (the Tailwind Plus kit cloud already uses) as the foundation of the OS dashboard, so the two malmo surfaces are visually consistent.

- **Olive palette + fonts in `@theme`.** Lifted the `--color-olive-50…950` OKLCH ramp and the two font families (`--font-sans: Inter`, `--font-display: Instrument Serif`) verbatim from cloud's `internal/web/tailwind/input.css` into `web-ui/src/style.css`, so the palettes match exactly.
- **Semantic remap, not find-and-replace.** Repointed the existing shadcn-vue token *values* (`--color-background`, `--color-foreground`, `--color-card`, `--color-muted`, `--color-muted-foreground`, `--color-border`, `--color-accent`, `--color-accent-foreground`) onto olive shades in the one `@theme` block. The app's ~1,000 utility references (`bg-background`, `text-muted-foreground`, …) recolor from this single file — no per-component churn. The accent is monochrome dark olive (`olive-900`), matching cloud's `bg-olive-950 hover:bg-olive-800` CTAs — Oatmeal has no separate accent hue.
- **Self-hosted fonts, no CDN.** Bundled Inter (400/500/600) + Instrument Serif (400) as local `woff2` under `web-ui/src/assets/fonts/`, declared via `@font-face`, and shipped each family's OFL 1.1 notice (`OFL.txt`) alongside. Vite fingerprints the assets into the build; verified the production bundle emits all four `woff2` and makes **zero** network font requests.
- **Auth surfaces restructured to the theme's composition** (beyond the issue's token-only scope, per maintainer direction to adapt our UI structure to the theme). Because Login, Setup + its four steps, and RecoverView all share the central `.auth` classes in `style.css`, restyling that one block propagates the Oatmeal composition to every auth screen with no per-file churn: Instrument Serif display wordmark, `olive-50` card panels with soft olive borders, `rounded-full` inputs/buttons, dark-olive CTA. The `.auth` block now carries **no raw hex** — every color is an olive semantic token. Login.vue's local `<style>` was tokenized to match.

## How it maps to the specs

- **WEB_UI.md # Styling** ("Tailwind CSS 4, CSS-based config via `@theme`, no `tailwind.config.js`") — realized: the olive design system is expressed entirely in the `@theme` block and mapped onto the pre-existing shadcn-vue token names, so shadcn components added later still inherit the palette.
- **DASHBOARD.md** (calm-launcher visual language) — the near-white canvas becomes the warm `olive-100`; the display serif gives the wordmark a calm, branded feel.
- **DECISIONS.md 2026-07-01** records (a) the shared Oatmeal/olive design system with cloud and (b) the self-hosted-fonts divergence and its reason.

## Known gaps & deviations

- **`destructive` / `success` stay semantic status colors, not olive.** Olive is monochrome (no red/green), and cloud itself keeps red for errors (`bg-red-50 text-red-800`). The issue text listed these among "remap onto olive values," but an olive error would destroy the signal; instead they are retuned to muted OKLCH red/green so they sit in the same color space. Documented as an intentional divergence from the issue's literal wording.
- **No dedicated `warning` token.** The recovery-code caution (`.hint.warn`, previously amber `#a05a00`) now borrows `destructive` — the consequence (a lost recovery code) is close to an error. A proper `warning` token is a follow-up if more amber-severity surfaces appear.
- **Per-user glyph avatar colors kept.** Login's user-picker glyphs keep their distinct hues (AUTH.md user-list identity pattern); they're the one non-olive accent, set inline from `Login.vue`, and left intentionally.
- **Home / Settings are recolor-only.** They pick up the olive palette + Inter through the token remap, but their *composition* was not restructured to the theme in this PR (scope was bounded to the auth surfaces). Full restructure tracked as follow-up issue #290.
- **`@font-face` lives in a separate file, not `style.css`.** Tailwind v4's Lightning CSS transform of the `@import "tailwindcss"` entry silently **drops the first `@font-face` rule** in that file (reproduced: reordering moves the dropped face; happens with minify on *and* off). Worked around by hosting the four faces in `web-ui/src/assets/fonts/fonts.css` (a plain CSS file Tailwind never processes) and importing it from `main.ts`. Verified all four faces + `woff2` emit.
- **Tailwind Plus license precondition.** This slice copies only palette *values* and font *choices* (config, not Oatmeal component source), which the Tailwind Plus license permits inside an open-source End Product. It relies on the project holding a valid Tailwind Plus license (cloud already relies on this) — confirm it remains in place.
- **Visual verification.** The auth theme was rendered headlessly against the production CSS (olive canvas, serif wordmark, dark-olive `rounded-full` CTA, `rounded-full` inputs, muted-red error, self-hosted fonts, no network requests). Home/Settings recolor should be eyeballed under `make dev` with the brain running.

## What's next

1. **Restructure Home + Settings to the Oatmeal composition** (follow-up issue #290) — bring the calm-launcher home grid and Settings panels onto the theme's component shapes, not just its colors.
2. **Dark mode** — the olive ramp gives a clean path (token swap), still a deferred downstream decision (WEB_UI.md # Open questions).
3. **Dedicated `warning` token** if more amber-severity surfaces appear.
