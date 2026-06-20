#!/bin/bash
# Cloud boot-proof in-VM assertions (C2, #205). Baked into the image at
# /usr/local/bin/cloud-assertions.sh and run once at first boot by
# malmo-cloud-assertions.service. Writes a single verdict line to the serial
# console for dev/cloud/run-cloud-tests.sh to grep:
#
#     MALMO_CLOUD_ASSERTIONS: PASS
#     MALMO_CLOUD_ASSERTIONS: FAIL: <reason>
#
# The cloud analogue of dev/test-qemu/medium-assertions.sh, trimmed to the
# control-plane-up proof (ENVIRONMENT.md # Provisioning — "C2 asserts the brain
# serves, not that an admin exists"): systemd userspace up with no failed units,
# PSI live, the baked control-plane images loaded, the four containers running,
# the dashboard answering through Caddy, and the hosted /setup gate returning 503
# (no seed yet — the open-/setup window stays closed; ENVIRONMENT.md # Admin
# bootstrap — as built). No app-install, no admin, no SSH (all later / hosted-cut).
#
# -u + pipefail but NOT -e: every check is `... || fail`. Reads only — safe to
# run once on a real provisioned boot.
set -uo pipefail

SENTINEL=/dev/console
MARKER=/var/lib/malmo/.cloud-assertions-done
# Brain default dashboard host (MALMO_DASHBOARD_HOST); host-agent does not
# override it, so the brain installs the dashboard route under this Host. The
# assertion is a Host-header route match over localhost — no DNS/mDNS involved.
DASH_HOST=malmo.local

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
        echo "-- host-agent journal (tail 15) --"
        journalctl -u host-agent.service -b --no-pager 2>&1 | tail -15
        echo "=== END MALMO_CLOUD_DIAG ==="
    } > "$SENTINEL" 2>&1 || true
}
fail() {
    echo "cloud-assertions FAIL: $*" >&2
    diag
    emit "FAIL: $*"
    mkdir -p "$(dirname "$MARKER")" 2>/dev/null || true
    : > "$MARKER" 2>/dev/null || true
    exit 1
}
ok() {
    emit "PASS"
    mkdir -p "$(dirname "$MARKER")" 2>/dev/null || true
    : > "$MARKER" 2>/dev/null || true
    exit 0
}

echo "cloud-assertions: starting boot-proof checks"

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
for repo in malmo-brain malmo-ui caddy tecnativa/docker-socket-proxy; do
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

# --- 9. the hosted /setup gate is armed: no seed ingested → 503, NOT the
# appliance's open empty-box behavior (ENVIRONMENT.md # Admin bootstrap). This is
# the proof the brain resolved profile=hosted in its container (host-agent mounts
# the marker; brainlaunch.Config.ProfileMarkerPath) — an appliance-mode brain
# would 409/200 here and leave first-caller-becomes-admin open.
setup=""
for _i in $(seq 1 30); do
    setup="$(http_post_status /api/v1/setup "$DASH_HOST" '{"username":"probe","password":"probe-pw-123"}' 2>/dev/null || true)"
    grep -qE ' (503|401|409|200)' <<<"$setup" && break
    sleep 1
done
grep -q ' 503' <<<"$setup" || fail "hosted /setup gate not armed: status='$setup' (want 503; an appliance-mode brain would 409/200 — profile marker not reaching the container?)"

echo "cloud-assertions: control plane up, dashboard + /api served through Caddy, hosted /setup gate armed (503)"
ok
