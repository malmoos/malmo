#!/bin/bash
# Cloud boot-proof in-VM assertions (C2, #205; seed/gate scenarios C3a, #220).
# Baked into the boot-proof image at /usr/local/bin/cloud-assertions.sh and run on
# each boot by malmo-cloud-assertions.service. Writes a single verdict line to the
# serial console for dev/cloud/run-cloud-tests.sh to grep:
#
#     MALMO_CLOUD_ASSERTIONS: PASS
#     MALMO_CLOUD_ASSERTIONS: FAIL: <reason>
#
# The cloud analogue of dev/test-qemu/medium-assertions.sh. Every boot first does
# the control-plane-up proof (systemd userspace up with no failed units, PSI live,
# the baked control-plane images loaded, the four containers running, the dashboard
# + /api answering through Caddy), then asserts the hosted portal-to-box SSO gate
# (#275; ENVIRONMENT.md # Admin bootstrap — as built) for the scenario the harness
# selected via the malmo.assert credential. This box-only lane has no portal private
# key, so it asserts the gate's negative properties (the verifier is armed and
# refuses what it should); the positive owner-create + wizard path needs a real
# assertion and is the joint cloud on-ramp acceptance (cloud docs/ops/e2e-onramp.md):
#
#     unseeded         no seed → no verification key → GET /_malmo/sso ⇒ 503;
#                      POST /setup ⇒ 403 (disabled on hosted)
#     seeded           seed on disk → key ingested → a bad/unsigned token on
#                      GET /_malmo/sso ⇒ 401 (verifier armed); /setup ⇒ 403; the
#                      brain logged 'provisioning seed ingested'
#     frozen:<box-id>  reboot with a DIFFERENT seed → the dashboard + /api still
#                      serve under the ORIGINAL <box-id> (Caddy route unchanged ⇒
#                      identity frozen), and the brain does NOT re-ingest the seed
#
# On PASS the script powers the box off cleanly (the serial-only analogue of the
# medium lane's SSH `systemctl poweroff`) so the brain's SQLite box-id write flushes
# to the persisted overlay before the harness boots the next scenario.
#
# -u + pipefail but NOT -e: every check is `... || fail`. All checks are reads or
# rejected probes — no admin is created in this lane.
set -uo pipefail

SENTINEL=/dev/console
SEED=/var/lib/malmo/seed.json
# Host the dashboard + /api + /setup are served under, resolved per scenario below
# (just before step 7, once json_str is defined). An UNPROVISIONED hosted box has no
# box-id yet, so the brain installs the route under the appliance-style "malmo.local"
# apex; a SEEDED/FROZEN box installs it under "<box-id>.malmo.network" — the apex of
# the box's wildcard cert (C3b, #207). The assertion is a Host-header route match over
# localhost — no DNS/mDNS involved. Default is the unprovisioned host.
DASH_HOST=malmo.local
# Which scenario to assert — set by the harness over SMBIOS (ImportCredential=
# malmo.assert in the unit). Absent/empty ⇒ unseeded (the bare boot-proof default).
MODE="$(tr -d '\r\n' < "${CREDENTIALS_DIRECTORY:-/nonexistent}/malmo.assert" 2>/dev/null || true)"
[ -n "$MODE" ] || MODE=unseeded

