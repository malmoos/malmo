# Reading the release manifest: verify, parse, decide, cache

- **Status:** done
- **Date:** 2026-08-12
- **Specs touched:** none (realizes `docs/specs/RELEASE_MANIFEST.md`)

Closes #395. The apply transaction ([control-plane-update-transaction.md](control-plane-update-transaction.md)) and its trigger ([control-plane-update-trigger.md](control-plane-update-trigger.md)) are both built, and nothing picks the target: `POST /api/v1/system/update` takes two image refs an admin typed. On an appliance the target comes from a signed release manifest. This is the half of that with no network and no timer in it.

## What was done

New `internal/hostagent/relmanifest`, three files, four jobs.

**Verify** (`minisign.go`) — a minisign verifier, Ed25519, no signing anywhere. It accepts both algorithm modes: `Ed` (sign the file) and `ED` (sign BLAKE2b-512 of the file). Only the second is what minisign writes by default, but a verifier that took one of the two would reject files made by the standard tool, and finding that out on the day of the first release is not the plan.

**It checks both signatures in the file, not just the first.** The main one covers the manifest. The global one covers the signature **plus the trusted comment**, which is where minisign convention puts the file name and timestamp. Checking only the first would let anyone rewrite that comment on a genuinely signed file, and the verifier would hand its caller attacker-chosen text that looks authenticated.

**The verifier holds a list of keys, not one key.** `RELEASE_MANIFEST.md` # Signing builds key rotation on this: ship a host-agent that accepts `{old, new}`, let the apt rollout reach the fleet, dual-sign for a window, drop the old key. One constant instead would turn a lost or compromised key into a synchronized fleet update — the outage shape the whole design avoids — and adding the list later is itself the flag day.

