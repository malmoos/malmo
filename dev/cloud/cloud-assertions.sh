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

# --- 1b. root grown to fill the provider disk. malmo-grow-root.service runs
# systemd-repart at boot to extend the baked 8 GiB root partition to the whole
# disk, then runs systemd-growfs directly to grow the ext4 inside it (issue: a
# hosted box left on 8 GiB has docker image storage + the brain's SQLite store
# sharing that volume, so one app install fills it and 500s login). This QEMU
# boot-proof disk is fixed-size with no spare space, so both steps are a no-op
# here — but the unit must still complete cleanly, which proves systemd-repart
# and systemd-growfs are present in the lean image and the unit is wired. Real
# full-disk growth (partition AND filesystem) can only be proven on a live
# provider box (the cloud on-ramp), not this lane — a prior version of this unit
# passed this exact boot-proof while only growing the partition and leaving the
# filesystem at 8 GiB, because the growfs step was missing.
command -v systemd-repart >/dev/null 2>&1 || fail "systemd-repart missing from the lean image — malmo-grow-root cannot grow the root disk"
[ -x /usr/lib/systemd/systemd-growfs ] || fail "systemd-growfs missing from the lean image — malmo-grow-root cannot grow the root filesystem"
grow_state="$(systemctl is-active malmo-grow-root.service 2>&1 || true)"
# Assert the unit actually completed (active, held by RemainAfterExit) — not merely
# "not failed". An inactive/unknown state means the .wants symlink was dropped or the
# unit was skipped, i.e. the grow never ran; that must fail the proof, not pass it.
[ "$grow_state" = active ] || fail "malmo-grow-root.service did not complete successfully (state=$grow_state): $(journalctl -u malmo-grow-root.service -b --no-pager 2>/dev/null | tail -10)"
echo "cloud-assertions: root-grow unit ok (state=$grow_state; systemd-repart + systemd-growfs present and wired — this lane cannot prove real growth, only that both steps ran)"

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
access)   DASH_HOST="$(json_str "$SEED" box_id).malmo.network" ;;
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
# For the unseeded/seeded/frozen boots this lane has no portal private key, so it
# asserts the *negative* gate properties (the verifier is armed and refuses every
# token it shouldn't accept); the positive path against the REAL production portal —
# owner auto-create → session → wizard — is the joint cloud on-ramp acceptance (cloud
# docs/ops/e2e-onramp.md), not this box-only boot lane. The `access` boot (#308) is
# the deliberate exception: it seeds a *test-portal* key whose private half the
# harness holds, so it drives the positive session path here to prove the per-app
# forward-auth access modes (see the access case below).

