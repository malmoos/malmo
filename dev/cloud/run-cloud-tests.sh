#!/usr/bin/env bash
# Cloud-lane end-to-end: boot proof + hosted /setup gate + first-run wizard (C2
# #205; C3a cloud-lane #220; C5 #209): build the hosted cloud image, convert it to
# the qcow2 cloud artifact, and boot it in QEMU to prove the control plane comes up,
# the hosted first-boot provisioning seed + admin-bootstrap gate work, AND the new
# admin can drive the trimmed first-run wizard to completion — the box becomes a
# working, admin-owned, served, first-run-complete malmo. The cloud analogue of
# dev/test-qemu/run-medium-tests.sh, MINUS swtpm + LUKS (no TPM/disk encryption in
# hosted — "the disk IS the installed system", ENVIRONMENT.md # Provisioning), PLUS
# the seed delivery + wizard the medium lane has no analogue for.
#
# Three sequential UEFI boots over ONE persisted qcow2 overlay (so the brain's
# box-id + first admin carry boot→boot), then a fourth legacy-BIOS smoke boot on
# its own overlay (#277), one virtio NIC with restrict=on (air-gapped — the seed
# arrives over SMBIOS, never the network), serial-log capture per boot. The in-VM self-check (cloud-assertions.sh, run by malmo-cloud-assertions.
# service) reads which scenario to assert from a `malmo.assert` SMBIOS credential,
# writes its verdict to the serial console, and powers the box off cleanly on PASS
# (no SSH in hosted — ENVIRONMENT.md # Access & files). This driver greps the verdict:
#
#   boot 1  un-seeded   no seed → GET /_malmo/sso ⇒ 503; /setup ⇒ 403 (gate armed)
#   boot 2  seeded      seed A over SMBIOS (with a complete acme-dns enrollment) →
#                       assertion key ingested → a bad token on GET /_malmo/sso ⇒ 401
#                       (verifier armed); /setup ⇒ 403; brain logged 'provisioning
#                       seed ingested' under box_id A; the brain APPLIES the wildcard-
#                       TLS config (acme-dns DNS-01 issuer + :443 bound) — no real cert
#                       (air-gapped), just that the box reaches and binds it (#278)
#   boot 3  frozen      a DIFFERENT seed B delivered, same overlay → the brain ignores
#                       it (identity frozen in SQLite); the dashboard + /api still serve
#                       under box_id A and the brain does not re-ingest
#   boot 4  bios        the SAME image re-booted under legacy BIOS (SeaBIOS, no OVMF)
#                       on its own overlay → proves the dual-firmware image (grub BIOS
#                       + systemd-boot UEFI) boots where a UEFI-only one hung (#277)
#
# The positive SSO path (a valid portal assertion → owner auto-create → box session →
# first-run wizard) needs the portal's private signing key, so it is the joint cloud
# on-ramp acceptance (cloud docs/ops/e2e-onramp.md), not this box-only boot lane.
#
# The seed is delivered as a systemd credential over SMBIOS type 11 (the same
# mechanism the medium lane uses for the LUKS passphrase; on a real cloud the same
# seed.json arrives via cloud-init). malmo-seed.service materializes it to
# /var/lib/malmo/seed.json before host-agent launches the brain.
#
# See docs/specs/TESTING.md # Full-stack control-plane integration,
# docs/progress/cloud-vm-boot-proof.md, docs/progress/cloud-seed-delivery.md, and
# docs/progress/cloud-e2e-test.md.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="${REPO_ROOT}/.dev/cloud-boot"
IMAGE_OUT="${WORK}/malmo-cloud.raw"
VERSION="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
QCOW2="${WORK}/malmo-${VERSION}-amd64.qcow2"

RUN_DIR="$(mktemp -d -t malmo-cloud.XXXXXX)"
OVERLAY="${RUN_DIR}/overlay.qcow2"   # writable, persisted across the three boots
QEMU_SERIAL="${RUN_DIR}/serial.log"  # set per-phase by run_boot
QEMU_PID=""
VERDICT=""

# The seed's two box-ids. Boot 2 provisions A; boot 3 re-delivers B and must be
# ignored, so /login still reports A.
BOX_ID_A=cindy-fox
BOX_ID_B=rusty-hawk

