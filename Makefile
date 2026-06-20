# malmo dev orchestration. The fast inner loop runs everything natively on the
# host (no VM): host-agent + brain as Go processes, Caddy as a container, the
# UI on Vite. The VM is the outer loop for host-integrated parts (boot, LUKS,
# systemd) and is not wired here yet.

GO ?= $(shell command -v go || echo $(HOME)/.local/go/bin/go)
# gofmt from the same toolchain as $(GO), so `make check` matches CI exactly
# regardless of what's on PATH.
GOFMT ?= $(shell $(GO) env GOROOT)/bin/gofmt
DEV_DIR := .dev
STATE_DIR := $(DEV_DIR)/state
AGENT_SOCK := $(abspath $(DEV_DIR)/agent.sock)

export MALMO_AGENT_SOCK := $(AGENT_SOCK)
export MALMO_STATE_DIR := $(STATE_DIR)
export MALMO_CATALOG_DIR := ./catalog

.PHONY: build host-agent brain host-agent-real host-agent-real-hosted brain-image ui-image control-plane-images build-cloud-image check check-web fmt fmt-check vet test test-all test-nopam test-caddy test-avahi test-netstate test-health test-usermgr test-usermgr-nspawn test-boot-chain-nspawn test-medium-qemu test-cloud-qemu run-agent run-brain net caddy caddy-down ui dev stop openapi openapi-check clean check-state-owner help

# msteinert/pam v2.1.0 uses RTLD_NEXT, a GNU extension that requires
# _GNU_SOURCE at C compile time. Apply globally; harmless to non-cgo builds.
export CGO_CFLAGS := -D_GNU_SOURCE

help:
	@echo "make check       - pre-PR gate: gofmt + vet + full test suite (Go). Run before every PR."
	@echo "make check-web   - pre-PR gate for frontend changes: web-ui typecheck + build"
	@echo "make openapi     - regenerate api/openapi.{json,yaml} from the brain (no server)"
	@echo "make fmt         - rewrite Go sources into gofmt-canonical form (autofix)"
	@echo "make dev         - all three foreground procs in one terminal (recommended)"
	@echo "make build       - compile brain + host-agent"
	@echo "make host-agent-real-hosted - build the slim hosted-cloud host-agent (-tags hosted; #204/C1c)"
	@echo "make control-plane-images - build malmo-brain + malmo-ui images and docker-save the control-plane bundle to .dev/"
	@echo "make build-cloud-image - build the lean hosted cloud VM image via mkosi (no boot; #203)"
	@echo "make net         - create the malmo-ingress docker network"
	@echo "make caddy       - start the dev Caddy reverse proxy (container)"
	@echo "make run-agent   - run the fake host-agent (foreground)"
	@echo "make run-brain   - run the brain (foreground)"
	@echo "make ui          - run the Vite dev server (web-ui/)"
	@echo "make caddy-down  - stop the dev Caddy"
	@echo "make clean       - stop apps, remove dev state"
	@echo "make test-caddy  - end-to-end Caddy routing test (requires make dev)"
	@echo "make test-health - end-to-end storage-health pipeline (self-contained, ~3s)"
	@echo "make test-usermgr-nspawn - run usermgrtest in systemd-nspawn (needs sudo)"
	@echo "make test-boot-chain-nspawn - boot dist/systemd units in nspawn + assert shape (needs sudo)"
	@echo "make test-medium-qemu - QEMU+swtpm boot with real kernel + TPM (needs sudo; first run ~5 min)"
	@echo "make test-cloud-qemu - QEMU boot of the hosted cloud image; control plane up (needs sudo; no swtpm/LUKS)"
	@echo ""
	@echo "One-terminal: make dev   (Caddy started detached; Ctrl-C stops the rest)"
	@echo "Four terminals: make caddy ; make run-agent ; make run-brain ; make ui"

# ---- Quality gate -------------------------------------------------------
# `make check` is the pre-PR gate. It mirrors CI's Go job and the
# definition-of-done in docs/dev/contributing.md: gofmt-clean, vet-clean, and
# the full test suite green. Cheapest checks run first so it fails fast.
# Frontend changes additionally need `make check-web`. The full test suite
# needs libpam0g-dev (see docs/dev/running-locally.md); use the individual
# targets if you don't have the headers.
check: fmt-check vet openapi-check test

# Web typecheck + production build (mirrors CI's web job). Needs node/npm.
# Regenerates the OpenAPI TS client from the committed spec and fails if the
# checked-in copy (web-ui/src/generated/openapi.ts) is stale — keeps the
# generated client honest the way openapi-check keeps the spec honest.
check-web:
	cd web-ui && npm ci && npm run gen:api
	@git diff --quiet web-ui/src/generated/openapi.ts || { \
	  echo "web-ui/src/generated/openapi.ts is stale — regenerate with: (cd web-ui && npm run gen:api)"; exit 1; }
	cd web-ui && npm run build

