# A Settings screen for SSH

- **Status:** done
- **Date:** 2026-09-11
- **Specs touched:** docs/specs/AUTH.md, docs/specs/SETTINGS.md

Closes #482. The UI half of the per-account SSH work that [ssh-per-account-access.md](ssh-per-account-access.md) built the brain and host side of, and that [ssh-in-the-images.md](ssh-in-the-images.md) packaged. It was follow-up 1 of #463 and had no issue until now.

## What was done

`web-ui/src/views/settings/SshSection.vue`, routed at `/settings/ssh` and listed in the "You" group of the settings shell under Account. It is open to every signed-in user, because the endpoints it calls sit under `/api/v1/me` and nobody manages anybody else's shell access.

The screen has two switches and a key list. The first switch turns SSH on for the signed-in account. The second is the optional second factor. The key list shows each key's name, fingerprint and date, with a remove button, and the add form takes either a pasted key or a `.pub` file. The file path reads the file in the browser and fills the same box a paste would, so one request shape reaches the server and the server validates once. Nothing about a key is parsed in the browser.

**The profile rule is read, not re-implemented.** `SSHAccessDTO` already carries `key_required`, which is true on hosted and false on the appliance. The screen renders from that flag: on hosted the on/off switch is disabled while the key list is empty and says so, and the password is offered as an extra lock; on the appliance the switch works with no keys and the password shows as always on and not switchable. `AUTH.md` # Device access is explicit that both rows are enforced in the brain and not in the UI, so this screen would be wrong to decide anything — it only describes what the server will do.

**Wording is "an extra lock, not another way in."** The two factors are demanded together; `AuthenticationMethods publickey,password` means neither alone authenticates. Describing the second one as another sign-in method would say the opposite of what it does and would make the hosted posture look optional.

**The hosted box owner cannot use the password lock, and this screen is the only place that knows it.** Their box password is generated during the portal handshake and discarded (`internal/api/sso.go`), so nobody has ever seen it. Turning the lock on would have sshd demand a string the owner cannot type, breaking their SSH with no error anywhere. The option renders disabled for them with a line saying they sign in through the portal and their key is their way in. A box user the owner created does have a password and keeps the option.

The brain does not guard that case and is not the right place to: it cannot tell an owner asking for a second lock from anybody else asking for one, and the fix a guard would force — refusing the field for one account — would be a worse API than a disabled switch and a sentence. The comment in the file says this, so the next person does not read the missing server guard as an oversight.

All three writes go through `withElevation`, because all three are elevation-class server-side. Guard rejections surface as inline messages and no control is ever hidden: the server's own plain-English text is shown for a hosted enable with no key, for removing the only key while SSH is on, for a duplicate key, for a pasted private key and for the key limit. A dismissed confirm prompt reads as a no-op rather than an error, matching `UsersSection.vue`.

## What was tested

- `npx vue-tsc --noEmit` clean.
- Not yet clicked in a browser; `web-ui` still has no test runner, so nothing here is covered by an automated test.

## Known gaps & deviations

- **The screen is named SSH, and the specs called the panel "Device access (SSH + SMB)".** SMB has no API, so a Device access screen would today hold one thing. `SETTINGS.md` # panel inventory and `AUTH.md` # Device access step 1 are updated to say SSH now and to say the SMB toggle joins this screen when file shares ship.
- **No browser run.** The two profiles branch on `key_required` and on box ownership, and neither branch has been seen rendered. The hosted branches in particular need a brain running the hosted profile to observe.
- **Nothing shows the user the command to type.** The screen says they can sign in as their username, but not the box's address, which it does not have to hand. Someone enabling SSH still has to know `ssh <user>@<host>`.
- **No drift surface.** If the host and the brain disagree about who has a shell, this screen shows the brain's row. That is the reconcile loop [ssh-per-account-access.md](ssh-per-account-access.md) named as missing, and it is still missing.
- **SMB is untouched**, so `AUTH.md`'s Device access flow is half-built by design: the SSH steps exist and the SMB ones have no UI.
