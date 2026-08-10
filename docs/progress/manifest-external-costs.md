# Declare what a third party charges to make an app useful

- **Status:** done
- **Date:** 2026-08-10
- **Specs touched:** `docs/specs/APP_MANIFEST.md` — `external_costs` added to # A. Identity and metadata (sample block, field table, and the rule for writing an estimate)

Adds an optional `external_costs:` block to the manifest schema. It names money a **third party** charges to make the app useful, so the store can say so before install.

## The problem

Some apps in the catalog do not work until the user pays somebody else. OpenClaw cannot answer a question without a model-provider API key, and the provider bills per use. An email app sends through a mail provider the user registers with. Today the only place that is written down is prose in `description.long`'s "Good to know" section, which means no surface can show it as a fact, filter on it, or put it next to the install button. A user finds out when the bill arrives.

The curation source already screens for this at the admissibility gate, so the knowledge exists. It just had nowhere structured to live.

## What was done

**`ExternalCost` added to `internal/manifest`** with `id`, `title`, `description`, `required`, `estimate`, `estimate_checked`. The brain does not act on any of it — like the rest of section A, it is display metadata carried to the store.

**`validateExternalCosts()`** enforces the shape every consumer depends on: kebab-case unique `id`, non-blank `title`/`description`, and the pairing that actually matters — `estimate` and `estimate_checked` are required together. A price with no date cannot be re-checked; a date with no price describes nothing. An absent block stays a no-op, so every manifest authored before this parses unchanged.

**The estimate is capped at 100 runes, not bytes.** The curation source runs its own check over the same field in Python, where the natural length is characters. A byte-counted cap here would reject a line of euro signs that the other gate passes, and two validators disagreeing about one rule is worse than either rule.

## The design line: malmo's price is not in this schema

The obvious question is why the app's own price is not here too. It is a commercial decision that changes without the app changing, and it can differ per surface, so it belongs with the curation that owns `listed` and `environments` — store `status.yml` gained a `price:` block in the same week. This schema stays a description of the app.

Neither cost is modelled as a limitation. A limitation is a broken feature; paying for something is not a defect, and giving it a `severity` would have forced a dishonest value.

`required: true` marks a cost the app's main job does not work without. It never gates install, deliberately. An app that will not *boot* until a human supplies something paid is a `blocks-start` curation verdict, and routing that through a display field would have hidden a real admission problem behind a nicer-looking one.

## Why an estimate is allowed to be empty

`estimate` is a unit rate ("$0.20 per 1000 emails"), never a monthly total, because a total depends on how one person uses the app. Empty is explicitly valid. Without that, an author under pressure to fill the field invents a number, and an invented number is the one error `estimate_checked` cannot catch: it looks exactly like a real one. The full wording rules live with the curation source that authors these files (store `docs/app-description.md` # External costs), which also enforces the house style this package does not — no em dashes, no vendor names, the provider's own currency and unit.

## What was tested

- `go build`, `go vet`, `gofmt` clean on `internal/manifest` and `cmd/malmo`.
- `go test ./internal/manifest/` green, including four new tests: a happy two-entry block (one with an estimate, one deliberately without), an absent block on a pre-existing manifest, a ten-case rejection table (missing/non-kebab/duplicate id, missing or blank title, missing description, estimate without a date, date without an estimate, unparseable date, over-length estimate), and one asserting the cap counts runes so a 100-character euro-sign estimate is accepted.
- The whole-repo `make check` was **not** run here: `go build ./...` fails in this checkout on the PAM cgo dependency (`libpam0g-dev` absent), which is unrelated to this change. CI runs it.

## Known gaps

- **The field is not on the catalog wire yet**, so no box can render it. `wireApp` carries the verbatim `manifest.yml`, so the bytes do reach a box and re-parse correctly — but the box's store view is built from the wire struct's own fields, not the re-parsed manifest. Surfacing the costs on a detail page needs the coordinated cloud + os change (`internal/catalog/wire.go` here, `internal/catalog/published.go` there), and that changes the index digest contract, so it is deliberately its own piece of work.
- **The control plane does not publish it.** Cloud's `catalog-sync` validates store manifests through `malmo manifest lint`, so a manifest carrying the block lints clean from today. Carrying it into the published payload is part of the same cloud change.
- **No app declares a cost yet.** The curation source authors them, grouped by cost type, once this lands.