emit() { echo "MALMO_CLOUD_ASSERTIONS: $1" > "$SENTINEL" 2>/dev/null || true; }
# Dump control-plane state to the serial console on failure — the brain's
# EnsureControlPlane error lives in its container log, which isn't otherwise on
# the serial the harness captures (mirrors the medium lane's install_diag).
diag() {
    {
        echo "=== MALMO_CLOUD_DIAG ==="
        echo "-- docker ps -a --"
        docker ps -a --format '{{.Names}}\t{{.Status}}\t{{.Image}}' 2>&1
        echo "-- docker network ls --"
        docker network ls 2>&1
        echo "-- malmo-ingress containers --"
        docker network inspect malmo-ingress --format '{{range .Containers}}{{.Name}}={{.IPv4Address}} {{end}}' 2>&1
        echo "-- brain networks --"
        docker inspect malmo-brain --format '{{json .NetworkSettings.Networks}}' 2>&1
        echo "-- proxy networks --"
        docker inspect malmo-docker-proxy --format '{{json .NetworkSettings.Networks}}' 2>&1
        echo "-- forwarding sysctls --"
        echo "ip_forward=$(cat /proc/sys/net/ipv4/ip_forward 2>&1) bridge-nf-call-iptables=$(cat /proc/sys/net/bridge/bridge-nf-call-iptables 2>/dev/null || echo '<module not loaded>')"
        echo "-- docker info (firewall backend / warnings) --"
        docker info 2>&1 | grep -iE "firewall|iptables|nftables|warning|cgroup version" | head
        echo "-- iptables-save (full ruleset) --"
        iptables-save 2>&1
        echo "-- brain netns -> proxy probe (route/neigh/tcp from inside the brain's network ns) --"
        bp="$(docker inspect -f '{{.State.Pid}}' malmo-brain 2>/dev/null)"
        if [ -n "$bp" ]; then
            nsenter -t "$bp" -n ip route get 172.18.0.2 2>&1
            nsenter -t "$bp" -n ip neigh 2>&1
            nsenter -t "$bp" -n bash -c '(echo >/dev/tcp/172.18.0.2/2375) 2>&1 && echo "tcp 172.18.0.2:2375 OPEN" || echo "tcp 172.18.0.2:2375 FAIL"' 2>&1
        fi
        echo "-- proxy netns (eth0 up? ip? neigh?) --"
        pp="$(docker inspect -f '{{.State.Pid}}' malmo-docker-proxy 2>/dev/null)"
        if [ -n "$pp" ]; then
            nsenter -t "$pp" -n ip -br addr 2>&1
            nsenter -t "$pp" -n ip -br link 2>&1
            nsenter -t "$pp" -n ip neigh 2>&1
            nsenter -t "$pp" -n bash -c '(echo >/dev/tcp/172.18.0.3/8080) 2>&1 && echo "proxy->brain:8080 OPEN" || echo "proxy->brain:8080 FAIL"' 2>&1
        fi
        echo "-- host bridge state (ports / fdb) --"
        ip -br link 2>&1 | grep -E 'br-|docker0|veth' || true
        bridge link show 2>&1 || true
        bridge fdb show 2>&1 | grep -E 'br-' | head -20 || true
        echo "-- networkd view of docker links (should be 'unmanaged') --"
        networkctl list 2>&1 | grep -iE 'docker|veth|br-|IDX' || true
        echo "-- loaded netfilter/bridge modules (/proc/modules) --"
        grep -iE 'br_netfilter|nf_conntrack|nf_nat|^bridge |^veth |iptable|nft|overlay' /proc/modules 2>&1 || echo "(none matched)"
        echo "-- proxy logs (tail 15) --"
        docker logs malmo-docker-proxy 2>&1 | tail -15
        echo "-- malmo-brain logs (tail 40) --"
        docker logs malmo-brain 2>&1 | tail -40
        echo "-- malmo-brain resolved profile (grep, not tail) --"
        docker logs malmo-brain 2>&1 | grep -iE 'environment profile resolved|provisioning seed|SSO stays closed' || echo "(no profile line in brain log)"
        echo "-- malmo-brain mounts (is /etc/malmo/profile bind-mounted?) --"
        docker inspect malmo-brain --format '{{range .Mounts}}{{.Source}} -> {{.Destination}}{{println}}{{end}}' 2>&1
        echo "-- host-agent journal (tail 15) --"
        journalctl -u host-agent.service -b --no-pager 2>&1 | tail -15
        echo "=== END MALMO_CLOUD_DIAG ==="
    } > "$SENTINEL" 2>&1 || true
}
fail() {
    echo "cloud-assertions FAIL: $*" >&2
    diag
    emit "FAIL: $*"
    # No poweroff on failure: leave the VM up so run-cloud-tests.sh can scrape the
    # serial diag, then kill it and keep the run artifacts.
    exit 1
}
ok() {
    emit "PASS"
    # Clean poweroff so the brain's SQLite writes (the persisted box-id) flush to
    # the qcow2 overlay before the harness boots the next scenario over it. --no-block
    # so this oneshot's ExecStart returns; systemd then runs an orderly shutdown.
    systemctl --no-block poweroff 2>/dev/null || true
    exit 0
}

echo "cloud-assertions: starting boot-proof checks (mode=${MODE})"

