# Hosted owners can confirm privileged actions through the portal

- **Status:** done
- **Date:** 2026-09-11
- **Specs touched:** docs/specs/AUTH.md, docs/specs/ENVIRONMENT.md

Closes #469. It picks up the gap [per-account-ssh-real-sshd.md](per-account-ssh-real-sshd.md) named and worked around: that lane had to set the owner's password as host root to get past the re-auth gate, because a real hosted owner could not pass it at all.

## What was done

Destructive admin actions sit behind a five-minute elevation window, and entering it meant re-typing the account password. A hosted owner has no password to type: the portal signs them in and the box gives their PAM account a random one that is generated and thrown away. So on a hosted box every elevation-class action — add a user, change a role, reset a password, the three mail-provider writes, the three SSH writes — was unreachable.

The hosted confirm step is now a second portal round-trip instead of a prompt.

- **The box mints a one-time challenge.** `POST /api/v1/auth/elevate/challenge` (hosted-only, 404 on appliance, session required) returns an opaque 256-bit id with a five-minute expiry, stored in a new `elevation_challenges` table keyed to the user and cascading with them. It is single-use: the row is deleted in the same transaction that reads it, and past-expiry rows are pruned on each mint, so the table stays bounded with no sweeper.
- **The dashboard hands it to the portal.** On a hosted box, when a mutation comes back `elevation_required`, the owner is sent to the portal's open-box route with the current page as the return path and the challenge inside that path's query. There is no prompt for them.
- **The SSO landing redeems it.** `GET /_malmo/sso` now takes an optional `return` path. When that path carries a `confirm` challenge that verifies, the landing marks the session it just minted elevated for the normal window and redirects to the path with the spent challenge stripped out. Without a challenge it signs the owner in and elevates nothing, exactly as before.
- **`GET /me` says who the owner is.** A new `owner` field, set only on hosted and only for the account the handshake created, is how the dashboard picks the confirm step. A box user the owner created does have a password, so they keep the password prompt — this is not a "hosted uses the portal" branch, it is an "the owner has no password" branch.

The appliance is untouched. `/auth/elevate` is unchanged, the dialog is unchanged, and `owner` is omitted from appliance responses so they stay byte-for-byte what they were.

The portal half — forwarding a `return` param through `GET /api/boxes/current/open` — lives in the private cloud repo and is not in this change. Until it lands the box side is inert: a landing with no `return` behaves exactly as it did.

**Proof on a real box.** The cloud lane's `access` boot now drives the whole thing through the real handshake. With the plain owner session a create-user is refused 403; the dashboard mints a challenge; a second real assertion lands with that challenge on its return path; the same create-user then returns 200 and a real PAM account `tester` exists on the host. A third assertion with an off-box return path still lands on `/` with a valid session. Each step needs its own assertion because the box spends a `jti` on first use, so `mkassertion` grew a `-tokens N` flag and the harness delivers three.

## The security questions, answered

The issue asked four, and asked for the reasoning whether or not the answer was "no problem".

- **Does elevating on every SSO landing widen anything? Yes, so it does not happen.** Elevation is set only when the landing carries a valid challenge. A first sign-in, or any plain "open my box" click, arrives unelevated.
- **Cross-site risk — reachable, and it is why the challenge exists.** The portal's open-box route is a plain GET, and its host-only `SameSite=Lax` cookie *is* sent on a top-level cross-site navigation. So a cross-site page can cause the round-trip. The route's own comment reasons that it "reveals nothing a cross-site page could misuse", and that was true while the token only signed you in; it stops being true the moment the same token opens a privileged window. The confirm therefore needs something the attacker cannot supply. That is the challenge: minting one takes a JSON `POST` to the box's own API with the box session, which a cross-origin page cannot make (the content type forces a preflight the brain does not answer), and which it could not read the response of anyway. An attacker who can drive the navigation gets a sign-in, which the victim already had, and no window.
- **Does the forward-auth cookie inherit anything? No.** Elevation is a column on the session row, read only by `Validate` on the dashboard path. `ValidateForwardAuth` resolves the same session for app subdomains, but nothing on that path consults elevation — the verify endpoint answers allow/deny for one app request and forwards an identity header. No elevation-gated route is reachable with the Domain-scoped cookie: the API middleware reads only the host-only session cookie.
- **Replay — the single-use check happens first.** The assertion's own `jti` is spent before the owner is even resolved, so a replayed token never reaches this code. The challenge is spent (row deleted) before `Elevate` is called, so a replayed return URL finds nothing and opens no window. Both replays are audited as `auth.elevate.failure`.

**Minting is owner-only** (from Greptile's review). Any signed-in hosted account could originally mint a challenge, which bought them nothing — the landing refuses a challenge belonging to another user — but it left a write path open to an account that can never use it. The mint now 404s for anyone but the recorded owner, and for a box with no owner recorded, matching how the route already hides itself on the appliance.

One more that the issue did not ask and the code had to answer: a challenge is bound to the user it was minted for, and a challenge minted for another account does not elevate this one. Not reachable in v1 (hosted is owner-only for the handshake), but the check is one comparison and the alternative is a latent bug in the first multi-user hosted box.

## Verification of the issue's claims

All six were re-read off the tree and all six held: the `requireElevated` call sites are the ten named (four in `users.go`, three in `mail.go`, three in `ssh.go`) and nothing else needs the window; `elevate` re-verifies through host-agent and `changeMyPassword` needs the current password, so no path sets a password you do not know; `createSSOOwner` and `adoptSSOOwner` both discard the generated password and no recovery code is issued; installing an app is not elevation-class; the landing did not elevate and always went to `/`; and the assertion is single-use by `jti` with a ~60s mint TTL.

## How it maps to the specs

`AUTH.md` # Roles gains the hosted confirm step beside the password one, with the three rules that make it safe. `ENVIRONMENT.md` # Owner sign-in & seed ingestion — as built gains the landing's two new query params and the `owner` field on `/me`. No locked decision flipped, so no `DECISIONS.md` entry: the five-minute window, the sudo-in-UI pattern, and PAM as the source of truth all stand. What changed is which proof opens the window for one account that has no password to give.

## Known gaps & deviations

- **The portal half is not built.** Until the cloud repo forwards a `return` param, a hosted owner still cannot confirm end to end in production — the box is ready and the lane proves the box side with a harness that plays the portal. This is the one thing that cannot be checked by reading a diff here.
- **The pending action is not resumed.** The confirm is a full-page navigation, so the mutation the user clicked is lost; they land back on the page with the window open and click again. Resuming a destructive write across a redirect would mean replaying it without a fresh click, which is worse.
- **No unit test for the dashboard branch.** There is no test harness for `web-ui/` yet, so `isHosted() && isBoxOwner()` picking the portal path is covered only by the cloud lane's box-side proof and by hand.
- **The `ssh` boot still sets a password as host root.** It creates its own second account and needs several elevations across a long boot; moving it to the confirm step would need one assertion per elevation. The `access` boot is where the confirm step is proved.
- **Elevation is not surfaced in the UI.** The owner has no way to see the window is open or how long is left, on either profile. That predates this change.

## What's next

1. The portal side in the cloud repo: accept and forward a `return` param on `GET /api/boxes/current/open`, refusing anything that is not a relative path, so the round-trip closes.
2. Decide whether a hosted box user the owner creates should be able to reach the box at all today — they can log in with a password and elevate, but nothing in the dashboard creates them on hosted yet.
3. Surface the elevation window in the dashboard (time left, and a way to drop it), which would also make the hosted round-trip legible rather than a mysterious bounce through the portal.
