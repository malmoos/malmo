# The appliance keeps its mandatory SSH password

- **Status:** done
- **Date:** 2026-09-11
- **Specs touched:** docs/specs/AUTH.md

Closes #477. Second of three fixes from the Greptile review on the v0.12.0 release PR (#474), stacked on [cors-origin-reflection.md](cors-origin-reflection.md).

## What was done

`AUTH.md` # Device access gives each profile a mandatory factor and an optional second one: appliance takes the malmo password with a key as the extra lock, hosted takes a key with the password as the extra lock. Only the hosted row was enforced. On the appliance, an account that added a key and left `require_password` unset got `AuthenticationMethods publickey` — the mandatory factor replaced rather than supplemented.

The field is `omitempty`, so this is the default path a client takes, not an odd one. A panel sending `{"enabled": true}` produced it.

**The fix is in the brain, and that placement is the substance of it.** `internal/hostagent/sshaccess` renders exactly what it is handed, and given a key with `RequirePassword` false its `methods()` correctly writes `publickey` alone. host-agent does not know the environment profile and must not guess which factor is mandatory — [ssh-per-account-access.md](ssh-per-account-access.md) states that division of labour outright. So host-agent was not wrong; the brain was handing it a `false` the appliance is not allowed to ask for.

`setMySSH` now resolves the caller's value against the profile through `effectiveRequirePassword` before anything is written. On the appliance the answer is always true; on hosted the account's choice stands.

**Once, at the single entry point.** `setMySSH` is the only place a caller-supplied `require_password` enters the brain. `reapplySSH`, the `deleteUser` restore and both rollback paths all read the stored row. So normalising before the store write makes every downstream push correct without touching them, and the row stops describing a posture sshd is not running. The audit record uses the effective value too, so Activity says what the box did rather than what was asked.

## What was tested

- Three tests in `internal/api/ssh_test.go`. The appliance sends `require_password: true` to the host and reports it in the DTO after a key is added; the appliance with no key does the same; hosted still honours a `false` and authenticates with the key alone.
- The two appliance tests were run against the unfixed handler and fail there, with the host call showing `RequirePassword:false` next to the key. The hosted test passes either way, which is the point — it is there to prove the fix did not spill across the profile seam.
- `make check` green.

## Known gaps & deviations

- **Nothing is proved against a real sshd.** The assertion stops at what the brain sends host-agent. That the resulting `AuthenticationMethods publickey,password` makes sshd demand both is covered by the `ssh` cloud boot from [ssh-in-the-images.md](ssh-in-the-images.md), on hosted only. No lane exercises the appliance renderer on a booted box.
- **Existing rows are not migrated.** An appliance account that enabled SSH before this change keeps `require_password = false` in the store until it next writes. There are no such boxes — SSH has never shipped in a release and there is no UI to reach it — so a migration would be code with no subject.
- **`require_password` stays in the appliance request body** rather than being rejected there. Sending it is harmless and ignored. A 422 would be stricter but would break a client that sets the field uniformly across profiles, and the DTO's `key_required` already tells a panel which factor is which.
- No UI, so this is still only reachable by API. Unchanged by this fix.

## What's next

1. The third fix from the same review: host-side drift when a daemon call fails.
2. An appliance assertion in the QEMU medium lane that the rendered block is `publickey,password`, which is the only place the appliance renderer meets a real sshd.