# --- 1. no control-plane unit has failed.
# NOTE: we deliberately do NOT gate on `systemctl is-system-running == running`:
# this script runs as a boot-transaction unit (WantedBy=multi-user.target), so
# the system stays 'starting' until the script itself finishes — gating on
# 'running' here would self-deadlock. The concrete per-unit / container / HTTP
# checks below are the real control-plane-up proof. This step is the early
# fast-fail: the unit is ordered After the control-plane units, so any that died
# during boot is already 'failed' by now.
failed="$(systemctl list-units --state=failed --no-legend --plain 2>/dev/null | awk '{print $1}')"
for u in docker.service systemd-networkd.service host-agent.service malmo-load-images.service; do
    grep -qx "$u" <<<"$failed" && fail "control-plane unit failed: $u (failed: $(tr '\n' ' ' <<<"$failed"))"
done

# --- 2. PSI is live (BUILD.md # 1 — psi=1 on the cmdline). Without it the
# ram-pressure health detector silently reads zeros; a boot test must catch that.
# NB: read the CONTENT — /proc/pressure/memory reports st_size=0 like most proc
# files, so `test -s` always sees it as empty even when PSI is active. When PSI is
# OFF the file does not exist (the directory is absent), so cat fails / is empty.
psi_mem="$(cat /proc/pressure/memory 2>/dev/null || true)"
[ -n "$psi_mem" ] || fail "/proc/pressure/memory unreadable/empty — PSI not active (psi=1 missing?)"
grep -q '^some ' <<<"$psi_mem" || fail "/proc/pressure/memory malformed: $psi_mem"

# --- 3. the single NIC came up via systemd-networkd DHCP (no NetworkManager).
command -v nmcli >/dev/null 2>&1 && fail "NetworkManager present — hosted must bring the NIC up via networkd only"
nwd_state="$(systemctl is-active systemd-networkd.service 2>&1 || true)"
[ "$nwd_state" = active ] || fail "systemd-networkd is '$nwd_state' (want active)"

# --- 4. docker up and the four control-plane images loaded from the baked bundle.
docker_state="$(systemctl is-active docker.service 2>&1 || true)"
[ "$docker_state" = active ] || fail "docker.service is '$docker_state' (want active)"
for _i in $(seq 1 60); do
    [ -f /var/lib/malmo/.control-plane-images-loaded ] && break
    systemctl is-failed --quiet malmo-load-images.service && \
        fail "malmo-load-images.service failed: $(journalctl -u malmo-load-images.service -b --no-pager 2>/dev/null | tail -10)"
    sleep 1
done
[ -f /var/lib/malmo/.control-plane-images-loaded ] || fail "control-plane image-load marker never appeared after 60s"
cp_images="$(docker images --format '{{.Repository}}' 2>&1 || true)"
# Hosted bakes the caddy-dns/acmedns Caddy build (malmo-caddy-acmedns), not stock
# caddy:2-alpine — the wildcard cert needs the DNS-01 module (os #207/C3b).
for repo in malmo-brain malmo-ui malmo-caddy-acmedns tecnativa/docker-socket-proxy; do
    grep -qx "$repo" <<<"$cp_images" || fail "baked image '$repo' not loaded (have: $(tr '\n' ' ' <<<"$cp_images"))"
done

# --- 5. the brain brought the control plane up: four containers running. The
# brain bootstrap + compose up race this unit, so poll.
want="malmo-brain malmo-caddy malmo-ui malmo-docker-proxy"
running=""
for _i in $(seq 1 120); do
    running="$(docker ps --format '{{.Names}}' 2>/dev/null | tr '\n' ' ')"
    miss=0
    for c in $want; do grep -qw "$c" <<<"$running" || miss=1; done
    [ "$miss" = 0 ] && break
    sleep 1
done
for c in $want; do
    grep -qw "$c" <<<"$running" || fail "control-plane container '$c' not running after 120s (have: $running)"
done

# --- 6. proxy boundary: the brain reaches Docker only through the socket-proxy,
# never the raw socket (CONTROL_PLANE.md # Docker socket exposure).
brain_sock="$(docker inspect malmo-brain --format '{{range .Mounts}}{{println .Source}}{{end}}' 2>/dev/null | grep -c 'docker.sock' || true)"
[ "$brain_sock" = 0 ] || fail "raw docker.sock mounted into malmo-brain (proxy boundary breached)"

