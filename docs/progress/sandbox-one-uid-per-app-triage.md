# Sandbox one-UID-per-app — triage of the baked-non-root-UID limitation (#193)

- **Status:** done
- **Date:** 2026-06-20
- **Specs touched:** `docs/specs/APP_ISOLATION.md` (# What this does not cover — named the conflicting-per-service-UID shape and folded it into the existing deferral), `docs/specs/NEXT.md` (# User-namespace remap for hardcoded-internal-UID app images — count-trigger updated, per-service-UID note added, parked-apps list extended), `docs/dev/catalog-import-gaps.md` (new `nonroot-data-ownership — formbricks` entry)

#193 was filed as a **design/triage item** ("No code change proposed here") off the Formbricks catalog import (#182): malmo's lifecycle pins one malmo-assigned UID + `cap_drop: ALL` + `no-new-privileges` on **every** service in an app, and admission rejects a numeric `user:`. That breaks two cases #182 hit on a live boot — (1) an image that writes its own image-baked files owned by a fixed non-root UID (Formbricks' `nextjs`/1001 migration runner), and (2) an app whose services appear to have *conflicting* UID needs (a bundled `pgvector` Postgres that wants non-root alongside the 1001 main app). It asked whether to relax the one-UID model.

## What was done

Read the governing spec end-to-end before proposing anything, and the answer was already in it — so the unit of work is a **triage decision recorded in the right doc homes**, not a mechanism change.

- **The directions #193 floated are foreclosed by a locked decision.** `APP_ISOLATION.md` # Runtime identity & data ownership is explicit: *"There is deliberately no manifest field naming a numeric UID/GID"* — a manifest-named UID resolves in the host namespace (malmo runs no userns remap) and could alias a real host principal, which `THREAT_MODEL.md` names a top adversary; a numeric `user:` is an admission rejection for both doors (`DECISIONS.md` 2026-06-10). So "an opt-in field naming a UID / run as the image's declared `USER`" is off the table without flipping that decision — out of scope for one triage issue.
- **Both Formbricks cases reduce to the already-deferred hardcoded-internal-UID class.** Case 1 (main app mutates its own 1001-owned baked dir; root under `cap_drop: ALL` can't write it — no `CAP_DAC_OVERRIDE`) is the **same class as poznote/postiz** in the import ledger, which `APP_ISOLATION.md` # *What this does not cover* + # *Not in v1* already mark as curation-rejects pending user-namespace remap. Case 2 is **not a separate need**: a bundled Postgres adopts any non-root UID fine via `service_user: true`, so only the main app's hardcoded-1001 writes block — case 1 again. A per-service distinct-UID mechanism would not unblock any real app (the services that would need distinct identities are themselves hardcoded-internal-UID images, and malmo treats an app's services as one trust unit), so it folds into the same deferral rather than standing alone.
- **The genuine, fixable gap is early detection — already handled.** #193's "static `manifest check` gives a false GO, the failure only shows at boot" is the real risk, but the catalog curation flow already mandates a **live boot smoke test** (the backstop that surfaced this for #182). A reliable *static* gate isn't feasible — a baked `USER` alone doesn't predict UID-tolerance (gitea declares `USER` yet tolerates arbitrary UIDs) — so no new check is warranted.

Recorded the verdict where the project keeps this knowledge: a `nonroot-data-ownership — formbricks` ledger entry (both cases + the boot evidence + why per-service-UID doesn't help), the matching update to the `NEXT.md` userns-remap item (Formbricks is the 3rd app of the class, so the count-trigger is met — though the mechanism stays gated on the unanswered runtime-feasibility spike), and a one-sentence tightening of `APP_ISOLATION.md` # *What this does not cover* to name the conflicting-per-service-UID shape explicitly.

## What's next

- No code change. The one-UID-per-app + `cap_drop: ALL` model stands; naming a UID stays an admission rejection.
- The hardcoded-internal-UID class (poznote, postiz, formbricks) now meets the `NEXT.md` count-trigger, but remains **deferred** behind that item's gating **feasibility spike** — can the runtime even do *per-app* userns remap (classic Docker `userns-remap` is daemon-global), and how does a remap reconcile with folder apps running as the owner's real host UID. Until that spike is answered, this is not implementation-ready and must not be filed as an implementation issue.
- Formbricks (#182) stays a curation-reject; revisit it and the other parked apps when userns-remap lands.
