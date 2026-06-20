#!/usr/bin/env bash
# Build the hosted cloud BOOT-PROOF image (C2, #205): the lean image plus the
# baked control plane, the slim host-agent, the single-NIC networkd config, and
# the serial-driven self-check. The cloud analogue of dev/test-qemu/bootstrap.sh,
# minus swtpm/LUKS/SSH (hosted cuts). dev/cloud/run-cloud-tests.sh calls this,
# then converts the raw to qcow2 and boots it once in QEMU.
#
# Sequence:
#   1. Host preflight (mkosi v22+, qemu, qemu-img, OVMF, docker, go, libpam).
#   2. Build the slim host-agent (`-tags hosted`, #204) + the control-plane image
#      bundle (`make control-plane-images`).
#   3. Stage dev/cloud/test/mkosi.extra/ with the agent + units + drop-in, the
#      image bundle + loader (reused from the medium lane), the control-plane
#      compose, the PAM stack, and the assertions oneshot + script.
#   4. Stage Docker's apt repo (trixie) so docker-ce resolves at build time.
#   5. `mkosi build` from dev/cloud/test/ → .dev/cloud-boot/malmo-cloud.raw.
#
# Idempotent via .dev/cloud-boot/.cloud-boot-ready (versioned content gate).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
TEST_DIR="${REPO_ROOT}/dev/cloud/test"
WORK="${REPO_ROOT}/.dev/cloud-boot"
EXTRA="${TEST_DIR}/mkosi.extra"
PKGMNGR="${TEST_DIR}/mkosi.pkgmngr"
CANARY="${WORK}/.cloud-boot-ready"
CANARY_VERSION="v11"  # bump when staging/mkosi.conf/repart changes require a clean rebuild
IMAGE_OUT="${WORK}/malmo-cloud.raw"

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
    echo "must run as root (mkosi escalates; QEMU+KVM later)" >&2
    exit 1
fi

# Resolve the invoking non-root user for build-artifact ownership.
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

# --- 1. host preflight
missing=()
for tool in mkosi qemu-system-x86_64 qemu-img curl python3 docker; do
    command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done
# host-agent-real is a CGO binary (PAM verify is kept in hosted); needs the headers.
[ -f /usr/include/security/pam_appl.h ] || missing+=("libpam0g-dev (PAM headers for host-agent-real)")
# OVMF (UEFI firmware) — location varies by distro.
OVMF_CODE=""
for cand in /usr/share/OVMF/OVMF_CODE.fd /usr/share/ovmf/OVMF.fd \
            /usr/share/OVMF/OVMF.fd /usr/share/edk2-ovmf/x64/OVMF_CODE.fd; do
    [ -r "$cand" ] && { OVMF_CODE="$cand"; break; }
