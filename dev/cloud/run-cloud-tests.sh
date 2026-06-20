#!/usr/bin/env bash
# Cloud-lane test driver: boot the hosted cloud image once under QEMU and prove
# the control plane comes up (#205/C2). The cloud analogue of
# run-medium-tests.sh, MINUS swtpm, the LUKS recovery credential, the two-boot
# enroll/unseal cycle, and SSH:
#
#   - The disk IS the installed system (ENVIRONMENT.md # Provisioning — no
#     installer), so a single plain boot, no LUKS-on-target.
#   - The hosted image ships no sshd (lean cut list), so assertions run from a
#     baked oneshot (malmo-cloud-assert.service) that writes a PASS/FAIL sentinel
#     to the serial console and powers off. This driver injects the SMBIOS
#     credential that arms that unit, captures the serial log, and reads the
#     verdict from it.
#   - One virtio NIC with restrict=on AIR-GAPS the guest (#167): a missing
#     bundled image hard-fails instead of pulling from a registry, proving the
#     offline control-plane bundle is complete. No SSH hostfwd is needed.
#
# See docs/specs/TESTING.md # Full-stack control-plane integration and
# docs/progress/cloud-image-boot-proof.md.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CLOUD_DIR="${REPO_ROOT}/dev/cloud"
WORK="${REPO_ROOT}/.dev/cloud"

RUN_DIR="$(mktemp -d -t malmo-cloud.XXXXXX)"
QEMU_SERIAL="${RUN_DIR}/serial.log"
QEMU_PID=""

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
    echo "must run as root (QEMU+KVM)" >&2
    exit 1
fi

# Resolve the invoking non-root user: the image build runs unprivileged (mkosi
# escalates for the disk ops it needs), and the kept serial log is handed back
# caller-readable for debugging.
CALLER="${SUDO_USER:-}"
if [ -z "$CALLER" ] || [ "$CALLER" = "root" ]; then
    CALLER="$(logname 2>/dev/null || true)"
fi
CALLER_HOME=""
if [ -n "$CALLER" ] && [ "$CALLER" != "root" ]; then
    CALLER_HOME="$(getent passwd "$CALLER" | cut -d: -f6)"
fi
# sudo strips PATH; fold the caller's user-local bin back in so pipx-installed
# mkosi etc. are visible to the build invoked below.
if [ -n "$CALLER_HOME" ] && [ -d "$CALLER_HOME/.local/bin" ]; then
    PATH="$CALLER_HOME/.local/bin:$PATH"
fi

# Copy the serial log somewhere the caller can read and surface the lines that
# explain a boot/control-plane failure.
dump_serial() {
    [ -r "$QEMU_SERIAL" ] || return 0
    local saved="${WORK}/last-serial.log"
    mkdir -p "$WORK"
    cp "$QEMU_SERIAL" "$saved" 2>/dev/null || true
    if [ -n "$CALLER" ] && [ "$CALLER" != "root" ]; then
        chown "$CALLER":"$(id -gn "$CALLER" 2>/dev/null || echo "$CALLER")" \
            "$saved" 2>/dev/null || true
    fi
    echo "--- serial: control-plane / assertion path ---" >&2
    grep -niE 'cloud-assert|MALMO-CLOUD-ASSERT|host-agent|docker|brain|caddy|fail' \
        "$QEMU_SERIAL" 2>/dev/null | tail -40 >&2 || true
    echo "--- serial: tail 30 ---" >&2
    tail -30 "$QEMU_SERIAL" >&2 || true
    echo "--- full serial log (caller-readable): ${saved} ---" >&2
}

cleanup() {
    local rc=$?
    if [ -n "$QEMU_PID" ] && kill -0 "$QEMU_PID" 2>/dev/null; then
        kill -KILL "$QEMU_PID" 2>/dev/null || true
    fi
    if [ "$rc" -eq 0 ]; then
        rm -rf "$RUN_DIR"
    else
        echo "run artifacts kept at $RUN_DIR (serial log: $QEMU_SERIAL)" >&2
    fi
    return "$rc"
}
trap cleanup EXIT

# --- 1. build the image (unprivileged; bootstrap escalates via mkosi). Run as
# the caller so artifacts are caller-owned and Docker is reachable from the
# caller's group membership.
echo "=== building hosted cloud image ==="
if [ -n "$CALLER" ] && [ "$CALLER" != "root" ]; then
    sudo -u "$CALLER" env "PATH=$PATH" "HOME=$CALLER_HOME" bash "${CLOUD_DIR}/bootstrap.sh"
else
    bash "${CLOUD_DIR}/bootstrap.sh"
fi

RAW="$(ls -1 "$WORK"/*.raw 2>/dev/null | head -n1 || true)"
[ -n "$RAW" ] || { echo "no raw image under $WORK after build" >&2; exit 1; }

