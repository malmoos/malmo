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
# + /api answering through Caddy), then asserts the hosted /setup admin-bootstrap
# gate (ENVIRONMENT.md # Admin bootstrap — as built) for the scenario the harness
# selected via the malmo.assert credential:
#
#     unseeded         no seed ingested → POST /setup ⇒ 503 (gate armed, closed)
#     seeded           seed on disk → wrong secret ⇒ 401, correct ⇒ 200 + box_id;
#                      then the new admin drives the first-run wizard to completion
#                      (C4 #208): PAM /login → set timezone + telemetry → first-run-
#                      complete, asserting the box becomes admin-owned and served with
#                      the marker set and box_id persisted (the C5 #209 acceptance bar)
#     frozen:<box-id>  reboot with a DIFFERENT seed → /login still reports <box-id>
#                      (the brain's persisted identity is frozen; the new seed is
#                      ignored), seed.json on disk holds the new ignored box-id, and
#                      first_run_complete persists (the wizard does not reappear)
#
# On PASS the script powers the box off cleanly (the serial-only analogue of the
# medium lane's SSH `systemctl poweroff`) so the brain's SQLite box-id write flushes
# to the persisted overlay before the harness boots the next scenario.
#
# -u + pipefail but NOT -e: every check is `... || fail`. The gate POSTs create at
# most one admin (the seeded scenario); all other checks are reads.
set -uo pipefail

SENTINEL=/dev/console
SEED=/var/lib/malmo/seed.json
# Brain default dashboard host (MALMO_DASHBOARD_HOST); host-agent does not
# override it, so the brain installs the dashboard route under this Host. The
# assertion is a Host-header route match over localhost — no DNS/mDNS involved.
DASH_HOST=malmo.local
# First-admin credentials the seeded scenario creates and the frozen scenario logs
# back in with (a fresh process each boot; these constants are the only shared
# state besides the persisted disk). pam_unix has no complexity policy, but keep a
# realistic password. validateUsername only bars '--'/'xn--' prefixes.
SETUP_USER=admin
SETUP_PW=malmo-setup-pw-2026
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
# Like http_post_status but returns the FULL response (status line + headers +
# body) so the gate scenarios can read box_id out of the JSON body, not just the
# status. The body is single-line JSON from the brain, so a `tail -1` grabs it.
http_post() { # PATH HOST JSON -> full response
    local body="$3" len
    len="$(printf '%s' "$body" | wc -c | tr -d ' ')"
    exec 3<>/dev/tcp/127.0.0.1/80 || return 1
    printf 'POST %s HTTP/1.0\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s' \
        "$1" "$2" "$len" "$body" >&3
    cat <&3
    exec 3>&- 3<&-
}
# Extract a JSON string field's value from a compact one-line document. The seed
# the harness generates is compact and its fields (box_id, admin_bootstrap_secret)
# are plain strings with no embedded quotes, so a targeted sed is sufficient (no
# jq in the lean image).
json_str() { # FILE KEY -> value
    sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$1" | head -1
}

# Extract the session cookie ("malmo_session=<tok>") from a /login (or /setup)
# response's Set-Cookie header, dropping the attributes after the first ';'. The
# wizard's box-configuration endpoints are admin-gated, so they need this cookie.
session_cookie() { # RESPONSE -> "malmo_session=<tok>"
    grep -i '^Set-Cookie:' <<<"$1" \
        | sed -E 's/^[Ss]et-[Cc]ookie:[[:space:]]*([^;]*).*/\1/' | tr -d '\r' | head -1
}
# Authenticated POST carrying the session cookie -> full response. An empty body
# arg is sent as a bodyless POST (Content-Length: 0, no Content-Type) for the
# no-body endpoints (POST /system/first-run-complete).
auth_post() { # COOKIE PATH HOST [JSON] -> full response
    local cookie="$1" path="$2" host="$3" body="${4:-}" len
    exec 3<>/dev/tcp/127.0.0.1/80 || return 1
    if [ -n "$body" ]; then
        len="$(printf '%s' "$body" | wc -c | tr -d ' ')"
        printf 'POST %s HTTP/1.0\r\nHost: %s\r\nCookie: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s' \
            "$path" "$host" "$cookie" "$len" "$body" >&3
    else
        printf 'POST %s HTTP/1.0\r\nHost: %s\r\nCookie: %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n' \
            "$path" "$host" "$cookie" >&3
    fi
    cat <&3
    exec 3>&- 3<&-
}
# Authenticated GET carrying the session cookie -> full response.
auth_get() { # COOKIE PATH HOST -> full response
    exec 3<>/dev/tcp/127.0.0.1/80 || return 1
    printf 'GET %s HTTP/1.0\r\nHost: %s\r\nCookie: %s\r\nConnection: close\r\n\r\n' "$2" "$3" "$1" >&3
    cat <&3
    exec 3>&- 3<&-
}
# First line of an HTTP response, CR-stripped (for status-line checks/messages).
status_of() { printf '%s' "$1" | head -1 | tr -d '\r'; }

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

