#!/usr/bin/env bash
# Shared hosted-cloud control-plane staging (#242). SOURCED by both the
# production lean build (dev/cloud/bootstrap.sh) and the boot-proof test lane
# (dev/cloud/test/bootstrap.sh) so the runtime wiring is staged identically for
# both — the test image must validate the real production wiring, not a divergent
# copy (ENVIRONMENT.md # How the profile is realized).
#
# It builds + stages everything a hosted tenant box needs to self-bootstrap on
# first boot, into dev/cloud/mkosi.extra.wiring/ (an ExtraTree of dev/cloud/, so
# it lands in BOTH the production image and the test image, which Include=.. it):
#   - the slim host-agent (-tags hosted, #204) + its unit + the cloud brain drop-in
#   - the PAM stack host-agent-real's verify-password needs
#   - the control-plane image bundle (brain/ui/proxy + the acmedns Caddy) + the
#     first-boot loader unit (reused verbatim from the medium lane)
#   - the control-plane compose + caddy.json at the same host path the brain sees
#   - the first-boot provisioning-seed materializer + its unit
#
# Deliberately NOT staged here: the serial-driven self-check (cloud-assertions.sh
# + its unit) — that is test-lane-only and stays in dev/cloud/test/.
#
# Expects these set by the sourcing script: REPO_ROOT, CALLER (may be empty), GO,
# CP_BUNDLE, WIRING (the dev/cloud/mkosi.extra.wiring/ target dir), WORK (a build
# scratch dir for the intermediate host-agent binary).
# Sourced, not executed — inherits the caller's shell options. Both callers have
# set -euo pipefail; mirror it here so sourcing from a non-strict script still fails fast.
set -euo pipefail

# Build a Go binary, as the invoking user when running under sudo so the caller
# owns the build cache. CGO on (PAM verify is kept in hosted) + CGO_CFLAGS as the
# Makefile sets — dynamic against the build host's libpam, run on the Debian VM.
stage_build_go() { # OUT PKG [extra go-build args...]
    local out="$1" pkg="$2"; shift 2
    if [ -n "$CALLER" ]; then
        sudo -u "$CALLER" env CGO_ENABLED=1 CGO_CFLAGS=-D_GNU_SOURCE "$GO" build "$@" -o "$out" "$pkg"
    else
        CGO_ENABLED=1 CGO_CFLAGS=-D_GNU_SOURCE "$GO" build "$@" -o "$out" "$pkg"
    fi
}

