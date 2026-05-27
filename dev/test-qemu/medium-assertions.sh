#!/usr/bin/env bash
# Medium-lane in-VM assertions, baked into the image at
# /usr/local/bin/medium-assertions.sh by dev/test-qemu/bootstrap.sh.
#
# Driven over SSH by dev/test-qemu/run-medium-tests.sh after the VM
# boots. Writes the verdict to /var/lib/malmo-medium-result; the host
# driver scp's it back.
#
# Scope is slice-0021 scaffolding: prove real kernel + real systemd +
# real TPM (via swtpm) + our reporter all work end-to-end. LUKS root
# unseal is slice 0022.

# -u + pipefail but deliberately NOT -e: every assertion is `... || fail
# "..."`. The EXIT trap upgrades the STARTED sentinel to a generic FAIL
# if anything falls through without ok/fail. Same posture as slice 0020.
set -uo pipefail

RESULT=/var/lib/malmo-medium-result

# Sentinel non-matching the host's ^(PASS|FAIL:) regex so the driver
# doesn't tear us down before assertions complete.
echo "STARTED" > "$RESULT"

fail() { echo "FAIL: $*" > "$RESULT"; sync; exit 1; }
ok()   { echo "PASS"     > "$RESULT"; sync; exit 0; }

trap '
    if ! grep -qE "^(PASS|FAIL:)" "$RESULT" 2>/dev/null; then
        echo "FAIL: assertion script aborted before reaching ok/fail" > "$RESULT"
    fi
    sync
' EXIT

# --- 1. real systemd userspace reached multi-user
state="$(systemctl is-system-running 2>&1 || true)"
case "$state" in
    running|degraded) ;;
    *) fail "system state is '$state' (expected running or degraded)" ;;
esac

# --- 2. storage-verify ran end-to-end
verify_state="$(systemctl is-active malmo-storage-verify.service 2>&1 || true)"
[ "$verify_state" = "active" ] \
    || fail "malmo-storage-verify.service is '$verify_state' (expected active)"

# --- 3. reporter output exists and is shaped correctly
test -s /run/malmo/health/storage.json \
    || fail "/run/malmo/health/storage.json missing or empty"

payload="$(cat /run/malmo/health/storage.json)"
compact="$(tr -d ' \n\t' <<<"$payload")"
grep -q '"checked_at"' <<<"$compact" \
    || fail "storage.json missing checked_at: $payload"
grep -q '"findings"' <<<"$compact" \
    || fail "storage.json missing findings: $payload"

# Level-0 VM has no data drive enrolled — expect empty findings.
case "$compact" in
    *'"findings":null'*|*'"findings":[]'*) ;;
    *) fail "expected empty findings on Level-0 VM, got: $payload" ;;
esac
grep -q '"id":' <<<"$compact" \
    && fail "spurious finding on clean Level-0 VM: $payload"

# --- 4. TPM plumbing is live
# swtpm via QEMU exposes /dev/tpm0 and /dev/tpmrm0. tpm2_pcrread is the
# cheapest test that the device is wired and responding. PCR 7 (Secure
# Boot policy state per STORAGE.md # Encryption posture) should be
# readable — its value isn't asserted here (slice 0022 will use it).
test -c /dev/tpmrm0 \
    || fail "/dev/tpmrm0 not present (TPM device not exposed to VM?)"

pcr_out="$(tpm2_pcrread sha256:7 2>&1)" \
    || fail "tpm2_pcrread sha256:7 failed: $pcr_out"
grep -q '7 *:' <<<"$pcr_out" \
    || fail "tpm2_pcrread output unexpected shape: $pcr_out"

ok
