#!/usr/bin/env bash
# Prepare the medium-lane test image (slice 0021).
#
# Sequence:
#   1. Probe host for mkosi, swtpm, qemu-system-x86_64, OVMF firmware.
#      Exit with a clear install pointer if anything is missing — we
#      don't auto-apt-install host packages (these are user decisions).
#   2. Build the storage-verify binary statically.
#   3. Generate a test SSH keypair under .dev/qemu/ if absent.
#   4. Stage mkosi.extra/ with: dist/systemd/ units at their on-target
#      paths, the storage-verify binary at /usr/lib/malmo/, root's
#      authorized_keys, and sshd config drop-in.
#   5. Invoke `mkosi build` (cached after first run).
#
# Idempotent via .dev/qemu/.malmo-medium-ready (versioned content gate,
# same idiom as dev/test-nspawn/bootstrap.sh).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TEST_DIR="${REPO_ROOT}/dev/test-qemu"
WORK="${REPO_ROOT}/.dev/qemu"
EXTRA="${TEST_DIR}/mkosi.extra"
CANARY="${WORK}/.malmo-medium-ready"
CANARY_VERSION="v10"  # bump when mkosi.conf changes require a clean rebuild
IMAGE_OUT="${WORK}/malmo-medium.raw"
SSH_KEY="${WORK}/ssh-key"

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
    echo "must run as root (mkosi + qemu+swtpm need privileged ops later)" >&2
    exit 1
fi

# Resolve invoking non-root user for file ownership of build artifacts.
CALLER="$(logname 2>/dev/null || true)"
if [ -z "$CALLER" ] || [ "$CALLER" = "root" ]; then
    CALLER="${SUDO_USER:-}"
fi
if [ -z "$CALLER" ] || [ "$CALLER" = "root" ]; then
    CALLER=""
fi
CALLER_HOME=""
if [ -n "$CALLER" ]; then
    CALLER_HOME="$(getent passwd "$CALLER" | cut -d: -f6)"
fi

# Sudo strips PATH (secure_path in sudoers); fold the caller's user-local
# bin dir back in so tools like mkosi installed via `pipx`/`pip --user`/
# symlinked into ~/.local/bin/ are visible to the preflight probe below.
if [ -n "$CALLER_HOME" ] && [ -d "$CALLER_HOME/.local/bin" ]; then
    PATH="$CALLER_HOME/.local/bin:$PATH"
fi

# --- 1. host preflight
preflight_missing=()
for tool in mkosi swtpm qemu-system-x86_64 ssh ssh-keygen scp; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        preflight_missing+=("$tool")
    fi
done

# OVMF firmware location varies by distro.
OVMF_CODE=""
for cand in /usr/share/OVMF/OVMF_CODE.fd \
            /usr/share/ovmf/OVMF.fd \
            /usr/share/OVMF/OVMF.fd \
            /usr/share/edk2-ovmf/x64/OVMF_CODE.fd; do
    if [ -r "$cand" ]; then OVMF_CODE="$cand"; break; fi
done
if [ -z "$OVMF_CODE" ]; then
    preflight_missing+=("ovmf (UEFI firmware — install package: ovmf)")
fi

