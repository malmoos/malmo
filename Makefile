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

.PHONY: build host-agent brain run-agent run-brain net caddy caddy-down ui openapi clean help

help:
	@echo "make build       - compile brain + host-agent"
	@echo "make net         - create the malmo-ingress docker network"
	@echo "make caddy       - start the dev Caddy reverse proxy (container)"
	@echo "make run-agent   - run the fake host-agent (foreground)"
	@echo "make run-brain   - run the brain (foreground)"
	@echo "make ui          - run the Vite dev server (web-ui/)"
	@echo "make caddy-down  - stop the dev Caddy"
	@echo "make clean       - stop apps, remove dev state"
	@echo ""
	@echo "Typical: 4 terminals -> make caddy ; make run-agent ; make run-brain ; make ui"

build: host-agent brain

host-agent:
	$(GO) build -o $(DEV_DIR)/host-agent ./cmd/host-agent

brain:
	$(GO) build -o $(DEV_DIR)/brain ./cmd/brain

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

# Emit the OpenAPI schema (BRAIN_UI_PROTOCOL.md CI hook). The running brain
# serves it; this just fetches it.
openapi:
	@curl -s localhost:8080/openapi.json | python3 -m json.tool > openapi.json && echo "wrote openapi.json"

clean: caddy-down
	-@docker ps -aq --filter "label=com.docker.compose.project" --filter "name=malmo-" | xargs -r docker rm -f
	-@docker network ls -q --filter "name=malmo-app-" | xargs -r docker network rm
	rm -rf $(DEV_DIR)/state
