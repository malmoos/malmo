#!/usr/bin/env bash
# In-VM boot-proof assertions for the hosted cloud image (#205/C2), baked at
# /usr/local/bin/cloud-assertions.sh and run by malmo-cloud-assert.service.
#
# Unlike the medium lane (which SSHes in and scp's a verdict file back), the
# hosted image ships no sshd (ENVIRONMENT.md # Networking & discovery — SSH is
# off in hosted v1; the lean image cuts openssh-server). So the verdict is a
# single sentinel line written to the serial console; run-cloud-tests.sh greps
# the captured serial log for it. The image also has no curl/jq/python3, so HTTP
# is hand-built over bash /dev/tcp and JSON is matched with grep — same idiom as
# medium-assertions.sh.
#
# Scope (C2): boot + control-plane-up only. systemd userspace up, PSI live, the
# baked images loaded, the brain container running behind the socket-proxy, and
# the dashboard reachable through Caddy. PLUS the security pairing: hosted +
# unseeded /setup must return 503 (the open-/setup window stays closed). No app
# install, no admin, no seed (those are C3/C4/C5).
#
# -u + pipefail but NOT -e: each assertion is `... || fail "..."`.
set -uo pipefail

CONSOLE=/dev/console
RESULT=/run/malmo-cloud-assert-result

say() { echo "cloud-assert: $*" > "$CONSOLE" 2>/dev/null || true; }

fail() {
    echo "FAIL: $*" > "$RESULT" 2>/dev/null || true
    diag
    echo "MALMO-CLOUD-ASSERT: FAIL: $*" > "$CONSOLE" 2>/dev/null || true
    sync
    exit 1
}
ok() {
    echo "PASS" > "$RESULT" 2>/dev/null || true
    echo "MALMO-CLOUD-ASSERT: PASS" > "$CONSOLE" 2>/dev/null || true
    sync
    exit 0
}

# On failure, surface what the brain/Docker were doing to the serial log.
diag() {
    {
        echo "--- cloud-assert diagnostics ---"
        echo "is-system-running: $(systemctl is-system-running 2>&1 || true)"
        echo "failed units:"; systemctl --failed --no-legend --plain 2>&1 | head -20
        echo "docker ps -a:"; docker ps -a --format '{{.Names}}\t{{.Status}}\t{{.Image}}' 2>&1 | head -20
        echo "brain log (tail 30):"; docker logs malmo-brain 2>&1 | tail -30
        echo "--- end diagnostics ---"
    } > "$CONSOLE" 2>/dev/null || true
}

# GET the status line for PATH on HOST through Caddy on :80 (bash /dev/tcp).
http_status() { # PATH HOST -> "HTTP/1.0 200 OK"
    exec 3<>/dev/tcp/127.0.0.1/80 || return 1
    printf 'GET %s HTTP/1.0\r\nHost: %s\r\nConnection: close\r\n\r\n' "$1" "$2" >&3
    head -1 <&3
    exec 3>&- 3<&-
}
# POST a JSON body and return the status line.
http_post_status() { # PATH HOST BODY -> "HTTP/1.0 503 ..."
    local body="$3" len
    len="$(printf '%s' "$body" | wc -c | tr -d ' ')"
    exec 3<>/dev/tcp/127.0.0.1/80 || return 1
    printf 'POST %s HTTP/1.0\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s' \
        "$1" "$2" "$len" "$body" >&3
    head -1 <&3
    exec 3>&- 3<&-
}

say "starting boot-proof assertions"

# --- 1. the image stamped the hosted marker. Cheap sanity that C1b's marker
# landed (and the source the brain's mount reads exists).
prof="$(tr -d '[:space:]' < /etc/malmo/profile 2>/dev/null || true)"
[ "$prof" = "hosted" ] || fail "/etc/malmo/profile reads '$prof', want hosted"

# --- 2. systemd userspace reached multi-user. Poll: the control-plane oneshots
# race the assert unit's start. degraded is acceptable here (a non-control-plane
# unit may have failed); the per-unit checks below are the load-bearing ones.
state=""
for _i in $(seq 1 120); do
    state="$(systemctl is-system-running 2>&1 || true)"
    case "$state" in running|degraded) break ;; esac
    sleep 1
done
case "$state" in
    running|degraded) ;;
    *) fail "system state is '$state' after 120s (want running or degraded)" ;;
esac

# --- 3. PSI is live (pairs with the image's psi=1 cmdline — BUILD.md # 1).
# Without it the ram-pressure health detector reads zeros: a false all-clear a
# boot test must catch.
test -s /proc/pressure/memory \
    || fail "/proc/pressure/memory missing or empty (psi=1 not active on cmdline?)"

