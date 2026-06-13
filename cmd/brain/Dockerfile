# malmo-brain control-plane image (BUILD.md # 5; DECISIONS.md 2026-06-13).
# Build context is the repo root:  docker build -f cmd/brain/Dockerfile -t malmo-brain:dev .
#
#   build   — compile the brain as a static binary. CGO is off: the brain's only
#             sqlite is modernc.org/sqlite (pure Go), so nothing links libc.
#   runtime — debian:trixie-slim with the docker CLI + compose plugin bundled.
#             NOT distroless: the brain orchestrates apps by shelling out to
#             `docker` / `docker compose` (internal/lifecycle/docker.go), which a
#             shell-less distroless runtime cannot host (DECISIONS.md 2026-06-13).

FROM golang:1.25-bookworm AS build
WORKDIR /src
# Resolve the module graph before copying the tree so the download layer caches
# across source-only changes.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/malmo-brain ./cmd/brain

FROM debian:trixie-slim AS runtime
# docker-ce-cli + docker-compose-plugin from Docker's official apt repo — the
# same trusted source as the host engine (BUILD.md # Docker package source). The
# repo codename is read from /etc/os-release so this tracks the base image.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl; \
    install -m 0755 -d /etc/apt/keyrings; \
    curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc; \
    chmod a+r /etc/apt/keyrings/docker.asc; \
    . /etc/os-release; \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian ${VERSION_CODENAME} stable" \
        > /etc/apt/sources.list.d/docker.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends docker-ce-cli docker-compose-plugin; \
    apt-get purge -y --auto-remove curl; \
    rm -rf /var/lib/apt/lists/*
# Declare the wire-protocol major host-agent checks before launch (lockstep
# guard — CONTROL_PLANE.md # Locked: host-agent launches the brain container).
# Must equal internal/protocol.Major; bump both together with the /v<N> URL
# prefix.
LABEL malmo.protocol.major="1"
COPY --from=build /out/malmo-brain /usr/local/bin/malmo-brain
# State dir and catalog are bind-mounts the supervisor provides at runtime; the
# brain reads its config from MALMO_* env (cmd/brain/main.go # loadConfig).
ENTRYPOINT ["/usr/local/bin/malmo-brain"]
