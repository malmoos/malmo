# App-secret injection — `MALMO_SECRET_*`

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** `APP_MANIFEST.md`, `SERVICE_PROVISIONING.md`, `DECISIONS.md`, `NEXT.md`

Closes the `blocks-start` gap captured in `docs/dev/catalog-import-gaps.md` # `secret-injection — kan`: an app that needs an app-specific random signing secret (`BETTER_AUTH_SECRET` for Better Auth, and the whole class of JWT/HMAC/`SECRET_KEY_BASE` secrets) had no way to obtain one — the author can't ship a value in a public catalog and the non-technical user can't be asked to generate one, so the app simply wouldn't boot. The brain now generates and injects it. Design rationale in `DECISIONS.md` 2026-06-05.

## What was done

### Manifest — `internal/manifest`

- New top-level `secrets: [{name, bytes?}]` field (`Secret` struct). `name` is lowercase snake_case (validated by `secretName`); `bytes` defaults to `DefaultSecretBytes` (32) and is floored at `MinSecretBytes` (16). `validateSecrets` rejects bad names and duplicates and normalizes `bytes` in place, wired into `Manifest.validate`.

### Store — `internal/store`

- New `instance_secrets` table (`instance_id, name, value`, PK `(instance_id, name)`, `FOREIGN KEY … ON DELETE CASCADE` — secrets are reclaimed with the instance via the already-enabled `foreign_keys=ON`).
- `InstanceSecret` type + `SetInstanceSecrets` / `GetInstanceSecrets`, mirroring the `instance_images` CRUD shape.

### Lifecycle — `internal/lifecycle`

- Install step `5b` (`generating_secrets`): `generateSecrets` draws each declared secret from `crypto/rand`, base64url-encodes it, and the result is persisted via `SetInstanceSecrets` before `.env` is written. On any failure the existing install rollback removes the row (and its cascaded secrets).
- `writeEnv` re-emits the persisted secrets as `MALMO_SECRET_<NAME>` by **reading them back from the store**, never regenerating — so the value is stable across every `.env` rewrite. Stability is the load-bearing property: a token-signing secret that changed on restart would invalidate every live session.

### Catalog — `catalog/kan`

- Manifest declares `secrets: [{name: auth}]`; compose maps `BETTER_AUTH_SECRET: ${MALMO_SECRET_AUTH}` (was a hand-set-before-start placeholder).
- Same pass closed kan's `app-url-injection` ledger entry: `NEXT_PUBLIC_BASE_URL: ${MALMO_APP_URL}`. The ledger's claim that no URL-injection mechanism existed was wrong — `writeEnv` has injected `MALMO_APP_URL` (`http://<slug>.local`) all along; both kan `app-url` entries are corrected in the ledger.

### Tests

- `manifest`: byte normalization (default/floor/explicit) and rejection of bad/duplicate names.
- `store`: secrets roundtrip + cascade-on-instance-delete.
- `lifecycle`: install injects a non-empty `MALMO_SECRET_AUTH` that matches the persisted value; two installs get distinct secrets; `generateSecrets` entropy → encoded length (32→43, 16→22 chars).
- `malmo manifest check catalog/kan/manifest.yml` passes (schema + admission).

## What's next

- **Security hardening is deliberately deferred**, parked as `NEXT.md` # App-secret injection hardening: `.env` is still `0o644` (should be `0o600`/root-owned now that it holds a secret); env-var delivery's leak surface (`docker inspect`, `/proc/environ`, child inheritance) vs. the `_FILE` convention; at-rest encryption (plaintext in SQLite + `.env`, relationship to LUKS); backup-archive encryption (the secret must travel in the app's backup for a restored app to keep validating old tokens); rotation and log/audit hygiene. These were reviewed and parked, not missed — the mechanism ships correct-but-unhardened under the household trust model.
- **`docuseal` `app-url-injection`** stays `open` in the ledger: no platform gap (it's the same `MALMO_APP_URL` mapping), but its compose wasn't rewritten here — revisit on the next docuseal touch.
- **App update** doesn't yet exist; when it lands it must preserve the persisted secret (read from store, never regenerate), which is exactly why persistence — not just the on-disk `.env` — is the source of truth.
