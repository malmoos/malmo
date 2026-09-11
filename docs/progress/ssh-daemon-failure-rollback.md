# A failed systemd call puts the SSH host state back

- **Status:** done
- **Date:** 2026-09-11
- **Specs touched:** docs/specs/BRAIN_HOST_PROTOCOL.md

Closes #479. Last of three fixes from the Greptile review on the v0.12.0 release PR (#474), stacked on [appliance-ssh-password-factor.md](appliance-ssh-password-factor.md).

## What was done

`sshaccess.SetAccess` writes in a fixed order: key file, then drop-in, then the systemd call. The first two steps already undid themselves — `snapshotKeys` restores the key file, and `writeDropIn` restores the drop-in when the combined `sshd -t` rejects the render. The third step had nothing. A failed `enable --now`, `disable --now` or `reload` returned an error with both files left as written.

That matters because of what happens on the other side. The brain reads the error as a host failure and rolls back only its SQLite row (`CLAUDE.md` # Brain commits first), so the two sides then disagree about who has a shell. It does not stay a bookkeeping difference either: `readDropIn` treats the rendered file as truth on the next call, so a stale entry is **merged back in** rather than overwritten.

Two outcomes, both now covered by tests:

- **A failed first enable** left the account in the drop-in with its key on disk while the brain recorded nothing. The next account to enable SSH starts the daemon, and the forgotten account comes up with it — access granted by a call about a different user.
- **A failed disable** dropped the entry and the key file while the brain put its row back to enabled, so the dashboard showed access the host was no longer configured for.

`snapshotDropIn` is added alongside `snapshotKeys` and taken before the render is installed. On an `applyDaemon` failure, `undo` restores the drop-in, then the key file, then reconciles the daemon to the enabled set as it was before the call.

**The daemon half of the undo is the part worth arguing for.** Restoring the two files alone would be half an undo: a failed call can still have changed the run state, since `enable --now` starts the unit before a later `reload` in the same sequence can fail. Reconciling is the same full-state convergence `SetAccess` already performs, which is why running it on a path where a systemctl call has just failed is safe rather than reckless — it either works or it is reported. When the undo itself fails, the error says the host could not be put back, matching how `writeDropIn` already reports a failed restore.

## What was tested

- Four tests in `internal/hostagent/sshaccess/sshaccess_test.go`, all using a faked runner where `sshd` validates and `systemctl` fails — the real shape of this failure, where the render is good so both writes commit and only the last step refuses.
- A failed first enable leaves no key file and no drop-in. A failed disable keeps both. A drop-in restore that itself fails still gets the key file restored, and leaves the daemon alone; a key restore that fails leaves the daemon alone too, which is the case that decides whether a rejected key goes live. That last test was wrong on its first attempt — the injector reset its own marker when the undo reloaded a second time, so it passed against the unfixed gate. Both it and the others were re-run against the unfixed code and fail there. The undo reconciles the daemon rather than only the files, shown with a runner that fails the enable alone so the undo's own `systemctl disable` can be observed. A failed undo is reported in the error text.
- The first two were run against the unfixed code and fail there, the disable case showing `DenyUsers *` where the account should still be.
- `make check` green.

**Two things the review of this change corrected.** The drop-in snapshot was first taken between the key write and the render; it is now taken before either write, so a snapshot that fails costs nothing — reading it after `writeKeys` would have left a new key file live with no restore to undo it, which on a key replacement silently changes who can authenticate. And `undo` returned at the first restore error, though the two files are separate paths: it now attempts both and joins the errors, and reconciles the daemon **only if both files actually went back**, since reloading against a file that could not be restored would put the failed change into effect rather than undo it. A second review pass caught that gating on the drop-in alone was not enough: a restored drop-in points at the very path a failed key restore has left holding the new key set, so a reload would make keys authenticate that the call had already reported as failed.

## Known gaps & deviations

- **Nothing here ran against real systemd.** The runner is faked, so this proves the ordering and the undo, not that `systemctl reload ssh` behaves as assumed on a box. The `ssh` cloud boot from [ssh-in-the-images.md](ssh-in-the-images.md) exercises the success path on a real daemon; no lane fails a systemd call on purpose.
- **The undo is best-effort by construction.** If the reconcile fails the host is left inconsistent and the only remedy is the error message. A reconcile loop reading `GET /v1/ssh/state` would repair it, and still does not exist — the gap [ssh-per-account-access.md](ssh-per-account-access.md) named and this does not close.
- **A crash, rather than an error, still drifts.** If host-agent dies between the render and the daemon call, nothing runs the undo. Converging from `GET /v1/ssh/state` is the only real answer to that, and it is the same missing loop.
- **The snapshot reordering is not covered by a test, and cannot be at this layer.** Reaching it needs `readDropIn` to succeed while `snapshotDropIn` fails on the same path a moment later — a race or a transient I/O error. `readDropIn` runs first and fails on anything unreadable, so any test that forces the failure stops there and would pass without the reorder too. The change is kept because it is strictly better and free, not because it is proved.
- The half-failed undo *is* covered, both ways round, by injecting the failure through the runner: the systemctl call that fails also removes the drop-in's directory, or the keys directory. Contrived, and the only way to reach those branches from a unit test.
- The brain is untouched. Its rollback was already correct; what was missing was the host keeping its end.

## What's next

1. The reconcile loop on `GET /v1/ssh/state`, which turns all three gaps above into one mechanism and is now named by three separate entries.
2. Re-cut v0.12.0 once this and the two fixes below it are on `dev`.
