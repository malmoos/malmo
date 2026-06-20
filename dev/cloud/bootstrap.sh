#!/usr/bin/env bash
# Build the hosted cloud-VM image (C1b #203 + C2 #205) and assert it is lean.
#
# C1b landed the lean image *definition* (package cut list + the `hosted`
# marker). C2 turns it into a bootable, control-plane-up image by baking the
# pieces the medium lane bakes, minus everything the hosted profile drops (no
# LUKS/TPM, no NetworkManager/Avahi, no sshd):
#
#   1. Preflight: mkosi (>=22), curl, python3, go, docker, qemu-img on PATH,
#      libpam headers (the hosted host-agent is CGO+PAM, same as appliance).
#   2. Build the slim `-tags hosted` host-agent (#204/C1c).
#   3. Build + `docker save` the control-plane image bundle (`make
#      control-plane-images`) — baked + first-boot docker-loaded, offline-first.
#   4. Stage the generated ExtraTree (mkosi.extra.generated): host-agent unit +
#      binary + env drop-in, the first-boot image loader, the staged
#      control-plane compose, the PAM service, and the in-VM assertion harness.
#   5. Stage Docker's apt repo (trixie pocket) so docker-ce resolves at build
#      time, then `mkosi build` → a raw GPT disk image under .dev/cloud/.
#   6. Assert the appliance cut list is absent + the committed marker reads
#      `hosted`, and convert the raw image to the qcow2 cloud artifact
#      (BUILD.md # 6).
#
# Runs UNPRIVILEGED (mkosi escalates for the disk ops it needs); the QEMU boot
# proof that consumes the image is dev/cloud/run-cloud-tests.sh (root, KVM).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CLOUD_DIR="${REPO_ROOT}/dev/cloud"
TESTQEMU_DIR="${REPO_ROOT}/dev/test-qemu"
WORK="${REPO_ROOT}/.dev/cloud"
PKGMNGR="${CLOUD_DIR}/mkosi.pkgmngr"
GENERATED="${CLOUD_DIR}/mkosi.extra.generated"
CP_BUNDLE="${REPO_ROOT}/.dev/control-plane"

# --- 1. preflight
missing=()
command -v mkosi    >/dev/null 2>&1 || missing+=("mkosi (pipx install mkosi; need v22+)")
command -v curl     >/dev/null 2>&1 || missing+=("curl")
command -v python3  >/dev/null 2>&1 || missing+=("python3")
command -v docker   >/dev/null 2>&1 || missing+=("docker (control-plane image bundle)")
command -v qemu-img >/dev/null 2>&1 || missing+=("qemu-img (raw->qcow2 cloud artifact)")

# The hosted host-agent is a CGO binary (PAM verify_password is kept in hosted —
# ENVIRONMENT.md # How the profile is realized), so the build host needs the
# libpam development headers, same as host-agent-real.
if [ ! -f /usr/include/security/pam_appl.h ]; then
    missing+=("libpam0g-dev (PAM headers for the hosted host-agent)")
fi

# Resolve `go` for the host-agent build (Makefile-equivalent fallbacks).
if [ -z "${GO:-}" ]; then
    GO="$(command -v go 2>/dev/null || true)"
fi
if [ -z "${GO:-}" ]; then
    for cand in "$HOME/.local/go/bin/go" "/usr/local/go/bin/go"; do
        if [ -x "$cand" ]; then GO="$cand"; break; fi
    done
fi
[ -n "${GO:-}" ] && [ -x "$GO" ] || missing+=("go (build the hosted host-agent)")