# --- 6b. metadata SSRF block (#251): forwarded / app-container egress to the cloud
# metadata endpoint (169.254.169.254) is dropped, while the host-root first-boot
# seed fetch (OUTPUT path) is not. The QEMU lane delivers the seed over SMBIOS, so
# there is no real 169.254.169.254 server to positively probe host reachability —
# instead assert the rule's SHAPE (a forward hook, never an output hook, matching
# the metadata IP) plus that a real container packet HITS the drop: probe from
# inside the brain's netns (a genuine forward-path source over malmo-ingress) and
# require the drop counter to increment. Together: containers blocked, the host
# OUTPUT path structurally untouched (so the seed fetch still works).
fw_rules="$(nft list table inet malmo_metadata 2>/dev/null)" || \
    fail "metadata firewall: nft table 'inet malmo_metadata' absent — egress block not loaded (#251; malmo-metadata-firewall.service is $(systemctl is-active malmo-metadata-firewall.service 2>&1))"
grep -q 'hook forward' <<<"$fw_rules" || \
    fail "metadata firewall: drop chain is not a forward hook (#251) — rules: $(tr '\n' ' ' <<<"$fw_rules")"
grep -q 'hook output' <<<"$fw_rules" && \
    fail "metadata firewall: an output hook is present — would break the host-root first-boot seed fetch (#251)"
grep -q '169\.254\.169\.254' <<<"$fw_rules" || \
    fail "metadata firewall: no rule matches 169.254.169.254 (#251) — rules: $(tr '\n' ' ' <<<"$fw_rules")"

# Drop-counter probe: read packets matched before/after a container-origin connect.
md_packets() { nft list table inet malmo_metadata 2>/dev/null | awk '/169\.254\.169\.254/{for(i=1;i<=NF;i++) if($i=="packets") print $(i+1)}' | head -1; }
md_pid="$(docker inspect -f '{{.State.Pid}}' malmo-brain 2>/dev/null)"
[ -n "$md_pid" ] || fail "metadata firewall: malmo-brain pid not found for the egress probe (#251)"
# The live drop-counter probe needs the HOST to have a route to the metadata IP, so
# the container's forwarded packet is actually routed (and so traverses the forward
# hook) rather than rejected at the routing stage. The host does on a real cloud (it
# reaches 169.254.169.254 to fetch the seed) and under QEMU slirp (DHCP hands out a
# default route that covers it). If a routeless lane ever lacks it, fall back to the
# shape assertions above (rule loaded + forward-only) rather than a false-fail.
if ip route get 169.254.169.254 >/dev/null 2>&1; then
    md_before="$(md_packets)"
    # A DROP gives no RST, so the connect would hang — bound it; the SYN is emitted
    # (and counted) immediately, so 3s is ample. The probe is EXPECTED not to connect.
    # stderr is NOT suppressed so nsenter infrastructure failures (stale PID, permission
    # denied) appear in the serial log and are distinguishable from "DROP working".
    timeout 3 nsenter -t "$md_pid" -n bash -c 'exec 3<>/dev/tcp/169.254.169.254/80' 2>&1 || true
    md_after="$(md_packets)"
    [ -n "$md_before" ] && [ -n "$md_after" ] || fail "metadata firewall: could not read the drop counter (#251)"
    [ "$md_after" -gt "$md_before" ] || \
        fail "metadata firewall: a container probe to 169.254.169.254 did NOT hit the forward DROP (counter $md_before -> $md_after) — SSRF still open (#251)"
    echo "cloud-assertions: metadata SSRF block (#251) — forward-hook DROP loaded; container egress to 169.254.169.254 dropped (counter $md_before -> $md_after)"
else
    echo "cloud-assertions: metadata SSRF block (#251) — forward-hook DROP loaded (shape verified); live drop-probe skipped — host has no route to 169.254.169.254 in this lane"
fi

