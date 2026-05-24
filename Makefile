# malmo dev orchestration. The fast inner loop runs everything natively on the
# host (no VM): host-agent + brain as Go processes, Caddy as a container, the
# UI on Vite. The VM is the outer loop for host-integrated parts (boot, LUKS,
# systemd) and is not wired here yet.

GO ?= $(shell command -v go || echo $(HOME)/.local/go/bin/go)
DEV_DIR := .dev
STATE_DIR := $(DEV_DIR)/state
AGENT_SOCK := $(abspath $(DEV_DIR)/agent.sock)

export MALMO_AGENT_SOCK := $(AGENT_SOCK)
export MALMO_STATE_DIR := $(STATE_DIR)
export MALMO_CATALOG_DIR := ./catalog

.PHONY: build host-agent brain host-agent-real test test-all test-caddy run-agent run-brain net caddy caddy-down ui dev openapi clean help

# msteinert/pam v2.1.0 uses RTLD_NEXT, a GNU extension that requires
# _GNU_SOURCE at C compile time. Apply globally; harmless to non-cgo builds.
export CGO_CFLAGS := -D_GNU_SOURCE

help:
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
	@echo ""
	@echo "One-terminal: make dev   (Caddy started detached; Ctrl-C stops the rest)"
	@echo "Four terminals: make caddy ; make run-agent ; make run-brain ; make ui"

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

# End-to-end Caddy routing verification. Assumes `make dev` is running.
# Tests Host-header routing, confirms path-based routing does NOT work,
# and verifies route withdrawal after uninstall.
test-caddy:
	./dev/test-caddy-routing.sh

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
	  ($(DEV_DIR)/host-agent 2>&1 | sed -u 's/^/[agent] /') & \
	  ($(DEV_DIR)/brain      2>&1 | sed -u 's/^/[brain] /') & \
	  (cd web-ui && npm run dev 2>&1 | sed -u 's/^/[ui]    /') & \
	  wait

# Emit the OpenAPI schema (BRAIN_UI_PROTOCOL.md CI hook). The running brain
# serves it; this just fetches it.
openapi:
	@curl -s localhost:8080/openapi.json | python3 -m json.tool > openapi.json && echo "wrote openapi.json"

clean: caddy-down
	-@docker ps -aq --filter "label=com.docker.compose.project" --filter "name=malmo-" | xargs -r docker rm -f
	-@docker network ls -q --filter "name=malmo-app-" | xargs -r docker network rm
	rm -rf $(DEV_DIR)/state