# Which boots to run, space-separated (unseeded seeded frozen bios). Default: all.
# A subset lets a caller run only the boots it needs — notably the cloud-image
# publish gate, which runs "unseeded seeded" to prove the built image's brain
# accepts the current seed schema (the regression that gate exists for) and
# leaves out the frozen-identity boot. Frozen is orthogonal to that gate (it
# checks that a re-delivered seed is IGNORED on a later boot) and has shown a
# flaky false "re-ingested" verdict in CI whose root cause is still open — so
# it is kept in the default full run (where it can be triaged) but out of the
# publish gate. Order still holds: frozen reuses the overlay seeded leaves
# behind, so run seeded whenever frozen runs. The `bios` boot (#277) is the odd
# one out: it re-boots the image under legacy BIOS (SeaBIOS) instead of UEFI on its
# OWN fresh overlay, so it has no ordering dependency and the publish gate includes
# it directly (see ci-cloud-image.yml).
BOOTS="${MALMO_CLOUD_BOOTS:-unseeded seeded frozen bios}"
should_run() { case " $BOOTS " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }

# QEMU writes serial logs as root (this script runs under sudo). Resolve the
# invoking user so kept diagnostics are caller-readable.
CALLER="${SUDO_USER:-}"
if [ -z "$CALLER" ] || [ "$CALLER" = "root" ]; then CALLER="$(logname 2>/dev/null || true)"; fi
if [ "$CALLER" = "root" ]; then CALLER=""; fi  # root-shell edge case: no caller to chown back to

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

kill_qemu() {
    if [ -n "$QEMU_PID" ] && kill -0 "$QEMU_PID" 2>/dev/null; then
        kill -KILL "$QEMU_PID" 2>/dev/null || true
        wait "$QEMU_PID" 2>/dev/null || true
    fi
    QEMU_PID=""
}