# HTTP over Caddy :80 via bash /dev/tcp (no curl in the lean image). Same idiom
# as medium-assertions. Prints the status line; HTTP/1.0 + Connection: close so
# the server closes the stream.
http_status() { # PATH HOST -> status line
    exec 3<>/dev/tcp/127.0.0.1/80 || return 1
    printf 'GET %s HTTP/1.0\r\nHost: %s\r\nConnection: close\r\n\r\n' "$1" "$2" >&3
    head -1 <&3
    exec 3>&- 3<&-
}
http_post_status() { # PATH HOST JSON -> status line
    local body="$3" len
    len="$(printf '%s' "$body" | wc -c | tr -d ' ')"
    exec 3<>/dev/tcp/127.0.0.1/80 || return 1
    printf 'POST %s HTTP/1.0\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s' \
        "$1" "$2" "$len" "$body" >&3
    head -1 <&3
    exec 3>&- 3<&-
}
# Extract a JSON string field's value from a compact one-line document. The seed
# the harness generates is compact and its fields (box_id, assertion_verification_key)
# are plain strings with no embedded quotes, so a targeted sed is sufficient (no
# jq in the lean image).
json_str() { # FILE KEY -> value
    sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$1" | head -1
}

# Wait for a line matching a fixed pattern in the brain's container log. The brain
# writes each milestone to stdout ONCE at startup, but `docker logs` reads the
# daemon's json-file, which buffers the container's stream before flushing to disk
# — under a loaded TCG boot that flush can lag the brain's own log timestamp by
# several seconds. A single-shot (or short) grep therefore loses a genuine race:
# the line is emitted but not yet readable (a seeded boot's milestone has been seen
# in the brain log 3s before the check that "failed" to find it). Poll generously.
# The lag is bounded (seconds), so the default 90s window makes a miss effectively
# impossible; the happy path breaks on the first read, so the wide window costs no
# real time. Callers pair this with a deterministic co-signal (serving under the
# box-id host, :443 bound) that already proves the milestone causally happened —
# this only pins that the exact code path logged it. Returns 0 on match.
wait_brain_log() { # pattern [timeout_s]
    local pat="$1" timeout="${2:-90}" _i
    for _i in $(seq 1 "$timeout"); do
        docker logs malmo-brain 2>&1 | grep -qF "$pat" && return 0
        sleep 1
    done
    return 1
}

# Resolve the Host the brain actually serves the dashboard under for this scenario
# (see DASH_HOST above). A provisioned box (seeded/frozen) serves at its wildcard apex
# "<box-id>.malmo.network", not "malmo.local" — so steps 7–9 must probe that host or
# Caddy's catch-all answers 404. Seeded
# reads the box-id from the just-materialized seed; frozen uses the persisted identity
# carried in MODE (the brain ignores this boot's re-delivered seed, so the route stays
# under the original box-id).
case "$MODE" in
seeded)   DASH_HOST="$(json_str "$SEED" box_id).malmo.network" ;;
frozen:*) DASH_HOST="${MODE#frozen:}.malmo.network" ;;
esac
echo "cloud-assertions: probing control plane at Host=$DASH_HOST (mode=$MODE)"

# --- 7. the dashboard SPA answers through Caddy (the control-plane-up proof).
# The brain flips/installs the dashboard route a beat after Caddy comes up, so poll.
spa=""
for _i in $(seq 1 60); do
    spa="$(http_status / "$DASH_HOST" 2>/dev/null || true)"
    grep -q ' 200' <<<"$spa" && break
    sleep 1
done
grep -q ' 200' <<<"$spa" || fail "dashboard SPA not reachable through Caddy: status='$spa'"

# --- 8. /api routes to the brain (not the catch-all). /api/v1/me is a real brain
# route: 200 (with the setup flag) or 401. A 404 = catch-all swallowed it; a 502
# = route installed but the brain's listener isn't up yet, so poll.
api=""
for _i in $(seq 1 60); do
    api="$(http_status /api/v1/me "$DASH_HOST" 2>/dev/null || true)"
    grep -qE ' (200|401)' <<<"$api" && break
    sleep 1
done
grep -qE ' (200|401)' <<<"$api" || fail "/api not routed to the brain through Caddy: status='$api'"