if [ ${#missing[@]} -gt 0 ]; then
    cat >&2 <<EOF
cloud-image build preflight: missing tooling
  ${missing[*]}

Install (Ubuntu/Debian):
  sudo apt-get install -y curl python3 pipx docker.io qemu-utils libpam0g-dev
  pipx install mkosi        # need v22+
  # ensure ~/.local/bin is on PATH and your user is in the docker group

After installing, re-run \`make build-cloud-image\` (or \`make test-cloud-qemu\`).
EOF
    exit 1
fi

# mkosi version sanity (need >=22). Capture the whole output first so `head`
# closing the pipe early can't SIGPIPE mkosi 27 (pipefail would then abort us).
mkosi_version_full="$(mkosi --version 2>&1 || true)"
mkosi_version="$(printf '%s\n' "$mkosi_version_full" | head -n1 | awk '{print $NF}' | tr -d v)"
mkosi_major="${mkosi_version%%[!0-9]*}"
if [ -n "$mkosi_major" ] && [ "$mkosi_major" -lt 22 ] 2>/dev/null; then
    echo "mkosi version $mkosi_version is too old (need >=22). pipx install --upgrade mkosi" >&2
    exit 1
fi

mkdir -p "$WORK"

# --- 2. build the slim hosted host-agent (#204/C1c).
# CGO on (PAM) + CGO_CFLAGS as the Makefile sets — a dynamic binary linking the
# build host's libpam/glibc, run on the Debian VM (which carries libpam0g). The
# `-tags hosted` build compiles out the LAN/discovery stack (no NetworkManager,
# no Avahi, no watcher) — ENVIRONMENT.md # How the profile is realized.
echo "building the slim hosted host-agent (-tags hosted)..."
HOSTAGENT_BIN="${WORK}/host-agent-real-hosted"
CGO_ENABLED=1 CGO_CFLAGS=-D_GNU_SOURCE "$GO" build -tags hosted \
    -o "$HOSTAGENT_BIN" "${REPO_ROOT}/cmd/host-agent-real/"

# --- 3. control-plane image bundle (#163, reused as-is).
# Build + `docker save` malmo-brain, malmo-ui, caddy, docker-socket-proxy into
# .dev/control-plane/*.tar. The VM has no network (the boot proof air-gaps it),
# so every image the brain's compose references must be a local tarball.
echo "building + saving the control-plane image bundle (docker)..."
make -C "$REPO_ROOT" control-plane-images

# --- 4. stage the generated ExtraTree.
# Kept SEPARATE from the committed mkosi.extra/ (which holds only the static
# `hosted` marker) so the marker stays clean in git; mkosi overlays both.
rm -rf "$GENERATED"
mkdir -p "$GENERATED/etc/systemd/system/host-agent.service.d" \
         "$GENERATED/usr/lib/malmo" \
         "$GENERATED/usr/local/bin" \
         "$GENERATED/etc/pam.d" \
         "$GENERATED/var/lib/malmo/control-plane-images" \
         "$GENERATED/var/lib/malmo/control-plane"

# host-agent.service runs the real host-agent (#164/#165): it binds the agent
# socket and, after Docker is ready, seeds the brain's Docker transport (ingress
# network + socket-proxy) and launches the brain container, which reconciles
# Caddy + malmo-ui from the staged control-plane compose. The binary is the slim
# hosted build from step 2.
cp "${REPO_ROOT}/dist/systemd/host-agent.service" "$GENERATED/etc/systemd/system/"
cp "$HOSTAGENT_BIN" "$GENERATED/usr/lib/malmo/host-agent-real"
chmod 0755 "$GENERATED/usr/lib/malmo/host-agent-real"

# Brain bootstrap drop-in: the dev-tagged images + their baked tarballs, the
# staged control-plane dir, and the malmo-ui dial target. Ordered after the
# first-boot image load so every image is present when the bootstrap runs.
# MALMO_OFFLINE_INSTALL marks the registry-less box. No MALMO_CATALOG_DIR: C2
# does not install apps (that is C5/#209), so the brain stays catalog-less.
cat > "$GENERATED/etc/systemd/system/host-agent.service.d/10-malmo-brain-image.conf" <<'EOF'
[Unit]
After=malmo-load-images.service

[Service]
Environment=MALMO_BRAIN_IMAGE=malmo-brain:dev
Environment=MALMO_BRAIN_IMAGE_TAR=/var/lib/malmo/control-plane-images/malmo-brain.tar
Environment=MALMO_PROXY_IMAGE=tecnativa/docker-socket-proxy:v0.4.2
Environment=MALMO_PROXY_IMAGE_TAR=/var/lib/malmo/control-plane-images/docker-socket-proxy.tar
Environment=MALMO_CONTROL_PLANE_DIR=/var/lib/malmo/control-plane
Environment=MALMO_DASHBOARD_UI_UPSTREAM=malmo-ui:80
Environment=MALMO_OFFLINE_INSTALL=1
EOF

# First-boot docker-load oneshot + its script (reused as-is from the medium
# lane). The unit is run-once (ConditionPathExists on a marker); the postinst
# wires its .wants symlink.
cp "${TESTQEMU_DIR}/malmo-load-images.service"     "$GENERATED/etc/systemd/system/"
cp "${TESTQEMU_DIR}/load-control-plane-images.sh"  "$GENERATED/usr/lib/malmo/load-control-plane-images.sh"
chmod 0755 "$GENERATED/usr/lib/malmo/load-control-plane-images.sh"

# The control-plane image tarballs, docker-loaded at first boot.
cp "$CP_BUNDLE"/*.tar "$GENERATED/var/lib/malmo/control-plane-images/"

# The control-plane compose + caddy.json the brain runs `docker compose up` on.
# Staged at the SAME host path the brain container sees (the daemon resolves
# compose bind sources as host paths — socket-proxy-compose-validation.md).
cp "${REPO_ROOT}/dev/control-plane/compose.yml" "$GENERATED/var/lib/malmo/control-plane/"
cp "${REPO_ROOT}/dev/control-plane/caddy.json"  "$GENERATED/var/lib/malmo/control-plane/"

# PAM service for host-agent's verify_password (real login path; not exercised
# by C2 but baked for the hosted /setup once a seed lands — C3a/#220).
cp "${REPO_ROOT}/dev/pam/malmo" "$GENERATED/etc/pam.d/malmo"

# In-VM boot-proof assertions: the oneshot unit (credential-gated so it only
# fires under run-cloud-tests.sh) + the script it runs.
cp "${CLOUD_DIR}/cloud-assertions.sh"      "$GENERATED/usr/local/bin/cloud-assertions.sh"
chmod 0755 "$GENERATED/usr/local/bin/cloud-assertions.sh"
cp "${CLOUD_DIR}/malmo-cloud-assert.service" "$GENERATED/etc/systemd/system/"

# --- 5. Docker apt repo for the build's package manager.
# The build host has network; the VM never apt-installs Docker (baked). trixie
# pocket — the cloud image is Release=trixie.
rm -rf "$PKGMNGR"
mkdir -p "$PKGMNGR/etc/apt/keyrings" "$PKGMNGR/etc/apt/sources.list.d"
curl -fsSL https://download.docker.com/linux/debian/gpg \
    -o "$PKGMNGR/etc/apt/keyrings/docker.asc"
chmod a+r "$PKGMNGR/etc/apt/keyrings/docker.asc"
cat > "$PKGMNGR/etc/apt/sources.list.d/docker.list" <<'EOF'
deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian trixie stable
EOF

# --- 5b. build
echo "building hosted cloud image via mkosi (first run takes a few minutes)..."
cd "$CLOUD_DIR"
mkosi --force build

# --- 6. assert lean
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
    "nftables",
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

# Source-sanity check: verify the committed ExtraTrees marker still reads `hosted`
# so a stale or accidentally blanked file fails fast before the next build.
MARKER="${CLOUD_DIR}/mkosi.extra/etc/malmo/profile"
if [ "$(tr -d '[:space:]' < "$MARKER")" != "hosted" ]; then
    echo "source-sanity check failed: $MARKER does not read 'hosted'" >&2
    exit 1
fi
echo "source-sanity check passed: ExtraTrees source $MARKER reads 'hosted'"

# --- 7. qcow2 cloud artifact (BUILD.md # 6).
# mkosi emits Format=disk (raw); the cloud VM artifact is qcow2. Versioned
# naming (malmo-vX.Y.Z-amd64.qcow2) is a release-time concern — there is no
# version string yet — so this carries the image id.
RAW="$(ls -1 "$WORK"/*.raw 2>/dev/null | head -n1 || true)"
if [ -z "$RAW" ]; then
    echo "mkosi build did not produce a .raw image under $WORK" >&2
    ls -la "$WORK" >&2 || true
    exit 1
fi
QCOW2="${WORK}/malmo-cloud-amd64.qcow2"
qemu-img convert -O qcow2 "$RAW" "$QCOW2"
echo "hosted cloud image built:"
echo "  raw:   $RAW"
echo "  qcow2: $QCOW2"
