#!/bin/bash
# Hosted first-boot seed materializer (C3a cloud-lane, #220; promoted to the
# production image in #242; real-cloud channel added in #246). Baked into the
# hosted cloud image at /usr/local/bin/ and run by malmo-seed.service before
# host-agent launches the brain. It lands the provisioning seed at the well-known
# path the brain reads — /var/lib/malmo/seed.json (profile.DefaultSeedPath,
# overridable with MALMO_SEED_PATH) — from one of two symmetric channels carrying
# the IDENTICAL bytes (the brain's ReadSeed input; no unwrap/decode either side):
#
#   1. SMBIOS / systemd credential (ImportCredential=malmo.seed) — the test lane
#      (dev/cloud/run-cloud-tests.sh injects over SMBIOS type 11) and any cloud
#      that delivers via fw_cfg. Tried FIRST and, when present, written without
#      ever touching the network — so the air-gapped QEMU lane stays fast and the
#      first-boot network ordering below never runs there.
#   2. Real-cloud metadata/user-data (#246) — Hetzner delivers user-data via its
#      metadata service, not SMBIOS. Only reached when the credential is ABSENT:
#      the seed is fetched verbatim over HTTP from the link-local endpoint
#      (default http://169.254.169.254/hetzner/v1/userdata; MALMO_SEED_METADATA_URL
#      overrides — keeps the GCP/other-cloud swap a config change, not a rewrite).
#
# Channel 2 rides out the first-boot DHCP race with a bounded in-script retry
# (MALMO_SEED_FETCH_DEADLINE, default 60s) rather than a unit-level network
# dependency: malmo-seed.service stays Before=host-agent.service with only a
# passive After=network.target, so systemd-networkd-wait-online can stay disabled
# (ENVIRONMENT.md # Provisioning & first-boot). It never blocks forever — on a
# clean 404 (the un-seeded case on a real cloud) or once the deadline elapses (an
# air-gapped box that will never have metadata), it logs "no seed" and exits 0,
# identical to the un-seeded behavior the brain expects (/setup stays 503).
#
# Security note (#246): on Hetzner the metadata endpoint keeps the user-data
# (which carries the admin-bootstrap secret + acme-dns password) retrievable for
# the server's life, reachable by any local process / app container. Blocking
# egress to 169.254.169.254 belongs to host-agent's hosted firewall posture
# (deferred — cmd/host-agent-real/wiring_hosted.go), not this first-boot script;
# tracked as a follow-up. See docs/progress/cloud-image-real-cloud-seed-channel.md.
#
# The credential/user-data is optional: an un-seeded box delivers neither, so the
# brain finds no seed and keeps /setup closed (503) rather than falling back to the
# appliance's open-on-empty-box trust. A delivered seed is written 0600 root:root.
#
# Deliberately NO "don't clobber an existing file" guard: the frozen-identity
# reboot scenario re-delivers a DIFFERENT seed on a later boot, and overwriting
# proves the brain ignores it (the box-id is frozen in the brain's SQLite, not on
# this file — cmd/brain loadHostedEnvironment). The file is the brain's input, not
# its memory.
set -uo pipefail

DEST="${MALMO_SEED_PATH:-/var/lib/malmo/seed.json}"
# systemd exports CREDENTIALS_DIRECTORY for a unit with ImportCredential=; the
# delivered seed lands at $CREDENTIALS_DIRECTORY/malmo.seed.
SRC="${CREDENTIALS_DIRECTORY:-}/malmo.seed"

# Write SRC to DEST atomically with the final mode/owner — no window where the
# seed is world-readable. root:root 0600 matches the cloud-init-delivered file.
materialize_from_file() { # SRC
    mkdir -p "$(dirname "$DEST")"
    if ! install -m 0600 -o root -g root "$1" "$DEST"; then
        echo "malmo-seed: failed to write $DEST" >&2
        exit 1
    fi
    echo "malmo-seed: provisioning seed materialized at $DEST" >&2
}