done
[ -n "$OVMF_CODE" ] || missing+=("ovmf (UEFI firmware — package: ovmf)")
if [ ${#missing[@]} -gt 0 ]; then
    cat >&2 <<EOF
cloud boot-proof preflight: missing tooling
  ${missing[*]}

Install (Ubuntu/Debian):
  sudo apt-get install -y qemu-system-x86 ovmf curl python3 libpam0g-dev
  sudo apt-get install -y pipx && pipx install mkosi   # need v22+; ensure ~/.local/bin on PATH

After installing, re-run \`sudo make test-cloud-qemu\`.
EOF
    exit 1
fi

# mkosi version sanity (need >=22). Capture full output first so a SIGPIPE from
# head can't abort mkosi under pipefail (same guard as the lean/medium lanes).
mkosi_version_full="$(mkosi --version 2>&1 || true)"
mkosi_version="$(printf '%s\n' "$mkosi_version_full" | head -n1 | awk '{print $NF}' | tr -d v)"
mkosi_major="${mkosi_version%%[!0-9]*}"
if [ -n "$mkosi_major" ] && [ "$mkosi_major" -lt 22 ] 2>/dev/null; then
    echo "mkosi $mkosi_version too old (need >=22). pipx install --upgrade mkosi" >&2
    exit 1
fi

# Resolve go for the slim-agent build.
if [ -z "${GO:-}" ]; then GO="$(command -v go 2>/dev/null || true)"; fi
if [ -z "${GO:-}" ] && [ -n "$CALLER_HOME" ]; then
    for cand in "${CALLER_HOME}/.local/go/bin/go" "/usr/local/go/bin/go"; do
        [ -x "$cand" ] && { GO="$cand"; break; }
    done
fi
[ -n "${GO:-}" ] && [ -x "$GO" ] || { echo "go binary not found (\$GO=${GO:-})" >&2; exit 1; }

mkdir -p "$WORK"
[ -n "$CALLER" ] && chown "$CALLER":"$(id -gn "$CALLER")" "$WORK"

# Idempotency gate (re-runs cheap; mkosi caches incremental rebuilds).
if [ -f "$CANARY" ] && [ "$(cat "$CANARY")" = "$CANARY_VERSION" ] && [ -f "$IMAGE_OUT" ]; then
    echo "cloud boot-proof image already built ($IMAGE_OUT); skipping bootstrap"
    exit 0
fi

# --- 2. build the slim host-agent (-tags hosted, #204) + control-plane bundle.
# CGO on (PAM) + CGO_CFLAGS as the Makefile sets — dynamic against the build
# host's libpam, run on the Debian VM (libpam0g is in the lean base).
HOSTAGENT_BIN="${WORK}/host-agent-real-hosted"
build_go() { # OUT PKG [extra go-build args...]
    local out="$1" pkg="$2"; shift 2
    if [ -n "$CALLER" ]; then
        sudo -u "$CALLER" env CGO_ENABLED=1 CGO_CFLAGS=-D_GNU_SOURCE "$GO" build "$@" -o "$out" "$pkg"
    else
        CGO_ENABLED=1 CGO_CFLAGS=-D_GNU_SOURCE "$GO" build "$@" -o "$out" "$pkg"
    fi
}
build_go "$HOSTAGENT_BIN" "${REPO_ROOT}/cmd/host-agent-real/" -tags hosted

CP_BUNDLE="${REPO_ROOT}/.dev/control-plane"
# Rebuild the control-plane image bundle only when absent (or forced via
# MALMO_REBUILD_CP=1). `make control-plane-images` re-runs the brain (Go) + UI
# (Vue) docker builds, regenerating ~13 GB of BuildKit cache each time — on a
# tight disk that fills it across iterations. The images don't change while
# iterating on the boot harness, so reuse the existing tarballs.
if [ "${MALMO_REBUILD_CP:-0}" = "1" ] || ! ls "$CP_BUNDLE"/malmo-brain.tar "$CP_BUNDLE"/malmo-ui.tar \
        "$CP_BUNDLE"/caddy.tar "$CP_BUNDLE"/docker-socket-proxy.tar >/dev/null 2>&1; then
    echo "building + saving control-plane image bundle (docker)..."
    make -C "$REPO_ROOT" control-plane-images
else
    echo "reusing existing control-plane image bundle (set MALMO_REBUILD_CP=1 to force)"
fi

# --- 3. stage mkosi.extra/
rm -rf "$EXTRA"
mkdir -p "$EXTRA/etc/systemd/system/host-agent.service.d" \
         "$EXTRA/usr/lib/malmo" \
         "$EXTRA/usr/local/bin" \
         "$EXTRA/etc/pam.d" \
         "$EXTRA/var/lib/malmo/control-plane-images" \
         "$EXTRA/var/lib/malmo/control-plane"

# Slim host-agent at the production path host-agent.service ExecStarts.
cp "$HOSTAGENT_BIN" "$EXTRA/usr/lib/malmo/host-agent-real"
chmod 0755 "$EXTRA/usr/lib/malmo/host-agent-real"
cp "${REPO_ROOT}/dist/systemd/host-agent.service" "$EXTRA/etc/systemd/system/"

# host-agent bootstrap drop-in: point the brain bootstrap at the baked dev-tagged
# images + tarballs + the staged control-plane dir, and order after the first-boot
# image load so every image is present when the bootstrap runs. The brain reads
# /etc/malmo/profile (mounted from the host by host-agent — brainlaunch
# ProfileMarkerPath) to resolve profile=hosted; no MALMO_PROFILE_* env needed.
cat > "$EXTRA/etc/systemd/system/host-agent.service.d/10-cloud-brain.conf" <<'EOF'
[Unit]
After=malmo-load-images.service

[Service]
Environment=MALMO_BRAIN_IMAGE=malmo-brain:dev
Environment=MALMO_BRAIN_IMAGE_TAR=/var/lib/malmo/control-plane-images/malmo-brain.tar
Environment=MALMO_PROXY_IMAGE=tecnativa/docker-socket-proxy:v0.4.2
Environment=MALMO_PROXY_IMAGE_TAR=/var/lib/malmo/control-plane-images/docker-socket-proxy.tar
Environment=MALMO_CONTROL_PLANE_DIR=/var/lib/malmo/control-plane
Environment=MALMO_DASHBOARD_UI_UPSTREAM=malmo-ui:80
EOF

# PAM stack for host-agent-real's verify-password (kept in hosted). Without it
# pam_start("malmo") falls back to /etc/pam.d/other (deny). The malmo group is
# provisioned by the postinst.
cp "${REPO_ROOT}/dev/pam/malmo" "$EXTRA/etc/pam.d/malmo"

# Control-plane image bundle + first-boot loader (reused verbatim from the
# medium lane — same offline-first mechanism, TESTING.md # Full-stack control-
# plane integration). The VM is air-gapped, so every image is a local tarball.
cp "$CP_BUNDLE"/*.tar "$EXTRA/var/lib/malmo/control-plane-images/"
cp "${REPO_ROOT}/dev/test-qemu/load-control-plane-images.sh" "$EXTRA/usr/lib/malmo/"
chmod 0755 "$EXTRA/usr/lib/malmo/load-control-plane-images.sh"
cp "${REPO_ROOT}/dev/test-qemu/malmo-load-images.service" "$EXTRA/etc/systemd/system/"

# Control-plane compose + caddy.json staged at the SAME host path the brain
# container sees (same-path bind constraint — socket-proxy-compose-validation.md).
cp "${REPO_ROOT}/dev/control-plane/compose.yml" "$EXTRA/var/lib/malmo/control-plane/"
cp "${REPO_ROOT}/dev/control-plane/caddy.json"   "$EXTRA/var/lib/malmo/control-plane/"

# Serial-driven boot self-check + its oneshot.
cp "${REPO_ROOT}/dev/cloud/cloud-assertions.sh" "$EXTRA/usr/local/bin/cloud-assertions.sh"
chmod 0755 "$EXTRA/usr/local/bin/cloud-assertions.sh"
cp "${TEST_DIR}/malmo-cloud-assertions.service" "$EXTRA/etc/systemd/system/"

# --- 4. Docker apt repo for the build's package manager (trixie pocket — the
# cloud image is Release=trixie). Build-host network only; the VM never apt-installs.
rm -rf "$PKGMNGR"
mkdir -p "$PKGMNGR/etc/apt/keyrings" "$PKGMNGR/etc/apt/sources.list.d"
curl -fsSL https://download.docker.com/linux/debian/gpg -o "$PKGMNGR/etc/apt/keyrings/docker.asc"
chmod a+r "$PKGMNGR/etc/apt/keyrings/docker.asc"
cat > "$PKGMNGR/etc/apt/sources.list.d/docker.list" <<'EOF'
deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian trixie stable
EOF

# --- 5. mkosi build (from dev/cloud/test; Include=.. pulls in the lean base).
echo "building cloud boot-proof image via mkosi (first run takes a few minutes)..."
# Re-own the staged tree + work dir to the caller; mkosi runs as $CALLER and
# auto-sudos for privileged ops. mkosi preserves mode inside the image regardless.
# NOT $CP_BUNDLE: mkosi reads the tarball *copies* staged under $EXTRA, never the
# bundle itself, and it is shared with the medium lane (test-medium-qemu) — leave
# its ownership alone, as that lane does.
if [ -n "$CALLER" ]; then
    chown -R "$CALLER":"$(id -gn "$CALLER")" "$EXTRA" "$WORK" "$PKGMNGR"
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
        "$MKOSI_BIN" --directory "$TEST_DIR" --force build
else
    MKOSI_INTERPRETER="$MKOSI_INTERPRETER" "$MKOSI_BIN" --directory "$TEST_DIR" --force build
fi

# mkosi writes to OutputDirectory=.dev/cloud-boot. Confirm the raw exists.
if [ ! -f "$IMAGE_OUT" ]; then
    for cand in "${WORK}/malmo-cloud.raw" "${WORK}/malmo-cloud"; do
        [ -f "$cand" ] && { ln -sf "$(basename "$cand")" "$IMAGE_OUT" 2>/dev/null || cp "$cand" "$IMAGE_OUT"; break; }
    done
fi
if [ ! -f "$IMAGE_OUT" ]; then
    echo "mkosi build did not produce $IMAGE_OUT" >&2
    ls -la "$WORK" >&2 || true
    exit 1
fi

echo -n "$CANARY_VERSION" > "$CANARY"
[ -n "$CALLER" ] && chown "$CALLER":"$(id -gn "$CALLER")" "$CANARY" "$IMAGE_OUT" 2>/dev/null || true
echo "cloud boot-proof image ready at $IMAGE_OUT"
