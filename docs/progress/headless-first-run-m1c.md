# M1c — minimal headless first-run (`/setup` over SSH)

- **Status:** done
- **Date:** 2026-06-15
- **Specs touched:** none — realizes `TESTING.md` # Full-stack control-plane integration (the *headless first-run* + *Real PAM login* rows) and exercises `FIRST_RUN.md` # Identity & display names + `USERS_AND_GROUPS.md` # Roles end-to-end; no spec change.

Closes #166 (M1c, part of #161), the next link after [brain-control-plane-stack.md](brain-control-plane-stack.md) (#165/M1b), which brought the dashboard `/api` leg live through Caddy and explicitly left M1c as "the harness helper + assertion, built directly on this slice." The brain `/setup` handler (brain-commits-first-then-host with rollback) and `host-agent-real`'s `usermgr` (real `useradd`/`chpasswd`/`gpasswd`/`userdel`) were already complete from earlier slices — this change makes that path **exercisable end-to-end on a real VM** by provisioning the box prerequisites it depends on and adding the scriptable, browser-free assertion the QEMU lane drives.

## What was done

The Go control-plane path needed **no change** — `/setup` (`internal/api/auth.go`) and the `usermgr.LinuxUserManager` shell-outs already implement the real first-run. The gap was entirely in the **outer-loop image + harness** (`dev/test-qemu/`): the medium-lane VM had no `malmo` primary group, no `sudo` group, and no `/etc/pam.d/malmo`, so the real `/setup` would 502 (`useradd --gid malmo` / `gpasswd -a … sudo`) and `verify-password` would deny (PAM falling back to `/etc/pam.d/other`).

**Box prerequisites (build time).**

- **`mkosi.conf`** adds the `sudo` package, which provisions the `sudo` group at its canonical GID so `/setup`'s `SetRole` (`gpasswd -a <user> sudo`) has a real group to add the first admin to. Mirrors the nspawn lane, which installs `sudo` for the same reason, and is faithful to a real box (admins hold `sudo` for rescue SSH — `USERS_AND_GROUPS.md` # Roles).
- **`mkosi.postinst.chroot`** creates the `malmo` primary group at GID 3000 (`getent group malmo || groupadd -g 3000 malmo`, idempotent), per `FIRST_RUN.md` # Identity & display names. `usermgr` defaults `PrimaryGroup` to `malmo`, so this group must exist before `useradd --gid malmo` runs.
- **`bootstrap.sh`** stages the canonical `dev/pam/malmo` into the image at `/etc/pam.d/malmo` — the PAM service `host-agent-real`'s `pamverifier` dials (`Service: "malmo"`). Without it `pam_start("malmo")` falls back to `/etc/pam.d/other` (deny) and a correct `/etc/shadow` entry still 401s on `/login`.

**Harness assertion (`medium-assertions.sh`, M1c block).** After the M1b control-plane checks, a new block drives the headless first-run over Caddy on `:80` — the same scriptable HTTP path with no browser. The image carries no curl/jq, so a new `http_post` helper hand-builds the request over bash `/dev/tcp` (the POST analogue of the existing `http_status` GET helper, with `Content-Type`/`Content-Length` and the JSON body):

1. **`POST /api/v1/setup`** — accept `200` on a fresh box or `409` ("setup has already completed") when the disk has already been through first-run (the medium lane reuses one disk across the first-boot and second-boot phases, so the admin and its brain SQLite row persist into boot 2). A `502` (missing group, `useradd`/`chpasswd` error) is polled briefly to ride out brain-not-ready, then surfaced with the response body for diagnosis.
2. **Host-side account checks** — the admin is a real Linux user with primary group `malmo` (proves `SetPassword`'s `useradd --gid malmo`) and a member of `sudo` (proves `SetRole` — the first admin lands in `sudo` at creation).
3. **`POST /api/v1/login`** — a single attempt (no retry, since the brain rate-limits failed logins) must return `200`, proving the account authenticates against `/etc/shadow` via host-agent `verify-password` (PAM `pam_unix`, service `malmo`). This is the M1c "Done when." The brain holds no password hash; the round-trip is the only authentication path.

The block runs in the common (pre-phase) section, so it asserts on both the first-boot (fresh → `200`) and second-boot (disk reuse → `409`, account + shadow survived the encrypted-root reboot) phases.

## How it maps to specs

- `TESTING.md` # Full-stack control-plane integration — the *"real, headless first-run"* row (`/setup` creates the admin through the real PAM/`useradd`/`chpasswd` path, driven over SSH with no interactive UI) and the *"Real PAM login"* row (the first-run admin authenticates against `/etc/shadow` through host-agent `verify-password`, no brain-side password hash) are now both exercised by the medium lane.
- `FIRST_RUN.md` # Identity & display names — the `malmo` primary group at GID 3000 is the box-side half of the slug → Linux-user mapping.
- `USERS_AND_GROUPS.md` # Roles — the first admin is added to `sudo` at creation; the assertion verifies the real group membership.
- Brain-commits-first-then-host (`CLAUDE.md` load-bearing decision) — unchanged; `/setup` already follows it, and the assertion's `409`-on-retry behaviour depends on the brain row being the durable fence.

## Verification

- **`http_post` wire format + status parsing** smoke-tested in isolation against a throwaway local HTTP server: correct POST request line, accurate `Content-Length` (byte length of the ASCII body), body delivered, full response captured via `cat`, and the `*" 200"*`/`*" 409"*` `case` matching confirmed. All three edited shell scripts pass `bash -n`.
- **Brain side already covered** — `/setup` and `/login` handlers (200/409/422/401/502 paths, the rollback ordering) are unit-tested in `internal/api/auth_test.go`; `usermgr`'s `useradd`/`chpasswd`/`gpasswd` shell-outs in `internal/hostagent/usermgr/`. No Go change here, so `make check` is unaffected (run as the pre-PR gate).
- **VM-boot acceptance pending** — the end-to-end "Done when" runs only on a real boot: `sudo make test-medium-qemu` on an mkosi/swtpm/QEMU host, asserting the new `control-plane M1c: /setup created the admin, verify-password authenticated it against /etc/shadow` line on both phases. Not run in this environment (no QEMU/KVM here); it rides the same outstanding `test-medium-qemu` acceptance as the M0/M1a/M1b slices.

## What's next

- **Run the medium lane on a real QEMU host** to confirm the M1c assertion passes on both boot phases — the shared outer-loop acceptance still outstanding for M0/M1a/M1b.
- **M2 (#167): full-stack integration assertions** — app install end-to-end through the now-first-run box, the final link in #161.
- **UID range is the Debian default, not the malmo-reserved 3000+.** This slice provisions the `malmo` *group* at GID 3000 but does not tune `/etc/login.defs` `UID_MIN`, so the test admin gets UID 1000. Faithful UID allocation (`FIRST_RUN.md` # Identity & display names) is a box-build concern that does not affect the verify-password round-trip; left out to keep the change surgical.
- **The full first-run *wizard* (network pick + recovery-passphrase) headless automation is separate** (deferred per #161). This is the *minimal* scriptable `/setup` only; the wizard's headless path is tracked on its own and must coordinate the `/setup` touch-points to avoid collision.
