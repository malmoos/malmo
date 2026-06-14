# Owner-visible app secrets — `show: true` + the reveal endpoint

- **Status:** done
- **Date:** 2026-06-13
- **Specs touched:** `APP_MANIFEST.md` (# D2 — `show` flag + example, locked decisions), `SERVICE_PROVISIONING.md` (# Env-var injection — owner-visible paragraph, locked decisions), `DASHBOARD.md` (# Installed apps — Setup secrets surface), `DECISIONS.md` 2026-06-13

Closes #152. The brain already generated and injected per-app secrets (`MALMO_SECRET_<NAME>`), but had no way to *show* one to the owner — so a self-authenticating app (login gated by a token rather than malmo's session) had to ship a **published constant** as its bootstrap token (Jupyter's `molma-setup`, #124/#136), whose self-grepping disable gate fails *open* into a permanent LAN backdoor on a future image bump. This adds the missing molma-side capability: a manifest can mark a secret owner-visible, and the owner reads its value from the app detail page — so the bootstrap token can be per-instance random instead of public. The Jupyter manifest migration off `molma-setup` is a deliberate follow-up PR (see Known gaps).

## What was done

### Manifest (`internal/manifest/manifest.go`)

- `Secret` gains an optional `Show bool` (`yaml:"show,omitempty"`). `show: true` opts the secret into owner-visibility; omitted (default) keeps it internal. Purely additive — no `manifest_version` bump, back-compatible, and `validateSecrets` is unchanged (the flag needs no normalization). Test: `TestParseSecretShowFlag`.

### Lifecycle (`internal/lifecycle/lifecycle.go`)

- New `RevealSecrets(id)` joins the instance-dir manifest's `show` flags against the stored secret values and returns only the owner-visible `{name, value}` pairs (nil when none declared, short-circuiting before the store read). The manifest read is the installer's persisted instance-dir copy, so a later catalog withdrawal never changes what's revealable. The internal-only filter means a single reveal can't dump every injected credential (a managed-service password stays internal). Tests: `TestRevealSecretsReturnsOnlyOwnerVisible`, `…EmptyWhenNoneVisible`, `…UnknownInstance`.

### API (`internal/api/appsecrets.go`, `api.go`)

- `GET /api/v1/apps/{id}/secrets` (`get-app-secrets`) behind `authorizeAppMutation` — the **same owner-or-admin control-authorization gate as stop/start**: a member reads their own personal app's secret, a household app is admin-only, another user's personal app 404s (existence leak guard). DTOs `AppSecretDTO{name,value}` / `AppSecretsDTO{secrets[]}`; a host/manifest read failure maps to 500, not a misleading empty 200. Revealing is a **pure read, so it does not audit** (only elevation-class mutations audit, per the house rule). Registered via `registerAppSecrets` in `registerAll`. Tests (`appsecrets_test.go`): 401 (unauth via the full router + direct handler), owner-sees-only-visible, admin-sees-any, other-member 404, member-household 403, reveal-error 500 — handler + registration at 100%, `RevealSecrets` at 94% (the lone gap is the defensive store-error wrap).

### Web UI (`web-ui/src/views/settings/InstalledAppDetailSection.vue`, `api.ts`)

- A **Setup secrets** section on the app detail page, shown only when `canControl` and the app declares an owner-visible secret (the list comes back empty otherwise). Each secret is **masked by default** (`••••••••••••`) with a per-secret **Reveal/Hide** toggle and a **Copy** button. Copy is best-effort: `navigator.clipboard` is unavailable on the HTTP-only `.local` origin, so the value stays `select-all` on screen as the fallback (same constraint `RecoverView.vue` already handles for recovery codes). New `AppSecrets`/`AppSecret` type aliases. OpenAPI spec + generated TS regenerated.

## What's next / Known gaps

- **Jupyter migration (the motivating follow-up).** With the reveal in place, `catalog/jupyter/compose.yml` should move off the published `molma-setup` constant to a per-instance random `secrets: [{name: setup_token, show: true}]` value — a separate PR per #152's "Done when". Until it lands, the `BUMPING THE IMAGE TAG` note in that compose stays the floor.
- **No first-class "this app authenticates itself" seam.** This issue addressed only the surfacing gap (#152's root-cause half 1); the larger gap — auth hand-rolled in a compose `command:` rather than a manifest declaration — is unaddressed and tracked in `NEXT.md`.
- **At-rest + reveal hardening.** Secret values are plaintext in SQLite and in the `.env` on disk (unchanged by this PR); reveal adds an owner-gated read surface but no new at-rest exposure. Encryption/rotation/delivery hardening stays open in `NEXT.md` # App-secret injection hardening.
- **No install-success surface.** #152 allowed "install success and/or the detail page"; this ships the durable detail-page home only. An install-completion reveal is a possible later enhancement.
- **Drive-by, not part of #152's scope:** re-applied the pre-existing `main` `LiveResources.vue` vue-tsc break fix (hoist the load line into a `loadLine` computed — an inline `.map` over `sample.load` defeats the `v-else` null-narrowing). It is required for this PR's `ci-web` gate (path-filtered to `web-ui/**`) to pass, and is the same fix carried by the in-flight #149/PR #158; whichever of the two merges second resolves the trivial overlap.