# Rewrite Go sources into gofmt-canonical form (autofix).
fmt:
	$(GOFMT) -w $$(git ls-files '*.go')

# Fail (listing offenders) if any Go source isn't gofmt-clean. Pure check —
# never mutates the tree; run `make fmt` to fix.
fmt-check:
	@out=$$($(GOFMT) -l $$(git ls-files '*.go')); \
	  if [ -n "$$out" ]; then \
	    echo "These files are not gofmt-clean:"; echo "$$out"; \
	    echo "Fix with: make fmt"; exit 1; \
	  fi

vet:
	$(GO) vet ./...

build: host-agent brain

host-agent:
	$(GO) build -o $(DEV_DIR)/host-agent ./cmd/host-agent

host-agent-real:
	$(GO) build -o $(DEV_DIR)/host-agent-real ./cmd/host-agent-real

# Slim hosted-cloud host-agent (ENVIRONMENT.md # How the profile is realized —
# "A build-tagged slim cloud host-agent"; #204/C1c). The same production binary
# with the appliance's LAN/discovery stack — NetworkManager (netstate) + Avahi
# mDNS publish (avahipublisher) + the network watcher — compiled out via
# `-tags hosted`; the kept seams (PAM verify, user mgmt, health/system reporters,
# per-app logs, reboot, brain launch) are identical. Linux + CGO + libpam0g-dev,
# same as host-agent-real. The cloud image build (#203/C1b, #205/C2) consumes it.
host-agent-real-hosted:
	$(GO) build -tags hosted -o $(DEV_DIR)/host-agent-real-hosted ./cmd/host-agent-real

brain:
	$(GO) build -o $(DEV_DIR)/brain ./cmd/brain

# ---- Control-plane images (M0, #163) -----------------------------------
# Build the two malmo OCI images and `docker save` them — together with the two
# third-party control-plane images the brain's compose pulls — into a tarball
# bundle under .dev/ (BUILD.md # 5 / # 5b; TESTING.md # Full-stack control-plane
# integration). The medium-lane VM bakes this bundle and docker-loads it at
# first boot; it has no network, so the third-party images must be in the bundle
# too. Needs only Docker (the images build hermetically — no host Go/Node).
CP_IMAGE_DIR := $(DEV_DIR)/control-plane
BRAIN_IMAGE  := malmo-brain:dev
UI_IMAGE     := malmo-ui:dev
CADDY_IMAGE  := caddy:2-alpine
PROXY_IMAGE  := tecnativa/docker-socket-proxy:v0.4.2

brain-image:
	docker build -f cmd/brain/Dockerfile -t $(BRAIN_IMAGE) .

ui-image:
	docker build -f web-ui/Dockerfile -t $(UI_IMAGE) web-ui

control-plane-images: brain-image ui-image
	@mkdir -p $(CP_IMAGE_DIR)
	docker pull $(CADDY_IMAGE)
	docker pull $(PROXY_IMAGE)
	docker save $(BRAIN_IMAGE) -o $(CP_IMAGE_DIR)/malmo-brain.tar
	docker save $(UI_IMAGE)    -o $(CP_IMAGE_DIR)/malmo-ui.tar
	docker save $(CADDY_IMAGE) -o $(CP_IMAGE_DIR)/caddy.tar
	docker save $(PROXY_IMAGE) -o $(CP_IMAGE_DIR)/docker-socket-proxy.tar
	@echo "saved control-plane image bundle to $(CP_IMAGE_DIR)/"

# Run the full suite. Requires libpam0g-dev for the pamverifier package.
test:
	$(GO) test ./...

# Skip the pamverifier package (no libpam0g-dev required).
test-nopam:
	$(GO) test $$($(GO) list ./... | grep -v pamverifier)

# Integration tests for the Avahi DBus publisher. Requires avahi-daemon
# running on the host. No sudo needed (default DBus policy allows it).
test-avahi:
	$(GO) test -tags avahitest ./internal/hostagent/avahipublisher/

# Integration tests for the NetworkManager LAN-interface provider. Requires
# NetworkManager running on the host. No sudo needed (read-only DBus calls).
test-netstate:
	$(GO) test -tags nmtest ./internal/hostagent/netstate/

# Integration tests for LinuxUserManager. Exercises real useradd + chpasswd
# against /etc/passwd and /etc/shadow. MUST run as root and is intended for
# the nspawn lane — do NOT run on a developer laptop. See
# docs/progress/0015-host-agent-set-password.md.
test-usermgr:
	sudo -E $(GO) test -tags usermgrtest ./internal/hostagent/usermgr/