stage_control_plane() {
    local hostagent_bin="${WORK}/host-agent-real-hosted"

    # --- slim host-agent (-tags hosted, #204).
    stage_build_go "$hostagent_bin" "${REPO_ROOT}/cmd/host-agent-real/" -tags hosted

    # --- control-plane image bundle. Rebuild only when absent (or forced via
    # MALMO_REBUILD_CP=1): `make control-plane-images` re-runs the brain (Go) + UI
    # (Vue) docker builds, regenerating ~13 GB of BuildKit cache each time. The
    # images don't change while iterating on the boot wiring, so reuse the tarballs.
    if [ "${MALMO_REBUILD_CP:-0}" = "1" ] || ! ls "$CP_BUNDLE"/malmo-brain.tar "$CP_BUNDLE"/malmo-ui.tar \
            "$CP_BUNDLE"/caddy.tar "$CP_BUNDLE"/docker-socket-proxy.tar >/dev/null 2>&1; then
        echo "building + saving control-plane image bundle (docker)..."
        make -C "$REPO_ROOT" control-plane-images
    else
        echo "reusing existing control-plane image bundle (set MALMO_REBUILD_CP=1 to force)"
    fi

    # --- stage mkosi.extra.wiring/ (generated; gitignored).
    rm -rf "$WIRING"
    mkdir -p "$WIRING/etc/systemd/system/host-agent.service.d" \
             "$WIRING/usr/lib/malmo" \
             "$WIRING/usr/local/bin" \
             "$WIRING/etc/pam.d" \
             "$WIRING/var/lib/malmo/control-plane-images" \
             "$WIRING/var/lib/malmo/control-plane" \
             "$WIRING/var/lib/malmo/catalog"

    # Slim host-agent at the production path host-agent.service ExecStarts.
    cp "$hostagent_bin" "$WIRING/usr/lib/malmo/host-agent-real"
    chmod 0755 "$WIRING/usr/lib/malmo/host-agent-real"
    cp "${REPO_ROOT}/dist/systemd/host-agent.service" "$WIRING/etc/systemd/system/"

    # host-agent bootstrap drop-in: point the brain bootstrap at the baked
    # dev-tagged images + tarballs + the staged control-plane dir, and order after
    # the first-boot image load so every image is present when the bootstrap runs.
    # The brain reads /etc/malmo/profile (mounted from the host by host-agent —
    # brainlaunch ProfileMarkerPath) to resolve profile=hosted; no env needed.
    cat > "$WIRING/etc/systemd/system/host-agent.service.d/10-cloud-brain.conf" <<'EOF'
[Unit]
After=malmo-load-images.service

[Service]
Environment=MALMO_BRAIN_IMAGE=malmo-brain:dev
Environment=MALMO_BRAIN_IMAGE_TAR=/var/lib/malmo/control-plane-images/malmo-brain.tar
Environment=MALMO_PROXY_IMAGE=tecnativa/docker-socket-proxy:v0.4.2
Environment=MALMO_PROXY_IMAGE_TAR=/var/lib/malmo/control-plane-images/docker-socket-proxy.tar
Environment=MALMO_CONTROL_PLANE_DIR=/var/lib/malmo/control-plane
Environment=MALMO_DASHBOARD_UI_UPSTREAM=malmo-ui:80
Environment=MALMO_CADDY_IMAGE=malmo-caddy-acmedns:dev
EOF

    # PAM stack for host-agent-real's verify-password (kept in hosted). Without it
    # pam_start("malmo") falls back to /etc/pam.d/other (deny). The malmo group is
    # provisioned by the postinst.
    cp "${REPO_ROOT}/dev/pam/malmo" "$WIRING/etc/pam.d/malmo"

    # Control-plane image bundle + first-boot loader (reused verbatim from the
    # medium lane — same offline-first mechanism, TESTING.md # Full-stack control-
    # plane integration). A tenant box is air-gapped at boot, so every image is a
    # local tarball; the VM never pulls.
    cp "$CP_BUNDLE"/*.tar "$WIRING/var/lib/malmo/control-plane-images/"
    # Hosted-only Caddy swap: the wildcard cert needs the caddy-dns/acmedns module
    # (ACME DNS-01 — os #207/C3b), which stock caddy:2-alpine lacks. Build the
    # xcaddy recipe and docker-save it OVER the *staged* caddy.tar — not the shared
    # $CP_BUNDLE copy, which the appliance/medium lane keeps on stock caddy (it does
    # no ACME). The drop-in above sets MALMO_CADDY_IMAGE so the brain's control-plane
    # compose runs this image; load-control-plane-images.sh loads it from the tar
    # regardless of filename (build-host network only; the VM never pulls).
    local caddy_acmedns_image="malmo-caddy-acmedns:dev"
    echo "building hosted Caddy with the caddy-dns/acmedns module (xcaddy)..."
    docker build -t "$caddy_acmedns_image" "${REPO_ROOT}/dev/control-plane/caddy-acmedns/"
    docker save "$caddy_acmedns_image" -o "$WIRING/var/lib/malmo/control-plane-images/caddy.tar"
    cp "${REPO_ROOT}/dev/test-qemu/load-control-plane-images.sh" "$WIRING/usr/lib/malmo/"
    chmod 0755 "$WIRING/usr/lib/malmo/load-control-plane-images.sh"
    cp "${REPO_ROOT}/dev/test-qemu/malmo-load-images.service" "$WIRING/etc/systemd/system/"

    # Control-plane compose + caddy.json staged at the SAME host path the brain
    # container sees (same-path bind constraint — socket-proxy-compose-validation.md).
    cp "${REPO_ROOT}/dev/control-plane/compose.yml" "$WIRING/var/lib/malmo/control-plane/"
    cp "${REPO_ROOT}/dev/control-plane/caddy.json"   "$WIRING/var/lib/malmo/control-plane/"

    # Door-1 app-store catalog. The brain installs store apps from MALMO_CATALOG_DIR,
    # which host-agent-real defaults to <DataDir>/catalog = /var/lib/malmo/catalog
    # (cmd/host-agent-real/main.go); it rides the brain's /var/lib/malmo bind mount
    # (read-only — manifests + icons). Without it the store can't load
    # ("read catalog: open /var/lib/malmo/catalog: no such file or directory").
    # The real shipping catalog (catalog/) is baked, unlike the test lanes' test-only
    # whoami catalog. It is data, not apt packages, so the lean package check is
    # unaffected.
    cp -r "${REPO_ROOT}/catalog/." "$WIRING/var/lib/malmo/catalog/"

    # First-boot provisioning-seed materializer + its oneshot (C3a cloud-lane, #220).
    # Lands the delivered seed at /var/lib/malmo/seed.json before host-agent launches
    # the brain; the postinst enables the unit. The materializer reads the SMBIOS
    # systemd-credential channel (the test lane + clouds that deliver via fw_cfg)
    # first, falling back to the real-cloud metadata/user-data endpoint when absent
    # (Hetzner — #246).
    cp "${REPO_ROOT}/dev/cloud/malmo-seed-materialize.sh" "$WIRING/usr/local/bin/malmo-seed-materialize.sh"
    chmod 0755 "$WIRING/usr/local/bin/malmo-seed-materialize.sh"
    cp "${REPO_ROOT}/dev/cloud/malmo-seed.service" "$WIRING/etc/systemd/system/"
}
