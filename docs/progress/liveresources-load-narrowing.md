# Fix vue-tsc null-narrowing break in LiveResources load line

- **Status:** done
- **Date:** 2026-06-13
- **Specs touched:** `LOCAL_ANALYTICS.md` (Real-time system resources — display only; no spec change)

Bug fix for a `ci-web` break that reached `main` on a non-web commit. The failure surfaced on the next PR touching `web-ui/`.

## What was done

`web-ui/src/LiveResources.vue` — hoisted the inline `.map` closure in the load-line template expression into a `loadLine` computed property:

- The original template expression mapped over `sample.load` directly in the `<template>`. Inside that closure, vue-tsc loses the `v-else` narrowing that proves `sample` is non-null (TS18047), and `noUncheckedIndexedAccess` flagged the tuple index `sample.load[i]` as possibly-undefined (TS2532).
- The computed's early `if (!s) return ""` guard narrows `s` cleanly. Iterating `s.load.map((v, i) => ...)` drops the indexed-access concern. Rendered output is unchanged.

`make check-web` was red before; green after. `make check` (Go suite) was already green and unaffected.

## Known gaps & deviations

None. The same one-line hoist was carried as a drive-by in PRs #158 and #168 just to keep their own `ci-web` green; whichever of those lands first will produce a no-op overlap on rebase.

## What's next

No follow-up work. PRs #158 and #168 (both touch `web-ui/`) should rebase onto `main` after this merges so their `ci-web` gates go green without the drive-by copy.