# Run the usermgrtest-tagged tests inside systemd-nspawn (fast lane per
# docs/specs/TESTING.md). Bootstraps a minimal Debian rootfs at
# .dev/nspawn/rootfs on first run (cached after); each test invocation
# runs in an ephemeral overlay. Requires mmdebstrap + systemd-container.
# See docs/progress/0018-nspawn-usermgr-lane.md.
test-usermgr-nspawn:
	sudo -E ./dev/test-nspawn/run-usermgr-tests.sh

# Boot-chain fast-lane test: systemd-nspawn --boot of the dist/systemd
# units, asserting dependency shape, drop-in application, and end-to-end
# storage-verify reporter execution. Reuses the .dev/nspawn/rootfs
# bootstrapped by run-usermgr-tests.sh (bumped to v2 for systemd-sysv).
# See docs/progress/0020-nspawn-boot-chain-lane.md.
test-boot-chain-nspawn:
	sudo -E ./dev/test-nspawn/run-boot-chain-tests.sh

# Medium-lane test: QEMU+swtpm boot of a mkosi-built bookworm image
# with a real kernel, real systemd userspace, and an emulated TPM.
# Proves the scaffolding for the TESTING.md # Medium lane is operational.
# First run builds the image (~3-5 min); subsequent runs ~1-2 min.
# Requires mkosi v22+, swtpm, qemu-system-x86, ovmf — bootstrap.sh
# prints an install pointer if anything is missing.
# See docs/progress/0021-qemu-medium-lane-scaffolding.md.
test-medium-qemu:
	sudo -E ./dev/test-qemu/run-medium-tests.sh

# Cloud-lane boot proof (C2, #205): build the hosted cloud image, convert it to
# the qcow2 cloud artifact, and boot it ONCE in QEMU to prove the control plane
# comes up and serves — no swtpm, no LUKS, no installer ("the disk IS the
# installed system", ENVIRONMENT.md # Provisioning). The in-VM self-check
# (cloud-assertions.sh) asserts the baked images loaded, the four control-plane
# containers run, the dashboard answers through Caddy, and the hosted /setup gate
# returns 503 (no seed). Air-gapped (restrict=on) so a stray pull hard-fails.
# Requires mkosi v22+, qemu-system-x86, ovmf, docker, go, libpam0g-dev —
# bootstrap.sh prints an install pointer if anything is missing.
# See docs/progress/cloud-vm-boot-proof.md.
test-cloud-qemu:
	sudo -E ./dev/cloud/run-cloud-tests.sh

# Build the lean hosted cloud-VM image (C1b, #203) via mkosi and assert it is
# genuinely lean (no NetworkManager/Avahi/Samba/mergerfs/cryptsetup/tpm2-tools)
# with the /etc/malmo/profile=hosted marker baked in. Output: a raw GPT disk
# image under .dev/cloud/. This is the image *definition* slice only — the
# qcow2 packaging + QEMU boot proof (control plane up) are #205/C2, and the
# slim cloud host-agent it boots is #204/C1c. Needs mkosi v22+; bootstrap.sh
# prints an install pointer if anything is missing.
# See docs/progress/hosted-cloud-image.md.
build-cloud-image:
	./dev/cloud/bootstrap.sh

# End-to-end Caddy routing verification. Assumes `make dev` is running.
# Tests Host-header routing, confirms path-based routing does NOT work,
# and verifies route withdrawal after uninstall.
test-caddy:
	./dev/test-caddy-routing.sh

# Self-contained end-to-end test of the storage-health pipeline
# (docs/progress/0019). Builds the three binaries, spins up the fake
# host-agent + brain in a tempdir, exercises six cases through the real
# wire format, and tears down. ~3 seconds. No daemons required.
test-health:
	./dev/test-health.sh

net:
	@docker network inspect malmo-ingress >/dev/null 2>&1 || docker network create malmo-ingress

caddy: net
	docker compose -f dev/docker-compose.yml up -d

caddy-down:
	docker compose -f dev/docker-compose.yml down

run-agent: host-agent
	@mkdir -p $(DEV_DIR)
	$(DEV_DIR)/host-agent

run-brain: brain net
	@mkdir -p $(STATE_DIR)
	$(DEV_DIR)/brain

# WEB_UI.md specifies pnpm; npm is used here until pnpm is set up on the box.
ui:
	cd web-ui && npm install && npm run dev

