# Custom Caddy with the acme-dns DNS provider (caddy-dns/acmedns) compiled in.
#
# The hosted profile serves every app at "<slug>.<box-id>.malmo.network" over a
# real Let's Encrypt wildcard cert obtained via ACME DNS-01 against the box's
# per-box acme-dns account (ENVIRONMENT.md # Networking & discovery; os #207 /
# C3b). DNS-01 needs a DNS-provider module in the Caddy binary, and the stock
# caddy:2-alpine image ships none — hence this xcaddy build.
#
# Only the hosted image points MALMO_CADDY_IMAGE at this build (see
# ../compose.yml). Appliance keeps riding stock caddy:2-alpine: it does no ACME
# (plain-HTTP ".local"), so adding the module there would be unused surface and
# would drop it off the upstream official image. When the appliance secure-URLs
# toggle ships, flipping it onto this same image is a one-line change.
#
# Build (from this directory):
#   docker build -t malmo-caddy-acmedns:<tag> .
# The real DNS-01 issuance path is exercised end-to-end in the cloud on-ramp
# (cloud #6 / CL6), not the inner loop (no real ACME/DNS locally).
# 2.10.0's builder ships Go 1.24.x; caddy-dns/acmedns >=0.7.0 requires Go >=1.24,
# and the builder pins GOTOOLCHAIN=local (no toolchain auto-download), so an older
# Caddy (2.8.4 → Go 1.23) fails `xcaddy build` with a "requires go >= 1.24" error.
ARG CADDY_VERSION=2.10.0
FROM caddy:${CADDY_VERSION}-builder AS builder
RUN xcaddy build --with github.com/caddy-dns/acmedns

FROM caddy:${CADDY_VERSION}-alpine
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
