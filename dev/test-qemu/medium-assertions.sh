#!/usr/bin/env bash
# Medium-lane in-VM assertions, baked into the image at
# /usr/local/bin/medium-assertions.sh by dev/test-qemu/bootstrap.sh.
#
# Driven over SSH by dev/test-qemu/run-medium-tests.sh after the VM
# boots. Writes the verdict to /var/lib/malmo-medium-result; the host
# driver scp's it back.
#
# Takes a phase argument (slice 0023 Stages 2/3); the harness runs the
# image through two boots of the same disk and asserts a different thing
# on each:
#   first-boot   common checks + the run-once systemd-cryptenroll landed
#                a PCR-7-bound systemd-tpm2 token in the LUKS header.
#   second-boot  common checks + the token persisted, AND this boot got
#                here at all — the harness withheld the passphrase
#                credential, so reaching multi-user proves the initrd
#                unsealed root unattended via that TPM2 token.
#   combined     (default) common checks only — for `mkosi qemu` manual
#                debugging where no harness phase is supplied.
#
# -u + pipefail but deliberately NOT -e: every assertion is `... || fail
# "..."`. The EXIT trap upgrades the STARTED sentinel to a generic FAIL
# if anything falls through without ok/fail. Same posture as slice 0020.
set -uo pipefail

PHASE="${1:-combined}"
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

echo "medium-assertions phase=$PHASE"

# --- 1. real systemd userspace reached multi-user.
# Poll, don't sample once: on first boot the run-once
# malmo-tpm-enroll.service is part of the boot transaction
# (WantedBy=malmo-storage-ready.target) and its Argon2 keyslot operation
# takes a couple of seconds, during which is-system-running legitimately
# reports 'starting'. sshd has no ordering against the enroll, so it can
# win the race and let us in before the transaction settles — sampling
# once here would spuriously fail (and the driver's follow-up poweroff
# would then SIGTERM the enroll mid-operation). Wait for the transaction
# to converge; a failed enroll still converges (Wants=, not Requires=) to
# 'degraded', which the marker-wait below then diagnoses precisely.
state=""
for _i in $(seq 1 120); do
    state="$(systemctl is-system-running 2>&1 || true)"
    case "$state" in
        running|degraded) break ;;
    esac
    sleep 1
done
case "$state" in
    running|degraded) ;;
    *) fail "system state is '$state' after 120s (expected running or degraded)" ;;
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
# Boot policy state per STORAGE.md # Encryption posture) is the bank our
# keyslot policy binds to.
test -c /dev/tpmrm0 \
    || fail "/dev/tpmrm0 not present (TPM device not exposed to VM?)"

pcr_out="$(tpm2_pcrread sha256:7 2>&1)" \
    || fail "tpm2_pcrread sha256:7 failed: $pcr_out"
grep -q '7 *:' <<<"$pcr_out" \
    || fail "tpm2_pcrread output unexpected shape: $pcr_out"

# --- 5. root actually sits on a LUKS/dm-crypt device (slice 0023 Stage 1)
# The whole point of 0023: prove we booted the *encrypted* root, not a
# silent plaintext fallback. Reaching here means systemd-cryptsetup
# unlocked the LUKS container in the initrd (via the passphrase credential
# on first boot, via the TPM2 token on second boot) and switch-root landed
# on the mapper device. Assert the mount source is the dm-crypt device.
root_src="$(findmnt -no SOURCE / 2>/dev/null || true)"
case "$root_src" in
    /dev/mapper/luks-*|/dev/dm-*) ;;
    *) fail "root is not on a dm-crypt device (source='$root_src'); encrypted-root path not exercised" ;;
esac
# Belt-and-suspenders: confirm it's a live LUKS mapping, not a same-named
# mapper device of some other dm target.
cryptsetup status "$root_src" 2>/dev/null | grep -qiE 'type:[[:space:]]*LUKS' \
    || fail "root device '$root_src' is not a live LUKS mapping"

# Resolve the LUKS backing partition (where the header + tokens live).
# Needed for the token assertions below (the header is on the partition,
# not the mapper). awk on the `device:` line of cryptsetup status.
luks_backing() {
    cryptsetup status "$root_src" 2>/dev/null \
        | awk '/^[[:space:]]*device:/ {print $2}'
}

# Assert the LUKS header carries a systemd-tpm2 token bound to PCR 7.
# Parsed from the JSON metadata with grep/sed only — the guest image has
# no jq or python3. Compact the JSON, find a systemd-tpm2 token, isolate
# its tpm2-pcrs array (hyphenated key in the on-disk token JSON), and
# check 7 is a member (comma-wrap so "7" can't match inside "17").
assert_tpm2_pcr7_token() {
    local backing json jcompact arr
    backing="$(luks_backing)"
    [ -n "$backing" ] || fail "could not resolve LUKS backing device for $root_src"

    json="$(cryptsetup luksDump --dump-json-metadata "$backing" 2>/dev/null || true)"
    [ -n "$json" ] || fail "cryptsetup luksDump --dump-json-metadata $backing produced no output"
    jcompact="$(tr -d ' \n\t' <<<"$json")"

    grep -q '"type":"systemd-tpm2"' <<<"$jcompact" \
        || fail "no systemd-tpm2 token in LUKS header of $backing (json: $jcompact)"

    arr="$(grep -oE '"tpm2-pcrs":\[[0-9,]*\]' <<<"$jcompact" | head -1 \
        | sed -E 's/.*\[//; s/\].*//')"
    grep -q ',7,' <<<",${arr}," \
        || fail "systemd-tpm2 token not bound to PCR 7 (tpm2-pcrs=[$arr]) on $backing"
}

case "$PHASE" in
    first-boot)
        # The run-once enrollment unit (malmo-tpm-enroll.service) is
        # ordered Before=malmo-storage-ready.target but ssh.service has no
        # ordering against it, so SSH can win the race and we may arrive
        # before enrollment finishes. Wait for the marker (written only on
        # a successful enroll); fail fast if the unit itself failed.
        for _i in $(seq 1 120); do
            [ -f /var/lib/malmo/.luks-tpm-enrolled ] && break
            if systemctl is-failed --quiet malmo-tpm-enroll.service; then
                fail "malmo-tpm-enroll.service failed: $(journalctl -u malmo-tpm-enroll.service -b --no-pager 2>/dev/null | tail -20)"
            fi
            sleep 1
        done
        [ -f /var/lib/malmo/.luks-tpm-enrolled ] \
            || fail "enrollment marker never appeared after 120s (enroll service stuck?)"
        assert_tpm2_pcr7_token
        ;;
    second-boot)
        # Reaching here is itself the unattended-unseal proof: the harness
        # launched this boot's QEMU with no cryptsetup.passphrase
        # credential and the console is serial-only, so the recovery
        # keyslot was unusable — the only way root unlocked is the
        # PCR-7-bound TPM2 token enrolled on the first boot. Confirm the
        # token persisted and the enrollment marker survived the reboot.
        [ -f /var/lib/malmo/.luks-tpm-enrolled ] \
            || fail "enrollment marker missing on second boot (did first-boot enrollment not persist?)"
        assert_tpm2_pcr7_token
        # Best-effort, non-fatal: surface the initrd TPM2-unlock line into
        # the serial log for debugging. Wording varies across systemd
        # versions, so this is a hint, not an assertion — the structural
        # proof above is load-bearing.
        journalctl -b --no-pager 2>/dev/null \
            | grep -iE 'tpm2|cryptsetup' | tail -5 || true
        ;;
    combined) ;;
    *) fail "unknown phase '$PHASE'" ;;
esac

ok