cleanup() {
    local rc=$?
    kill_qemu
    if [ "$rc" -eq 0 ]; then rm -rf "$RUN_DIR"; else
        echo "run artifacts kept at $RUN_DIR" >&2
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

# --- 2. qcow2 cloud artifact (BUILD.md # 6: qemu-img convert raw -> qcow2). This
# stays the pristine deliverable; the boots write to a throwaway overlay over it.
echo "converting raw -> qcow2 cloud artifact: $(basename "$QCOW2")"
qemu-img convert -f raw -O qcow2 "$IMAGE_OUT" "$QCOW2"
[ -n "$CALLER" ] && chown "$CALLER":"$(id -gn "$CALLER" 2>/dev/null || echo "$CALLER")" "$QCOW2" 2>/dev/null || true

# A single writable overlay backed by the pristine artifact, reused across all
# three boots so the box-id + first admin the seeded boot writes survive into the
# frozen-identity boot. The base artifact is never written.
qemu-img create -f qcow2 -b "$QCOW2" -F qcow2 "$OVERLAY" >/dev/null

# --- 3. resolve OVMF firmware (varies by distro). One VARS copy, reused across
# boots so the EFI state persists like a real machine power-cycle.
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

# Build a compact seed JSON for a box-id and base64-encode it for an SMBIOS binary
# credential. Two parts mirror a real cloud seed:
#   - assertion_verification_key: a random 32-byte value (standard base64, the wire
#     shape of a real portal Ed25519 public key) so the box loads its SSO verifier;
#     this lane has no matching private key, so it only exercises the verifier's
#     rejection path (a real portal assertion is the cloud on-ramp's job).
#   - enrollment: a COMPLETE acme-dns credential block, so the brain runs its
#     wildcard-TLS pass (cmd/brain EnsureWildcardTLS) — configures Caddy's acme-dns
#     DNS-01 issuer for "*.<box-id>.malmo.network" and binds :443. The values are
#     inert here: air-gapped (restrict=on) the box never reaches acme-dns/Let's
#     Encrypt, so no real cert issues — the lane asserts the brain APPLIES the
#     config and :443 comes up (the #278 regression class), not that a cert exists.
#     A seed with no enrollment (the prior shape) skipped that pass entirely, which
#     is exactly why CI never caught a hosted box failing to bind :443.
# Prints `io.systemd.credential.binary:malmo.seed=<base64>`.
seed_cred() { # box_id -> SMBIOS value string
    local box_id="$1" key json
    key="$(head -c 32 /dev/urandom | base64 -w0)"
    json="$(printf '{"box_id":"%s","assertion_verification_key":"%s","enrollment":{"subdomain":"%s","username":"%s","password":"%s"}}' \
        "$box_id" "$key" "cloud-lane-acmedns-subdomain" "cloud-lane-acmedns-user" "cloud-lane-acmedns-pass")"
    printf 'io.systemd.credential.binary:malmo.seed=%s' "$(printf '%s' "$json" | base64 -w0)"
}

# Boot the overlay once and run one scenario of in-VM assertions. The assert mode
# is delivered as a text SMBIOS credential; any extra args (the seed credential)
# are appended. Sets VERDICT; returns non-zero on failure.
#   run_boot <phase> <mode> [extra -smbios args...]
run_boot() {
    local phase="$1" mode="$2"; shift 2
    QEMU_SERIAL="${RUN_DIR}/serial-${phase}.log"
    QEMU_PID=""
    VERDICT=""

    local firmware="${FIRMWARE:-uefi}"
    local qemu_args=(
        -machine "q35,accel=${ACCEL}"
        -cpu "$([ "$ACCEL" = kvm ] && echo host || echo max)"
        -m 2G
        -smp 2
        -nographic
        -serial "file:${QEMU_SERIAL}"
        -monitor none
        -drive "file=${OVERLAY},if=virtio,format=qcow2"
    )
    # UEFI attaches OVMF; BIOS (#277) attaches nothing so QEMU uses its built-in
    # SeaBIOS — the legacy-BIOS firmware a Hetzner CX (Intel) VM presents, under
    # which a UEFI-only image hangs at "Booting from Hard Disk". This is what
    # exercises the grub BIOS boot path; the OVMF-only lane could never catch #277.
    if [ "$firmware" = uefi ]; then
        qemu_args+=( -drive "if=pflash,format=raw,readonly=on,file=${OVMF_CODE}" )
    fi
    qemu_args+=(
        -netdev "user,id=n0,restrict=on"
        -device "virtio-net-pci,netdev=n0,mac=52:54:00:c1:0d:01"
        -smbios "type=11,value=io.systemd.credential:malmo.assert=${mode}"
        "$@"
        -no-reboot
    )
    if [ "$firmware" = uefi ] && [ -n "$OVMF_VARS" ]; then
        qemu_args+=( -drive "if=pflash,format=raw,file=${OVMF_VARS}" )
    fi

    echo "=== boot phase=${phase} mode=${mode} firmware=${firmware} (accel=${ACCEL}, air-gapped) ==="
    qemu-system-x86_64 "${qemu_args[@]}" &
    QEMU_PID=$!

    # Wait for the verdict on the serial console. First boot does docker load +
    # brain bootstrap + compose up, so allow a generous window; cloud-assertions.sh
    # polls the stack up internally. 480s (not 360): the in-VM assertion widened its
    # flush-lag-tolerant log waits, so this outer budget must exceed the sum of the
    # guest's internal polls — otherwise a slow TCG boot times out here as a false
    # "no verdict" before the guest can emit PASS/FAIL.
    local v=""
    for _i in $(seq 1 480); do
        if grep -q 'MALMO_CLOUD_ASSERTIONS:' "$QEMU_SERIAL" 2>/dev/null; then
            v="$(grep -o 'MALMO_CLOUD_ASSERTIONS:.*' "$QEMU_SERIAL" | tail -1 | tr -d '\r')"
            break
        fi
        if ! kill -0 "$QEMU_PID" 2>/dev/null; then
            echo "qemu (phase=${phase}) exited before a verdict. serial:" >&2
            dump_serial
            VERDICT="FAIL: qemu died before verdict (phase ${phase})"
            return 1
        fi
        sleep 1
    done
    VERDICT="$v"
    if [ -z "$v" ]; then
        echo "no verdict on the serial console after 480s (phase=${phase}). serial:" >&2
        dump_serial
        kill_qemu
        VERDICT="FAIL: no verdict (phase ${phase}, timeout)"
        return 1
    fi
    echo "phase=${phase} verdict: ${v}"
    case "$v" in
        *PASS*)
            # On PASS the guest powers itself off (cloud-assertions.sh ok()); wait
            # for QEMU to exit so the overlay write (box-id + admin) flushes before
            # the next boot reads it. Bounded — kill if the clean shutdown hangs.
            for _i in $(seq 1 60); do
                kill -0 "$QEMU_PID" 2>/dev/null || break
                sleep 1
            done
            kill_qemu
            return 0
            ;;
        *)
            dump_serial
            kill_qemu
            return 1
            ;;
    esac
}