# --- 2. resolve OVMF firmware (varies by distro).
OVMF_CODE=""
OVMF_VARS_TEMPLATE=""
for cand in /usr/share/OVMF/OVMF_CODE.fd \
            /usr/share/ovmf/OVMF.fd \
            /usr/share/OVMF/OVMF.fd \
            /usr/share/edk2-ovmf/x64/OVMF_CODE.fd; do
    if [ -r "$cand" ]; then OVMF_CODE="$cand"; break; fi
done
for cand in /usr/share/OVMF/OVMF_VARS.fd \
            /usr/share/ovmf/OVMF_VARS.fd \
            /usr/share/edk2-ovmf/x64/OVMF_VARS.fd; do
    if [ -r "$cand" ]; then OVMF_VARS_TEMPLATE="$cand"; break; fi
done
[ -n "$OVMF_CODE" ] || { echo "OVMF code firmware not found (install: ovmf)" >&2; exit 1; }
OVMF_VARS="${RUN_DIR}/OVMF_VARS.fd"
if [ -n "$OVMF_VARS_TEMPLATE" ]; then
    cp "$OVMF_VARS_TEMPLATE" "$OVMF_VARS"
else
    OVMF_VARS=""
fi

# --- 3. writable per-run overlay over the read-only golden raw (keeps the built
# image clean; this run's docker-load + container state are discarded on exit).
OVERLAY="${RUN_DIR}/disk.qcow2"
qemu-img create -q -f qcow2 -F raw -b "$RAW" "$OVERLAY"

ACCEL=tcg
if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
    ACCEL=kvm
fi

# --- 4. launch QEMU (single boot). The assert unit is armed by the
# malmo.cloud-assert systemd credential delivered over SMBIOS type 11 (the same
# channel the medium lane uses for the LUKS passphrase); without it the unit is
# inert, so this credential is what turns a plain image boot into a test run.
QEMU_ARGS=(
    -machine "q35,accel=${ACCEL}"
    -cpu "$([ "$ACCEL" = kvm ] && echo host || echo max)"
    -m 2G
    -smp 2
    -nographic
    -serial "file:${QEMU_SERIAL}"
    -monitor none
    -drive "file=${OVERLAY},if=virtio,format=qcow2"
    -drive "if=pflash,format=raw,readonly=on,file=${OVMF_CODE}"
    -netdev "user,id=mn1,restrict=on"
    -device "virtio-net-pci,netdev=mn1,mac=52:54:00:c1:0d:01"
    -smbios "type=11,value=io.systemd.credential:malmo.cloud-assert=1"
    -no-reboot
)
if [ -n "$OVMF_VARS" ]; then
    QEMU_ARGS+=( -drive "if=pflash,format=raw,file=${OVMF_VARS}" )
fi

echo "=== booting hosted cloud image (accel=${ACCEL}) ==="
qemu-system-x86_64 "${QEMU_ARGS[@]}" &
QEMU_PID=$!

# --- 5. wait for the verdict sentinel in the serial log. The assert unit polls
# the control plane internally (up to ~7 min worst case across its stages, capped
# by the unit's TimeoutStartSec=600), prints the sentinel, then powers off. The
# cap here must exceed boot + that budget so a late PASS isn't cut off as a false
# timeout; bail early if QEMU dies first.
VERDICT=""
for _i in $(seq 1 720); do
    if grep -qE '^MALMO-CLOUD-ASSERT:' "$QEMU_SERIAL" 2>/dev/null; then
        VERDICT="$(grep -E '^MALMO-CLOUD-ASSERT:' "$QEMU_SERIAL" | head -1 | tr -d '\r')"
        break
    fi
    if ! kill -0 "$QEMU_PID" 2>/dev/null; then
        echo "qemu exited before the assertion verdict appeared" >&2
        QEMU_PID=""
        dump_serial
        echo "cloud-lane test: FAIL (no verdict; VM exited early)" >&2
        exit 1
    fi
    sleep 1
done

if [ -z "$VERDICT" ]; then
    echo "no assertion verdict after 720s" >&2
    dump_serial
    echo "cloud-lane test: FAIL (timeout)" >&2
    exit 1
fi

# The unit powers the VM off after writing the verdict; wait briefly for a clean
# exit, then evaluate.
for _i in $(seq 1 30); do
    kill -0 "$QEMU_PID" 2>/dev/null || break
    sleep 1
done
if kill -0 "$QEMU_PID" 2>/dev/null; then
    kill -KILL "$QEMU_PID" 2>/dev/null || true
fi
wait "$QEMU_PID" 2>/dev/null || true
QEMU_PID=""

echo "verdict: $VERDICT"
case "$VERDICT" in
    "MALMO-CLOUD-ASSERT: PASS")
        echo "cloud-lane test: PASS"
        exit 0
        ;;
    *)
        dump_serial
        echo "cloud-lane test: ${VERDICT}" >&2
        exit 1
        ;;
esac