# /setup is disabled on every hosted boot (the owner uses SSO): 403, never the
# appliance's open empty-box 200/409. Proof the profile marker reached the container.
# Break only on a definitive brain answer (403, or the appliance-mode 409/200 we
# want to catch below) — NOT on a 502/503. Those are Caddy's "no ready upstream for
# /api" during the first second after the stack comes up (the brain's listener /
# dashboard route land a beat behind the container), a transient this poll must ride
# through exactly as the /api/v1/me poll above does. Breaking on a transient 503 was
# a latent race: the box is correct (the brain returns 403 once its upstream is
# ready), but a probe that caught the startup window failed the proof. A genuinely
# stuck /setup still fails — the loop exhausts its 30s window holding the last 503,
# and the 403 assertion below rejects it.
setup=""
for _i in $(seq 1 30); do
    setup="$(http_post_status /api/v1/setup "$DASH_HOST" \
        '{"username":"probe","password":"probe-pw-once"}' 2>/dev/null || true)"
    grep -qE ' (403|409|200)' <<<"$setup" && break
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
access)
    # Per-app forward-auth access-mode proof (#308), the positive path the box-only
    # SSO gate above can't reach: it needs a real owner session, so this scenario is
    # seeded with a TEST-PORTAL key (the harness holds the matching private key —
    # dev/cloud/mkassertion) and the harness delivers a valid owner assertion over
    # the malmo.sso_token credential. The box verifies it exactly as a real portal
    # assertion, auto-creates the owner, and mints both cookies. We then install a
    # real app and drive every access mode end-to-end through the box's own Caddy:
    #   - restricted (the hosted default): unauthenticated ⇒ 302 to the box login;
    #     the owner's forward-auth cookie ⇒ proxied through with no second login;
    #   - public (after the exposure toggle): reachable with no session;
    #   - the Cookie header never reaches the app upstream in EITHER mode (#306's
    #     whole-header strip — the load-bearing anti-leak invariant).
    [ -f "$SEED" ] || fail "access mode but $SEED absent (seed materializer did not run?)"
    box_id="$(json_str "$SEED" box_id)"
    [ -n "$box_id" ] || fail "access mode: could not read box_id from $SEED"
    apex="${box_id}.malmo.network"
    app_host="whoami.${apex}"

    # The signed owner assertion the harness minted with the test-portal private key,
    # delivered over SMBIOS (ImportCredential=malmo.sso_token in the unit).
    sso_token="$(tr -d '\r\n' < "${CREDENTIALS_DIRECTORY:-/nonexistent}/malmo.sso_token" 2>/dev/null || true)"
    [ -n "$sso_token" ] || fail "access mode: malmo.sso_token credential missing (harness did not mint/deliver the owner assertion)"

    # Full-response HTTP helpers (headers + body) over Caddy :80 — the status-only
    # helpers above can't see Set-Cookie / Location / the whoami echo body. Scoped to
    # this mode; ${N:-} keeps them safe under `set -u` when a cookie arg is omitted.
    full_get() { # PATH HOST [COOKIE] -> full response
        exec 3<>/dev/tcp/127.0.0.1/80 || return 1
        if [ -n "${3:-}" ]; then
            printf 'GET %s HTTP/1.0\r\nHost: %s\r\nCookie: %s\r\nConnection: close\r\n\r\n' "$1" "$2" "$3" >&3
        else
            printf 'GET %s HTTP/1.0\r\nHost: %s\r\nConnection: close\r\n\r\n' "$1" "$2" >&3
        fi
        cat <&3
        exec 3>&- 3<&-
    }
    full_send() { # METHOD PATH HOST COOKIE JSON -> full response
        local len; len="$(printf '%s' "$5" | wc -c | tr -d ' ')"
        exec 3<>/dev/tcp/127.0.0.1/80 || return 1
        printf '%s %s HTTP/1.0\r\nHost: %s\r\nCookie: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s' \
            "$1" "$2" "$3" "$4" "$len" "$5" >&3
        cat <&3
        exec 3>&- 3<&-
    }
    status_of() { head -1 <<<"$1" | tr -d '\r'; }
    # Extract NAME=VALUE from the first Set-Cookie carrying NAME (drops attributes).
    cookie_val() { grep -i '^Set-Cookie:' <<<"$1" | grep -oE "$2=[^;[:space:]]+" | head -1; }

    # 1. portal-to-box SSO, driven ONCE (the jti is single-use — a retry replays and
    #    401s). Steps 7-9 already proved the control plane up + the verifier armed, so
    #    a valid token now lands the owner. Expect 303 + both cookies: the host-only
    #    session and the Domain-scoped forward-auth credential.
    sso_resp="$(full_get "/_malmo/sso?token=${sso_token}" "$apex" 2>/dev/null || true)"
    sso_status="$(status_of "$sso_resp")"
    grep -q ' 303' <<<"$sso_status" \
        || fail "access: SSO landing did not 303 to the dashboard (owner auto-create failed?): status='$sso_status'"
    session_cookie="$(cookie_val "$sso_resp" malmo_session)"
    fa_cookie="$(cookie_val "$sso_resp" malmo_forward_auth)"
    [ -n "$session_cookie" ] || fail "access: no malmo_session cookie from the SSO landing"
    [ -n "$fa_cookie" ] || fail "access: no malmo_forward_auth cookie from the SSO landing"
    echo "cloud-assertions: SSO owner session established (session + forward-auth cookies minted; box_id=$box_id)"

    # 2. install whoami air-gapped: offline mode trusts the catalog-promised digest of
    #    the docker-loaded image (no pull). 202 starts the async install job.
    inst_status="$(status_of "$(full_send POST /api/v1/apps "$apex" "$session_cookie" '{"manifest_id":"whoami","scope":"personal"}' 2>/dev/null)")"
    case "$inst_status" in
        *" 202"*|*" 200"*) ;;
        *) fail "access: install whoami did not start: status='$inst_status' (offline bundle/catalog cache missing?)" ;;
    esac

    # 3. RESTRICTED (the hosted default), with the owner's forward-auth cookie ⇒ the
    #    app proxies through. Poll until whoami actually answers (install + compose up
    #    + the route flip from splash to app race this): a 200 whose body is the
    #    whoami echo (Hostname:) means the whole transaction converged AND the
    #    forward_auth verify let the owner through. Send an extra throwaway cookie so
    #    the strip assertion below proves a WHOLE-header strip, not just the fa cookie.
    a_resp=""; a_status=""
    for _i in $(seq 1 150); do
        a_resp="$(full_get / "$app_host" "${fa_cookie}; probe=leakcheck" 2>/dev/null || true)"
        a_status="$(status_of "$a_resp")"
        grep -q ' 200' <<<"$a_status" && grep -qi 'Hostname:' <<<"$a_resp" && break
        sleep 1
    done
    grep -q ' 200' <<<"$a_status" && grep -qi 'Hostname:' <<<"$a_resp" \
        || fail "access: restricted app with the owner forward-auth cookie never proxied through to whoami after 150s: status='$a_status'"
    grep -qiE '^X-Malmo-User:' <<<"$a_resp" \
        || fail "access: forward-auth identity header X-Malmo-User was not forwarded to the app upstream"
    grep -qiE '^Cookie:' <<<"$a_resp" \
        && fail "access: COOKIE LEAK (restricted) — the app upstream received a Cookie header; the #306 whole-header strip is broken"
    echo "cloud-assertions: restricted app proxies the owner through with no second login (identity forwarded, Cookie stripped)"

    # 3a. RESTRICTED, NO session ⇒ 302 to the box login. Now that the app has
    #     converged, an unauthenticated GET exercises the forward_auth gate's closed
    #     path: the brain verify 401s and Caddy turns it into a redirect to the box
    #     dashboard (https://<box-id>.malmo.network/, the login).
    n_resp="$(full_get / "$app_host" 2>/dev/null || true)"
    n_status="$(status_of "$n_resp")"
    grep -q ' 302' <<<"$n_status" \
        || fail "access: restricted app without a session did not 302 to the box login: status='$n_status'"
    grep -iE "^Location: *https://${apex}/" <<<"$n_resp" >/dev/null \
        || fail "access: restricted-app 302 Location is not the box login: $(grep -i '^Location:' <<<"$n_resp" | tr -d '\r')"
    echo "cloud-assertions: restricted app gates an unauthenticated request (302 → box login)"

    # 4. flip to PUBLIC via the exposure toggle (owner session; the endpoint is
    #    hosted-only + owner-or-admin). Resolve the instance id from the running
    #    container's malmo.instance_id label (whoami is FROM-scratch — no shell to
    #    exec — so read it host-side, as the medium lane does).
    cname="$(docker ps --format '{{.Names}}' | grep -i whoami | head -1)"
    [ -n "$cname" ] || fail "access: no running whoami container to resolve the instance id (docker ps: $(docker ps --format '{{.Names}}' | tr '\n' ' '))"
    inst_id="$(docker inspect "$cname" --format '{{ index .Config.Labels "malmo.instance_id" }}' 2>/dev/null)"
    [ -n "$inst_id" ] || fail "access: whoami container $cname has no malmo.instance_id label"
    exp_status="$(status_of "$(full_send PUT "/api/v1/apps/${inst_id}/exposure" "$apex" "$session_cookie" '{"exposure":"public"}' 2>/dev/null)")"
    grep -q ' 200' <<<"$exp_status" || fail "access: exposure toggle to public failed: status='$exp_status'"

    # 4a. PUBLIC, NO session ⇒ reachable (200), no gate. The route flip from
    #     forward_auth to a bare proxy lands a beat after the PUT, so poll.
    p_resp=""; p_status=""
    for _i in $(seq 1 30); do
        p_resp="$(full_get / "$app_host" 2>/dev/null || true)"
        p_status="$(status_of "$p_resp")"
        grep -q ' 200' <<<"$p_status" && grep -qi 'Hostname:' <<<"$p_resp" && break
        sleep 1
    done
    grep -q ' 200' <<<"$p_status" && grep -qi 'Hostname:' <<<"$p_resp" \
        || fail "access: public app not reachable without a session after the toggle: status='$p_status'"
    echo "cloud-assertions: public app reachable with no session (200)"

    # 4b. PUBLIC + a forward-auth cookie ⇒ STILL stripped before the app upstream. A
    #     public app must never receive the Domain-scoped cookie, or it could replay
    #     it against the owner's restricted apps — the reason #306 strips the whole
    #     Cookie header on every hosted route, public included.
    pl_resp="$(full_get / "$app_host" "${fa_cookie}; probe=leakcheck" 2>/dev/null || true)"
    grep -qi 'Hostname:' <<<"$pl_resp" || fail "access: public-app cookie-leak probe did not reach whoami"
    grep -qiE '^Cookie:' <<<"$pl_resp" \
        && fail "access: COOKIE LEAK (public) — the app upstream received a Cookie header; the #306 whole-header strip is broken"
    echo "cloud-assertions: public app also strips the Cookie header (no forward-auth cookie leaks to a public upstream)"

    echo "cloud-assertions: hosted per-app access modes verified end-to-end (restricted gate + owner proxy-through, public reachability, Cookie strip in both modes)"
    ;;
*)
    fail "unknown assert mode '$MODE'"
    ;;
esac

echo "cloud-assertions: control plane up, dashboard + /api served through Caddy; gate scenario '$MODE' OK"
ok