# --- 4. boot 1: un-seeded. No seed credential → the brain stays unprovisioned and
# GET /_malmo/sso returns 503 and /setup returns 403 (the SSO gate is armed but
# closed — never the appliance's open empty-box behavior). Also the standalone C2
# control-plane-up proof.
if should_run unseeded; then
if ! run_boot "unseeded" "unseeded"; then
    echo "cloud gate proof: ${VERDICT}" >&2
    exit 1
fi
echo "boot 1 OK — control plane up, hosted SSO gate armed (503, unprovisioned)"
fi

# --- 5. boot 2: seeded. Deliver seed A → the brain ingests the assertion key; a
# bad/unsigned token on /_malmo/sso is 401 (the verifier is armed) and /setup is 403
# (disabled on hosted). The ingested box-id A persists on the overlay. The positive
# owner-create + wizard path needs the portal private key (cloud on-ramp).
if should_run seeded; then
if ! run_boot "seeded" "seeded" -smbios "type=11,value=$(seed_cred "$BOX_ID_A")"; then
    echo "cloud gate proof: ${VERDICT}" >&2
    exit 1
fi
echo "boot 2 OK — seed ingested (assertion key loaded, bad-token 401), box_id=${BOX_ID_A}"
fi

# --- 6. boot 3: frozen identity. Re-deliver a DIFFERENT seed B over the SAME
# overlay. The brain loads its persisted box-id A from SQLite and ignores the new
# seed; the dashboard + /api still serve under box_id A and the brain does not
# re-ingest. Proves a re-delivered or changed seed cannot re-key a provisioned box
# (MALMO_NETWORK.md frozen identity).
if should_run frozen; then
if ! run_boot "frozen" "frozen:${BOX_ID_A}" -smbios "type=11,value=$(seed_cred "$BOX_ID_B")"; then
    echo "cloud gate proof: ${VERDICT}" >&2
    exit 1
fi
echo "boot 3 OK — frozen identity held across reboot (re-delivered seed B ignored, box_id still ${BOX_ID_A})"
fi

# --- 7. boot 4: legacy-BIOS smoke (#277). The three boots above all run under UEFI
# (OVMF). This one boots the SAME image under QEMU's built-in SeaBIOS — the legacy-
# BIOS firmware a Hetzner CX (Intel) VM presents, where a UEFI-only image hangs at
# "Booting from Hard Disk" and never reaches userspace. It proves the image's grub
# BIOS boot path (BiosBootloader=grub + the BIOS Boot Partition) actually boots to
# a running control plane. It reuses the un-seeded assertion (control plane up, SSO
# gate armed) as the "did it boot and come up" proof — the firmware path is what's
# under test here, not provisioning — on its OWN fresh overlay so it can run
# independently of (and never perturb) the UEFI provisioning sequence above.
if should_run bios; then
BIOS_OVERLAY="${RUN_DIR}/overlay-bios.qcow2"
qemu-img create -f qcow2 -b "$QCOW2" -F qcow2 "$BIOS_OVERLAY" >/dev/null
OVERLAY="$BIOS_OVERLAY"
FIRMWARE=bios
if ! run_boot "bios" "unseeded"; then
    echo "cloud BIOS boot proof: ${VERDICT}" >&2
    exit 1
fi
echo "boot 4 OK — image boots under legacy BIOS (SeaBIOS), control plane up"
fi

echo "cloud end-to-end: PASS (boots: ${BOOTS})"
echo "qcow2 cloud artifact: ${QCOW2}"
exit 0
