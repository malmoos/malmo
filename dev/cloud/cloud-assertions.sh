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
#     access           a valid owner assertion → session → per-app forward-auth
#                      access modes end-to-end through real Caddy (#308)
#     update           a real control-plane update and a real failed-update-then-
#                      revert, pulled by digest from a registry inside the guest
#                      (#382), then the same update again driven by an update-target
#                      source rather than an admin (#401) — including a refusal of an
#                      answer that is not pinned to a digest. The only scenario that
#                      changes the box's images.
#
# On PASS the script powers the box off cleanly (the serial-only analogue of the
# medium lane's SSH `systemctl poweroff`) so the brain's SQLite box-id write flushes
# to the persisted overlay before the harness boots the next scenario.
#
# -u + pipefail but NOT -e: every check is `... || fail`. The unseeded/seeded/frozen
# scenarios only read or probe. The access and update scenarios do change the box —
# each on its own throwaway overlay — because the thing under test is a mutation:
# an owner session plus an app install (access), and the box's own control-plane
# images (update).
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
        # The update boot (#382) fails on state that lives in files and in
        # host-agent's log, neither of which the blocks above show. A red update
        # boot has to be diagnosable from this serial dump alone.
        echo "-- control-plane declaration (images.json) --"
        cat /var/lib/malmo/control-plane/images.json 2>&1 || true
        echo "-- control-plane compose (image lines) --"
        grep -n 'image:' /var/lib/malmo/control-plane/compose.yml 2>&1 || true
        echo "-- brain snapshots --"
        ls -la /var/lib/malmo/brain-snapshots 2>&1 || true
        echo "-- host-agent update lines --"
        journalctl -u host-agent.service -b --no-pager 2>&1 | grep -iE 'system-update|control plane|revert|pull|snapshot' | tail -25
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

# --- 1c. the baked host-agent carries a real build stamp (BUILD.md # Versioning:
# "every build stamps two fields"). An unstamped build reports internal/version's
# "dev" default; the brain's minimumAgentVersion check hands that to semver, an
# unparseable core sorts before every valid version, and the box raises
# version-mismatch — blocking app installs on a box that is otherwise perfectly
# healthy. v0.4.0 shipped exactly that: stage-control-plane.sh built the agent
# without the Makefile's -ldflags, so the image's agent reported "dev".
#
# This has to be asserted on a BUILT IMAGE, which makes this lane the only place
# it can be caught. No unit test can: the stamp is applied by the build command,
# so a `go test` binary is unstamped by construction and asserting anything about
# version.Version in one only ever pins the default. The release workflow's
# tag-vs-VERSION assert doesn't reach it either — it checks the file, not what
# landed in the binary.
ha_version="$(/usr/lib/malmo/host-agent-real --version 2>&1 || true)"
grep -qE '^malmo [0-9]+\.[0-9]+\.[0-9]+ ' <<<"$ha_version" || \
    fail "baked host-agent is not version-stamped: --version reports '$ha_version' (want 'malmo X.Y.Z (g<sha>)'; an unstamped 'dev' build raises version-mismatch and blocks app installs on a healthy box)"
echo "cloud-assertions: host-agent build stamp ok ($ha_version)"

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
# Full-response HTTP helpers (headers + body) over Caddy :80 — the status-only
# helpers above can't see Set-Cookie / Location / a response body. Used by the
# access boot (cookies, the whoami echo) and the update boot (the job id and the
# job's JSON status). ${N:-} keeps them safe under `set -u` when a cookie arg is
# omitted.
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
# The WHOLE raw Set-Cookie line for NAME, attributes included — cookie_val above
# deliberately drops them, but the Domain attribute is exactly what the two-cookie
# safety model rests on, so it has to be asserted, not just carried.
cookie_line() { grep -i '^Set-Cookie: *'"$2"'=' <<<"$1" | head -1 | tr -d '\r'; }

