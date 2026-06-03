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

.PHONY: build host-agent brain host-agent-real check check-web fmt fmt-check vet test test-all test-nopam test-caddy test-avahi test-health test-usermgr test-usermgr-nspawn test-boot-chain-nspawn test-medium-qemu run-agent run-brain net caddy caddy-down ui dev openapi openapi-check clean help

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

brain:
	$(GO) build -o $(DEV_DIR)/brain ./cmd/brain

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

# One-terminal dev loop. Pure bash: backgrounds the three foreground procs,
# prefixes their output with [agent]/[brain]/[ui], and the trap kills the
# whole process group on Ctrl-C. Caddy is started detached because it's
# already a long-running container — no point supervising it here.
dev: build caddy
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
	  go run ./cmd/openapi-gen -o $$tmp; \
	  if ! diff -q api/openapi.json $$tmp/openapi.json >/dev/null || ! diff -q api/openapi.yaml $$tmp/openapi.yaml >/dev/null; then \
	    echo "api/openapi.{json,yaml} is stale — regenerate with: make openapi"; \
	    diff -u api/openapi.json $$tmp/openapi.json || true; \
	    rm -rf $$tmp; exit 1; \
	  fi; \
	  rm -rf $$tmp; echo "openapi spec is fresh"

clean: caddy-down
	-@docker ps -aq --filter "label=com.docker.compose.project" --filter "name=malmo-" | xargs -r docker rm -f
	-@docker network ls -q --filter "name=malmo-app-" | xargs -r docker network rm
	rm -rf $(DEV_DIR)/state