if [ ${#preflight_missing[@]} -gt 0 ]; then
    cat >&2 <<EOF
medium-lane host preflight: missing tooling
  ${preflight_missing[*]}

Install (Ubuntu/Debian):
  sudo apt-get install -y qemu-system-x86 swtpm ovmf openssh-client
  # mkosi v22+: Ubuntu 20.04's apt has v9 (too old). Install via pipx:
  sudo apt-get install -y pipx
  pipx install mkosi
  # ensure ~/.local/bin is on PATH (or /root/.local/bin under sudo)

After installing, re-run \`sudo make test-medium-qemu\`.
EOF
    exit 1
fi

# mkosi version sanity (need >=22 for the config schema we use). Capture
# the whole output first to avoid SIGPIPE killing mkosi when `head -1`
# closes the pipe early (mkosi 27 surfaces this; `set -e` + `pipefail`
# would then abort us with rc=141).
mkosi_version_full="$(mkosi --version 2>&1 || true)"
mkosi_version_line="$(printf '%s\n' "$mkosi_version_full" | head -n1)"
mkosi_version="$(printf '%s' "$mkosi_version_line" | awk '{print $NF}' | tr -d v)"
# Strip a trailing suffix like `~devel`/`-dev`/`rc1` so `-lt` sees a pure int.
mkosi_major="${mkosi_version%%[!0-9]*}"
if [ -n "$mkosi_major" ] && [ "$mkosi_major" -lt 22 ] 2>/dev/null; then
    echo "mkosi version $mkosi_version is too old (need >=22). pipx install --upgrade mkosi" >&2
    exit 1
fi

# Resolve `go` for the static build below.
if [ -z "${GO:-}" ]; then
    GO="$(command -v go 2>/dev/null || true)"
fi
if [ -z "${GO:-}" ] && [ -n "$CALLER_HOME" ]; then
    for cand in "${CALLER_HOME}/.local/go/bin/go" "/usr/local/go/bin/go"; do
        if [ -x "$cand" ]; then GO="$cand"; break; fi
    done
fi
if [ -z "${GO:-}" ] || [ ! -x "$GO" ]; then
    echo "go binary not found (\$GO=${GO:-}, CALLER=${CALLER:-}, PATH=$PATH)" >&2
    exit 1
fi

# Idempotency gate. Re-runs are cheap; mkosi's cache handles incremental
# rebuilds. Bump CANARY_VERSION when a downstream schema change requires
# a full rebuild.
mkdir -p "$WORK"
if [ -n "$CALLER" ]; then
    chown "$CALLER":"$(id -gn "$CALLER")" "$WORK"
fi

if [ -f "$CANARY" ] && [ "$(cat "$CANARY")" = "$CANARY_VERSION" ] \
   && [ -f "$IMAGE_OUT" ]; then
    echo "medium-lane image already built (${IMAGE_OUT}); skipping bootstrap"
    exit 0
fi

# --- 2. build storage-verify statically
VERIFY_BIN="${WORK}/malmo-storage-verify"
if [ -n "$CALLER" ]; then
    sudo -u "$CALLER" env CGO_ENABLED=0 "$GO" build -o "$VERIFY_BIN" \
        "${REPO_ROOT}/cmd/malmo-storage-verify/"
else
    CGO_ENABLED=0 "$GO" build -o "$VERIFY_BIN" \
        "${REPO_ROOT}/cmd/malmo-storage-verify/"
fi

# --- 3. SSH keypair
if [ ! -f "$SSH_KEY" ]; then
    if [ -n "$CALLER" ]; then
        sudo -u "$CALLER" ssh-keygen -t ed25519 -N "" -C "malmo-medium-test" \
            -f "$SSH_KEY"
    else
        ssh-keygen -t ed25519 -N "" -C "malmo-medium-test" -f "$SSH_KEY"
    fi
fi
chmod 0600 "$SSH_KEY"

# --- 4. stage mkosi.extra/
rm -rf "$EXTRA"
mkdir -p "$EXTRA/etc/systemd/system" \
         "$EXTRA/usr/lib/malmo" \
         "$EXTRA/root/.ssh" \
         "$EXTRA/etc/ssh/sshd_config.d" \
         "$EXTRA/usr/local/bin"

# dist/systemd units. Same shape as 0020's staging but installed
# permanently into the image rather than bind-mounted at runtime.
cp "${REPO_ROOT}/dist/systemd/malmo-storage-ready.target"   "$EXTRA/etc/systemd/system/"
cp "${REPO_ROOT}/dist/systemd/malmo-storage-verify.service" "$EXTRA/etc/systemd/system/"
cp "${REPO_ROOT}/dist/systemd/malmo-recovery.target"        "$EXTRA/etc/systemd/system/"

# host-agent.service references /usr/lib/malmo/host-agent-real which we
# don't have in the medium-lane image (the brain stack isn't part of
# this slice). Drop a /bin/true symlink so the unit loads if anything
# inspects it; but DON'T enable the unit — nothing in /etc/systemd/system/
# *.wants/ pulls host-agent.service in.
cp "${REPO_ROOT}/dist/systemd/host-agent.service" "$EXTRA/etc/systemd/system/"
ln -sf /bin/true "$EXTRA/usr/lib/malmo/host-agent-real"

# storage-verify binary.
cp "$VERIFY_BIN" "$EXTRA/usr/lib/malmo/malmo-storage-verify"
chmod 0755 "$EXTRA/usr/lib/malmo/malmo-storage-verify"

# sshd: allow root key-login, no passwords (test image only).
cat >"$EXTRA/etc/ssh/sshd_config.d/medium-test.conf" <<'EOF'
PermitRootLogin prohibit-password
PasswordAuthentication no
PubkeyAuthentication yes
UseDNS no
EOF

# authorized_keys for root.
cp "${SSH_KEY}.pub" "$EXTRA/root/.ssh/authorized_keys"
chmod 0700 "$EXTRA/root/.ssh"
chmod 0600 "$EXTRA/root/.ssh/authorized_keys"

# Assertion script bundled into the image at a stable path. The host
# driver could scp it in at runtime, but baking it means the image is
# self-testable via `mkosi qemu` for debugging.
cp "${TEST_DIR}/medium-assertions.sh" "$EXTRA/usr/local/bin/medium-assertions.sh"
chmod 0755 "$EXTRA/usr/local/bin/medium-assertions.sh"

# --- 5. mkosi build
# Run as caller for cache ownership. mkosi auto-sudos for the privileged
# ops it needs (loopback mount, etc.).
echo "building medium-lane image via mkosi (first run takes a few minutes)..."
cd "$TEST_DIR"
# The staging steps above run as root and leave files owned root:root,
# some at restrictive modes (0700 for /root/.ssh). mkosi runs as $CALLER
# and would fail to traverse them. Re-own the entire staged tree (and
# the work dir mkosi writes into) to the caller. mkosi preserves mode
# inside the image regardless of host ownership.
if [ -n "$CALLER" ]; then
    chown -R "$CALLER":"$(id -gn "$CALLER")" "$EXTRA" "$WORK"
fi
# Resolve absolute path so the nested `sudo -u` invocation finds mkosi
# even though sudo strips PATH (same idiom as $GO above).
MKOSI_BIN="$(command -v mkosi || true)"
if [ -z "$MKOSI_BIN" ]; then
    echo "mkosi disappeared from PATH between preflight and build" >&2
    exit 1
fi

# mkosi's launcher shim runs `python3` from its environment; >=3.10 is
# required. Probe explicitly so we can pass MKOSI_INTERPRETER through to
# the (sudo-stripped) build invocation. Required on focal where system
# python is 3.8 and deadsnakes no longer ships 3.10 (focal EOL); the
# caller's conda/miniconda python typically satisfies the bar.
MKOSI_INTERPRETER=""
for cand in python3.13 python3.12 python3.11 python3.10; do
    if path="$(command -v "$cand" 2>/dev/null)"; then
        MKOSI_INTERPRETER="$path"; break
    fi
done
if [ -z "$MKOSI_INTERPRETER" ] && [ -n "$CALLER_HOME" ]; then
    for cand in "$CALLER_HOME/anaconda3/bin/python3" \
                "$CALLER_HOME/miniconda3/bin/python3" \
                "$CALLER_HOME/.pyenv/shims/python3"; do
        if [ -x "$cand" ] && "$cand" -c \
            'import sys; sys.exit(0 if sys.version_info >= (3,10) else 1)' \
            2>/dev/null; then
            MKOSI_INTERPRETER="$cand"; break
        fi
    done
fi
if [ -z "$MKOSI_INTERPRETER" ]; then
    cat >&2 <<'EOF'
mkosi requires Python >=3.10; none found on PATH or in caller's conda/miniconda.
Install options:
  - jammy/noble: apt-get install python3.10 python3.10-venv
  - focal:       deadsnakes no longer ships 3.10 (focal EOL). Use conda,
                 pyenv, or `uv python install 3.10`.
EOF
    exit 1
fi
echo "mkosi interpreter: $MKOSI_INTERPRETER"

if [ -n "$CALLER" ]; then
    sudo -u "$CALLER" env "MKOSI_INTERPRETER=$MKOSI_INTERPRETER" \
        "$MKOSI_BIN" --force build
else
    MKOSI_INTERPRETER="$MKOSI_INTERPRETER" "$MKOSI_BIN" --force build
fi

# mkosi writes to OutputDirectory=../../.dev/qemu/. Confirm the
# expected artifact exists (filename pattern depends on mkosi version
# and ImageId).
if [ ! -f "$IMAGE_OUT" ]; then
    # mkosi 22+ default extension is .raw; some versions emit
    # malmo-medium.raw or malmo-medium.
    for cand in "${WORK}/malmo-medium.raw" "${WORK}/malmo-medium"; do
        if [ -f "$cand" ]; then
            ln -sf "$(basename "$cand")" "$IMAGE_OUT" 2>/dev/null || \
                cp "$cand" "$IMAGE_OUT"
            break
        fi
    done
fi
if [ ! -f "$IMAGE_OUT" ]; then
    echo "mkosi build did not produce $IMAGE_OUT — check OutputDirectory" >&2
    ls -la "$WORK" >&2 || true
    exit 1
fi

echo -n "$CANARY_VERSION" > "$CANARY"
echo "medium-lane image ready at $IMAGE_OUT"