# Status line from an arbitrary address, not just Caddy on :80 — the update boot
# probes the brain container's own /healthz and the in-guest registry, neither of
# which is reachable through Caddy.
http_status_addr() { # IP PORT PATH -> status line
    exec 3<>"/dev/tcp/$1/$2" || return 1
    printf 'GET %s HTTP/1.0\r\nHost: %s\r\nConnection: close\r\n\r\n' "$3" "$1" >&3
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
# Same, over a document already in a variable — an HTTP response, headers and all.
# The update boot reads the job id out of the trigger's 202 body this way.
json_str_of() { # DOC KEY -> value
    sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" <<<"$1" | head -1
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
update)   DASH_HOST="$(json_str "$SEED" box_id).malmo.network" ;;
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
    #   - malmo_forward_auth never reaches the app upstream in EITHER mode, while an
    #     app's own cookie DOES (#335's per-cookie strip — the whole-header delete it
    #     replaced made every third-party app with a browser login unusable, #306).
    [ -f "$SEED" ] || fail "access mode but $SEED absent (seed materializer did not run?)"
    box_id="$(json_str "$SEED" box_id)"
    [ -n "$box_id" ] || fail "access mode: could not read box_id from $SEED"
    apex="${box_id}.malmo.network"
    app_host="whoami.${apex}"

    # The signed owner assertion the harness minted with the test-portal private key,
    # delivered over SMBIOS (ImportCredential=malmo.sso_token in the unit).
    sso_token="$(tr -d '\r\n' < "${CREDENTIALS_DIRECTORY:-/nonexistent}/malmo.sso_token" 2>/dev/null || true)"
    [ -n "$sso_token" ] || fail "access mode: malmo.sso_token credential missing (harness did not mint/deliver the owner assertion)"

    # The full-response HTTP + cookie helpers this scenario needs (full_get,
    # full_send, status_of, cookie_val, cookie_line) are defined once above, beside
    # the status-only helpers — the update boot (#382) drives the same SSO landing.

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

    # 1a. THE TWO-COOKIE SAFETY MODEL, asserted on the wire (#304's headline claim).
    #     The whole design rests on the two cookies having DIFFERENT scopes, and until
    #     now that was only ever asserted structurally in unit tests — this lane
    #     captured the real Set-Cookie headers and then looked only at their values.
    #     Assert the attributes:
    #       - malmo_session carries NO Domain ⇒ host-only, scoped to the dashboard host
    #         alone. A Domain here would send the ADMIN session to every app subdomain,
    #         where a third-party app could replay it as the owner. This is the single
    #         most dangerous regression in the whole epic and it is one attribute wide.
    #       - malmo_forward_auth carries Domain=<box-id>.malmo.network ⇒ deliberately
    #         domain-wide, which is what lets the browser present it to an app subdomain
    #         (and is why the app route must strip it — probed below).
    sess_line="$(cookie_line "$sso_resp" malmo_session)"
    fa_line="$(cookie_line "$sso_resp" malmo_forward_auth)"
    grep -qiE 'Domain=' <<<"$sess_line" \
        && fail "access: SESSION COOKIE IS DOMAIN-SCOPED — the dashboard session must be host-only or an app subdomain receives it and can replay it as the owner: $sess_line"
    grep -qiE "Domain=\.?${apex}(;|$)" <<<"$fa_line" \
        || fail "access: forward-auth cookie is not Domain-scoped to the box apex (${apex}); the browser would never present it to an app subdomain: $fa_line"
    echo "cloud-assertions: cookie scopes correct on the wire (malmo_session host-only, malmo_forward_auth Domain=${apex})"

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
    #    forward_auth verify let the owner through. Send an extra throwaway cookie:
    #    the strip assertion below proves the strip is PER-COOKIE (#335) — the probe
    #    must survive to the app upstream, and malmo_forward_auth must not.
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
    grep -qiE '^Cookie:.*malmo_forward_auth=' <<<"$a_resp" \
        && fail "access: COOKIE LEAK (restricted) — the app upstream received malmo_forward_auth; the #335 per-cookie strip is broken"
    grep -qiE '^Cookie:.*probe=leakcheck' <<<"$a_resp" \
        || fail "access: restricted app upstream did not receive its own cookie (probe=leakcheck) — the strip is removing more than malmo_forward_auth: $(grep -i '^Cookie:' <<<"$a_resp" | tr -d '\r')"
    echo "cloud-assertions: restricted app proxies the owner through with no second login (identity forwarded, only malmo_forward_auth stripped)"

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
    #     it against the owner's restricted apps — the reason the route builder
    #     strips malmo_forward_auth on every hosted route, public included (#335
    #     narrows this from #306's whole-header delete to just that one cookie; the
    #     probe cookie must still reach a public app, same as a restricted one).
    pl_resp="$(full_get / "$app_host" "${fa_cookie}; probe=leakcheck" 2>/dev/null || true)"
    grep -qi 'Hostname:' <<<"$pl_resp" || fail "access: public-app cookie-leak probe did not reach whoami"
    grep -qiE '^Cookie:.*malmo_forward_auth=' <<<"$pl_resp" \
        && fail "access: COOKIE LEAK (public) — the app upstream received malmo_forward_auth; the #335 per-cookie strip is broken"
    grep -qiE '^Cookie:.*probe=leakcheck' <<<"$pl_resp" \
        || fail "access: public app upstream did not receive its own cookie (probe=leakcheck) — the strip is removing more than malmo_forward_auth: $(grep -i '^Cookie:' <<<"$pl_resp" | tr -d '\r')"
    echo "cloud-assertions: public app also strips only malmo_forward_auth (no forward-auth cookie leaks to a public upstream, app's own cookie intact)"

    echo "cloud-assertions: hosted per-app access modes verified end-to-end (restricted gate + owner proxy-through, public reachability, per-cookie strip in both modes)"
    ;;
update)
    # Control-plane update proof (#382): a REAL update and a REAL failed-update-
    # then-revert, on a booted box, driven through the real admin trigger
    # (POST /api/v1/system/update → host-agent's system-update job → internal/
    # hostagent/cpupdate). Everything under that endpoint was proven only against a
    # fake Docker: no real daemon, no real registry, no real brain restart, no real
    # revert. This scenario is where those meet.
    #
    # The riskiest step in the whole design is here: **host-agent recreates the
    # brain while the brain is what served the request that asked for it.** So the
    # happy-path assertions are written to make a failure there unmistakable — the
    # brain container id must change, the running brain must carry the marker label
    # only the new image has, and the box must answer again on the new pair.
    #
    # Pull-by-digest is proven, not simulated: the guest runs its own registry on
    # 127.0.0.1:5000, the target images are pushed into it and then DROPPED from the
    # local image store, so the updater's `docker pull <ref>@sha256:…` has to fetch
    # them back. Docker treats a localhost registry as insecure by default, so this
    # needs no daemon config.
    [ -f "$SEED" ] || fail "update mode but $SEED absent (seed materializer did not run?)"
    box_id="$(json_str "$SEED" box_id)"
    [ -n "$box_id" ] || fail "update mode: could not read box_id from $SEED"
    apex="${box_id}.malmo.network"

    # 1. owner session. The trigger is admin-only, so this boot is seeded with the
    #    test-portal key and given a signed owner assertion, exactly as the access
    #    boot is (dev/cloud/mkassertion). Driven once — the jti is single-use.
    sso_token="$(tr -d '\r\n' < "${CREDENTIALS_DIRECTORY:-/nonexistent}/malmo.sso_token" 2>/dev/null || true)"
    [ -n "$sso_token" ] || fail "update mode: malmo.sso_token credential missing (harness did not mint/deliver the owner assertion)"
    sso_resp="$(full_get "/_malmo/sso?token=${sso_token}" "$apex" 2>/dev/null || true)"
    grep -q ' 303' <<<"$(status_of "$sso_resp")" \
        || fail "update: SSO landing did not 303 to the dashboard: status='$(status_of "$sso_resp")'"
    session_cookie="$(cookie_val "$sso_resp" malmo_session)"
    [ -n "$session_cookie" ] || fail "update: no malmo_session cookie from the SSO landing"
    echo "cloud-assertions: update — owner session established (box_id=$box_id)"

    # 2. the in-guest registry. Loaded from the test-only tarball (the production
    #    image ships none of this) and run on the host loopback, where the Docker
    #    daemon that does the pulling can reach it.
    docker load -i /var/lib/malmo/test-images/registry.tar >/dev/null 2>&1 \
        || fail "update: could not docker-load the test registry image (/var/lib/malmo/test-images/registry.tar missing from the boot-proof image?)"
    docker rm -f malmo-test-registry >/dev/null 2>&1 || true
    docker run -d --name malmo-test-registry -p 127.0.0.1:5000:5000 registry:2 >/dev/null 2>&1 \
        || fail "update: could not start the in-guest registry container"
    reg=""
    for _i in $(seq 1 90); do
        reg="$(http_status_addr 127.0.0.1 5000 /v2/ 2>/dev/null || true)"
        grep -qE ' (200|401)' <<<"$reg" && break
        sleep 1
    done
    grep -qE ' (200|401)' <<<"$reg" || fail "update: in-guest registry never answered on 127.0.0.1:5000 (last status='$reg'): $(docker logs malmo-test-registry 2>&1 | tail -5)"
    echo "cloud-assertions: update — in-guest registry serving on 127.0.0.1:5000"

    # 3. publish a new generation of an image and print the digest ref to update to.
    #    `docker commit`, not `docker build`: the guest is air-gapped and has no Go
    #    toolchain, and commit derives from the image the box is ALREADY running, so
    #    the new brain is the real brain plus one changed thing. Labels merge on
    #    commit, so the derived brain keeps malmo.protocol.major and passes the
    #    lockstep guard the way a real release would.
    #
    #    Both local references are dropped after the push. That is what makes the
    #    updater's pull a genuine fetch instead of a no-op over an image that never
    #    left the box — and it is asserted, not assumed.
    publish_gen() { # BASE_REF REPO_TAG [dockerfile-change...] -> prints the digest ref
        local base="$1" repotag="$2"; shift 2
        local tmp=malmo-cpupdate-src target="127.0.0.1:5000/${repotag}" args=(commit) c digest
        docker rm -f "$tmp" >/dev/null 2>&1 || true
        docker create --name "$tmp" "$base" >/dev/null 2>&1 || return 1
        for c in "$@"; do args+=(--change "$c"); done
        args+=("$tmp" "$target")
        docker "${args[@]}" >/dev/null 2>&1 || { docker rm -f "$tmp" >/dev/null 2>&1; return 1; }
        docker rm -f "$tmp" >/dev/null 2>&1 || true
        docker push "$target" >/dev/null 2>&1 || return 1
        digest="$(docker inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$target" 2>/dev/null | grep '^127\.0\.0\.1:5000/' | head -1)"
        [ -n "$digest" ] || return 1
        docker rmi "$target" >/dev/null 2>&1 || true
        docker rmi "$digest" >/dev/null 2>&1 || true
        printf '%s' "$digest"
    }

    brain_before_id="$(docker inspect -f '{{.Id}}' malmo-brain 2>/dev/null || true)"
    brain_before_ref="$(docker inspect -f '{{.Config.Image}}' malmo-brain 2>/dev/null || true)"
    ui_before_ref="$(docker inspect -f '{{.Config.Image}}' malmo-ui 2>/dev/null || true)"
    [ -n "$brain_before_id" ] && [ -n "$brain_before_ref" ] && [ -n "$ui_before_ref" ] \
        || fail "update: could not read the running control-plane pair (brain id='$brain_before_id' brain='$brain_before_ref' ui='$ui_before_ref')"

    brain_v2="$(publish_gen "$brain_before_ref" malmo-brain:v2 'LABEL malmo.test.generation=v2')" \
        || fail "update: could not publish the gen-2 brain image to the in-guest registry"
    ui_v2="$(publish_gen "$ui_before_ref" malmo-ui:v2 'LABEL malmo.test.generation=v2')" \
        || fail "update: could not publish the gen-2 ui image to the in-guest registry"
    # The pull has to be real. If either image is still in the local store the
    # digest pull would be satisfied without the registry, and this whole scenario
    # would prove recreate/revert while quietly skipping what production does.
    for r in "$brain_v2" "$ui_v2"; do
        docker image inspect "$r" >/dev/null 2>&1 \
            && fail "update: $r is still in the local image store before the update — the updater's pull would not be a real registry fetch"
    done
    echo "cloud-assertions: update — gen-2 pair published by digest and dropped locally (brain=$brain_v2 ui=$ui_v2)"

    # Poll one update job to a terminal state. The brain is recreated in the middle
    # of this, so /api is briefly gone: ride through every non-200 rather than
    # treating it as a verdict. The job record lives in host-agent, which stays up,
    # which is the whole reason the job id is host-agent's and not the brain's.
    JOB_RESP=""
    poll_job() { # JOB_ID TIMEOUT_S -> 0 when the job reached completed/failed
        local id="$1" timeout="$2" _i resp=""
        for _i in $(seq 1 "$timeout"); do
            resp="$(full_get "/api/v1/system/update/${id}" "$apex" "$session_cookie" 2>/dev/null || true)"
            if grep -q ' 200' <<<"$(status_of "$resp")" \
                && grep -qE '"status"[[:space:]]*:[[:space:]]*"(completed|failed)"' <<<"$resp"; then
                JOB_RESP="$resp"
                return 0
            fi
            sleep 1
        done
        JOB_RESP="$resp"
        return 1
    }

    # 4. THE HAPPY PATH. Both refs move, so this is the coordinated ship: pull both,
    #    snapshot, declare, recreate both, health-check both, commit.
    up_resp="$(full_send POST /api/v1/system/update "$apex" "$session_cookie" \
        "{\"brain_image\":\"${brain_v2}\",\"ui_image\":\"${ui_v2}\"}" 2>/dev/null || true)"
    grep -q ' 202' <<<"$(status_of "$up_resp")" \
        || fail "update: POST /api/v1/system/update was not accepted: status='$(status_of "$up_resp")'"
    job_id="$(json_str_of "$up_resp" job_id)"
    [ -n "$job_id" ] || fail "update: no job_id in the accepted update response: $(tail -1 <<<"$up_resp")"
    echo "cloud-assertions: update — update job ${job_id} accepted (the brain is now replacing itself)"

    poll_job "$job_id" 420 || fail "update: job $job_id never reached a terminal state (last: $(status_of "$JOB_RESP")) — is the box serving at all after the brain was recreated? $(docker ps --format '{{.Names}} {{.Status}}' | tr '\n' ';')"
    grep -qE '"status"[[:space:]]*:[[:space:]]*"completed"' <<<"$JOB_RESP" \
        || fail "update: the happy-path job did not complete: $(tail -1 <<<"$JOB_RESP")"
    grep -qE '"brain_changed"[[:space:]]*:[[:space:]]*true' <<<"$JOB_RESP" \
        || fail "update: job reports brain_changed=false on a moved brain ref: $(tail -1 <<<"$JOB_RESP")"
    grep -qE '"ui_changed"[[:space:]]*:[[:space:]]*true' <<<"$JOB_RESP" \
        || fail "update: job reports ui_changed=false on a moved ui ref: $(tail -1 <<<"$JOB_RESP")"
    grep -qE '"reverted"[[:space:]]*:[[:space:]]*true' <<<"$JOB_RESP" \
        && fail "update: the happy-path update reverted: $(tail -1 <<<"$JOB_RESP")"

    # 4a. the brain really was replaced — not left running and merely re-declared.
    brain_after_id="$(docker inspect -f '{{.Id}}' malmo-brain 2>/dev/null || true)"
    [ -n "$brain_after_id" ] || fail "update: no malmo-brain container after the update"
    [ "$brain_after_id" != "$brain_before_id" ] \
        || fail "update: the brain container was NEVER recreated (same id $brain_before_id) — the update reported success without replacing the brain"
    brain_after_ref="$(docker inspect -f '{{.Config.Image}}' malmo-brain 2>/dev/null || true)"
    [ "$brain_after_ref" = "$brain_v2" ] \
        || fail "update: the running brain is on '$brain_after_ref', not the target '$brain_v2'"
    gen="$(docker inspect -f '{{index .Config.Labels "malmo.test.generation"}}' malmo-brain 2>/dev/null || true)"
    [ "$gen" = v2 ] \
        || fail "update: the running brain does not carry the gen-2 marker label (got '$gen') — it is not the image this update targeted"
    ui_after_ref="$(docker inspect -f '{{.Config.Image}}' malmo-ui 2>/dev/null || true)"
    [ "$ui_after_ref" = "$ui_v2" ] \
        || fail "update: the running ui is on '$ui_after_ref', not the target '$ui_v2'"
    echo "cloud-assertions: update — both containers recreated on the new pair (brain id $brain_before_id -> $brain_after_id)"

    # 4b. the declaration, in BOTH files (UPDATES.md # 8.3): images.json is what
    #     host-agent reads at the next boot, compose.yml is what the brain
    #     reconciles to. A box whose containers moved but whose declaration did not
    #     silently rolls back on its next reboot.
    ledger=/var/lib/malmo/control-plane/images.json
    [ -f "$ledger" ] || fail "update: no ledger at $ledger after a successful update"
    grep -qF "$brain_v2" "$ledger" || fail "update: ledger does not name the new brain ref: $(tr -d '\n' < "$ledger")"
    grep -qF "$ui_v2" "$ledger" || fail "update: ledger does not name the new ui ref: $(tr -d '\n' < "$ledger")"
    grep -qF "$brain_before_ref" "$ledger" \
        || fail "update: ledger does not record the previous brain ref '$brain_before_ref' — there is nothing to roll back to: $(tr -d '\n' < "$ledger")"
    grep -qE "^[[:space:]]*image:[[:space:]]*${ui_v2}\$" /var/lib/malmo/control-plane/compose.yml \
        || fail "update: the staged compose does not pin the new ui ref '$ui_v2': $(grep -n 'image:' /var/lib/malmo/control-plane/compose.yml | tr '\n' ' ')"
    echo "cloud-assertions: update — declaration written in both files (images.json current+previous, compose.yml ui image)"

    # 4c. the new brain is really serving: /healthz on the container itself (the
    #     same probe the updater uses), and the box answering through Caddy again.
    brain_ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' malmo-brain 2>/dev/null | awk '{print $1}')"
    [ -n "$brain_ip" ] || fail "update: the recreated brain has no address on the ingress network"
    hz=""
    for _i in $(seq 1 60); do
        hz="$(http_status_addr "$brain_ip" 8080 /healthz 2>/dev/null || true)"
        grep -q ' 200' <<<"$hz" && break
        sleep 1
    done
    grep -q ' 200' <<<"$hz" || fail "update: the updated brain does not answer /healthz on $brain_ip:8080 (status='$hz')"
    ver_resp="$(full_get /api/v1/system/version "$apex" "$session_cookie" 2>/dev/null || true)"
    grep -q ' 200' <<<"$(status_of "$ver_resp")" \
        || fail "update: GET /api/v1/system/version after the update: status='$(status_of "$ver_resp")'"
    grep -qF "$ui_v2" <<<"$ver_resp" \
        || fail "update: system/version does not report the new ui image '$ui_v2': $(tail -1 <<<"$ver_resp")"
    echo "cloud-assertions: update — HAPPY PATH OK (brain replaced itself, /healthz 200 on the new image, system/version reports the new pair)"

    # 5. THE REVERT. Point an update at a brain that starts but never serves, and
    #    make it do damage on the way: it truncates the brain's SQLite database and
    #    leaves a marker file. That turns two silent claims into observable facts —
    #    the bad brain really ran (marker present) and the snapshot really came back
    #    (the database is a valid SQLite file again, and the owner session still
    #    works). A revert that restored nothing would leave the clobbered file.
    broken_marker=/var/lib/malmo/broken-brain-ran
    rm -f "$broken_marker"
    brain_bad="$(publish_gen "$brain_v2" malmo-brain:bad \
        'ENTRYPOINT ["/bin/sh","-c","echo BROKEN > /var/lib/malmo/state/malmo.db; touch /var/lib/malmo/broken-brain-ran; sleep 900"]')" \
        || fail "update: could not publish the deliberately-broken brain image"

    bad_resp="$(full_send POST /api/v1/system/update "$apex" "$session_cookie" \
        "{\"brain_image\":\"${brain_bad}\"}" 2>/dev/null || true)"
    grep -q ' 202' <<<"$(status_of "$bad_resp")" \
        || fail "update: POST of the failing update was not accepted: status='$(status_of "$bad_resp")'"
    bad_job="$(json_str_of "$bad_resp" job_id)"
    [ -n "$bad_job" ] || fail "update: no job_id for the failing update: $(tail -1 <<<"$bad_resp")"

    # The health wait is 60s (UPDATES.md # 3 step 3d) and the revert runs after it,
    # so this window is deliberately wide.
    poll_job "$bad_job" 420 || fail "update: the failing job $bad_job never reached a terminal state (last: $(status_of "$JOB_RESP")) — the box may not have come back from the revert: $(docker ps --format '{{.Names}} {{.Status}}' | tr '\n' ';')"
    grep -qE '"status"[[:space:]]*:[[:space:]]*"failed"' <<<"$JOB_RESP" \
        || fail "update: an update to a brain that never serves was reported as success: $(tail -1 <<<"$JOB_RESP")"
    grep -qE '"reverted"[[:space:]]*:[[:space:]]*true' <<<"$JOB_RESP" \
        || fail "update: the failed update did not revert: $(tail -1 <<<"$JOB_RESP")"
    grep -qE '"failure_mode"[[:space:]]*:[[:space:]]*"health"' <<<"$JOB_RESP" \
        || fail "update: the failed update blames the wrong step (want failure_mode=health): $(tail -1 <<<"$JOB_RESP")"
    grep -q '"revert_error"' <<<"$JOB_RESP" \
        && fail "update: the revert itself failed: $(tail -1 <<<"$JOB_RESP")"

    # 5a. the bad brain really ran. Without this the whole revert half could pass on
    #     a box where the broken image never started, which would prove nothing.
    [ -f "$broken_marker" ] \
        || fail "update: the broken brain never started (no $broken_marker) — the revert proof would be vacuous"

    # 5b. images and declaration are back on the good pair.
    brain_reverted_ref="$(docker inspect -f '{{.Config.Image}}' malmo-brain 2>/dev/null || true)"
    [ "$brain_reverted_ref" = "$brain_v2" ] \
        || fail "update: after the revert the brain is on '$brain_reverted_ref', not the previous good ref '$brain_v2'"
    grep -qF "$brain_v2" "$ledger" \
        || fail "update: after the revert the ledger does not name the good brain ref again: $(tr -d '\n' < "$ledger")"
    grep -qF "$brain_bad" "$ledger" \
        && fail "update: after the revert the ledger still names the failed brain ref '$brain_bad' — the next boot would launch it: $(tr -d '\n' < "$ledger")"

    # 5c. the SQLite snapshot was restored over what the bad brain wrote.
    ls -d /var/lib/malmo/brain-snapshots/* >/dev/null 2>&1 \
        || fail "update: no pre-update snapshot under /var/lib/malmo/brain-snapshots (UPDATES.md # 3 step 3b)"
    db_head="$(head -c 15 /var/lib/malmo/state/malmo.db 2>/dev/null || true)"
    [ "$db_head" = "SQLite format 3" ] \
        || fail "update: the brain database was NOT restored after the revert (starts with '$db_head', the broken brain's write is still there)"

    # 5d. the box is serving again on the restored pair, with the SAME session — a
    #     restored database that could not answer a signed-in request would be a
    #     restore in name only.
    me=""
    for _i in $(seq 1 90); do
        me="$(status_of "$(full_get /api/v1/me "$apex" "$session_cookie" 2>/dev/null || true)")"
        grep -q ' 200' <<<"$me" && break
        sleep 1
    done
    grep -q ' 200' <<<"$me" \
        || fail "update: after the revert the box does not answer an authenticated /api/v1/me (status='$me') — the restored database or the restored brain is not serving"
    spa_after="$(http_status / "$DASH_HOST" 2>/dev/null || true)"
    grep -q ' 200' <<<"$spa_after" || fail "update: after the revert the dashboard does not answer: status='$spa_after'"
    echo "cloud-assertions: update — REVERT OK (health failure detected, both refs and the SQLite snapshot restored, box serving on the old pair with the same session)"

    # 6. THE TARGET-DRIVEN PATH (os#401). Everything above was driven by an admin
    #    POSTing two refs. On a hosted box nobody types them: host-agent reads an
    #    update-target source and applies what it is handed, in the update window,
    #    with no prompt (UPDATES.md # 8.4). This proves that loop against a real
    #    source, a real registry pull and the real transaction — and, first, proves
    #    it REFUSES an answer that is not pinned to a digest.
    target_dir=/var/lib/malmo/test-target
    target_url=http://127.0.0.1:5001/target.json
    mkdir -p "$target_dir"

    write_target() { # BRAIN_REF UI_REF -> writes the answer the box will read
        printf '{"version":"%s","channel":"stable","brain_image":"%s","brain_digest":"%s","ui_image":"%s","ui_digest":"%s","published_at":"2026-01-01T00:00:00Z","an_unknown_field":true}\n' \
            "$3" "$1" "${1##*@}" "$2" "${2##*@}" > "$target_dir/target.json"
    }

    # Restarting host-agent is how a tick is forced: the loop polls every 15
    # minutes but ticks once immediately at startup, and a boot proof cannot wait
    # a quarter of an hour. The drop-in also opens the window to the whole day and
    # points the repository assertion at the in-guest registry, which is the same
    # configurability a box under test gets in production.
    mkdir -p /etc/systemd/system/host-agent.service.d
    cat > /etc/systemd/system/host-agent.service.d/30-update-target.conf <<EOF
[Service]
Environment=MALMO_UPDATE_TARGET_URL=${target_url}
Environment=MALMO_UPDATE_WINDOW=00:00-23:59
Environment=MALMO_UPDATE_BRAIN_REPO=127.0.0.1:5000/malmo-brain
Environment=MALMO_UPDATE_UI_REPO=127.0.0.1:5000/malmo-ui
EOF
    systemctl daemon-reload || fail "update-target: systemctl daemon-reload failed"

    # The source: a file server on the loopback, run from the Caddy image the box
    # already has, so this needs nothing the boot-proof image does not ship.
    caddy_image="$(docker inspect -f '{{.Config.Image}}' malmo-caddy 2>/dev/null || true)"
    [ -n "$caddy_image" ] || fail "update-target: no malmo-caddy container to borrow a file-server image from"
    docker rm -f malmo-test-target >/dev/null 2>&1 || true
    write_target "127.0.0.1:5000/malmo-brain:v3" "127.0.0.1:5000/malmo-ui:v3" "v0.0.0-unpinned"
    docker run -d --name malmo-test-target -p 127.0.0.1:5001:80 \
        -v "$target_dir":/srv:ro "$caddy_image" \
        caddy file-server --root /srv --listen :80 >/dev/null 2>&1 \
        || fail "update-target: could not start the in-guest update-target file server"
    tgt=""
    for _i in $(seq 1 60); do
        tgt="$(http_status_addr 127.0.0.1 5001 /target.json 2>/dev/null || true)"
        grep -q ' 200' <<<"$tgt" && break
        sleep 1
    done
    grep -q ' 200' <<<"$tgt" || fail "update-target: the in-guest source never answered on 127.0.0.1:5001 (last status='$tgt'): $(docker logs malmo-test-target 2>&1 | tail -5)"

    # 6a. THE REFUSAL. The answer names TAGS. A box that pulled them would be
    #     trusting a movable label, so it must refuse and stay exactly where it is.
    brain_id_before_target="$(docker inspect -f '{{.Id}}' malmo-brain 2>/dev/null || true)"
    systemctl restart host-agent.service || fail "update-target: could not restart host-agent"
    sleep 30
    journalctl -u host-agent.service -b --no-pager 2>&1 | grep -q "refusing the answer" \
        || fail "update-target: host-agent did not log a refusal for an unpinned answer: $(journalctl -u host-agent.service -b --no-pager 2>&1 | grep -i 'update target' | tail -5)"
    [ "$(docker inspect -f '{{.Id}}' malmo-brain 2>/dev/null || true)" = "$brain_id_before_target" ] \
        || fail "update-target: the box acted on an UNPINNED answer — the brain container was replaced"
    echo "cloud-assertions: update-target — REFUSAL OK (a tagged answer was refused, box unchanged)"

    # 6b. THE APPLY. A pinned gen-3 pair, published and dropped locally like the
    #     ones above, so the loop's apply is a real registry pull.
    brain_v3="$(publish_gen "$(docker inspect -f '{{.Config.Image}}' malmo-brain)" malmo-brain:v3 'LABEL malmo.test.generation=v3')" \
        || fail "update-target: could not publish the gen-3 brain image"
    ui_v3="$(publish_gen "$(docker inspect -f '{{.Config.Image}}' malmo-ui)" malmo-ui:v3 'LABEL malmo.test.generation=v3')" \
        || fail "update-target: could not publish the gen-3 ui image"
    write_target "$brain_v3" "$ui_v3" "v0.0.0-target-test"
    systemctl restart host-agent.service || fail "update-target: could not restart host-agent for the pinned answer"

    applied=""
    for _i in $(seq 1 420); do
        if grep -qF "$brain_v3" "$ledger" 2>/dev/null && grep -qF "$ui_v3" "$ledger" 2>/dev/null; then
            applied=yes
            break
        fi
        sleep 1
    done
    [ -n "$applied" ] \
        || fail "update-target: nothing applied the pinned target within 420s (ledger: $(tr -d '\n' < "$ledger")): $(journalctl -u host-agent.service -b --no-pager 2>&1 | grep -i 'update target\|system-update' | tail -10)"

    # The containers really moved, not just the declaration.
    gen3=""
    for _i in $(seq 1 120); do
        gen3="$(docker inspect -f '{{index .Config.Labels "malmo.test.generation"}}' malmo-brain 2>/dev/null || true)"
        [ "$gen3" = v3 ] && break
        sleep 1
    done
    [ "$gen3" = v3 ] \
        || fail "update-target: the running brain does not carry the gen-3 marker (got '$gen3') — the ledger moved but the container did not"
    me_after=""
    for _i in $(seq 1 90); do
        me_after="$(status_of "$(full_get /api/v1/me "$apex" "$session_cookie" 2>/dev/null || true)")"
        grep -q ' 200' <<<"$me_after" && break
        sleep 1
    done
    grep -q ' 200' <<<"$me_after" \
        || fail "update-target: after the target-driven update the box does not answer an authenticated /api/v1/me (status='$me_after')"
    echo "cloud-assertions: update-target — APPLY OK (the box read its target, pulled the pinned pair and applied it with no prompt)"
    ;;
*)
    fail "unknown assert mode '$MODE'"
    ;;
esac

echo "cloud-assertions: control plane up, dashboard + /api served through Caddy; gate scenario '$MODE' OK"
ok
