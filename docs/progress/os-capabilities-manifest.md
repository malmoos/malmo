# Platform capabilities manifest — machine-readable, gap-class-keyed

- **Status:** done
- **Date:** 2026-07-02
- **Specs touched:** `docs/specs/CAPABILITIES.md` (new — governing spec), `docs/dev/capabilities.yml` (new — the manifest), `docs/dev/catalog-import-gaps.md` (triage step wired to the manifest)

## What was done

Published a **machine-readable capabilities manifest** so catalog curation stops depending on someone remembering which platform gaps have closed. When an app is withdrawn from the store because malmo lacks a feature, and that feature later ships, the app can graduate — but the only signal today is a human grepping the gap ledger. This makes the connection a data lookup.

- **`docs/dev/capabilities.yml`** — a versioned list of the capabilities malmo has shipped. Each entry is `{id, since, ref, summary}`. **The `id` is the gap-class tag** from `catalog-import-gaps.md`, reused verbatim, so the manifest, the ledger, and a curation record's blocker reference all speak one vocabulary. Seeded with the seven gap-classes the ledger already marks `implemented`/`resolved`: `secret-injection`, `oneshot-job-restart` (#92), `managed-mysql` (#108), `managed-redis` (#159), `service-user` (#147), `smtp-relay` (#122), `operator-env-config` (#264). `since` is `"0.x"` for all of them — no brain version is cut yet (`RELEASE_MANIFEST.md`), so `ref` (PR / `DECISIONS.md` date / spec) carries the real provenance until releases are tagged. A top-level `version` is bumped on every list change so a consumer can tell how current the set it validated against was.
- **`docs/specs/CAPABILITIES.md`** — the governing spec: why the manifest exists, its shape, and the **append-only discipline** (a shipped capability never un-ships; append, never remove or renumber). It names the one nuance the ledger already carries — a gap-class can be *partially* closed (`nonroot-data-ownership` shipped `service-user` for adopting images but the hardcoded-internal-UID facet still waits on the unshipped `userns-remap`), so list only the capability that actually shipped, under its own id.
- **`docs/dev/catalog-import-gaps.md`** — the triage instruction now says: when a ledger entry flips to `implemented`/`resolved`, add its gap-class to `capabilities.yml` and bump `version` **in the same change**, because that entry is what mechanically fires the re-screen. Turns the ledger's forgettable "grep for waiting apps" note into a data edit.

## How it maps to the specs

Realizes the freshness-coupling mechanism: the OS side publishes capabilities as gap-class-keyed data; a curation record names the gap-classes it waits on; a re-screen cross-check surfaces every app whose blockers are all shipped. This repo owns only the manifest and the discipline that keeps it honest — the cross-check consumer lives with the catalog curation records outside this repo, and this file names capabilities in OS terms only, one-directional.

## Known gaps & deviations

- **`since` is `"0.x"` for every entry.** No brain version is cut yet (`RELEASE_MANIFEST.md` describes the release manifest but no releases are tagged), so a real semver would be fiction. `ref` carries the honest provenance; backfill `since` when releases start tagging.
- **No CLI emitter.** The manifest is a plain checked-in YAML read directly, not a `malmo capabilities` subcommand. It is data, not the manifest schema the brain enforces — the "delegate validation to the CLI" discipline (the seed-wire pattern) does not apply, and a plain file is readable by any consumer without building the Go binary.
- **Documentation-only change.** No brain/host-agent/UI code moves; nothing enforces the manifest inside a box. It is a curation-tooling contract consumed downstream.

## What's next

- The re-screen cross-check that consumes this manifest lives with the catalog curation records (the tracking item is off-repo). With the manifest as-is, the first app it should surface is one whose only blocker (`operator-env-config`) already shipped in #264 — the mechanism's headline value: catching a graduation nobody re-screened.
- Backfill `since` with real brain semvers once `RELEASE_MANIFEST.md` releases are tagged.
