# BYO outgoing mail — SMTP providers, per-app bindings, MALMO_MAIL_* injection

- **Status:** done
- **Date:** 2026-06-12
- **Specs touched:** `SERVICE_PROVISIONING.md` (# BYO outgoing mail — new section, injection-family table, locked decisions), `APP_MANIFEST.md` (# D3 — new section, locked decisions), `SETTINGS.md` (panel inventory), `DASHBOARD.md` (consent dialog), `DECISIONS.md` 2026-06-12, `NEXT.md` (# Outgoing mail Tier-3 entry; mail passwords folded into # App-secret injection hardening), `docs/dev/catalog-import-gaps.md` (kimai/gitea `smtp-relay` flips)

Closes #122, the `smtp-relay` gap-class the catalog import sprint kept hitting (ghost, kimai, gitea, docuseal). Apps that send email — password resets, reminders, invites — now have a malmo story: the admin registers their own SMTP account(s) in Settings → Outgoing email, each mail-capable app binds to one (install-time picker, rebindable later), and the brain direct-injects the bound provider's credentials as `MALMO_MAIL_*` env vars. No malmo-run relay or smarthost — residential IPs can't deliver mail, so the app dials the admin's real provider itself over its declared `internet` permission.

## What was done

### Store (`internal/store/mail.go`, `store.go`)

- Two tables: `mail_providers` (label UNIQUE, encryption CHECK `none|starttls|tls`; password plaintext-at-rest with a comment pointing at `NEXT.md` # App-secret injection hardening) and `instance_mail_bindings` (instance_id PK, FKs to both sides with ON DELETE CASCADE — deleting a provider unbinds its apps, uninstalling an app drops its binding).
- CRUD following the house conventions: `isUniqueErr` → `ErrConflict` (duplicate label), `RowsAffected == 0` → `ErrNotFound` on update/delete, `ListMailProviders` ordered by label. `GetInstanceMailProvider` (JOIN) returns `ErrNotFound` when unbound — that absence is `writeEnv`'s signal to inject nothing.

### Manifest (`internal/manifest`)

- New optional top-level `mail:` block, a `*Mail` pointer mirroring `*HealthProbe` (nil = absent). v1 admits only `optional: true`: an explicit `optional: false` — or a bare `mail: {}` — is rejected at parse, because an app that can't run unbound couldn't be installed on a box with zero registered providers.

### Lifecycle (`internal/lifecycle/mail.go`, `lifecycle.go`)

- `Install` gains a `mailProviderID` param: a guard at the top rejects a mail election on a non-mail manifest before any state is written; step 5d (after service grants, before isolation) persists the binding with rollback on failure — a deleted provider is caught by the FK.
- `writeEnv` appends `mailEnvLines` when bound: the discrete `MALMO_MAIL_HOST/_PORT/_USER/_PASSWORD/_FROM/_ENCRYPTION` plus a Symfony-style `MALMO_MAIL_DSN` (`smtps://` for implicit TLS, `smtp://` otherwise; credentials URL-escaped via `url.UserPassword`).
- `RebindMail(ctx, id, providerID)` — providerID `""` unbinds. Per-instance lock, `ErrNoMailSupport` backstop (the API's 422), brain-commits-first: binding row → surgical `.env` rewrite → `compose up -d` only if running (env is read at container create; stopped instances pick the change up at next start). `rewriteEnvMail` re-stamps **only** the `MALMO_MAIL_*` lines, leaving every other line byte-identical — a full `writeEnv` would need install-time isolation state (folder elections aren't persisted) and could never accidentally re-roll a stable secret.

### API (`internal/api/mail.go`, `api.go`, `install_plan.go`, `internal/audit`)

- Provider CRUD at `/api/v1/mail-providers` (admin + elevation for writes; audit success **and** failure per the elevation-class rule; passwords write-only — an empty password on update keeps the stored one). `POST /{id}/test` sends a real message synchronously via `net/smtp` (implicit-TLS dial or STARTTLS, AUTH PLAIN when a username is set, 15s deadline; failure → 502 with the SMTP error).
- `GET /mail-providers/options` (id + label, any authenticated user) feeds the rebind picker — members can rebind their own personal apps without admin read access. The install dialog's picker rides the install plan instead (`InstallPlanDTO.mail` with the provider options, attached when the manifest declares mail).
- `POST /apps` accepts `config.mail_provider_id`, validated authoritatively (mail-capable manifest + provider exists → 422s audit as failed installs). `PUT /apps/{id}/mail-binding` runs `RebindMail` as a job behind `authorizeAppMutation` (household = admin, personal = owner/admin). `GET /apps/{id}` enriches `mail_supported` + `mail_provider_id` for the detail page.
- New audit actions: `mail.provider.{create,update,delete,test}`, `app.mail.rebind`.

### Web UI

- `OutgoingEmailSection.vue` (Settings → Outgoing email, admin-only nav + redirect): add/edit/delete with `withElevation`, per-row inline test-send, delete confirm spells out that bound apps stop sending.
- `InstallDialog.vue`: radio picker for mail-capable apps — None default (install with email off), a sole registered provider preselected, admin-only "add an account" link into Settings.
- `InstalledAppDetailSection.vue`: "Send email as" select (None + options), runs the rebind job and notes the brief restart.

### Catalog: Kimai

- `mail: {optional: true}`; compose maps `MAILER_URL: "${MALMO_MAIL_DSN:-null://null}"` and `MAILER_FROM: "${MALMO_MAIL_FROM:-kimai@example.com}"` — bound delivers, unbound keeps Symfony's null transport (upstream's documented "mail off" value) instead of a broken empty DSN. Description softened to point at the Settings flow.

## Verification

- Store/manifest/lifecycle/API unit + boundary tests: CRUD round-trips and conflict/not-found mapping; parse rejections; install bound (all 7 vars + escaped DSN in `.env`) vs unbound (nothing) vs mail-election-on-non-mail-app (no state written) vs missing-provider (FK fires, instance rolled back); rebind on a running app (env re-stamped, non-mail lines byte-identical, compose up called) and unbind (vars stripped); API fences (admin 403s, elevation 403, member rebind of own personal allowed), audit on success **and** failure for create/update/delete/test/rebind/install, password never echoed, and a test-send delivered end-to-end through an in-process SMTP sink (plus a refused-connection 502).
- `make check` + `make check-web` green; OpenAPI + TS types regenerated.
- Live-verified in the inner loop against a local SMTP sink: provider add + test-send delivered; Kimai installed bound (container env resolves `MAILER_URL` to the provider DSN) and its password-reset mail delivered with the provider's from address; rebind to None recreates the container onto the null-transport compose defaults (a reset request then degrades silently, app stays up) and rebind back restores the DSN; the audit feed records `mail.provider.create`/`test` and both `app.mail.rebind`s.

## Known gaps & deviations

- **Provider edits/deletes don't re-stamp bound apps.** A bound instance keeps the previously injected values until its next rebind or reinstall; running containers keep theirs until the next recreate. Accepted v1 lag, stated in the spec and the Settings UI ("apps pick up changes the next time they restart or rebind"). An explicit "apply now" action is the deferred answer (`NEXT.md` # Outgoing mail).
- **Provider passwords are plaintext at rest** in the brain's SQLite and in bound instances' `.env` — same status as managed-service credentials, folded into `NEXT.md` # App-secret injection hardening (and the only credential in that bucket that unlocks an external account).
- **Gitea's manifest/compose not updated** — the mechanism flip is recorded in the ledger with the exact `GITEA__mailer__*` mapping for the revisit (Gitea consumes discrete fields, not a DSN, and needs an `ENABLED` guard for the unbound case).
- **No required-mail manifests** (`optional: false` rejected) and no box-default provider; both are deliberate deferrals (`NEXT.md` # Outgoing mail).

## What's next

- Gitea re-import with the `MALMO_MAIL_*` → `GITEA__mailer__*` mapping (ledger entry carries the recipe).
- The `NEXT.md` # Outgoing mail ladder: box-default provider, brain-sent email riding the same registry (the `OUTGOING_MAIL.md` promotion trigger), re-stamp-on-edit as an explicit action.
- At-rest encryption of `mail_providers.password` with the rest of the app-secret hardening bucket.