# --- 9. the hosted /setup admin-bootstrap gate (ENVIRONMENT.md # Admin bootstrap).
# The brain resolved profile=hosted in its container (host-agent mounts the marker;
# brainlaunch.Config.ProfileMarkerPath) and gates /setup on the seeded secret. The
# scenario the harness selected (MODE) decides what to assert.

# Wait until /setup answers definitively — the brain ran its synchronous seed
# ingestion before it began serving, so a settled /setup means ingestion is done.
# A deliberately-wrong secret is a safe probe: it never reaches first-admin creation
# (unseeded ⇒ 503, seeded/frozen ⇒ 401), so no admin is created here.
setup=""
for _i in $(seq 1 30); do
    setup="$(http_post_status /api/v1/setup "$DASH_HOST" \
        '{"username":"probe","password":"probe-pw-once","bootstrap_secret":"definitely-wrong"}' 2>/dev/null || true)"
    grep -qE ' (503|401|409|200)' <<<"$setup" && break
    sleep 1
done

case "$MODE" in
unseeded)
    # No seed ingested → 503, NOT the appliance's open empty-box 200/409. Proof the
    # gate stays closed until a seed lands (never falling back to open /setup).
    grep -q ' 503' <<<"$setup" || fail "unseeded /setup gate not armed: status='$setup' (want 503; an appliance-mode brain would 409/200 — profile marker not reaching the container?)"
    echo "cloud-assertions: hosted /setup gate armed (503, unprovisioned)"
    ;;
seeded)
    [ -f "$SEED" ] || fail "seeded mode but $SEED absent (seed materializer did not run?)"
    box_id="$(json_str "$SEED" box_id)"
    secret="$(json_str "$SEED" admin_bootstrap_secret)"
    [ -n "$box_id" ] && [ -n "$secret" ] || fail "could not read box_id/admin_bootstrap_secret from $SEED"

    # Wrong secret → 401: the seed was ingested (the gate has a hash) and rejects a
    # bad secret, audited as setup.failure.
    wrong="$(http_post_status /api/v1/setup "$DASH_HOST" \
        "{\"username\":\"$SETUP_USER\",\"password\":\"$SETUP_PW\",\"bootstrap_secret\":\"wrong-$secret\"}" 2>/dev/null || true)"
    grep -q ' 401' <<<"$wrong" || fail "seeded /setup with a wrong secret: status='$wrong' (want 401)"

    # Correct secret → 200: first admin created, and the response surfaces the
    # provisioned box_id (fullUserDTO; ENVIRONMENT.md # Admin bootstrap — box_id on /me).
    resp="$(http_post /api/v1/setup "$DASH_HOST" \
        "{\"username\":\"$SETUP_USER\",\"password\":\"$SETUP_PW\",\"bootstrap_secret\":\"$secret\"}" 2>/dev/null || true)"
    line="$(printf '%s' "$resp" | head -1)"
    grep -q ' 200' <<<"$line" || fail "seeded /setup with the correct secret: status='$line' (want 200)"
    grep -q "\"box_id\":\"$box_id\"" <<<"$resp" || fail "seeded /setup 200 did not surface box_id=$box_id (body: $(printf '%s' "$resp" | tail -1))"
    echo "cloud-assertions: hosted /setup gate — wrong secret 401, correct secret 200 + box_id=$box_id"

    # --- Wizard completion end-to-end (C4 #208; the C5 #209 acceptance bar). The
    # admin just created drives the trimmed hosted first-run wizard to completion,
    # turning the seeded box into a working, admin-owned, served, first-run-complete
    # malmo. The brain endpoints are admin-gated, so first authenticate.

    # PAM login: the new admin authenticates against /etc/shadow via host-agent
    # verify-password (the real PAM path, not the seed secret). Keep the session
    # cookie for the admin-gated wizard steps.
    login="$(http_post /api/v1/login "$DASH_HOST" \
        "{\"username\":\"$SETUP_USER\",\"password\":\"$SETUP_PW\"}" 2>/dev/null || true)"
    grep -q ' 200' <<<"$(status_of "$login")" || \
        fail "PAM /login after setup: status='$(status_of "$login")' (want 200 — verify-password against /etc/shadow)"
    cookie="$(session_cookie "$login")"
    [ -n "$cookie" ] || fail "no session cookie from /login (status: $(status_of "$login"))"

    # Wizard step — time zone: a real pass-through to host-agent timedatectl (wired
    # in the hosted build). 200 proves the step works on the real hosted box, not
    # only against the fake host-agent. This is the first C4 (#208) endpoint the
    # lane hits, so its status discriminates the two likely setup mistakes: 404 ⇒
    # the baked brain image predates C4 (rebuild the control-plane bundle with
    # MALMO_REBUILD_CP=1); 502 ⇒ timedatectl rejected the zone, i.e. tzdata missing
    # from the lean image (dev/cloud/mkosi.conf).
    tz="$(auth_post "$cookie" /api/v1/system/timezone "$DASH_HOST" '{"timezone":"Europe/Stockholm"}' 2>/dev/null || true)"
    grep -q ' 200' <<<"$(status_of "$tz")" || \
        fail "wizard set-timezone: status='$(status_of "$tz")' (want 200; 404 ⇒ baked brain predates C4 — rebuild the CP bundle, MALMO_REBUILD_CP=1; 502 ⇒ timedatectl failed — tzdata missing from the image?)"

    # Wizard step — telemetry: record the box-level choice (off; TELEMETRY.md — the
    # founding admin makes the box-wide decision once).
    tel="$(auth_post "$cookie" /api/v1/system/telemetry "$DASH_HOST" '{"enabled":false}' 2>/dev/null || true)"
    grep -q ' 200' <<<"$(status_of "$tel")" || \
        fail "wizard set-telemetry: status='$(status_of "$tel")' (want 200)"

    # Wizard step — Done: mark first-run complete (the bootstrap marker C5 asserts).
    done_resp="$(auth_post "$cookie" /api/v1/system/first-run-complete "$DASH_HOST" '' 2>/dev/null || true)"
    grep -q ' 200' <<<"$(status_of "$done_resp")" || \
        fail "wizard first-run-complete: status='$(status_of "$done_resp")' (want 200)"
    grep -q '"first_run_complete":true' <<<"$done_resp" || \
        fail "first-run-complete 200 did not confirm the marker (body: $(printf '%s' "$done_resp" | tail -1))"

    # The wizard will not reappear: the public bootstrap probe now reports
    # first_run_complete=true (App.vue gates the wizard on this flag, not has_users).
    state="$(auth_get "$cookie" /api/v1/auth/state "$DASH_HOST" 2>/dev/null || true)"
    grep -q '"first_run_complete":true' <<<"$state" || \
        fail "/auth/state first_run_complete != true after the wizard — it would reappear (body: $(printf '%s' "$state" | tail -1))"

    # box_id is persisted and surfaced on the authenticated identity (/me), not just
    # echoed by the one-shot /setup response.
    me="$(auth_get "$cookie" /api/v1/me "$DASH_HOST" 2>/dev/null || true)"
    grep -q "\"box_id\":\"$box_id\"" <<<"$me" || \
        fail "/me did not surface persisted box_id=$box_id after the wizard (body: $(printf '%s' "$me" | tail -1))"
    echo "cloud-assertions: wizard complete — PAM /login 200, tz+telemetry set, first-run-complete; box_id=$box_id persisted on /me; wizard will not reappear"
    ;;
