#!/bin/bash
# Hosted first-boot seed materializer (C3a cloud-lane, #220). Baked into the
# boot-proof image at /usr/local/bin/ and run by malmo-seed.service before
# host-agent launches the brain. It lands the provisioning seed delivered as a
# systemd credential (over SMBIOS by dev/cloud/run-cloud-tests.sh) at the
# well-known path the brain reads — /var/lib/malmo/seed.json (profile.DefaultSeedPath,
# overridable with MALMO_SEED_PATH). On a real cloud the same file arrives via
# cloud-init write_files; this is the test-lane analogue (ENVIRONMENT.md #
# Provisioning & first-boot — "both delivery mechanisms materialize that same file").
#
# The credential is optional: an un-seeded boot delivers none, so the brain finds
# no seed and keeps /setup closed (503) rather than falling back to the appliance's
# open-on-empty-box trust. A delivered seed is written 0600 root:root.
#
# Deliberately NO "don't clobber an existing file" guard: the frozen-identity
# reboot scenario re-delivers a DIFFERENT seed on a later boot, and overwriting
# proves the brain ignores it (the box-id is frozen in the brain's SQLite, not on
# this file — cmd/brain loadHostedEnvironment). The file is the brain's input, not
# its memory.
set -uo pipefail

DEST="${MALMO_SEED_PATH:-/var/lib/malmo/seed.json}"
# systemd exports CREDENTIALS_DIRECTORY for a unit with ImportCredential=; the
# delivered seed lands at $CREDENTIALS_DIRECTORY/malmo.seed.
SRC="${CREDENTIALS_DIRECTORY:-}/malmo.seed"

if [ -z "${CREDENTIALS_DIRECTORY:-}" ] || [ ! -f "$SRC" ]; then
    echo "malmo-seed: no seed credential delivered; leaving box unprovisioned" >&2
    exit 0
fi

mkdir -p "$(dirname "$DEST")"
# install writes atomically with the final mode/owner — no window where the seed
# is world-readable. root:root 0600 matches the cloud-init-delivered file.
if ! install -m 0600 -o root -g root "$SRC" "$DEST"; then
    echo "malmo-seed: failed to write $DEST" >&2
    exit 1
fi
echo "malmo-seed: provisioning seed materialized at $DEST" >&2
