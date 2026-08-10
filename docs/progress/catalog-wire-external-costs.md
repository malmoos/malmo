# Carry third-party costs onto the box

- **Status:** done
- **Date:** 2026-08-10
- **Specs touched:** none — `APP_MANIFEST.md` already describes `external_costs`; this is the wire/projection half

Follow-up to [manifest-external-costs.md](manifest-external-costs.md), which added `external_costs:` to the manifest schema and recorded "not on the catalog wire yet, so no box renders it" as a known gap. This closes it. The control-plane half is a coordinated PR in cloud (`internal/catalog` there).

## The problem

An app can declare what a third party charges it to be useful, but no box could see it. The published record carries a display subset per app, and the box's store view is built from that subset — not from the verbatim manifest bytes, which are the install payload. So the field existed, lint enforced it, and a user still learned about the model-provider bill after installing.

## What was done

**`wireApp` gained `ExternalCosts`**, typed as a new `catalog.ExternalCost` that mirrors the control plane's `catalog.ExternalCost` field for field.

Reusing `manifest.ExternalCost` was the first attempt and was wrong, for a reason only the generated OpenAPI showed: its `Required` is a `*bool`, so the spec came out as `boolean | null` on `GET /catalog/{id}` while the control plane's identical endpoint returns a plain `boolean` — two store surfaces disagreeing about one field. The pointer earns its place in the manifest, where it rejects an author who never states the field; by publish time it has been stated, so the wire and the API carry a plain bool. `externalCostsOf` collapses the tri-state for the disk source, which reads manifests directly.

**It sits between `Footprint` and `IconFile`, and that is load-bearing.** The index digest is a SHA-256 over `json.Marshal` of the app array, so the box must re-marshal to the identical bytes to verify a snapshot. A mirror that put this field anywhere else would parse fine, pass every field-level assertion, and fail only on the digest — against every real snapshot.

**`Detail` carries it; `Entry` does not.** A cost is a paragraph of reading, so it belongs on the page where someone decides to install, not on a grid card. Both sources project it: the remote one off the wire, the disk one straight off the parsed manifest.

## What was tested

- `go build`, `go vet`, `gofmt`, `make openapi-check`, and `go test` on `internal/catalog`, `internal/manifest`, `internal/api` green. The OpenAPI spec and the generated TS client were regenerated in the same change (`make openapi`, `npm run gen:api`) — CI's spec-freshness gate caught the first push, which is what surfaced the `*bool` leak above.
- **`TestExternalCostsSurviveTheDigest`** builds a snapshot that declares a cost, verifies it through `parseSnapshot`, and projects it onto `Detail`. No published app declares one yet, so the pinned fixture cannot exercise the field — this is what covers it until one does. It also marshals the `Entry` and fails if the key appears there.
- **The pinned fixture was refreshed** (`make catalog-fixture OS=../os` from cloud), which is what keeps `TestNoUnmodeledFields` from going blind. It picked up the `navidrome` record store had added and cloud had not yet republished — pre-existing drift, fixed in the cloud PR's first commit.
- **Verified end to end against real data, not only tests.** Added a real `external_costs` block to store's `apps/openclaw/manifest.yml`, ran the actual sync tool (`make catalog` in cloud, 35 apps published), and fed the resulting `dist/catalog.json` to this box's own `parseSnapshot` — the exact function `remote.go`'s `loadCache` calls at boot. It verified (so the digest reproduced with the new field present, which is the field-order proof) and `detailOfApp` returned the cost with `required=true`, its date, and the estimate verbatim. The store edit and the republished dist were both reverted afterwards; `make catalog-fixture-check` reports `current`.

## Known gaps

- **No UI yet.** `Detail.ExternalCosts` reaches the API but `web-ui`'s app detail page does not render it. Deliberate: how a cost card should look beside the install button is a design question, not a wire question.
- **No app declares a cost**, so the field is inert in production until the curation source authors them. That is the next piece of work, grouped by cost type.
- **What malmo charges is still not on this wire.** It is authored in the curation source (`status.yml` `price:`, every app free today) and nothing consumes it yet.
