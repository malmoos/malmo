# Hosted seed docs: no bootstrap secret, the owner signs in through the portal (#412)

- **Status:** done — documentation only, no code change.
- **Date:** 2026-09-07
- **Specs touched:** `ENVIRONMENT.md` (# Provisioning & first-boot; "Admin bootstrap — as built" renamed and rewritten), `FIRST_RUN.md` (# Step 2 hosted note), `LOGGING.md` (action vocabulary), `NEXT.md` (cross-reference), `DECISIONS.md` (new 2026-06-28 entry), `docs/dev/hosted-boot-proof.md` (seed field list).

Closes #412. [portal-box-sso.md](portal-box-sso.md) replaced the hosted admin-bootstrap secret with the portal-to-box SSO handshake in the code, but `ENVIRONMENT.md` and `FIRST_RUN.md` kept describing the old path. `grep -rn "admin_bootstrap_secret\|BootstrapSecret" --include=*.go` returns nothing, so the docs described a login that cannot happen — on the page `malmoos/cloud` is told to read before building against the seed. Same drift class as #404 → #407.

## What was done

Read the code first (`internal/profile/seed.go`, `internal/api/sso.go`, `internal/api/auth.go`, `internal/audit/audit.go`, `dev/cloud/cloud-assertions.sh`, `web-ui/src/Setup.vue`), then wrote what it does.

- **`ENVIRONMENT.md` # Provisioning & first-boot.** The first-boot-configuration bullet now names the **assertion-verification key**, not a bootstrap secret. The "Setup wizard, trimmed" bullet says the **first-admin step is gone too** on hosted (the wizard shell starts at time zone when an admin already exists). The "Admin bootstrap" bullet became "Owner sign-in".
- **The "as built" subsection** is renamed **"Owner sign-in & seed ingestion — as built"** and rewritten: the real seed shape `{box_id, assertion_verification_key, enrollment, update_target_url}` with which fields `ReadSeed` requires; ingestion order (key → enrollment → box-id, box-id last as the commit marker) with the key stored as delivered because it is a public key; the handshake at `GET /_malmo/sso` (signature + expiry, then `iss`/`box`/`sub`/`email`/`jti` policy, single-use `jti`, first assertion creates the PAM admin, later ones are owner-only); the status codes (503 un-provisioned, 401 signature/expiry/replay, 403 wrong box or non-owner, 404 on appliance); and `/setup` disabled on hosted (403, audited). The "Operator hand-off" bullet is deleted — there is no secret to hand off.
- **The #220 seed-delivery bullet** described a 3-boot lane asserting "four gate properties". The lane now runs `unseeded seeded bios access update`; the sentence names what those boots actually assert.
- **`FIRST_RUN.md` # Step 2.** The hosted note said an operator types a one-time secret next to the first admin's name and password. It now says the step does not run on hosted at all: the founding admin comes from the assertion, the username is derived from the owner's email, the password is random and discarded, and there is **no recovery code** (the portal account is the way back in — `createSSOOwner` writes no recovery hash).
- **`FIRST_RUN.md` elsewhere.** The profile-overview note (top of the doc) and the per-profile step list both still listed the first-admin step as a hosted step, contradicting the rewritten Step 2. Both now say it is gone on hosted and the wizard starts at time zone. (Raised by Greptile on the PR.)
- **`LOGGING.md`.** The pinned action table was missing three actions the hosted bootstrap path emits: `setup.failure`, `sso.success`, `sso.failure`. Added. The `setup.failure` row names the branches that actually audit — validation 422s and a recovery-code generation error do not, so the row says so rather than implying full coverage. (Also raised by Greptile.)
- **`DECISIONS.md`.** New 2026-06-28 entry records the flip. The 2026-06-20 and 2026-06-26 bootstrap-secret entries stay **as written** — the log records what we believed then; a later entry is how it changes.
- **`docs/dev/hosted-boot-proof.md`** step 1 carried the same wrong field list; corrected.

## Checked against the code and found correct (not changed)

- **`enrollment`** is still `{subdomain, username, password}` and still consumed by the wildcard-cert pass; the acme-dns API endpoint is still a box-side constant.
- **`update_target_url`** — optional, read by host-agent only, never frozen into SQLite, precedence seed → env → default. Matches `profile.Seed` and the bullet.
- **Frozen identity** — `loadHostedEnvironment` still ignores a re-delivered seed once the box-id is persisted.
- **Metadata-endpoint egress block (#251)** — still a FORWARD-hook drop, still applied every boot.
- **`TESTING.md` # Hosted cloud variant** already describes the SSO gate correctly; left alone.
- **The seed is still the only per-box channel** and still write-once; nothing in this change touches that.

## Tests

Documentation only — no code changed, so no test or lane was re-run. `grep -rn "admin_bootstrap_secret\|bootstrap secret\|admin-bootstrap" docs/` now hits only `docs/progress/` (frozen entries) and the two superseded `DECISIONS.md` entries, which are historical by design.

## What's next

- The **cloud side** documents the same seam from its end (cloud `specs/AUTH_AND_ACCESS.md`); nothing here changes the wire format, so no lockstep change is needed.
- `LOGGING.md`'s action table is still short of `internal/audit` (recovery and elevation actions are missing). Out of scope here; worth a pass if someone is in that file.
