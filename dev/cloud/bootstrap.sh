#!/usr/bin/env bash
# Build the hosted cloud-VM image (C1b #203; first-boot wiring #242) and assert it
# is genuinely lean. This is the image `deploy/hetzner-image/build-and-upload.sh`
# (cloud repo) snapshots and provisioning uploads as MALMO_HETZNER_IMAGE.
#
# C1b shipped this as a lean Debian + docker host with NO malmo wiring — it booted
# to an empty, network-less docker host on a real cloud (#242). This script now
# bakes the first-boot RUNTIME wiring (the same wiring the boot-proof test lane
# validates) so a provisioned box self-bootstraps: networkd brings the NIC up via
# DHCP, the baked control-plane images load, host-agent launches the brain, and the
# seed materializer + /setup gate run. The wiring adds no apt packages, so the lean
# check still passes (it asserts the appliance package cuts against the manifest).
#
# Sequence:
#   1. Preflight: root, mkosi (>=22), curl, python3, docker, go, libpam headers.
#   2. Stage the first-boot wiring into mkosi.extra.wiring/ via the shared
#      dev/cloud/stage-control-plane.sh (slim host-agent + units + control-plane
#      bundle + seed materializer; the SAME staging the test lane runs).
#   3. Stage Docker's apt repo (trixie pocket) into mkosi.pkgmngr/ so docker-ce
#      resolves at build time (build-host network only; the VM never apt-installs).
#   4. `mkosi build` → a raw GPT disk image under .dev/cloud/.
#   5. Assert the appliance cut list is absent from the package manifest and that
#      the committed ExtraTrees marker reads `hosted`.
#
# Needs root: it builds the control-plane image bundle (docker) and chowns build
# artifacts back to the caller; mkosi itself runs as the caller (it auto-escalates
# for the disk ops). `make build-cloud-image` runs it under `sudo -E`.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CLOUD_DIR="${REPO_ROOT}/dev/cloud"
WORK="${REPO_ROOT}/.dev/cloud"
WIRING="${CLOUD_DIR}/mkosi.extra.wiring"
PKGMNGR="${CLOUD_DIR}/mkosi.pkgmngr"
CP_BUNDLE="${REPO_ROOT}/.dev/control-plane"

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
    echo "must run as root (control-plane image build + mkosi disk ops). Use: sudo make build-cloud-image" >&2
    exit 1
fi

# Resolve the invoking non-root user for build-artifact ownership + the unprivileged
# go/mkosi sub-builds (same pattern as the test lane).
CALLER="$(logname 2>/dev/null || true)"
if [ -z "$CALLER" ] || [ "$CALLER" = "root" ]; then CALLER="${SUDO_USER:-}"; fi
if [ "$CALLER" = "root" ]; then CALLER=""; fi
CALLER_HOME=""
[ -n "$CALLER" ] && CALLER_HOME="$(getent passwd "$CALLER" | cut -d: -f6)"
# Sudo strips PATH; fold the caller's ~/.local/bin back in so a pipx-installed
# mkosi is visible to the preflight probe.
if [ -n "$CALLER_HOME" ] && [ -d "$CALLER_HOME/.local/bin" ]; then
    PATH="$CALLER_HOME/.local/bin:$PATH"
fi

# --- 1. preflight
missing=()
for tool in mkosi curl python3 docker; do
    command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done
