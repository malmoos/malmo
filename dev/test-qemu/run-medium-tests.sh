#!/usr/bin/env bash
# Medium-lane test driver: QEMU + swtpm + SSH-driven assertions.
#
# Slice 0021 scope: scaffolding only. Boot a real Linux kernel in QEMU
# with a software TPM, SSH in, run /usr/local/bin/medium-assertions.sh
# (baked into the image at build time), scp the verdict back, shut
# down. See docs/specs/TESTING.md # Medium lane and
# docs/progress/0021-qemu-medium-lane-scaffolding.md.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="${REPO_ROOT}/.dev/qemu"
IMAGE_OUT="${WORK}/malmo-medium.raw"
SSH_KEY="${WORK}/ssh-key"

# Per-run ephemeral state.
RUN_DIR="$(mktemp -d -t malmo-medium.XXXXXX)"
SWTPM_DIR="${RUN_DIR}/swtpm"
SWTPM_SOCK="${SWTPM_DIR}/sock"
RESULT_FILE="${RUN_DIR}/result"
QEMU_SERIAL="${RUN_DIR}/serial.log"
QEMU_PID=""
SWTPM_PID=""

cleanup() {
    local rc=$?
    if [ -n "$QEMU_PID" ] && kill -0 "$QEMU_PID" 2>/dev/null; then
        kill -KILL "$QEMU_PID" 2>/dev/null || true
    fi
    if [ -n "$SWTPM_PID" ] && kill -0 "$SWTPM_PID" 2>/dev/null; then
        kill -TERM "$SWTPM_PID" 2>/dev/null || true
    fi
    # Keep RUN_DIR on failure for debugging; clean only on success.
    if [ "$rc" -eq 0 ]; then
        rm -rf "$RUN_DIR"
    else
        echo "run artifacts kept at $RUN_DIR (serial log: $QEMU_SERIAL)" >&2
    fi
    return "$rc"
}
trap cleanup EXIT

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
    echo "must run as root (QEMU+KVM, mkosi build)" >&2
    exit 1
fi

# --- 1. always invoke bootstrap; it has its own canary-version gate
# and exits fast when the cached image is current. Skipping bootstrap
# here would make `CANARY_VERSION` bumps invisible to the driver.
"${REPO_ROOT}/dev/test-qemu/bootstrap.sh"

# --- 2. resolve OVMF firmware (varies by distro; bootstrap already
# confirmed one exists, repeat the probe here for hermeticity).
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
[ -n "$OVMF_CODE" ] || { echo "OVMF code firmware not found" >&2; exit 1; }

# Per-run VARS copy (firmware variables — writable).
OVMF_VARS="${RUN_DIR}/OVMF_VARS.fd"
if [ -n "$OVMF_VARS_TEMPLATE" ]; then
    cp "$OVMF_VARS_TEMPLATE" "$OVMF_VARS"
else
    # Some distros ship OVMF as a single file (combined CODE+VARS).
    # Fall back to using OVMF_CODE for both, with snapshot.
    OVMF_VARS=""
fi

# --- 3. allocate a free TCP port for SSH forward
SSH_PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("",0)); print(s.getsockname()[1]); s.close()' 2>/dev/null \
    || echo 0)"
if [ "$SSH_PORT" = "0" ] || [ -z "$SSH_PORT" ]; then
    # Fallback: scan for a free port in the 20000-40000 range.
    for p in $(seq 20000 40000); do
        if ! ss -ltn "sport = :$p" 2>/dev/null | grep -q LISTEN; then
            SSH_PORT="$p"; break
        fi
    done
fi
[ -n "$SSH_PORT" ] || { echo "could not allocate ssh forward port" >&2; exit 1; }
echo "ssh forward: 127.0.0.1:${SSH_PORT} -> guest:22"

# --- 4. launch swtpm
mkdir -p "$SWTPM_DIR"
swtpm socket \
    --tpmstate "dir=$SWTPM_DIR" \
    --ctrl "type=unixio,path=$SWTPM_SOCK" \
    --tpm2 \
    --daemon \
    --pid "file=${SWTPM_DIR}/pid" \
    --log "file=${SWTPM_DIR}/log,level=20"
# swtpm --daemon backgrounds and writes the pid file.
sleep 0.2
if [ -f "${SWTPM_DIR}/pid" ]; then
    SWTPM_PID="$(cat "${SWTPM_DIR}/pid")"
fi

