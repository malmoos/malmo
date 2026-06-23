#!/usr/bin/env bash
# Build the lean hosted cloud-VM image (C1b, #203) and assert it is genuinely
# lean. The analog of dev/test-qemu/bootstrap.sh, minus the QEMU/swtpm/LUKS
# harness — there is no boot proof here (that is #205/C2).
#
# Sequence:
#   1. Preflight: mkosi (>=22), curl, python3 on PATH.
#   2. Stage Docker's apt repo (trixie pocket) + signing key into the
#      auto-detected mkosi.pkgmngr/ tree so docker-ce resolves at build time.
#   3. `mkosi build` → a raw GPT disk image under .dev/cloud/.
#   4. Assert the appliance cut list is absent from the package manifest
#      (nftables is intentionally NOT cut — docker-ce hard-Depends on it as
#      its firewall backend; ENVIRONMENT.md # How the profile is realized)
#      and source-sanity-check the committed ExtraTrees marker reads `hosted`
#      (image-delivery verified end-to-end in #189/C2, not here).
#
# Runs unprivileged: mkosi escalates for the disk ops it needs.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CLOUD_DIR="${REPO_ROOT}/dev/cloud"
WORK="${REPO_ROOT}/.dev/cloud"
PKGMNGR="${CLOUD_DIR}/mkosi.pkgmngr"

# --- 1. preflight
missing=()
command -v mkosi   >/dev/null 2>&1 || missing+=("mkosi (pipx install mkosi; need v22+)")
command -v curl    >/dev/null 2>&1 || missing+=("curl")
command -v python3 >/dev/null 2>&1 || missing+=("python3")
if [ ${#missing[@]} -gt 0 ]; then
    cat >&2 <<EOF
cloud-image build preflight: missing tooling
  ${missing[*]}

Install (Ubuntu/Debian):
  sudo apt-get install -y curl python3 pipx
  pipx install mkosi        # need v22+
  # ensure ~/.local/bin is on PATH

After installing, re-run \`make build-cloud-image\`.
EOF
    exit 1
fi

# mkosi version sanity (need >=22 for the config schema we use). Capture the
# whole output first so `head` closing the pipe early can't SIGPIPE mkosi 27
# (pipefail would then abort us with rc=141) — same guard as the test lane.
mkosi_version_full="$(mkosi --version 2>&1 || true)"
mkosi_version="$(printf '%s\n' "$mkosi_version_full" | head -n1 | awk '{print $NF}' | tr -d v)"
mkosi_major="${mkosi_version%%[!0-9]*}"
if [ -n "$mkosi_major" ] && [ "$mkosi_major" -lt 22 ] 2>/dev/null; then
    echo "mkosi version $mkosi_version is too old (need >=22). pipx install --upgrade mkosi" >&2
    exit 1
fi

# --- 2. Docker apt repo for the build's package manager.
# The build host has network; the VM never apt-installs Docker (baked). trixie
# pocket — the cloud image is Release=trixie (the test lane uses bookworm). If
# Docker's trixie pocket proves unavailable at build time, fall back to the
# bookworm pocket and file a follow-up (per #203's Trixie sharp-edge clause).
rm -rf "$PKGMNGR"
mkdir -p "$PKGMNGR/etc/apt/keyrings" "$PKGMNGR/etc/apt/sources.list.d"
curl -fsSL https://download.docker.com/linux/debian/gpg \
    -o "$PKGMNGR/etc/apt/keyrings/docker.asc"
chmod a+r "$PKGMNGR/etc/apt/keyrings/docker.asc"
cat > "$PKGMNGR/etc/apt/sources.list.d/docker.list" <<'EOF'
deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian trixie stable
EOF

# --- 3. build
echo "building hosted cloud image via mkosi (first run takes a few minutes)..."
mkdir -p "$WORK"
cd "$CLOUD_DIR"
mkosi --force build

# --- 4. assert lean
MANIFEST="$(ls -1 "$WORK"/*.manifest 2>/dev/null | head -n1 || true)"
if [ -z "$MANIFEST" ]; then
    echo "no package manifest under $WORK (expected ManifestFormat=json output)" >&2
    ls -la "$WORK" >&2 || true
    exit 1
fi

python3 - "$MANIFEST" <<'PY'
import json, sys

# The appliance LAN/storage machinery that the hosted image must NOT carry
# (ENVIRONMENT.md # How the profile is realized — "absent, not disabled").
cuts = {
    "network-manager", "avahi-daemon", "avahi-utils", "samba",
    "mergerfs", "cryptsetup", "tpm2-tools", "openssh-server",
    # nftables is deliberately NOT cut. docker-ce hard-Depends on it
    # (Depends: ... iptables, nftables) as its firewall backend since
    # Docker 28, so the hosted image — which must run docker — carries it
    # unavoidably. The appliance ships nftables only to LAN-scope SSH/SMB
    # (both dropped here); malmo manages no firewall ruleset of its own in
    # hosted, so the package's presence is docker's, not appliance machinery
    # (#241, ENVIRONMENT.md # How the profile is realized / # Public-by-default).
}
with open(sys.argv[1]) as f:
    data = json.load(f)
names = {p.get("name") for p in data.get("packages", [])}
present = sorted(cuts & names)
if present:
    print(f"LEAN CHECK FAILED — appliance packages present in cloud image: {present}",
          file=sys.stderr)
    sys.exit(1)
print(f"lean check passed — none of {sorted(cuts)} are installed")
PY

# Source-sanity check: verify the committed ExtraTrees source file reads `hosted`
# so a stale or accidentally blanked file fails fast before the next build.
# This does NOT assert delivery into the baked image — ExtraTrees delivery is
# verified end-to-end in #189/C2 (boot proof). Mounting the image here is
# intentionally out of scope for this slice.
MARKER="${CLOUD_DIR}/mkosi.extra/etc/malmo/profile"
if [ "$(tr -d '[:space:]' < "$MARKER")" != "hosted" ]; then
    echo "source-sanity check failed: $MARKER does not read 'hosted'" >&2
    exit 1
fi
echo "source-sanity check passed: ExtraTrees source $MARKER reads 'hosted'"
echo "hosted cloud image built: $(ls -1 "$WORK"/*.raw 2>/dev/null | head -n1 || echo "<see $WORK>")"
