# Hosted `/setup`: prefill the admin-bootstrap secret from the link, validate its shape

- **Status:** done
- **Date:** 2026-06-26
- **Specs touched:** `ENVIRONMENT.md` # Admin bootstrap (no contract change; UX of the secret hand-off). Two-side change co-owned with the cloud control plane (it produces the link).

## What was done

On the hosted profile the first-run wizard's admin step collects the one-time admin-bootstrap secret the operator pastes (`ENVIRONMENT.md` # Admin bootstrap, C3a's `bootstrap_secret`). A live box was reported rejecting a *correct* secret with the gate's "invalid admin bootstrap secret" — the value was right, but re-typing/re-pasting it by hand turned a single dropped character into an opaque 401. This makes the secret arrive prefilled when the operator follows the link, and makes a bad hand-paste fail loudly instead of as a generic gate 401.

- **Prefill from the URL fragment.** The control plane links to the box's `/setup` with the secret in a fragment (`<box-url>/setup#secret=<value>`). `AdminStep.vue` now reads `location.hash` on mount, prefills `bootstrapSecret`, then strips the hash with `history.replaceState` so the secret does not linger in the address bar or browser history. A fragment is never sent to the server, so it stays out of the brain's access log and the `Referer` header.
- **Format-validate before the round-trip.** The secret is a fixed shape — 43 base64url characters (`/^[A-Za-z0-9_-]{43}$/`). On the hosted profile `submit` checks the trimmed value against that shape *before* calling `/setup`; a malformed (usually truncated) paste gets "That doesn't look like a complete setup secret. Paste the full 43-character code shown when your box was created." instead of the bootstrap gate's generic 401. The appliance profile, which ignores the secret entirely, skips the check.

The cloud half — appending `#secret=<value>` to the portal's `/setup` link — lands in the `malmoos/cloud` repo (`internal/web/static/dashboard.js`).

## How it maps to the specs

`ENVIRONMENT.md` # Admin bootstrap governs *that* the hosted `/setup` is gated by the seeded secret; it does not govern how the operator transports the secret from wherever it was shown into the form. This change adds a transport (a link-borne fragment) and a client-side shape check; the gate itself (`gateBootstrap`, constant-time SHA-256 compare) is unchanged, and the wire field (`bootstrap_secret`) is unchanged. The 43-character shape mirrors the cloud's one-time-secret format (32 random bytes, base64url, no padding); the coupling is documented in the component so a future change to the secret's entropy updates the check.

## Known gaps & deviations

- **Format check is coupled to the cloud's secret length.** If the control plane ever changes the secret's byte length, this `{43}` check must change with it. It is a deliberate, documented coupling — the secret shape is part of the seed hand-off — not an inferred constraint.
- **Prefill is best-effort.** It only fires when the operator follows the portal link from the same browser session; a secret transported to another device or typed by hand still uses the manual path (now backed by the shape check).
- **No component test.** `web-ui/src/setup/` has no `.spec` harness; the fragment-prefill, hash-strip, and shape-rejection paths are verified by the production build (`vue-tsc` clean) and manual click-through, not in CI.

## What's next

- **End-to-end click-through** on a fresh hosted provision: follow the portal link, confirm the field is prefilled, the hash is stripped, and setup completes — paired with the cloud-side verification.
- A small **component test** for `AdminStep.vue` covering: fragment prefill + hash strip on mount; shape rejection short-circuits before any network call; appliance profile skips the check.