# Split an http://host[:port]/path URL into the host/port/path globals the
# /dev/tcp fetch needs (bash can't open a URL directly). HTTP only — the metadata
# endpoint is plain-HTTP link-local, and /dev/tcp can't do TLS anyway.
parse_url() { # URL -> sets host port path
    local rest="${1#http://}"
    local hostport="${rest%%/*}"
    if [ "$hostport" = "$rest" ]; then path="/"; else path="/${rest#*/}"; fi
    host="${hostport%%:*}"
    port="${hostport##*:}"
    [ "$port" = "$hostport" ] && port=80
}

# One HTTP GET over bash /dev/tcp (no curl in the lean image — same idiom as
# dev/cloud/cloud-assertions.sh). `timeout` bounds a connect that hangs (coreutils,
# in the image); Connection: close makes the server send EOF so `cat` reads the
# whole response. Prints the body on stdout for a 200. Return: 0 = 200 (+ body),
# 1 = clean 404 (definitively no seed), 2 = transient (connect failed / other
# status) and the caller may retry.
http_get() { # host port path
    local host="$1" port="$2" path="$3" resp status
    # 2>/dev/null on the inner shell: a failed connect (un-provisioned box during
    # the retry window) otherwise spams "/dev/tcp: Connection refused" to the
    # journal on every attempt — bash reports the redirection failure before any
    # in-command redirect can catch it. The final "no seed" line is the signal.
    resp="$(timeout "${MALMO_SEED_FETCH_TIMEOUT:-3}" bash -c '
        host=$1; port=$2; path=$3
        exec 3<>"/dev/tcp/$host/$port" || exit 1
        printf "GET %s HTTP/1.0\r\nHost: %s\r\nConnection: close\r\n\r\n" "$path" "$host" >&3
        cat <&3
        exec 3>&- 3<&-
    ' _ "$host" "$port" "$path" 2>/dev/null)" || return 2
    # Status line: "HTTP/1.0 200 OK". Pull the numeric code with no awk dependency.
    status="${resp%%$'\r'*}"   # first line, CR-stripped
    status="${status#* }"       # drop "HTTP/1.x "
    status="${status%% *}"      # keep the code
    case "$status" in
        200) printf '%s' "${resp#*$'\r\n\r\n'}"; return 0 ;;  # body after CRLFCRLF
        404) return 1 ;;
        *)   return 2 ;;
    esac
}

# Fetch the seed from the metadata/user-data channel, riding out the first-boot
# DHCP race. Prints the seed body and returns 0 on success; returns 1 when there
# is definitively no seed (a clean 404) or the retry window elapses without the
# endpoint coming up (air-gapped box). Bounded — never blocks forever.
fetch_seed() { # URL -> body on stdout (0) | no seed (1)
    local url="$1" deadline="${MALMO_SEED_FETCH_DEADLINE:-60}" host port path body rc start
    parse_url "$url"
    start=$SECONDS
    while :; do
        body="$(http_get "$host" "$port" "$path")"; rc=$?
        if [ "$rc" -eq 0 ]; then printf '%s' "$body"; return 0; fi
        [ "$rc" -eq 1 ] && return 1   # clean 404 — no user-data set, not an error
        [ $((SECONDS - start)) -ge "$deadline" ] && return 1
        sleep "${MALMO_SEED_FETCH_INTERVAL:-2}"
    done
}

main() {
    # 1. SMBIOS / systemd-credential channel first. Present → write and exit,
    #    never touching the network (keeps the air-gapped test lane fast).
    if [ -n "${CREDENTIALS_DIRECTORY:-}" ] && [ -f "$SRC" ]; then
        materialize_from_file "$SRC"
        return 0
    fi

    # 2. Real-cloud metadata/user-data channel (credential absent).
    local url="${MALMO_SEED_METADATA_URL:-http://169.254.169.254/hetzner/v1/userdata}"
    local body tmp
    if ! body="$(fetch_seed "$url")"; then
        echo "malmo-seed: no seed delivered (SMBIOS or metadata); leaving box unprovisioned" >&2
        return 0
    fi
    tmp="$(mktemp)"
    printf '%s' "$body" > "$tmp"
    materialize_from_file "$tmp"
    rm -f "$tmp"
}

# Run main only when executed; sourcing (the unit test) gets the functions alone.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
