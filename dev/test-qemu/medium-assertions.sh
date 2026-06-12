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
#                unsealed root unattended via that TPM2 token. Then the
#                network-state slice (#130): NM LAN set, avahi
#                allow-interfaces sync, per-interface announcement,
#                interface-removal rewrite, IP-change replay.
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

# --- network-state slice (#130) -----------------------------------------
# Second boot only (the steady-state boot, after the LUKS/TPM checks).
# Drives /usr/lib/malmo/malmo-network-verify — the same netstate +
# avahipublisher packages cmd/host-agent-real wires, minus PAM — against
# the VM's real NetworkManager and avahi-daemon. The SSH NIC (MAC pinned
# in run-medium-tests.sh, NM-unmanaged) is never touched.
SSH_NIC_MAC="52:54:00:6d:6c:01"
MNV=/usr/lib/malmo/malmo-network-verify
AVAHI_CONF=/etc/avahi/avahi-daemon.conf
MNV_LOG=/var/log/malmo-network-verify.log

nic_ipv4() {
    ip -o -4 addr show dev "$1" 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1
}

assert_network_state() {
    command -v nmcli >/dev/null || fail "nmcli not in image"
    command -v avahi-resolve >/dev/null || fail "avahi-resolve not in image"
    [ -x "$MNV" ] || fail "$MNV missing from image"

    # Both NM NICs must reach 'connected'. DHCP on the isolated usernets
    # is quick but has no ordering against sshd, so poll.
    local nics=""
    for _i in $(seq 1 60); do
        nics="$(nmcli -t -f DEVICE,TYPE,STATE device 2>/dev/null \
            | awk -F: '$2 == "ethernet" && $3 == "connected" {print $1}' | sort)"
        [ "$(grep -c . <<<"$nics")" -eq 2 ] && break
        sleep 1
    done
    [ "$(grep -c . <<<"$nics")" -eq 2 ] \
        || fail "want 2 NM-connected ethernet NICs, have: $(nmcli device 2>&1)"
    local nic_a nic_b
    nic_a="$(sed -n 1p <<<"$nics")"
    nic_b="$(sed -n 2p <<<"$nics")"

    # The SSH NIC must be NM-unmanaged — its absence below proves the LAN
    # set is "active NM connections", not "all kernel interfaces".
    local ssh_nic
    ssh_nic="$(ip -o link | awk -v mac="$SSH_NIC_MAC" \
        'tolower($0) ~ mac {sub(/:$/, "", $2); print $2; exit}')"
    [ -n "$ssh_nic" ] || fail "no interface with SSH NIC MAC $SSH_NIC_MAC"
    grep -qx "$ssh_nic" <<<"$nics" \
        && fail "SSH NIC $ssh_nic leaked into the NM-connected set"

    # 1. netstate's LAN set == the NM set, SSH NIC excluded.
    local lan lan_names
    lan="$("$MNV" lan 2>&1)" || fail "malmo-network-verify lan: $lan"
    lan_names="$(grep -oE '"Name":"[^"]*"' <<<"$lan" | cut -d'"' -f4 | sort)"
    [ "$lan_names" = "$nics" ] \
        || fail "netstate LAN set [$lan_names] != NM set [$nics] (raw: $lan)"

    # 2. serve: the conf ships with no allow-interfaces, so the startup
    # sync exercises conf-change -> daemon restart -> republish; the
    # published name must then resolve to one of the LAN addresses.
    "$MNV" serve -slug malmotest >"$MNV_LOG" 2>&1 &
    local mnv_pid=$!
    local want_allow="allow-interfaces=${nic_a},${nic_b}"
    local got=""
    for _i in $(seq 1 30); do
        grep -qx "$want_allow" "$AVAHI_CONF" 2>/dev/null && { got=1; break; }
        kill -0 "$mnv_pid" 2>/dev/null || break
        sleep 1
    done
    [ -n "$got" ] || fail "allowlist never became '$want_allow': conf=$(grep allow-interfaces "$AVAHI_CONF" 2>/dev/null) log=$(tail -5 "$MNV_LOG" 2>/dev/null)"

    local addr_a addr_b resolved=""
    addr_a="$(nic_ipv4 "$nic_a")"
    addr_b="$(nic_ipv4 "$nic_b")"
    for _i in $(seq 1 30); do
        resolved="$(avahi-resolve -4 -n malmotest.local 2>/dev/null | awk '{print $2}')"
        [ -n "$resolved" ] && break
        sleep 1
    done
    [ -n "$resolved" ] || fail "malmotest.local never resolved: $(tail -5 "$MNV_LOG" 2>/dev/null)"
    case "$resolved" in
        "$addr_a"|"$addr_b") ;;
        *) fail "malmotest.local resolved to $resolved, want $addr_a or $addr_b" ;;
    esac

    # 3. interface removal: disconnecting nic_b must rewrite the allowlist
    # to nic_a alone (second conf-change -> restart -> republish round).
    nmcli device disconnect "$nic_b" >/dev/null 2>&1 \
        || fail "nmcli device disconnect $nic_b failed"
    got=""
    for _i in $(seq 1 30); do
        grep -qx "allow-interfaces=${nic_a}" "$AVAHI_CONF" 2>/dev/null && { got=1; break; }
        sleep 1
    done
    [ -n "$got" ] || fail "allowlist not rewritten after $nic_b disconnect: conf=$(grep allow-interfaces "$AVAHI_CONF" 2>/dev/null) log=$(tail -5 "$MNV_LOG" 2>/dev/null)"

    # 4. IP-change replay: committed entry groups hold the literal old
    # address, so only watcher -> RepublishAll makes the new one
    # resolvable. Conf is unchanged by an IP-only change (no restart).
    local conn
    conn="$(nmcli -t -f GENERAL.CONNECTION device show "$nic_a" 2>/dev/null | cut -d: -f2-)"
    [ -n "$conn" ] || fail "no NM connection on $nic_a"
    nmcli connection modify "$conn" ipv4.method manual \
        ipv4.addresses 10.0.9.99/24 ipv4.gateway "" ipv4.dns "" \
        || fail "nmcli connection modify '$conn' failed"
    nmcli connection up "$conn" >/dev/null 2>&1 \
        || fail "nmcli connection up '$conn' failed"
    resolved=""
    for _i in $(seq 1 30); do
        resolved="$(avahi-resolve -4 -n malmotest.local 2>/dev/null | awk '{print $2}')"
        [ "$resolved" = "10.0.9.99" ] && break
        sleep 1
    done
    [ "$resolved" = "10.0.9.99" ] \
        || fail "replay never re-announced 10.0.9.99 (last resolve: '$resolved'): $(tail -10 "$MNV_LOG" 2>/dev/null)"

    kill -0 "$mnv_pid" 2>/dev/null \
        || fail "malmo-network-verify serve died mid-test: $(tail -10 "$MNV_LOG" 2>/dev/null)"
    kill "$mnv_pid" 2>/dev/null
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
        assert_network_state
        ;;
    combined) ;;
    *) fail "unknown phase '$PHASE'" ;;
esac

ok
