# A Settings screen for SSH

- **Status:** done
- **Date:** 2026-09-11
- **Specs touched:** docs/specs/AUTH.md, docs/specs/SETTINGS.md

Closes #482. The UI half of the per-account SSH work that [ssh-per-account-access.md](ssh-per-account-access.md) built the brain and host side of, and that [ssh-in-the-images.md](ssh-in-the-images.md) packaged. It was follow-up 1 of #463 and had no issue until now.

## What was done

`web-ui/src/views/settings/SshSection.vue`, routed at `/settings/ssh` and listed in the settings nav between Notifications and Activity. It is open to every signed-in user, because the endpoints it calls sit under `/api/v1/me` and nobody manages anybody else's shell access.

The screen is one switch and then a pair. The switch turns SSH on for the signed-in account. Under a heading "Authentication methods" sit two cards with the same shape: the malmo password and the public key. Each card has its own switch, and the key card holds the key list and the add form.

**The pair is hidden while SSH is off.** A lock on a door nobody can open is noise. There is one exception, and it is the hosted box with no key yet: the key card is the only way to reach the first key, and the switch above cannot move until that key exists, so the pair shows there even though SSH is off.

**Both methods are one kind of thing, so they look alike.** They are locks this account must pass, and with both on sshd demands both. Drawing the password as a setting and the key as a list would have made them read as a menu of ways in, which is the opposite of what `AuthenticationMethods publickey,password` does. The wording says extra lock, never another way in.

**The profile rule is read, not re-implemented.** `SSHAccessDTO` already carries `key_required`, true on hosted and false on the appliance. The method the box makes mandatory shows an on switch that cannot be moved, and the other switch is live. So on the appliance the password is fixed on and the key is the account's choice, and on hosted it is the other way round. `AUTH.md` # Device access is explicit that both rows are enforced in the brain and not in the UI, so this screen would be wrong to decide anything. It only describes what the server will do, and the server refuses a hosted enable with no key whatever the screen shows.

**The key switch has no field behind it, and should not have one.** A key set of zero is the method being off, so the switch reads from the key count. Turning it on opens the add form, because a method with no key is not on yet, and the switch reads the open form as on: without that the first click would open the form while the switch stayed put. Turning it off means having no keys, so it asks first and then deletes them one call at a time, stopping at the first refusal. There is no "keys kept but ignored" state to fall back to, and a key the user pasted from another machine is not something to throw away silently. On hosted the switch is fixed on whatever the key count says, because the box requires the method, and the card asks for the first key.

**The hosted box owner cannot use the password method, and this screen is the only place that knows it.** Their box password is generated during the portal handshake and discarded (`internal/api/sso.go`), so nobody has ever seen it. Turning the method on would have sshd demand a string the owner cannot type, breaking their SSH with no error anywhere. The switch is disabled for them with a line saying they sign in through the portal. A box user the owner created does have a password and keeps the choice.

The brain does not guard that case and is not the right place to. It cannot tell an owner asking for a second lock from anybody else asking for one, and the fix a guard would force, refusing the field for one account, is a worse API than a disabled switch and a sentence. The comment in the file says this, so the next person does not read the missing server guard as an oversight.

All writes go through `withElevation`, because they are elevation-class server-side. Guard rejections surface as inline messages and no control is ever hidden: the server's own plain-English text is shown for a hosted enable with no key, for removing the only key while SSH is on, for a duplicate key, for a pasted private key and for the key limit. A dismissed confirm prompt reads as a no-op rather than an error, matching `UsersSection.vue`.

## What was tested

- `npx vue-tsc --noEmit` clean.
- Not yet clicked in a browser, and `web-ui` has no test runner, so nothing here is covered by an automated test.

## Known gaps & deviations

- **The screen is named SSH, and the specs called the panel "Device access (SSH + SMB)".** SMB has no API, so a Device access screen would today hold one thing. `SETTINGS.md` # panel inventory and `AUTH.md` # Device access step 1 are updated to say SSH now and to say the SMB toggle joins this screen when file shares ship.
- **The owner rule is a role check, not a check of whether a password exists.** Greptile found this and the review confirmed it. Another admin on a hosted box can set the owner's password through the admin reset route, which is not owner-exempt. After that the owner does have a password they know, and this screen still says they do not and keeps the switch disabled. Nothing on the wire says whether an account has a password anybody has seen, so a real fix needs a new field on the DTO. The cost of leaving it is one optional extra lock the owner cannot turn on, so it is recorded rather than fixed here.
- **The nav puts a My-account item in the System group.** `SETTINGS.md` files Device access under My account, and SSH sits under System, below Notifications. Greptile flagged it. The placement is a product call, and the group already held Notifications, which that same table also files under My account. So the divergence is the nav's, not this screen's, and #290 (restructure Home and Settings) is where the grouping gets settled.
- **No browser run.** The two profiles branch on `key_required` and on box ownership, and neither branch has been seen rendered. The hosted branches need a brain running the hosted profile to observe.
- **Turning the key method off is several deletes, not one call.** There is no bulk endpoint, so a failure part way leaves some keys gone and some kept. The list refetches and the error shows, so the state on screen is true, but the user has to finish the job by hand.
- **Nothing shows the user the command to type.** The screen names their username but not the box's address, which it does not have to hand. Someone enabling SSH still has to know `ssh <user>@<host>`.
- **No drift surface.** If the host and the brain disagree about who has a shell, this screen shows the brain's row. That is the reconcile loop [ssh-per-account-access.md](ssh-per-account-access.md) named as missing, and it is still missing.
- **SMB is untouched**, so `AUTH.md`'s Device access flow is half-built by design: the SSH steps exist and the SMB ones have no UI.
