# Settings → About shows what this box is running

- **Status:** done
- **Date:** 2026-08-12
- **Specs touched:** none (realizes `docs/specs/UPDATES.md` # 6 in part)

Closes #393. `GET /api/v1/system/version` has answered "what is this box running" since [system-version-whole-box.md](system-version-whole-box.md), and nothing in the dashboard read it — that entry listed "no dashboard surface" as a gap, and so did every updater slice after it. This is the smallest part of `UPDATES.md` # 6: before the dashboard can say an update is available, it has to be able to say what is installed.

## What was done

`AboutSection.vue` reads the endpoint with the same TanStack Query shape the other settings sections use, and shows the answer at two levels.

**The version sits on the front of the card**, next to the product name: "malmo 0.6.0". That is the one fact a non-technical owner might read out loud when asking for help, so it is not hidden behind anything.

**Commit, host-agent version and UI image sit behind a closed disclosure.** They matter to whoever reads a support thread, not to the person checking their photos. A `<details>` keeps them one click away and keeps the card calm, which is the rule the section already followed when it had nothing but a product blurb.

**Each part degrades on its own.** The endpoint returns 200 with `host_agent_version` or `ui_image` missing when it could not read that source (`internal/api/system.go` — a version report missing one of three parts is still useful, and the brain version is the one an updater needs first). So a missing part renders as "Unknown", never as an empty row, and never blanks the card. Only a failed request — not a partial answer — shows an error line.

The old comment in the file said the section would grow "when the brain exposes a build/version + box-name surface". Half of that arrived: there is still no box name, so the card does not claim one.

## Checked against a running box

`make dev`, first-run setup, then the real endpoint:

```
{"version":"0.6.0","commit":"be150b8","host_agent_version":"0.6.0"}
```

`ui_image` is **absent** on a dev box, which is the interesting case: in dev the brain runs natively with no staged control-plane compose to read, so the field is dropped. That is exactly the partial answer the section has to render, and it renders as "Unknown" rather than an empty row. The full three-field answer only exists on a real box.

## Known gaps & deviations

- **No update affordance, on purpose.** Nothing in the box discovers a new version yet — the release-manifest poll is not built, and the hosted trigger needs the box↔cloud credential (`NEXT.md` Tier 1). A button that can only ever say "nothing to do" is worse than no button.
- **No "last update" outcome.** The job record lives in host-agent's memory (`NEXT.md` Tier 2, from [control-plane-update-trigger.md](control-plane-update-trigger.md)), so the card cannot say what happened the last time this box updated.
- **No box name.** The endpoint does not report one, so About still does not show one.
- **`ui_image` is a raw image ref**, not a version. On a real box it reads like `ghcr.io/malmoos/malmo-ui@sha256:…`; the card shows it verbatim in a monospace row with the full string in a tooltip. Turning that into something a person would read needs the version to travel with the image.
- **No component test.** The dashboard has no component-test harness; this is covered by `vue-tsc` + the production build, plus the manual check above.

## What's next

1. **The versions on a real box.** Only the hosted cloud lane has a staged compose, so `ui_image` is unproven end-to-end in the dashboard.
2. **Where an update appears** (`UPDATES.md` # 6) — needs a trigger that discovers versions first.
3. **A durable last-update record**, so About (or a Settings → Updates view) can report the outcome after a host-agent restart.