# --- 9. the hosted portal-to-box SSO gate (#275; ENVIRONMENT.md # Admin bootstrap).
# The hosted box bootstraps its first admin through the portal-to-box SSO handshake,
# not a /setup secret. /setup is disabled on hosted, and GET /_malmo/sso verifies a
# portal-signed ownership assertion against the seed-delivered verification key.
# This lane has no portal private key, so it asserts the *negative* gate properties
# (the verifier is armed and refuses every token it shouldn't accept); the positive
# path — a valid assertion → owner auto-create → session → wizard completion — needs
# the real portal key and is the joint cloud on-ramp acceptance (cloud
# docs/ops/e2e-onramp.md), not this box-only boot lane.

# /setup is disabled on every hosted boot (the owner uses SSO): 403, never the
# appliance's open empty-box 200/409. Proof the profile marker reached the container.
setup=""
for _i in $(seq 1 30); do
    setup="$(http_post_status /api/v1/setup "$DASH_HOST" \
        '{"username":"probe","password":"probe-pw-once"}' 2>/dev/null || true)"
    grep -qE ' (403|503|409|200)' <<<"$setup" && break
    sleep 1
done
grep -q ' 403' <<<"$setup" || fail "hosted /setup not disabled: status='$setup' (want 403; an appliance-mode brain would 409/200 — profile marker not reaching the container?)"
echo "cloud-assertions: hosted /setup disabled (403 — bootstrap is via SSO)"

case "$MODE" in
unseeded)
    # No seed ingested → no verification key → GET /_malmo/sso returns 503, NOT a
    # redirect or a fall-through. Proof the SSO gate stays closed until a seed lands.
    sso="$(http_status '/_malmo/sso?token=x.y' "$DASH_HOST" 2>/dev/null || true)"
    grep -q ' 503' <<<"$sso" || fail "unseeded /_malmo/sso gate not armed: status='$sso' (want 503, unprovisioned)"
    echo "cloud-assertions: hosted SSO gate armed (503, unprovisioned)"
    ;;
seeded)
    [ -f "$SEED" ] || fail "seeded mode but $SEED absent (seed materializer did not run?)"
    box_id="$(json_str "$SEED" box_id)"
    key="$(json_str "$SEED" assertion_verification_key)"
    [ -n "$box_id" ] && [ -n "$key" ] || fail "could not read box_id/assertion_verification_key from $SEED"

    # The seed's verification key was ingested: GET /_malmo/sso now runs the verifier
    # and a syntactically-valid-but-unsigned token fails the signature check → 401
    # (not 503). Proof the key loaded and the verifier is wired on this box. Poll:
    # the route is served (step 8 passed) but the verifier arms a beat behind the
    # listener, so a single-shot read can catch a transient 503 before the key loads.
    sso=""
    for _i in $(seq 1 30); do
        sso="$(http_status '/_malmo/sso?token=ZmFrZQ.ZmFrZXNpZw' "$DASH_HOST" 2>/dev/null || true)"
        grep -q ' 401' <<<"$sso" && break
        sleep 1
    done
    grep -q ' 401' <<<"$sso" || fail "seeded /_malmo/sso with a bad token: status='$sso' (want 401 — key loaded, signature rejected)"
    echo "cloud-assertions: hosted SSO verifier armed (bad token 401, key loaded from seed; box_id=$box_id)"

    # The synchronous seed ingestion ran before the brain served — in fact it ran
    # before steps 7-8 above could pass: the dashboard + /api answered under
    # DASH_HOST=<box_id>.malmo.network, and the brain only installs that box-id route
    # AFTER reading the seed and learning its box-id (cmd/brain loadHostedEnvironment).
    # So the milestone has causally already been logged by now; this confirms the
    # exact line was emitted. Use the flush-lag-tolerant waiter — a single-shot grep
    # loses the docker json-log race even though the line is present moments later.
    wait_brain_log 'provisioning seed ingested' || \
        fail "brain did not log 'provisioning seed ingested' on the seeded boot"
    echo "cloud-assertions: seed ingested (box_id=$box_id persisted)"

    # The seed's complete acme-dns enrollment drives the brain's wildcard-TLS pass
    # (cmd/brain EnsureWildcardTLS): it configures Caddy's acme-dns DNS-01 issuer for
    # the apex + "*.$box_id.malmo.network" and adds the :443 listener. Real issuance
    # can't run here — air-gapped (restrict=on), no reach to acme-dns or Let's Encrypt
    # — so no cert is obtained; what this asserts is that the brain REACHES and APPLIES
    # the config and :443 actually binds. That application is the exact step a booted
    # hosted box was failing (#278: box-id site unrouted, :443 never bound, no wildcard
    # cert), and the air-gapped lane never exercised it before — the prior seed carried
    # no enrollment, so EnsureWildcardTLS was skipped.

    # Two proofs the brain APPLIED the wildcard-TLS config. Order matters: assert the
    # deterministic socket signal FIRST, then the log line. EnsureWildcardTLS binds
    # :443 as part of phase 1 and logs "caddy: wildcard TLS configured" in the same
    # synchronous call, so once :443 is listening the milestone has already been
    # emitted — the log grep is then a same-call confirmation the daemon has had ample
    # time to flush, not a race we start cold.

    # (a) The :443 listener actually bound. A plain TCP connect to Caddy's HTTPS port
    # succeeds even with no cert (the TLS handshake would fail, but the socket is
    # listening) — the ":443 never binds" symptom from #278, asserted positively. Poll:
    # the listener is patched in a beat after the config PUT.
    bound=""
    for _i in $(seq 1 30); do
        if timeout 3 bash -c 'exec 3<>/dev/tcp/127.0.0.1/443' 2>/dev/null; then bound=1; break; fi
        sleep 1
    done
    [ -n "$bound" ] || fail "Caddy :443 listener not bound on the seeded boot (#278 — :443 never came up)"
    echo "cloud-assertions: Caddy :443 listener bound"

    # (b) The brain logged the wildcard-TLS milestone. Flush-lag-tolerant waiter: the
    # line is emitted once during the (now-proven-complete) phase-1 call, and a
    # single-shot grep can still lose the race to the docker json-log flush.
    wait_brain_log 'caddy: wildcard TLS configured' || \
        fail "brain did not configure wildcard TLS on the seeded boot (#278 — EnsureWildcardTLS not reached/applied)"
    echo "cloud-assertions: wildcard TLS configured (acme-dns DNS-01 issuer + :443 set for *.$box_id.malmo.network)"
    ;;
