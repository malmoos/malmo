# Project rename: malmo → molma

## What was done

Project-wide rename from **malmo** to **molma** across all 191 tracked files with text content (~1 524 occurrences). Clean break — no aliases or back-compat shims. The GitHub home is now `github.com/molmaos/molma` (org `molmaos`, repo `molma`).

**Text replacements (sed, three case-aware passes):**

1. Special-case GitHub paths first (before the generic pass, to get the org name right):
   - `github.com/malmo/malmo` → `github.com/molmaos/molma` (Go module + imports)
   - `github.com/onel/malmo` → `github.com/molmaos/molma` (issue-tracker URLs in docs)
2. Generic lowercase: `malmo` → `molma`
3. Generic mixed-case: `Malmo` → `Molma`
4. Generic uppercase: `MALMO` → `MOLMA`

This single sweep covered all categories: Go identifiers (`MolmaAppUID`, `MolmaAppGID`, `MolmaSharedGID`), env vars (`MOLMA_NETWORK`, `MOLMA_AGENT_SOCK`, `MOLMA_DATA_DIR`, …), filesystem paths (`/var/lib/molma`, `/srv/molma`), binary/unit/service names, domains (`molma.local`, `molma.network`), runtime identifiers (session cookie `molma_session`, DB file `molma.db`, systemd drop-in `molma.conf`), Linux groups (`molma`, `molma-admin`), CLI tooling (`molmactl`), and all doc cross-references.

**Tracked paths renamed (git mv, 12 operations covering 14 files):**

| Before | After |
|---|---|
| `cmd/malmo/` | `cmd/molma/` |
| `cmd/malmo-storage-verify/` | `cmd/molma-storage-verify/` |
| `dev/pam/malmo` | `dev/pam/molma` |
| `dev/test-qemu/malmo-tpm-enroll.service` | `dev/test-qemu/molma-tpm-enroll.service` |
| `dev/test-qemu/mkosi.initrd.conf/…/malmo-luks.conf` | `…/molma-luks.conf` |
| `dist/systemd/dropins/avahi-daemon.service.d/malmo.conf` | `molma.conf` |
| `dist/systemd/dropins/docker.service.d/malmo.conf` | `molma.conf` |
| `dist/systemd/dropins/smbd.service.d/malmo.conf` | `molma.conf` |
| `dist/systemd/malmo-recovery.target` | `molma-recovery.target` |
| `dist/systemd/malmo-storage-ready.target` | `molma-storage-ready.target` |
| `dist/systemd/malmo-storage-verify.service` | `molma-storage-verify.service` |
| `docs/specs/MALMO_NETWORK.md` | `docs/specs/MOLMA_NETWORK.md` |

**Verification:**

- `git grep -il malmo` returns zero tracked files (the definitive check, case-insensitive)
- `git grep -n "github.com/molma/molma"` returns nothing (wrong-org guard)
- `git grep -n "github.com/molmaos/molma"` present in `go.mod` and all imports
- `go build` + `go vet` clean on the non-PAM package set (PAM CGO failure is pre-existing, no libpam0g-dev on dev box)
- `go test ./...` (non-PAM/usermgr) all pass under the new `github.com/molmaos/molma` module path

## What's next

- Rename the GitHub repo/org (done outside this repo): confirm `github.com/molmaos/molma` is live and public
- Confirm `molma.network` DNS is registered/pointed
- Update any CI/CD pipeline references once the GitHub rename is live