# host-agent-real is a CGO binary (PAM verify is kept in hosted); needs the headers.
[ -f /usr/include/security/pam_appl.h ] || missing+=("libpam0g-dev (PAM headers for host-agent-real)")
if [ ${#missing[@]} -gt 0 ]; then
    cat >&2 <<EOF
cloud-image build preflight: missing tooling
  ${missing[*]}

Install (Ubuntu/Debian):
  sudo apt-get install -y curl python3 docker.io libpam0g-dev
  sudo apt-get install -y pipx && pipx install mkosi   # need v22+; ensure ~/.local/bin on PATH

After installing, re-run \`sudo make build-cloud-image\`.
EOF
    exit 1
fi

# mkosi version sanity (need >=22 for the config schema we use). Capture the whole
# output first so `head` closing the pipe early can't SIGPIPE mkosi 27 (pipefail
# would then abort us with rc=141) — same guard as the test lane.
mkosi_version_full="$(mkosi --version 2>&1 || true)"
mkosi_version="$(printf '%s\n' "$mkosi_version_full" | head -n1 | awk '{print $NF}' | tr -d v)"
mkosi_major="${mkosi_version%%[!0-9]*}"
if [ -n "$mkosi_major" ] && [ "$mkosi_major" -lt 22 ] 2>/dev/null; then
    echo "mkosi version $mkosi_version is too old (need >=22). pipx install --upgrade mkosi" >&2
    exit 1
fi

# Resolve go for the slim-agent build (sudo strips it from PATH).
if [ -z "${GO:-}" ]; then GO="$(command -v go 2>/dev/null || true)"; fi
if [ -z "${GO:-}" ] && [ -n "$CALLER_HOME" ]; then
    for cand in "${CALLER_HOME}/.local/go/bin/go" "/usr/local/go/bin/go"; do
        [ -x "$cand" ] && { GO="$cand"; break; }
    done
fi
[ -n "${GO:-}" ] && [ -x "$GO" ] || { echo "go binary not found (\$GO=${GO:-})" >&2; exit 1; }

mkdir -p "$WORK"
[ -n "$CALLER" ] && chown "$CALLER":"$(id -gn "$CALLER")" "$WORK"

# --- 2. stage the first-boot wiring (shared with the test lane).
# shellcheck source=dev/cloud/stage-control-plane.sh
. "${CLOUD_DIR}/stage-control-plane.sh"
stage_control_plane

# --- 3. Docker apt repo for the build's package manager.
# The build host has network; the VM never apt-installs Docker (baked). trixie
# pocket — the cloud image is Release=trixie (the test lane uses bookworm).
rm -rf "$PKGMNGR"
mkdir -p "$PKGMNGR/etc/apt/keyrings" "$PKGMNGR/etc/apt/sources.list.d"
curl -fsSL https://download.docker.com/linux/debian/gpg \
    -o "$PKGMNGR/etc/apt/keyrings/docker.asc"
chmod a+r "$PKGMNGR/etc/apt/keyrings/docker.asc"
cat > "$PKGMNGR/etc/apt/sources.list.d/docker.list" <<'EOF'
deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian trixie stable
EOF

# --- 4. build. mkosi runs as the caller (it auto-sudos for privileged disk ops);
# re-own the staged trees first. NOT $CP_BUNDLE (shared with the medium lane; mkosi
# reads the tarball copies under $WIRING, never the bundle itself).
echo "building hosted cloud image via mkosi (first run takes a few minutes)..."
if [ -n "$CALLER" ]; then
    chown -R "$CALLER":"$(id -gn "$CALLER")" "$WIRING" "$WORK" "$PKGMNGR"
fi
MKOSI_BIN="$(command -v mkosi || true)"
[ -n "$MKOSI_BIN" ] || { echo "mkosi disappeared from PATH" >&2; exit 1; }

# mkosi's launcher needs python >=3.10; pass it through the sudo-stripped env.
MKOSI_INTERPRETER=""
for cand in python3.13 python3.12 python3.11 python3.10; do
    if path="$(command -v "$cand" 2>/dev/null)"; then MKOSI_INTERPRETER="$path"; break; fi
done
if [ -z "$MKOSI_INTERPRETER" ] && [ -n "$CALLER_HOME" ]; then
    for cand in "$CALLER_HOME/anaconda3/bin/python3" "$CALLER_HOME/miniconda3/bin/python3" \
                "$CALLER_HOME/.pyenv/shims/python3"; do
        if [ -x "$cand" ] && "$cand" -c 'import sys; sys.exit(0 if sys.version_info >= (3,10) else 1)' 2>/dev/null; then
            MKOSI_INTERPRETER="$cand"; break
        fi
    done
fi
[ -n "$MKOSI_INTERPRETER" ] || { echo "mkosi needs python >=3.10; none found" >&2; exit 1; }

if [ -n "$CALLER" ]; then
    sudo -u "$CALLER" env "MKOSI_INTERPRETER=$MKOSI_INTERPRETER" \
        "$MKOSI_BIN" --directory "$CLOUD_DIR" --force build
else
    MKOSI_INTERPRETER="$MKOSI_INTERPRETER" "$MKOSI_BIN" --directory "$CLOUD_DIR" --force build
fi

# --- 5. assert lean
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
    # nftables: TEMPORARILY removed from the cut list. docker-ce 29.6.0 now
    # HARD-Depends on nftables (Depends: ... iptables, nftables) — Docker 28+
    # moved its firewall backend to nftables. Unlike #237 (a recommends-only
    # path, fixed by WithRecommends=no), a hard dep can't be excluded, and the
    # hosted image must run docker. Tracked for a proper fix/decision in #241.
    # Re-evaluate (pin docker, or accept nftables permanently with an
    # ENVIRONMENT.md update) before this lands. Unblocks cloud#6 CL6 e2e.
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
MARKER="${CLOUD_DIR}/mkosi.extra/etc/malmo/profile"
if [ "$(tr -d '[:space:]' < "$MARKER")" != "hosted" ]; then
    echo "source-sanity check failed: $MARKER does not read 'hosted'" >&2
    exit 1
fi
echo "source-sanity check passed: ExtraTrees source $MARKER reads 'hosted'"

IMAGE_OUT="$(ls -1 "$WORK"/*.raw 2>/dev/null | head -n1 || true)"
[ -n "$CALLER" ] && [ -n "$IMAGE_OUT" ] && \
    chown "$CALLER":"$(id -gn "$CALLER")" "$IMAGE_OUT" 2>/dev/null || true
echo "hosted cloud image built: ${IMAGE_OUT:-<see $WORK>}"