frozen:*)
    expect="${MODE#frozen:}"
    [ -n "$expect" ] || fail "frozen mode missing the expected box-id (MODE='$MODE')"
    # A DIFFERENT seed was delivered this boot, but the brain's identity is frozen in
    # SQLite: it loads the persisted box-id and ignores the new seed. Two proofs that
    # need no admin session:
    #   1. The dashboard + /api checks above ran against DASH_HOST=<expect>.malmo.network
    #      (the ORIGINAL box-id) and passed — if a re-delivered seed had re-keyed the
    #      box, Caddy's dashboard route would be under this boot's box-id and those
    #      probes would have 404'd. So serving under <expect> *is* the frozen-identity
    #      proof.
    #   2. This boot does NOT re-ingest: the brain loads the persisted identity and
    #      never logs 'provisioning seed ingested' (that line is first-boot-only).
    sso="$(http_status '/_malmo/sso?token=ZmFrZQ.ZmFrZXNpZw' "$DASH_HOST" 2>/dev/null || true)"
    grep -q ' 401' <<<"$sso" || fail "frozen mode: /_malmo/sso bad token status='$sso' (want 401 — verifier still armed from the persisted key)"
    if docker logs malmo-brain 2>&1 | grep -q 'provisioning seed ingested'; then
        fail "frozen mode: brain re-ingested a seed — a re-delivered seed must be ignored on a frozen-identity boot"
    fi
    # Confirm the on-disk seed really is this boot's distinct seed (a no-op overwrite
    # would make the frozen assertion vacuous). A warning, not a failure: the identity
    # proof above is the real signal.
    if [ -f "$SEED" ]; then
        disk_box="$(json_str "$SEED" box_id)"
        [ -n "$disk_box" ] && [ "$disk_box" = "$expect" ] && \
            echo "cloud-assertions: WARN frozen seed.json box_id ($disk_box) == frozen identity — re-delivery not distinct" >&2
    fi
    echo "cloud-assertions: frozen identity held across reboot — served under box_id $expect, re-delivered seed ignored"
    ;;
*)
    fail "unknown assert mode '$MODE'"
    ;;
esac

echo "cloud-assertions: control plane up, dashboard + /api served through Caddy; gate scenario '$MODE' OK"
ok
