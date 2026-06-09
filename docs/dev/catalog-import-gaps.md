# Catalog import gaps

An **append-only ledger** of platform gaps surfaced while importing apps with [`authoring-apps-with-an-agent.md`](authoring-apps-with-an-agent.md). When an app *can* be adapted to a Door-1 catalog app but something didn't fully translate — an env var molma can't inject, a URL it can't supply, an auth secret it can't generate — the import agent records it here instead of losing it in a session transcript.

**This is capture, not the design backlog.** Entries here are raw findings. The curated, prioritized design backlog is [`../specs/NEXT.md`](../specs/NEXT.md); a human triages this ledger and *promotes* recurring or real gaps into NEXT.md (deduped and shaped), then back-references the promotion here. Do **not** write NEXT.md from an import run, and do **not** treat an entry here as a decision — it's a "we hit this, capture it" note.

Unlike `docs/progress/` entries (frozen ADR snapshots), this file is **mutable by design**: append new entries, and edit existing ones to advance their `Status:` (`open` → `planned` → `implemented`, or `won't-do`) as each gap is triaged and eventually resolved.

## How to use it

- **Import agents:** append one entry per distinct gap you hit, using the format below. Newest entries at the bottom. Don't edit NEXT.md.
- **Human reviewer (triage):** after an import, scan new entries (`grep "Status: open"` is the worklist — there's deliberately no standing "go check the ledger" task; the `open` entries *are* the to-do list). If a `gap-class` now recurs across apps, or a single gap is severe (`blocks-start`), promote it to a NEXT.md entry (its own design topic, or fold into an existing one) and the catalog-curation conversation, then move the entry's `Status:` to `planned`. When the mechanism ships, flip it to `implemented` so the next person can grep this ledger for every app that was waiting on that gap and revisit them.

**The ledger tracks resolution status; it does not make the decision.** The *design shape* of a `planned`/`implemented` fix lives in NEXT.md and the specs — `Status:` just points there. Keep that boundary: this file says *what gaps exist and where each one stands*; NEXT.md says *what we decided to do about the open ones*.

## Entry format

```
### <gap-class> — <app-id> (<YYYY-MM-DD>)

- **Severity:** cosmetic | degraded | blocks-start
- **Trigger:** the exact env var / directive / assumption that didn't translate
- **What breaks:** one line on the user-visible effect
- **Why molma can't satisfy it (v1):** the missing mechanism
- **Status:** open
```

**Fields:**

- **gap-class** — a short, stable tag so recurrence is greppable. Reuse an existing tag when the gap is the same mechanism (`secret-injection`, `app-url-injection`, …); coin a new one only for a genuinely new mechanism. Same tag across apps is the signal that it's worth promoting.
- **Severity** — the load-bearing field for triage:
  - `cosmetic` — a nicety is off; app fully usable.
  - `degraded` — a real feature is broken (OAuth, email links), but the app runs and core use works.
  - `blocks-start` — the app will not boot until a human sets something the platform should have supplied. This is also a **curation** signal (`NEXT.md` # Store catalog curation policy): a `blocks-start` app may not be shippable as-is.
- **Status** — where the gap stands. New entries start `open`; only a human moves them on (always with the pointer that justifies the move):
  - `open` — captured, not yet triaged. This is the worklist (`grep "Status: open"`).
  - `planned (<NEXT.md anchor / issue #>)` — promoted; the design topic or issue is the link. The shape is decided *there*, not here.
  - `implemented (<spec section / commit / PR>)` — the mechanism shipped. Leave the entry in place — it's now the record of which apps to revisit and re-import against the new mechanism.
  - `won't-do (<reason>)` — decided against for v1. The reason (or a `DECISIONS.md` pointer) lives on the line.

---

## Entries

### secret-injection — kan (2026-06-05)

- **Severity:** blocks-start
- **Trigger:** `BETTER_AUTH_SECRET` — the Better Auth library requires a 32+ char random secret to sign auth tokens and throws on startup without it.
- **What breaks:** the app will not start until the secret is set in the instance environment.
- **Why molma can't satisfy it (v1):** no per-app secret-generation/injection mechanism exists — there is no `MOLMA_SERVICE_*`-style or manifest field for an app-specific random secret. Affects any app needing a JWT/HMAC signing secret.
- **Status:** implemented (`DECISIONS.md` 2026-06-05; `APP_MANIFEST.md` # D2). Manifest `secrets: [{name}]` → brain generates a CSPRNG value and injects `MOLMA_SECRET_<NAME>`; kan's compose now maps `BETTER_AUTH_SECRET: ${MOLMA_SECRET_AUTH}`. Security hardening tracked open in `NEXT.md` # App-secret injection hardening.

### app-url-injection — kan (2026-06-05)

- **Severity:** degraded
- **Trigger:** `NEXT_PUBLIC_BASE_URL` — Next.js needs its own public URL for auth redirects (OAuth callbacks, password-reset email links).
- **What breaks:** OAuth providers and email links break; credential login (`NEXT_PUBLIC_ALLOW_CREDENTIALS: "true"`) works without it, so basic use is fine. Left empty.
- **Why molma can't satisfy it (v1):** ~~no URL-injection mechanism~~ — **correction:** the brain *does* inject the routed URL as `MOLMA_APP_URL` (`internal/lifecycle` writeEnv; e.g. `http://kan.local`). The original finding was wrong: this is a manifest-mapping fix, not a missing mechanism.
- **Status:** implemented (no platform change needed). kan's compose now maps `NEXT_PUBLIC_BASE_URL: ${MOLMA_APP_URL}`.

### app-url-injection — docuseal (2026-06-05)

- **Severity:** degraded
- **Trigger:** `FORCE_SSL=${HOST}` — DocuSeal uses the `HOST` env var (the app's public domain) to configure its own base URL for generating links in outbound emails (signature requests, completion notifications, document download links).
- **What breaks:** signature-request emails sent to recipients carry wrong or empty URLs if DocuSeal's Rails stack doesn't infer the public host from Caddy's forwarded headers (`X-Forwarded-Host`). Core app UI is fully reachable; the signing workflow via email links may be broken.
- **Why molma can't satisfy it (v1):** ~~same missing mechanism as `kan`~~ — **correction:** `MOLMA_APP_URL` is injected by the brain (see the kan entry). DocuSeal's compose can map `HOST: ${MOLMA_APP_URL}` (or `FORCE_SSL`) directly. Caddy does forward `X-Forwarded-Host`; whether DocuSeal's Rails config trusts it without explicit `HOST` is app-specific and unverified.
- **Status:** open (manifest-mapping fix, no platform gap — docuseal's compose not yet rewritten to map `MOLMA_APP_URL`; revisit on next docuseal touch)

### oneshot-job-restart — kan (2026-06-05)

- **Severity:** blocks-start
- **Trigger:** `migrate` service with `restart: "no"`, gated by `web`'s `depends_on: {migrate: {condition: service_completed_successfully}}` — the common "run migrations as a one-shot job, then serve" Compose shape.
- **What breaks:** the brain's `compose.override.yml` force-stamps `restart: unless-stopped` onto *every* service (`APP_LIFECYCLE.md` # Locked: override file contents). The migrate job exits 0, Docker restarts it, it never reaches the "completed" terminal state, so `web`'s completion gate never fires and `docker compose up -d` hangs indefinitely — the install transaction wedges (observed: live kan boot hung past a 600s timeout). Surfaced *after* managed-Postgres unblocked kan's database dependency.
- **Why molma can't satisfy it (v1):** the forced-restart rule doesn't distinguish long-running services from one-shot jobs. Fix = honor author-declared terminating policies + completion-gate targets, and bound `compose up -d` so a never-completing gate fails the install instead of hanging.
- **Status:** resolved (#92, 2026-06-05). `writeOverride` now exempts author-declared terminating jobs and completion-gate targets from the forced restart (`main_service` stays always-forced), and `compose up -d` is bounded by the health-wait budget. kan boots end-to-end against managed Postgres (`TestLiveKanBoot` un-skipped + passing). `DECISIONS.md` 2026-06-05, `docs/progress/one-shot-job-restart.md`.

### nonroot-data-ownership — poznote (2026-06-07)

- **Severity:** blocks-start
- **Trigger:** the image does its data writes as a non-root service user — php-fpm's pool is `user = www-data` (`docker/php-fpm/www.conf`) and a reminder worker is `user=www-data` (supervisord) — and its `init.sh` runs `chown -R www-data:www-data /var/www/html/data` (under `set -e`, no `|| true`) to make the data dir writable by that user.
- **What breaks:** the container exits 1 on first boot. `init.sh`'s chown fails (`chown: /var/www/html/data: Operation not permitted`) and `set -e` aborts startup before supervisord runs. And even past that, php-fpm (www-data) cannot create or write the SQLite DB under the data dir, so no notes can be saved. Both confirmed by running `ghcr.io/timothepoznanski/poznote:6` under molma's sandbox (`--cap-drop ALL --security-opt no-new-privileges:true`, root-owned data bind): the init chown fails, and a direct `--user 82:82` write to the root-owned bind returns `Permission denied`.
- **Why molma can't satisfy it (v1):** molma creates the folderless instance data dir (`<instance>/data`) owned by the brain's uid — root in production — at mode `0o755`, and never chowns it (`internal/lifecycle` `writeInstanceDir`; there is no `chown` anywhere in the brain). A folderless Tier-3 app gets no `user:` from the override (`APP_ISOLATION.md` # User content) and runs as the image default under `cap_drop: ALL`. So an app whose writes happen as a non-root user can't write the root-owned dir, and it can't chown the dir itself because `CAP_CHOWN` is stripped. Unlike a single-process app that can just run as root (cf. `runtime-self-patch — jotty`, which escaped this by running `node` as root), Poznote hardcodes www-data across php-fpm and a worker via baked configs; forcing it to root would mean rewriting `www.conf`, adding php-fpm's `-R` flag, and changing the supervisord worker user at container start — config surgery, not adaptation.
- **Status:** open — no `catalog/poznote/` files written. Recurs for any nginx+php-fpm / LinuxServer-style image that drops to a service user for its data writes. To unblock: a mechanism for molma to chown the folderless instance data dir to (and/or run the app as) a declared app uid — a design decision for the maintainer. Reported on #90, assigned to onel.

### managed-mysql — ghost (2026-06-07)

- **Severity:** blocks-start
- **Trigger:** `database__client: mysql` — Ghost requires MySQL 8 in production. Its SQLite path is dev-mode-only (official image docs: SQLite "is not available in production mode because an external MySQL server is required"), and Ghost has no Postgres support at all, so molma's managed Postgres cannot serve it.
- **What breaks:** Ghost will not boot — there is no database molma can give it. Managed services don't offer MySQL, and a MySQL bundled into the app's own compose fails to initialize (see below). The app was bailed, not adapted; no `catalog/ghost/` files were written.
- **Why molma can't satisfy it (v1):** two compounding gaps. **(1)** Managed services Tier 1 provisions **postgres only** — `internal/manifest` allows `{postgres, redis}` and `internal/lifecycle/services.go` provisions postgres; there is no MySQL/MariaDB type. **(2)** The "ship your own DB, adapted to binds" fallback also fails: `writeOverride` stamps `cap_drop: ALL` + `no-new-privileges` on **every** service in the app compose, and the official `mysql:8`/`mariadb` entrypoints need `CAP_CHOWN` to chown `/var/lib/mysql` and `CAP_SETUID`/`SETGID` (gosu) to drop to the `mysql` user on first init — both dropped, and `cap_add` is rejected by admission. Pinning a fixed `user:` doesn't rescue it either: molma only chowns binds to an elected uid for folder-grant apps (and to the *molma* app uid, not mysql's), and `cap_drop: ALL` also removes `DAC_OVERRIDE`, so a datadir the server doesn't own is unwritable. Net beyond Ghost: **any stateful server image that self-chowns its datadir can't be bundled under the Tier-3 sandbox** — such apps need a managed-service type.
- **Status:** implemented (#108, `SERVICE_PROVISIONING.md` # Catalog (v1), `docs/progress/managed-services-mysql.md`). Managed `mysql` (8.0, 8.4) and `mariadb` (10.11, 11.4) Tier-1 types now provision like Postgres — Ghost is re-importable with `services.database: {type: mysql, version: "8.0"}`.

### managed-mysql — kimai (2026-06-07)

- **Severity:** blocks-start
- **Trigger:** `DATABASE_URL` — Kimai is a Symfony/PHP app that requires an external **MySQL/MariaDB** server. Its entrypoint (`kimai/kimai` `.docker/entrypoint.sh`) parses the URL into host/port/user/pass, defaults the port to **3306**, and blocks indefinitely in `waitForDB()` (`until php /dbtest.php ...; sleep 3`) until a MySQL connection succeeds, then runs `kimai:install` (DB migrations). There is no SQLite production mode. Same gap-class first hit by Ghost (#85).
- **What breaks:** a fresh install never starts — the container loops forever waiting for a DB that doesn't exist, so the install transaction wedges (no main service ever becomes healthy).
- **Why molma can't satisfy it (v1):** molma's managed-database mechanism provisions **Postgres** for apps, not MySQL/MariaDB, and there is no manifest field to request a managed MySQL. The Door-1 model is "apps don't bundle their own database" (managed Postgres exists precisely so they don't), and Kimai speaks only the MySQL dialect, so neither path is available. Bundling a `mariadb`/`mysql` service in the catalog compose would also re-hit `nonroot-data-ownership` (the DB image drops to the `mysql` user, which can't write molma's root-owned instance data dir). Secondary, independent of the DB: Kimai's own entrypoint runs as root and `pwconv`/`grpconv`/setuid-drops the web server to `www-data` (needs the stripped `CAP_SETUID`/`CAP_SETGID`/`CAP_CHOWN`) → `nonroot-data-ownership` again.
- **Status:** implemented (#108, `SERVICE_PROVISIONING.md` # Catalog (v1), `docs/progress/managed-services-mysql.md`). Kimai is re-importable against a managed MySQL/MariaDB (`DATABASE_URL` ← `MOLMA_SERVICE_DATABASE_DSN`); the secondary finding stands on its own — Kimai's entrypoint still runs `pwconv`/setuid-drops to www-data, so re-import must re-check it under the sandbox.

### gpu-local-inference — open-webui (2026-06-07)

- **Severity:** degraded
- **Trigger:** upstream's `docker-compose.yaml` bundles an `ollama` service (`image: ollama/ollama`) so the app can run LLMs *on the box*; that's the headline "self-hosted local LLM" use. Local inference is only practical with a GPU.
- **What breaks:** molma can't pass a GPU through to a container yet (#67, GPU runtime wiring, `blocked`), and CPU-only Ollama is impractical for real models — so the bundled-Ollama mode isn't shippable. The catalog app drops the `ollama` service and ships **frontend-only**: Open WebUI is fully functional, but the user must point it at an *external* model backend (an OpenAI-compatible API over the internet, or an Ollama running on another LAN box) via the in-app Connections settings. No on-device inference.
- **Why molma can't satisfy it (v1):** no GPU passthrough mechanism (`gpu: true` is parsed in the manifest schema but the platform override + capacity check are unbuilt — #67). Without it there's no point bundling Ollama. Note: the frontend itself runs cleanly under the Tier-3 sandbox — the image defaults to root (`ARG UID=0`), so it writes molma's root-owned `./data` with no `nonroot-data-ownership` issue (smoke-tested healthy under `cap_drop: ALL` + `no-new-privileges`), and it fetches its RAG embedding model from HuggingFace on first boot (hence `internet: true`, cached in `./data` thereafter).
- **Status:** open — `catalog/open-webui/` shipped frontend-only (closes #74). When GPU passthrough (#67) lands, revisit to offer a bundled-Ollama variant for on-device inference.

### smtp-relay — kimai (2026-06-09)

- **Severity:** degraded
- **Trigger:** `MAILER_URL` — the image defaults it to `null://localhost`, Symfony's discard-everything transport, so all mail is silently dropped until an admin supplies a real SMTP DSN. Second app in this gap-class (see `smtp-relay — ghost`).
- **What breaks:** every email Kimai sends — "forgot password" reset links and any mail an admin enables later (scheduled report delivery, notifications). Silent failure: the UI reports the mail as sent. Workaround inside the app: an admin can reset any user's password from the admin UI, so nobody is permanently locked out. Core time-tracking is unaffected.
- **Why molma can't satisfy it (v1):** same missing mechanism as the ghost entry — molma has no outgoing-mail relay, no `MOLMA_SMTP_*` injection, and no per-app env-override UI through which a user could supply their own provider's credentials post-install.
- **Status:** open