# Guard against a root-owned dev state dir. App containers run as root in the
# skeleton and write root-owned files into instances/<id>/; a privileged run or
# a half-finished manual `rm` (which can't remove that root-owned data) can leave
# instances/ itself root-owned. The brain then fails an install mid-transaction
# with a cryptic `mkdir … permission denied` (after a SQLite row already exists).
# Catch it up front with an actionable message. The supported reset is `make
# clean` (reclaims root-owned data via a throwaway root container), never a hand `rm`.
check-state-owner:
	@if [ -d "$(STATE_DIR)/instances" ] && [ "$$(stat -c %u "$(STATE_DIR)/instances")" != "$$(id -u)" ]; then \
	  echo "error: $(STATE_DIR)/instances is owned by uid $$(stat -c %u "$(STATE_DIR)/instances"), not you (uid $$(id -u))."; \
	  echo "       App installs will fail with 'mkdir … permission denied'."; \
	  echo "       Reset with:  make clean      (or: sudo chown -R $$(id -un) $(STATE_DIR))"; \
	  exit 1; \
	fi

# One-terminal dev loop. Pure bash: backgrounds the three foreground procs,
# prefixes their output with [agent]/[brain]/[ui], and the trap kills the
# whole process group on Ctrl-C. Caddy is started detached because it's
# already a long-running container — no point supervising it here.
dev: check-state-owner build caddy
	@mkdir -p $(STATE_DIR)
	@cd web-ui && [ -d node_modules ] || npm install
	@trap 'kill 0' INT TERM EXIT; \
	  (MALMO_DEV_AVAHI=1 $(DEV_DIR)/host-agent 2>&1 | sed -u 's/^/[agent] /') & \
	  ($(DEV_DIR)/brain      2>&1 | sed -u 's/^/[brain] /') & \
	  (cd web-ui && npm run dev 2>&1 | sed -u 's/^/[ui]    /') & \
	  wait

# Regenerate the committed OpenAPI spec (api/openapi.{json,yaml}) from the huma
# handler registrations — no running brain, no port (BRAIN_UI_PROTOCOL.md
# # Codegen). The spec is the substrate for the web-ui's generated TS client
# (web-ui `npm run gen:api`) and the freshness gate below.
openapi:
	@go run ./cmd/openapi-gen -o api && echo "wrote api/openapi.json, api/openapi.yaml"

# Fail if the committed spec is stale (re-emit to a scratch dir and diff). Pure
# check — never mutates the tree; run `make openapi` to refresh. Mirrors the
# fmt-check pattern; wired into `make check` and CI so a brain DTO change that
# isn't regenerated can't merge silently.
openapi-check:
	@tmp=$$(mktemp -d); \
	  go run ./cmd/openapi-gen -o $$tmp || { rm -rf $$tmp; exit 1; }; \
	  if ! diff -q api/openapi.json $$tmp/openapi.json >/dev/null || ! diff -q api/openapi.yaml $$tmp/openapi.yaml >/dev/null; then \
	    echo "api/openapi.{json,yaml} is stale — regenerate with: make openapi"; \
	    diff -u api/openapi.json $$tmp/openapi.json || true; \
	    rm -rf $$tmp; exit 1; \
	  fi; \
	  rm -rf $$tmp; echo "openapi spec is fresh"

# Stop the native dev stack (`make dev` runs brain/host-agent/vite outside
# Docker). Without this, `clean` leaves the brain running with the deleted
# malmo.db still open (deleted-but-open inode), so it keeps serving the old
# state and the wiped DB silently comes back — `clean` looks like a no-op.
# Best-effort: pkill exits non-zero when nothing matches, hence the `-` prefix.
# The supervisor is matched by its MALMO_DEV_AVAHI env prefix; the binaries by
# their $(DEV_DIR) path; vite by this repo's absolute path so we don't reap an
# unrelated Vite on the box.
stop:
	-@pkill -f 'MALMO_DEV_AVAHI=1' 2>/dev/null
	-@pkill -f '$(DEV_DIR)/brain' 2>/dev/null
	-@pkill -f '$(DEV_DIR)/host-agent' 2>/dev/null
	-@pkill -f '$(CURDIR)/web-ui/node_modules/.bin/vite' 2>/dev/null
	@echo "stopped native dev stack (brain/host-agent/vite)"

# clean = back to a blank slate: stop the native stack (stop) and the Caddy
# container (caddy-down), remove app containers/networks, then wipe dev state.
# stop must run before the rm or the live brain keeps the DB inode alive.
clean: stop caddy-down
	-@docker ps -aq --filter "label=com.docker.compose.project" --filter "name=malmo-" | xargs -r docker rm -f
	-@docker network ls -q --filter "name=malmo-app-" | xargs -r docker network rm
	@# App containers (Postgres et al.) write their data as root inside bind
	@# mounts, so instances/<id>/data is root-owned on the host — same as prod,
	@# where the privileged uninstall path removes it. A plain `rm` as the dev
	@# user can't, so reclaim it via a throwaway root container first. No sudo.
	-@docker run --rm -v $(abspath $(DEV_DIR)/state):/state alpine:3 rm -rf /state 2>/dev/null || true
	rm -rf $(DEV_DIR)/state