**Zero keys is allowed, and it refuses everything.** That is the shipping state today: there is no release-signing key yet (`RELEASE_MANIFEST.md` # Signing defers key custody until there is a release to sign). A box with no key must ignore every manifest rather than trust one, so the appliance updater stays inert instead of acting on a file it cannot check. `ErrNoKeys` is its own error because "this box was never given a key" and "this signature is wrong" call for different operator actions.

**Parse** (`manifest.go`) — the v1 schema, with unknown fields ignored on purpose. The publisher may add optional fields without bumping `manifest_version`, so a box that refused them could not be shipped ahead of them.

**Decide** (`manifest.go`) — four answers, not two:

- `none` — the box already runs what the manifest names.
- `update` — the manifest names a different pair and the box may offer it.
- `rollback` — the kill switch is set and this box is running the retracted version.
- `hold-host-agent-too-old` — the manifest is valid and current, but this box's host-agent is older than `minimum_host_agent`. `RELEASE_MANIFEST.md` # Failure modes keeps this separate from "not for you": such a box is healthy and waiting for its apt-driven host-agent update, and collapsing the two would make a normal state look like an error.

**The kill switch is checked before the host-agent gate**, and the order is the interesting part. A box running a retracted version should be offered the way back even when it is behind on host-agent — the retracted version is what is hurting it now. The gate exists to stop a box moving *forward* onto a version its host side cannot drive.

**An unreadable version fails closed.** A host-agent version that will not parse cannot be compared, and calling it "new enough" would let the one safety belt here open itself whenever the string is malformed. (A dev build stamps `dev`, so this is not hypothetical.)

**Cache** (`cache.go`) — the manifest and its signature in **one** file at `/var/lib/malmo/manifest.json`, written with the same temp-fsync-rename-fsync ceremony the control-plane ledger uses.

Three decisions worth naming:

- **One file, not two, and review is what found this.** The first version wrote `manifest.json` beside `manifest.json.minisig`, each write atomic on its own. That cannot keep the promise `RELEASE_MANIFEST.md` # Failure modes makes — "the previous valid manifest stays in effect". Two files means two renames: lose power between them and the box comes back with the new manifest beside the old signature, a pair that fails verification, **and the previous good manifest is already gone** because the first rename overwrote it. The box would have no usable cache at all, which is worse than the failure the spec was describing. One file is one rename, so the box always comes back to a complete pair — the new one or the old one.
- **The raw bytes are stored, not a re-marshalled struct.** A re-marshal would drop the unknown fields `Parse` ignores and reorder the rest, and the signature covers the publisher's exact bytes — so the round trip would produce something that can never verify again. The publisher's bytes live in the file as a JSON string.
- **`Load` re-verifies before returning.** The cache is what an offline box acts on, so trusting it unchecked would make the local file system a way around the signature: anything that can write `/var/lib/malmo` could then choose the version the box runs.

## Testing

Table tests over the failure modes the spec names: bad signature, unknown key, rewritten trusted comment, truncated and malformed signature files, wrong `manifest_version`, wrong channel, non-semver versions, `rollback_to` set. The test file carries a small minisign **signer** (the box only ever verifies), so the parser is exercised against bytes in minisign's real layout rather than a shape invented to match the verifier.

Four mutation checks, each done by editing the code and restoring it:

1. Drop the global-signature check ⇒ only `TestVerifyRefusesARewrittenTrustedComment` fails.
2. Apply the prehash to the wrong algorithm mode ⇒ the both-modes test fails.
3. Treat an unreadable host-agent version as new enough ⇒ the fail-closed case in `TestDecide` fails.
4. Skip the re-verify in `Load` ⇒ `TestLoadRefusesACacheEditedOnDisk` fails.
5. Compare the running pair as strings instead of as versions ⇒ the "same version written two ways" test fails.

## Known gaps & deviations

- **Nothing calls this yet.** No poll, no HTTP, no dashboard prompt, no wiring into `cmd/host-agent-real`. Next slice.
- **No box has a key**, so on a real appliance every manifest is refused with `ErrNoKeys` and the appliance updater is inert. That is deliberate and safe, but it means this package is proven against test-generated keys only.
- **No interop test against the real `minisign` tool.** The test signer implements the format from the spec; a genuine `minisign -S` file has never been fed to it. Worth adding when a signing key exists, or as an optional test gated on the binary being installed.
- **The version → image-ref step is undecided and out of scope.** The manifest names versions, the transaction pulls **by digest** (`BUILD.md` # 6), and the published ghcr packages are **still private** — `BUILD.md` # 6 records that flipping them to public is a manual org-admin action that has not happened, so no box can pull them anonymously today. Resolving a version to a digest needs either a registry lookup at update time or digests in the manifest. That is a maintainer decision, not something to settle inside this package.
- **`writeFileAtomic` is duplicated.** `internal/hostagent/controlplane` has the same helper, byte for byte. Three consumers now share the logic, which is the point at which extracting it (say `internal/atomicfile`) stops being premature. Left out of this change to keep the diff to one package.
- **`controlplane` has the two-file version of the same crash problem.** It writes `compose.yml` and `images.json` as separate atomic writes, so a power cut between them leaves the declaration half-updated. That is merged code and a separate fix, recorded here because this review is what surfaced the shape of it.
- **The three-strikes pin** (`UPDATES.md` # 3 step 5) is not here; it belongs with the trigger that retries.
- **`Decide` has no notion of "I already refused this one".** A box that fails an update three times still gets `update` from this function; the counter that stops re-offering lives with the trigger.

## What's next

1. **The poll** — hourly fetch of `stable.json` + `.minisig`, verify, cache, and expose the decision to the brain. Needs a decision on where the accepted public keys are baked in (build flag, like the version stamp) and what a box does when the CDN is unreachable (keep the cache; the code already supports it).
2. **The dashboard prompt** (`UPDATES.md` # 6) — "Update available", admin-prompted, and the "Downgrade recommended" case the kill switch produces.
3. **Three consecutive failures then pin** (`UPDATES.md` # 3 step 5).
4. **Version → image ref**, blocked on the two facts above.
