# Hosted: close the public login picker

- **Status:** done
- **Date:** 2026-09-09
- **Specs touched:** docs/specs/AUTH.md, docs/specs/ENVIRONMENT.md

## What was done

`GET /api/v1/auth/users` returns **404 on the hosted profile**. On the appliance it is unchanged: still public, still sessionless, still the source of the login picker.

The route lists every account on the box as `{id, username}` and sits in `publicPaths`, so it answers with no session. That is deliberate and correct on the appliance — it draws the user-list login screen before anyone has a credential to present, and `AUTH.md` # Login screen UX accepts that anyone who reaches the dashboard URL sees who lives on the box. The reasoning behind that tradeoff is a perimeter: the appliance dashboard answers on `.local` or a mesh name, so the reader is already inside the house.

A hosted box has no perimeter. The dashboard answers on the public internet at `<box-id>.malmo.network`, so the same payload is a **tenant roster any scanner can read**, and it hands over the exact Linux usernames rather than display names. The box-id labels needed to find it are not secret either — every box holds a Let's Encrypt wildcard, and those are published in certificate transparency logs.

The endpoint had **no caller on hosted**. `App.vue` checks `isHosted() && !currentUser` and redirects to the portal, so `Login.vue` never renders there and never fetches the list. The owner comes back through the portal-to-box SSO handshake, which mints the session itself. So the route was reachable but unused on the one profile where it is exposed.

Changes:

- `internal/api/auth.go` — `authUsers` returns 404 when the profile is hosted, before touching the store. The operation summary now says appliance only.
- `internal/api/auth_hosted_test.go` — `TestHostedAuthUsers_NotFound`: a hosted harness with a member returns 404, and the response body does not contain the username. `TestAuthUsersPublicPicker` in `auth_test.go` is untouched and still asserts the appliance 200, so both profiles are pinned.
- `api/openapi.{json,yaml}` and `web-ui/src/generated/openapi.ts` regenerated for the summary change.
- `web-ui/src/Login.vue` — comments corrected. The file describes itself as public; it is appliance-only in practice.
- `docs/specs/AUTH.md` # Login screen UX — a new "Appliance only" paragraph stating the 404 and why the household tradeoff does not travel to a public host.
- `docs/specs/ENVIRONMENT.md` # Access & files (hosted) — a "No public login picker" bullet alongside the SSH and SMB entries, which are the other two appliance surfaces hosted drops.

**404, not 403.** The route does not exist on that profile, the same shape `ssoLanding` already uses in reverse — it serves 404 on the appliance, where there is no portal. A 403 would confirm the box has a user list worth protecting; a 404 says nothing.

## How it maps to the specs

Realizes the Layer 1 / Layer 2 split in `ENVIRONMENT.md` # Two layers: the auth model is identical across profiles, and only the narrow seams where exposure differs branch on the profile. This is a third such seam next to `/setup` (403 on hosted, `auth.go`) and `/_malmo/sso` (404 on appliance, `sso.go`), and it branches for the same reason both of those do — hosted is public-by-default and the appliance is not.

It does not reopen the `AUTH.md` locked decision on user-list login. That style stays exactly as specified on the appliance, which is the profile it was designed for.

Not an audit call site: this is a pure read, and `CLAUDE.md` scopes auditing to elevation-class mutations.

## Known gaps & deviations

- **Nothing verified on a booted hosted box.** The profile branch is exercised by the API test with a harness resolved to hosted, which is how every other hosted seam in `internal/api` is tested. No QEMU or `CI / Cloud image` run was made, and none asserts this route.
- **The privacy toggle in `AUTH.md` still does not exist.** The spec offers a Settings toggle to swap the picker for a blank username and password form. It is unbuilt, and this change does not build it. On the appliance the roster is still public to anyone who can reach the dashboard, which is what the spec says it should be.
- **The `publicPaths` entry is unchanged, on purpose.** The middleware still lets the route through without a session, because it must on the appliance. The profile check lives in the handler, matching `/setup` and `/_malmo/sso`.
- **Usernames leak by other means on hosted and this does not close those.** An attacker who reaches a login path can still probe names one at a time. This removes bulk enumeration, not enumeration.

## What's next

1. **Decide SSH auth for hosted** before any of it is built. The conversation that produced this change was about adding SSH to hosted boxes, and the open question is password versus key. The recommendation on the table is key-only on hosted, keeping password auth on the appliance where `BUILD.md` # SSH scopes port 22 to RFC1918 and the mesh. It needs a `DECISIONS.md` entry either way, since `ENVIRONMENT.md` # Access & files currently locks hosted SSH to off.
2. **Decide the operator debug path separately from the user toggle.** A per-user switch a tenant can turn off is not fleet access. An SSH certificate authority the hosted image trusts is the shape that does not depend on a customer setting, and it decides whether the image ships a `TrustedUserCAKeys` line.
3. **Build the appliance Device access panel.** `SETTINGS.md` reserves it and `AUTH.md` specifies the flow, but no SSH code exists anywhere: no host-agent operation, no protocol type, no brain endpoint, no UI.