# --- 4. Docker is up.
docker_state="$(systemctl is-active docker.service 2>&1 || true)"
[ "$docker_state" = "active" ] \
    || fail "docker.service is '$docker_state' (want active)"

# --- 5. the baked control-plane images docker-loaded at first boot (offline).
for _i in $(seq 1 60); do
    [ -f /var/lib/malmo/.control-plane-images-loaded ] && break
    if systemctl is-failed --quiet malmo-load-images.service; then
        fail "malmo-load-images.service failed: $(journalctl -u malmo-load-images.service -b --no-pager 2>/dev/null | tail -20)"
    fi
    sleep 1
done
[ -f /var/lib/malmo/.control-plane-images-loaded ] \
    || fail "control-plane image-load marker never appeared after 60s"
cp_images="$(docker images --format '{{.Repository}}' 2>&1 || true)"
for repo in malmo-brain malmo-ui caddy tecnativa/docker-socket-proxy; do
    grep -qx "$repo" <<<"$cp_images" \
        || fail "control-plane image '$repo' not loaded (have: $(tr '\n' ' ' <<<"$cp_images"))"
done
say "control-plane images loaded"

# --- 6. host-agent seeded the proxy + launched the brain; the brain reconciled
# Caddy + malmo-ui. The bootstrap races the assert unit, so poll.
want_containers="malmo-brain malmo-caddy malmo-ui malmo-docker-proxy"
running=""
for _i in $(seq 1 120); do
    running="$(docker ps --format '{{.Names}}' 2>/dev/null | tr '\n' ' ')"
    ok_all=1
    for c in $want_containers; do
        grep -qw "$c" <<<"$running" || ok_all=0
    done
    [ "$ok_all" = 1 ] && break
    sleep 1
done
for c in $want_containers; do
    grep -qw "$c" <<<"$running" \
        || fail "control-plane container '$c' not running after 120s (have: $running)"
done

# --- 7. proxy boundary: the brain reaches Docker only via the proxy, never the
# raw socket (CONTROL_PLANE.md # Docker socket exposure).
brain_sock="$(docker inspect malmo-brain \
    --format '{{range .Mounts}}{{println .Source}}{{end}}' 2>/dev/null \
    | grep -c 'docker.sock' || true)"
[ "$brain_sock" = 0 ] \
    || fail "raw docker.sock is mounted into malmo-brain (proxy boundary breached)"
brain_dockerhost="$(docker inspect malmo-brain \
    --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null \
    | sed -n 's/^DOCKER_HOST=//p')"
[ "$brain_dockerhost" = "tcp://docker-proxy:2375" ] \
    || fail "brain DOCKER_HOST='$brain_dockerhost', want tcp://docker-proxy:2375"

# --- 8. the dashboard SPA loads through Caddy. Caddy publishes :80; the brain
# installs the dashboard host route (malmo.local — the default the cloud image
# keeps until enrollment lands in C3b). Poll: the brain installs the route a beat
# after Caddy comes up.
spa_status=""
for _i in $(seq 1 60); do
    spa_status="$(http_status / malmo.local 2>/dev/null || true)"
    grep -q ' 200' <<<"$spa_status" && break
    sleep 1
done
grep -q ' 200' <<<"$spa_status" \
    || fail "dashboard SPA not reachable through Caddy: status='$spa_status'"

# --- 9. the /api leg routes to the brain (not Caddy's catch-all 404). auth/state
# is a public brain route returning 200 — proof the brain serves pre-setup. A 404
# would mean the catch-all swallowed it; a 502 that the route is installed but the
# brain's listener isn't up yet.
api_status=""
for _i in $(seq 1 60); do
    api_status="$(http_status /api/v1/auth/state malmo.local 2>/dev/null || true)"
    grep -q ' 200' <<<"$api_status" && break
    sleep 1
done
grep -q ' 200' <<<"$api_status" \
    || fail "/api not routed to the brain through Caddy: status='$api_status'"

# --- 10. the security pairing: hosted + unseeded /setup must be CLOSED. The
# containerized brain resolves profile=hosted (host-agent binds /etc/malmo/profile
# into it — #205/C2 Go change), and with no provisioning seed it has no bootstrap
# secret, so gateBootstrap returns 503 (api/auth.go) rather than the appliance's
# open-on-empty-box first-admin creation. This is the open-/setup window staying
# shut in branch history (the C2 security follow-up). 503 also doubly proves the
# brain took the hosted seam (an appliance-resolving brain would 200/409 here).
setup_status="$(http_post_status /api/v1/setup malmo.local '{"username":"probe","password":"probe"}' 2>/dev/null || true)"
grep -q ' 503' <<<"$setup_status" \
    || fail "hosted /setup not gated closed: status='$setup_status' (want 503; brain may have resolved appliance)"

say "all assertions passed"
ok
