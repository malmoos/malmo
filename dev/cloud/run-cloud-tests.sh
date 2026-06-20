#!/usr/bin/env bash
# Cloud-lane boot proof (C2, #205): build the hosted cloud image, convert it to
# the qcow2 cloud artifact, and boot it ONCE in QEMU to prove the control plane
# comes up and serves. The cloud analogue of dev/test-qemu/run-medium-tests.sh,
# MINUS swtpm, the LUKS recovery credential, and the two-boot enroll/unseal cycle
# — "the disk IS the installed system" (ENVIRONMENT.md # Provisioning), so there
# is nothing to unseal and no installer to run.
#
# Single UEFI boot (OVMF), one virtio NIC with restrict=on (air-gapped — a stray
# registry pull hard-fails, proving the offline bundle is complete), serial-log
# capture. The in-VM self-check (cloud-assertions.sh, baked + run by
# malmo-cloud-assertions.service) writes its verdict to the serial console; this
# driver greps it. No SSH (hosted ships none — ENVIRONMENT.md # Access & files).
#
# See docs/specs/TESTING.md # Full-stack control-plane integration and
# docs/progress/cloud-vm-boot-proof.md.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="${REPO_ROOT}/.dev/cloud-boot"
IMAGE_OUT="${WORK}/malmo-cloud.raw"
VERSION="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
QCOW2="${WORK}/malmo-${VERSION}-amd64.qcow2"

RUN_DIR="$(mktemp -d -t malmo-cloud.XXXXXX)"
QEMU_SERIAL="${RUN_DIR}/serial.log"
QEMU_PID=""

# QEMU writes serial.log as root (this script runs under sudo). Resolve the
# invoking user so kept diagnostics are caller-readable.
CALLER="${SUDO_USER:-}"
if [ -z "$CALLER" ] || [ "$CALLER" = "root" ]; then CALLER="$(logname 2>/dev/null || true)"; fi

dump_serial() {
    [ -r "$QEMU_SERIAL" ] || return 0
    local saved="${WORK}/last-serial.log"
    cp "$QEMU_SERIAL" "$saved" 2>/dev/null || true
    [ -n "$CALLER" ] && chown "$CALLER":"$(id -gn "$CALLER" 2>/dev/null || echo "$CALLER")" "$saved" 2>/dev/null || true
    echo "--- serial: control-plane / assertion lines ---" >&2
    grep -niE 'cloud-assertions|malmo|docker|caddy|brain|host-agent|networkd|fail' "$QEMU_SERIAL" 2>/dev/null | tail -40 >&2 || true
    echo "--- serial: tail 30 ---" >&2
    tail -30 "$QEMU_SERIAL" >&2 || true
    echo "--- full serial log saved (caller-readable): ${saved} ---" >&2
}

cleanup() {
    local rc=$?
    if [ -n "$QEMU_PID" ] && kill -0 "$QEMU_PID" 2>/dev/null; then
        kill -KILL "$QEMU_PID" 2>/dev/null || true
        wait "$QEMU_PID" 2>/dev/null || true
    fi
    if [ "$rc" -eq 0 ]; then rm -rf "$RUN_DIR"; else
        echo "run artifacts kept at $RUN_DIR (serial log: $QEMU_SERIAL)" >&2
    fi
    return "$rc"
}
trap cleanup EXIT

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
    echo "must run as root (QEMU+KVM, mkosi build)" >&2
    exit 1
fi

# --- 1. build (own canary gate; fast when current).
"${REPO_ROOT}/dev/cloud/test/bootstrap.sh"

# --- 2. qcow2 cloud artifact (BUILD.md # 6: qemu-img convert raw -> qcow2).
echo "converting raw -> qcow2 cloud artifact: $(basename "$QCOW2")"
qemu-img convert -f raw -O qcow2 "$IMAGE_OUT" "$QCOW2"
[ -n "$CALLER" ] && chown "$CALLER":"$(id -gn "$CALLER" 2>/dev/null || echo "$CALLER")" "$QCOW2" 2>/dev/null || true