# --- 5. launch QEMU
ACCEL=tcg
if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
    ACCEL=kvm
fi

QEMU_ARGS=(
    -machine "q35,accel=${ACCEL}"
    -cpu host
    -m 1G
    -smp 2
    -nographic
    -serial "file:${QEMU_SERIAL}"
    -monitor none
    -drive "file=${IMAGE_OUT},if=virtio,snapshot=on,format=raw"
    -drive "if=pflash,format=raw,readonly=on,file=${OVMF_CODE}"
    -chardev "socket,id=chrtpm,path=${SWTPM_SOCK}"
    -tpmdev "emulator,id=tpm0,chardev=chrtpm"
    -device tpm-crb,tpmdev=tpm0
    -nic "user,hostfwd=tcp::${SSH_PORT}-:22,model=virtio-net-pci"
    -no-reboot
)
if [ -n "$OVMF_VARS" ]; then
    QEMU_ARGS+=( -drive "if=pflash,format=raw,file=${OVMF_VARS}" )
fi

echo "launching qemu (accel=${ACCEL})..."
qemu-system-x86_64 "${QEMU_ARGS[@]}" &
QEMU_PID=$!

# --- 6. poll for SSH availability (boot takes 10-30s on KVM)
SSH_OPTS=(
    -p "$SSH_PORT"
    -i "$SSH_KEY"
    -o StrictHostKeyChecking=no
    -o UserKnownHostsFile=/dev/null
    -o LogLevel=ERROR
    -o ConnectTimeout=2
    -o BatchMode=yes
)
echo "waiting for sshd..."
for _i in $(seq 1 90); do
    if ssh "${SSH_OPTS[@]}" "root@127.0.0.1" true 2>/dev/null; then
        echo "ssh up (${_i}s)"
        break
    fi
    if ! kill -0 "$QEMU_PID" 2>/dev/null; then
        echo "qemu died before sshd came up. serial log:" >&2
        tail -50 "$QEMU_SERIAL" >&2 || true
        exit 1
    fi
    sleep 1
done

# Final check — if we exited the loop without ssh, fail clearly.
if ! ssh "${SSH_OPTS[@]}" "root@127.0.0.1" true 2>/dev/null; then
    echo "ssh never reachable after 90s. serial log tail:" >&2
    tail -50 "$QEMU_SERIAL" >&2 || true
    exit 1
fi

# --- 7. run assertions inside the VM
echo "running in-VM assertions..."
ssh "${SSH_OPTS[@]}" "root@127.0.0.1" \
    "/usr/local/bin/medium-assertions.sh" || true
# scp the verdict back (note: scp uses -P for port, not -p).
scp -P "$SSH_PORT" -i "$SSH_KEY" \
    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR -o ConnectTimeout=2 -o BatchMode=yes \
    "root@127.0.0.1:/var/lib/malmo-medium-result" \
    "$RESULT_FILE" 2>/dev/null || true

# --- 8. shut down the guest cleanly
ssh "${SSH_OPTS[@]}" "root@127.0.0.1" "systemctl poweroff" 2>/dev/null || true

# Bounded wait for QEMU to exit on its own (clean poweroff). SIGKILL
# fallback after 15s — same pattern as the fast lane's container kill.
for _i in $(seq 1 15); do
    if ! kill -0 "$QEMU_PID" 2>/dev/null; then
        break
    fi
    sleep 1
done
QEMU_RC=0
if kill -0 "$QEMU_PID" 2>/dev/null; then
    kill -KILL "$QEMU_PID" 2>/dev/null || true
fi
wait "$QEMU_PID" 2>/dev/null || QEMU_RC=$?
if [ "$QEMU_RC" -ne 0 ] && [ "$QEMU_RC" -ne 137 ] && [ "$QEMU_RC" -ne 143 ]; then
    echo "warning: qemu exited rc=$QEMU_RC" >&2
fi

# --- 9. verdict
VERDICT="$(cat "$RESULT_FILE" 2>/dev/null || true)"
if [ -z "$VERDICT" ]; then
    echo "FAIL: no verdict written (qemu rc=$QEMU_RC, serial tail:)" >&2
    tail -20 "$QEMU_SERIAL" >&2 || true
    exit 1
fi
if [ "$VERDICT" = "PASS" ]; then
    echo "medium-lane test: PASS"
    exit 0
fi
echo "medium-lane test: $VERDICT" >&2
exit 1