frozen:*)
    expect="${MODE#frozen:}"
    [ -n "$expect" ] || fail "frozen mode missing the expected box-id (MODE='$MODE')"
    # A DIFFERENT seed was delivered + materialized this boot, but the brain's identity
    # is frozen in SQLite: it loads the persisted box-id and ignores the new seed. Log
    # in as the seeded boot's admin (persisted on the shared disk) and assert /me-grade
    # identity still reports the original box_id, not this boot's seed.
    login="$(http_post /api/v1/login "$DASH_HOST" \
        "{\"username\":\"$SETUP_USER\",\"password\":\"$SETUP_PW\"}" 2>/dev/null || true)"
    line="$(printf '%s' "$login" | head -1)"
    grep -q ' 200' <<<"$line" || fail "frozen mode: /login status='$line' (want 200 — the seeded boot's admin should persist across the reboot)"
    grep -q "\"box_id\":\"$expect\"" <<<"$login" || fail "frozen mode: /login box_id != $expect — a re-delivered seed re-keyed the box! (body: $(printf '%s' "$login" | tail -1))"
    # The first-run-complete marker written during the seeded boot survives the
    # power-cycle: the wizard does not reappear after a reboot (FIRST_RUN.md # Phase 3).
    fr_cookie="$(session_cookie "$login")"
    fr_state="$(auth_get "$fr_cookie" /api/v1/auth/state "$DASH_HOST" 2>/dev/null || true)"
    grep -q '"first_run_complete":true' <<<"$fr_state" || \
        fail "frozen mode: first_run_complete not persisted across the reboot — the wizard would reappear (body: $(printf '%s' "$fr_state" | tail -1))"
    # Confirm the on-disk seed really is this boot's distinct seed (a no-op overwrite
    # would make the frozen assertion vacuous). A warning, not a failure: the identity
    # assertion above is the real proof.
    if [ -f "$SEED" ]; then
        disk_box="$(json_str "$SEED" box_id)"
        [ -n "$disk_box" ] && [ "$disk_box" = "$expect" ] && \
            echo "cloud-assertions: WARN frozen seed.json box_id ($disk_box) == frozen identity — re-delivery not distinct" >&2
    fi
    echo "cloud-assertions: frozen identity held across reboot — box_id still $expect (re-delivered seed ignored)"
    ;;
*)
    fail "unknown assert mode '$MODE'"
    ;;
esac

echo "cloud-assertions: control plane up, dashboard + /api served through Caddy; gate scenario '$MODE' OK"
ok
