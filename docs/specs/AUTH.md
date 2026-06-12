# malmo Authentication & Sessions

> How the malmo dashboard authenticates users, how sessions are managed, and how admin vs. member roles are enforced. Companion to `FIRST_RUN.md`, `MALMO_NETWORK.md`, `CONTROL_PLANE.md`, `SERVICE_PROVISIONING.md`.

## Scope

The malmo session governs **malmo's own surfaces only**:

- The dashboard (install apps, see system status, browse the catalog).
- Settings (network, storage, users, telemetry).
- Tier-2 admin UIs (Tailscale, Samba, DLNA) — served *inside* the dashboard at `/settings/<service>/*`. Same origin as the dashboard, same session.

The malmo session does **not** govern:

- **Tier-3 apps** (`photos.local`, etc.). Each app has its own auth — explicit no-SSO call (`SPEC.md` # Accounts & users). Subdomain isolation is load-bearing for security; the malmo cookie is scoped to the dashboard host and never reaches app subdomains.
- **Device access (SSH + SMB).** Linux PAM + Samba directly. These services authenticate against the same password the user uses for the dashboard (PAM is the source of truth), but the brain's session cookie does not apply to them. Each protocol is opt-in per user — see "Device access (SSH + SMB)" below.

## Identity primitive: password

**One factor in v1: a password.** No passkeys, no TOTP, no email-based recovery (we have no email on file). Password is the floor; everything else is layered later without breaking changes.

**Why not passkeys in v1:**

- Passkeys are origin-bound by design. A passkey on `malmo.local` doesn't work on `cindy-zx9.malmo.network`. With the toggle that flips schemes, users would re-enroll per origin — terrible UX.
- No email = no fallback recovery for a lost passkey. Password recovery still has to exist anyway.
- WebAuthn ceremony + attestation + recovery flows is real complexity for a v1 audience that's tinkerers-then-households.

**Password storage: PAM is the source of truth.** The password lives in `/etc/shadow` (hashed via yescrypt or `pam_argon2`, whichever Debian ships configured), managed by Linux PAM. The brain does not store a password hash in its SQLite — it asks host-agent to verify on every login attempt.

**Why PAM, not brain SQLite:** the same password authenticates the dashboard, SSH, and SMB. PAM is the only credential store all three services can read uniformly. Keeping a copy in brain SQLite would create two sources of truth and a sync problem; routing dashboard verification through PAM keeps it to one. The per-login round-trip is brain → host-agent UNIX socket → PAM `authenticate()` — sub-millisecond, dwarfed by argon2id/yescrypt's deliberate cost.

The host-agent exposes a `verify_password(user, password)` endpoint to the brain. See `BRAIN_HOST_PROTOCOL.md` for the wire surface. The brain still owns sessions, roles, recovery codes — everything except the credential itself.

**Password rules:** minimum 8 characters, no upper bound, no composition rules. Surface the haveibeenpwned k-anonymity API check as a non-blocking warning ("this password has appeared in known breaches") if we have internet during account creation. Don't enforce.

## Sessions: server-side opaque cookie

The brain has a `sessions` table in SQLite. Login mints a row, returns a 256-bit random session ID as a cookie. The cookie is meaningless on its own — the brain looks it up server-side on every authenticated request.

**Cookie shape:**

| Attribute   | Value                                                                 |
|-------------|-----------------------------------------------------------------------|
| Name        | `malmo_session`                                                       |
| Value       | 256 bits of CSPRNG entropy, base64url-encoded                         |
| `HttpOnly`  | yes                                                                   |
| `Secure`    | yes on `.malmo.network`, no on `malmo.local` (HTTP-only there)        |
| `SameSite`  | `Lax`                                                                 |
| `Domain`    | *unset* — scoped to the exact host (`malmo.local`). Critical: do NOT set a broader `Domain` such as `.local`, which would leak the session to every `.local` host on the LAN — all app origins (`<slug>.local`) and any other `.local` device — defeating origin isolation. Apps are single-label siblings (`<slug>.local`), not subdomains of `malmo.local`, so host-scoping keeps the dashboard session off every app origin. |
| `Path`      | `/`                                                                   |

**Why opaque cookies over JWTs:** JWTs win when multiple services need to verify without a roundtrip. We have one backend (the brain). JWTs would just give us non-revocable tokens with bigger payloads. Opaque cookies give us instant server-side revocation (logout, password change, "sign out everywhere"), tiny client cookies, and no JWT-key rotation theater. The DB hit per request is negligible at home-server scale.

**Sessions table:**

```
sessions:
  id              text primary key
  user_id         integer not null
  created_at      timestamp
  expires_at      timestamp     -- absolute hard cap, see "Lifetime"
  last_seen_at    timestamp     -- refreshed on each request
  client_hint     text          -- user agent string, for the "active sessions" UI
  origin          text          -- "local" or "network", informational only
```

**Lifetime:**

- **30-day rolling.** `last_seen_at` updates on every authenticated request; the brain treats the session as valid for 30 days past `last_seen_at`.
- **90-day hard cap** via `expires_at`. Even if the user is constantly active, force re-login at 90 days.
- **Rationale:** home server you barely log into. A 24-hour expiry would feel like Synology — annoying. Browsers and password managers handle a real password gracefully; the long rolling window keeps day-to-day friction at zero.

**Invalidation:**

- Logout: delete the session row.
- "Sign out everywhere" in Settings: delete all rows for the user.
- Password change: delete all rows for the user, force fresh login.
- Admin force-logout member: delete that user's rows.

## Cross-origin behavior (the toggle)

The cookie is scoped to the exact host. So:

- On `malmo.local`, the user has a `malmo_session` cookie for `malmo.local`.
- On `cindy-zx9.malmo.network`, they have a separate `malmo_session` cookie for `cindy-zx9.malmo.network`. Different cookie, different session row server-side.

When the user flips the "Use secure URLs" toggle in Settings → Network (see `MALMO_NETWORK.md`):

1. Brain marks the current session's `origin` as the *old* one.
2. Brain proactively expires all sessions whose `origin` matches the off-mode (so a stale `.local` session doesn't linger after the user committed to `.network`).
3. User is redirected to the new origin's login screen and re-authenticates.

This is the **only** routine cross-origin transition in normal use. Re-auth here is accepted (`DECISIONS.md` 2026-05-14). Browsers and password managers handle it; the user types their password once.

## Login screen UX

**User-list style, like macOS / Plex / Synology DSM.**

The login page lists every account on the box — first name + colored letter glyph (avatars are deferred per `FIRST_RUN.md`). Click your name → password field appears → submit.

**Why a user list, not a username field:**

- Household device. 1–4 users. Set is small and known to people physically near the box.
- "Enter your username" is friction. Users remember first names; they forget exact slugs.
- Matches consumer multi-user OS UX (macOS login, Plex profile picker).

**Tradeoff:** anyone who reaches the dashboard URL sees the user list. Acceptable in the household trust model — the security boundary is "you're authenticated to malmo," not "you don't know who lives here." Tinkerers who want stricter posture can flip a Settings toggle to switch to a blank username + password form.

## Rate limiting

- **Per-username:** exponential backoff after failed attempts. 3 fails → 1s; 5 → 10s; 10 → 60s; 20 → account temporarily locked for 15 minutes.
- **Per-IP:** simple token bucket, 10 attempts per minute. Logs the IP but doesn't ban — most boxes only see LAN IPs.
- **Admin override:** an admin can clear a lock from the user-management UI ("Cindy is locked out, unlock her").
- All failed attempts logged. Audit log surfacing comes later.

This section governs the **login path only** — the deliberately-expensive PAM round-trip is the asset it protects. *General* request throttling for the rest of the public API (per-session and per-IP request-rate buckets, the `429`/`Retry-After` contract, SSE-stream concurrency) lives in `BRAIN_UI_PROTOCOL.md` # Rate limiting & abuse. The two don't overlap: `/login` keeps its stricter per-username backoff here; the general per-IP plane there is the backstop for the *other* unauthenticated routes.

## CSRF

`SameSite=Lax` blocks cross-site POSTs to the dashboard origin. For v1 that's sufficient — we don't accept GET requests for state-changing operations.

When we add an API for third-party tools (NEXT.md Tier 1), we'll either issue an explicit CSRF token at login and require it as a header on writes, or require Origin/Referer checks. Out of scope until then.

## Roles

Two roles in v1 (`FIRST_RUN.md` # Identity):

| Capability                                  | Admin | Member |
|---------------------------------------------|:-----:|:------:|
| Manage users (create/promote/demote/delete) | ✅    | ❌     |
| Configure box (network, storage, telemetry) | ✅    | ❌     |
| Install Tier-2 apps                         | ✅    | ❌     |
| Access Tier-2 admin UIs                     | ✅    | ❌     |
| Install Tier-3 apps as **household** (shared, admin-owned) | ✅ | ❌ |
| Install Tier-3 apps as **personal** (per-user instance)    | ✅ | ✅ |
| Use Tier-3 apps                             | ✅    | ✅ (own + permitted household) |

App instances are **owner-scoped** (`DASHBOARD.md` # the apps model): an admin elects household vs. personal at install; a member can only create personal instances they own. Duplicate installs warn but don't block. This is an authorization rule, enforced server-side like the rest of this table.

**Enforcement:** role is checked **server-side in the brain** on every authenticated request. The UI also hides admin-only sections from members — defense in depth, not the security boundary.

Routes are grouped by role at the router level (e.g., `/api/admin/*` requires admin; `/api/me/*` requires any authenticated user). Hard to introduce a bypass by missing a check on one handler.

**On top of the role check, destructive Settings operations re-prompt for the password** with a 5-minute elevation window per session (sudo-in-UI pattern). Full mechanics in `USERS_AND_GROUPS.md` # Elevation in the UI.

**Enrollment-class operations bypass the elevation window.** Add-drive and eject-drive (`STORAGE.md` # Adding a data drive, # Ejecting a data drive) require a fresh password prompt every time, regardless of recent elevation. These operations extend the box's LUKS keyslot set or remove a physically-attached drive; they're rare, deliberate, and not safely batched. The 5-minute window covers user-management batch work, not enrollment.

**Roles map to Linux groups on the host.** Members are unprivileged Linux users; admins are in the `sudo` group and can `sudo` over SSH for rescue work. See `USERS_AND_GROUPS.md` for the full posture, group reference table, and rescue path.

## Tier-2 admin surface lives in the dashboard

Critical architectural decision — separate doc-section because it shapes auth heavily.

Tier-2 apps (Tailscale, Samba, DLNA) install as **native Debian packages under systemd**, not Docker containers. Their admin UIs are **not exposed at their own subdomain**. Instead, the malmo dashboard surfaces a hand-curated UI for each Tier-2 service at `/settings/<service>/*` (e.g., `/settings/tailscale`, `/settings/shares`).

The brain edits config files (`/etc/samba/smb.conf`) and toggles systemd units (`systemctl restart smbd`) via host-agent. The user never sees the upstream admin UI; they see malmo's UI talking about the same underlying knobs.

**Why this collapses the auth problem:** Tier-2 routes are same-origin as the dashboard. The `malmo_session` cookie just works. No forward-auth, no per-app subdomain, no embedded iframes, no Authelia-style central-login redirect dance.

**Tier-2 vs. Tier-3 in one sentence:** Tier-2 is *malmo's UI for things it manages on the host*; Tier-3 is *third-party apps malmo runs in containers with their own UIs at their own subdomains*. Different shapes, different auth stories.

See `SERVICE_PROVISIONING.md` for the full Tier-2 architecture.

## Password lifecycle

### Setting a password

- **First admin:** Step 2 of first-run sets it (`FIRST_RUN.md`).
- **New member:** admin creates the account from Settings → Users, sets a temporary password, communicates it to the member out of band (verbal, messenger). On first login, the member is forced to change it before they can do anything else.
- **Self-service change:** `Settings → My account → Change password`. Requires current password.

### Forgetting a password

- **Member forgets:** an admin resets it from Settings → Users → Reset password. Generates a temporary; member is forced to change at next login. No email needed — this is a household device, members are physically reachable.
- **Admin forgets:** the recovery code path (below). If there are multiple admins, another admin can also reset.

### The recovery code (admin-only, opt-in)

Admins can opt into a one-time recovery code at account creation. The toggle is **on by default** and labelled as recommended.

**First-run framing (Step 2 of `FIRST_RUN.md`):**

After the admin sets their password, the wizard shows:

> ☑ **Save a recovery code** (Recommended)
>
> *If you forget your dashboard password, this code is the only way back in. Without it, you'd need to reinstall and restore from backup. Take a photo of the code with your phone — it'll back up automatically to your photos, and you'll have it when you need it.*

If the user proceeds, the brain generates a recovery code (24 hexadecimal characters — 12 random bytes, shown raw with no separator mask) and displays it once full-screen with a copy button and explicit "I have saved this" checkbox. The UI shows the code verbatim rather than a `XXXX-XXXX`-style mask: the real code is a continuous hex string, so a mask would reject a pasted code. Hash stored in the brain's SQLite, on the user row. Plaintext is **never persisted** — show-once is the floor.

If the user toggles it off, an explicit confirmation: *"You won't be able to recover your account if you forget your password. Continue without a recovery code?"* Forces acknowledgment of the tradeoff.

**Same flow runs when an admin is added later** (admin creates a second admin account; the second admin sees the recovery-code step on first login).

### Using the recovery code

Login screen has a "Forgot password" link. It asks for the recovery code; the brain validates it against the stored hash in SQLite. On match, the brain serves a **forced** "set new password" screen — no skip, no "I'll do this later." The new password is sent to host-agent → `passwd <user>` + `smbpasswd -a` sync → PAM accepts the new password for dashboard, SSH, and SMB. The brain then invalidates all existing sessions for that user and consumes the old recovery code.

Because the user now has no recovery code (single-use semantics), the next screen **generates and displays a fresh recovery code once**, with the same "I have saved this" checkbox as first-run. Reissue is **mandatory on the recovery path** — unlike first-run, there is no opt-out toggle here: `POST /api/v1/recover` always returns a fresh code, and for the non-technical target audience "recovery stays on" is the safer default (a user can never exit the recovery flow with no way back in). A user who genuinely wants no recovery code declines at first-run, not here.

**Order-of-operations rule:** the brain checks host-agent reachability *before* consuming the recovery code. If host-agent is unreachable (rare — both run on the same box), the password change can't be applied, so the recovery code must survive. Otherwise a single-use code burns without effect.

The user lands on the dashboard with a fresh password and a fresh recovery code. From here, every downstream feature that requires an admin password — including add-drive and eject-drive — works normally; there is no "logged in via recovery code, now wants to do X" carve-out because the recovery-code login terminates in a real password by construction.

### Threat model

- **Lost code, forgotten password = no recovery.** Same as LUKS recovery passphrase semantics. Honest.
- **Phone-photo of code lands in iCloud/Google Photos.** Worth being explicit about in the privacy doc. Threat trade is "I forget my password" (likely) vs. "cloud photo backup is breached AND attacker correlates it to my malmo box AND reaches my box on LAN" (extremely unlikely). For the household audience, convenience wins. Tinkerers who care write it down instead.
- **No physical-access reset.** Box gets stolen → TPM auto-unlocks LUKS → if "physical access = admin reset" were a path, the thief would become admin of a now-decrypted system. Rejected for this reason — see `DECISIONS.md` 2026-05-14.

### Separate from LUKS recovery passphrase

The LUKS recovery passphrase (shown at install, see `STORAGE.md`) recovers **disk decryption** when the TPM seal breaks (motherboard swap, firmware update). The dashboard recovery code recovers **account access**. Different things, different moments. Don't combine them onto one sheet — conflating them confuses threat models.

## Device access (SSH + SMB)

**One password for everything.** Dashboard, SSH, and SMB all authenticate against the same Linux account password — the one the user set at account creation, stored in PAM (`/etc/shadow`). Setting up SSH or mounting an SMB share uses the password the user already knows.

**What's per-protocol is the *access*, not the *credential*.** The password is set when the account is created; what changes when the user opts in is which services accept that password for that account.

**Default posture: services on, accounts off.** sshd and Samba are enabled at boot, but no Linux account can log in to either until explicitly allowed:

- `sshd_config.d/malmo-allowed.conf` carries an `AllowUsers` allowlist. Empty at install — sshd rejects every account by default.
- `smb.conf` carries a `valid users` directive per share. Empty at install — Samba rejects every account.

The password exists in PAM and is valid; the services just don't accept any user yet.

**Flow (per protocol):**

1. Settings → My account → Device access → toggle "Enable SSH" or "Enable file shares (SMB)."
2. Confirm dashboard password (re-auth gate, prevents stolen-session abuse).
3. Optional: paste an SSH public key (preferred for SSH; SMB doesn't use keys).
4. Brain calls host-agent → adds the user to the relevant allowlist (`sshd AllowUsers` and/or Samba `valid users`) → reloads the service. Optionally writes `~/.ssh/authorized_keys`.
5. User can now connect using their existing malmo password.

**Why one password instead of two:**

- Same threat model. SSH and SMB are non-browser access to the same box; the dashboard is browser access. Whatever the user types into a credential field, the security ceiling is the password's strength.
- Realistic user behavior: with two passwords, users reuse the same string anyway. We'd be enforcing a separation that exists only on paper while paying the UX cost of explaining it.
- Fewer credentials to manage means fewer credentials forgotten, written on Post-its, or stored in unsafe places.

**Samba password backend:** Samba historically wants its own password DB (`tdbsam`), which doesn't share storage with `/etc/shadow`. We use Samba's PAM passdb backend (`passdb backend = tdbsam` with `unix password sync = yes` + `pam password change = yes`) so a password change via `passwd` automatically updates Samba. host-agent does the change atomically (`passwd` + Samba sync as one operation) so drift doesn't occur in practice.

**Network scope:** SSH on :22 and SMB on :445 are firewalled to RFC1918 + the mesh interface — see `BUILD.md` # SSH. Both are structurally blocked from the public internet; both work from a paired mesh device. Pair the device to access the box remotely.

## Brain ↔ host-agent in the auth path

For the operations that need host privilege:

- **Dashboard login (every login):** brain → host-agent `verify_password(user, password)` → PAM `authenticate()` → yes/no. The brain mints a session on yes; rate-limits on no. The PAM service name is `malmo`; the stack lives at `/etc/pam.d/malmo`.
- **Password change** (Settings → My account → password, or recovery-code flow): brain → host-agent → `passwd <user>` + Samba sync (one atomic operation).
- **SSH/SMB opt-in toggles:** brain → host-agent → add user to `AllowUsers` / `valid users` allowlist + service reload. Optional `authorized_keys` write.
- **Tier-2 admin operations:** brain → host-agent → edit config, `systemctl` restart.

The brain's session middleware reaches host-agent for *credential verification* on each login (PAM is the credential store, not brain SQLite). After a session is established, role + ACL checks stay inside the brain — no per-request roundtrip. Host-agent trusts the brain because brain ↔ host-agent communication is over a private channel: a UNIX socket whose access is kernel-enforced via group membership. See `BRAIN_HOST_PROTOCOL.md` for the full protocol.

**Test invariant (CI must assert):** the `malmo` group on the running system contains exactly one member — the brain's container runtime UID. Any additional member is a configuration error and fails the test. This is the entire authn/authz model for the brain↔host-agent boundary; if group membership is wrong, the security boundary is broken.

## Sharp edges

- **Cookie isn't shared across the toggle flip.** Re-auth on `.local` ↔ `.network` is the cost. Accepted; happens once per deliberate mode switch.
- **Member's temporary password travels out-of-band.** No email = admin tells the member verbally. For a household, fine. For a future use case (small office), revisit.
- **A "forgotten admin" with no other admin and no recovery code is unrecoverable.** Honest position. Reinstall + restore from off-site backup. The toggle defaults to ON specifically to make this rare.
- **Recovery-code photo backup carries the code into the user's cloud.** Privacy doc covers this honestly.
- **No 2FA / TOTP in v1.** Mentioned to set expectations. Will add post-MVP, designed to coexist with the toggle (TOTP is origin-independent, unlike passkeys).

## Locked decisions

- **Identity primitive: password only in v1.** No passkeys, no TOTP, no email-based recovery.
- **Password storage: PAM (`/etc/shadow`) is the source of truth.** Brain verifies via host-agent's `verify_password`. Brain SQLite carries no password hash.
- **One password for dashboard, SSH, and SMB.** The user has a single malmo password; service access (SSH, SMB) is gated per-protocol via allowlists, not separate credentials.
- **Session shape: server-side opaque cookie**, 256-bit random ID, `HttpOnly`, `SameSite=Lax`, host-scoped (no `Domain` attribute), `Secure` on HTTPS origins.
- **Session lifetime: 30-day rolling, 90-day hard cap.**
- **Login UX: user-list style** with first name + letter glyph. Settings toggle to switch to a blank-form login for privacy-conscious users.
- **Roles enforced server-side in the brain.** UI hiding is defense in depth.
- **Tier-2 admin surface lives in the dashboard at `/settings/<service>/*`.** Same origin, same session, no forward-auth.
- **SSH and SMB are off-by-account-by-default.** Services run; per-user allowlists are empty until the user opts in via Settings.
- **Admin recovery code: opt-in toggle, default on.** Shown once, hashed (stored in brain SQLite), single-use, no physical-access reset path. Validating the code triggers a password change through host-agent → PAM.
- **No SSO into Tier-3 apps.** Locked already in `SPEC.md`; reiterated here.
- **Cross-origin re-auth on toggle flip is accepted.** No session handoff in v1.

## Knock-on to other docs

- `FIRST_RUN.md` — Step 2 adds the recovery-code sub-step. Per-account SSH/SMB opt-in explicitly out of the first-run flow (it's a post-install Settings toggle, not a wizard step).
- `SERVICE_PROVISIONING.md` — Tier-2 implementation locked as "native Debian + systemd; UI in the dashboard." Previously left as "container or host service — implementation detail."
- `CONTROL_PLANE.md` — host-agent scope expands to include Tier-2 systemd/config management, user-credential verification (`verify_password`), and credential mutations (`passwd`, `authorized_keys`, sshd/Samba allowlists).
- `MALMO_NETWORK.md` — "toggle-flip re-auth" sharp edge points here for the concrete mechanism.
- `SPEC.md` — "No malmo SSO into apps" still correct; this doc covers what the malmo session *does* govern.