# --- 3. resolve OVMF firmware (varies by distro).
OVMF_CODE=""
for cand in /usr/share/OVMF/OVMF_CODE.fd /usr/share/ovmf/OVMF.fd \
            /usr/share/OVMF/OVMF.fd /usr/share/edk2-ovmf/x64/OVMF_CODE.fd; do
    [ -r "$cand" ] && { OVMF_CODE="$cand"; break; }
done
[ -n "$OVMF_CODE" ] || { echo "OVMF code firmware not found" >&2; exit 1; }
OVMF_VARS_TEMPLATE=""
for cand in /usr/share/OVMF/OVMF_VARS.fd /usr/share/ovmf/OVMF_VARS.fd \
            /usr/share/edk2-ovmf/x64/OVMF_VARS.fd; do
    [ -r "$cand" ] && { OVMF_VARS_TEMPLATE="$cand"; break; }
done
OVMF_VARS=""
if [ -n "$OVMF_VARS_TEMPLATE" ]; then
    OVMF_VARS="${RUN_DIR}/OVMF_VARS.fd"
    cp "$OVMF_VARS_TEMPLATE" "$OVMF_VARS"
fi

ACCEL=tcg
if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then ACCEL=kvm; fi

# --- 4. single boot. snapshot=on keeps the qcow2 artifact pristine (assertion
# writes are discarded on exit). restrict=on air-gaps the guest: SLIRP still
# leases the NIC (proving networkd brings it up) but routes nothing out, so a
# missing bundled image hard-fails instead of silently pulling.
QEMU_ARGS=(
    -machine "q35,accel=${ACCEL}"
    -cpu "$([ "$ACCEL" = kvm ] && echo host || echo max)"
    -m 2G
    -smp 2
    -nographic
    -serial "file:${QEMU_SERIAL}"
    -monitor none
    -drive "file=${QCOW2},if=virtio,format=qcow2,snapshot=on"
    -drive "if=pflash,format=raw,readonly=on,file=${OVMF_CODE}"
    -netdev "user,id=n0,restrict=on"
    -device "virtio-net-pci,netdev=n0,mac=52:54:00:c1:0d:01"
    -no-reboot
)
[ -n "$OVMF_VARS" ] && QEMU_ARGS+=( -drive "if=pflash,format=raw,file=${OVMF_VARS}" )

echo "=== booting hosted cloud image (accel=${ACCEL}, air-gapped) ==="
qemu-system-x86_64 "${QEMU_ARGS[@]}" &
QEMU_PID=$!

# --- 5. wait for the self-check verdict on the serial console. The control plane
# (docker load + host-agent brain bootstrap + compose up) takes a while on first
# boot; cloud-assertions.sh polls it internally, so allow a generous window.
VERDICT=""
for _i in $(seq 1 360); do
    if grep -q 'MALMO_CLOUD_ASSERTIONS:' "$QEMU_SERIAL" 2>/dev/null; then
        VERDICT="$(grep -o 'MALMO_CLOUD_ASSERTIONS:.*' "$QEMU_SERIAL" | tail -1 | tr -d '\r')"
        break
    fi
    if ! kill -0 "$QEMU_PID" 2>/dev/null; then
        echo "qemu exited before the self-check reported. serial:" >&2
        dump_serial
        echo "cloud boot proof: FAIL (qemu died before verdict)" >&2
        exit 1
    fi
    sleep 1
done

if [ -z "$VERDICT" ]; then
    echo "no verdict on the serial console after 360s. serial:" >&2
    dump_serial
    echo "cloud boot proof: FAIL (timeout)" >&2
    exit 1
fi

echo "verdict: ${VERDICT}"
case "$VERDICT" in
    *PASS*)
        echo "cloud boot proof: PASS"
        echo "qcow2 cloud artifact: ${QCOW2}"
        exit 0
        ;;
    *)
        dump_serial
        echo "cloud boot proof: ${VERDICT}" >&2
        exit 1
        ;;
esac
