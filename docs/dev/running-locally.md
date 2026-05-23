# Running malmo locally

malmo's dev model is **two loops**:

- **Inner loop (seconds) — all native, no VM.** The product logic — `malmo-brain`
  and the dashboard — runs directly on your machine against the local Docker
  socket. The host-agent is a **fake** that speaks the real protocol but stubs
  host ops. This is where ~90% of development happens.
- **Outer loop (minutes) — a VM.** Only the host-integrated parts (boot
  ordering, LUKS/TPM, systemd, NetworkManager, Avahi) need a booted OS. This is
  QEMU + swtpm per `../specs/TESTING.md`, and is **not wired up yet**.

This guide covers the inner loop.

## Prerequisites

- **Docker** + the `docker compose` plugin (`docker compose version`).
- **Node 20+** (`web-ui/.nvmrc` pins 20).
- **Go 1.23+.** If `go` isn't on your `PATH`, the `Makefile` falls back to
  `~/.local/go/bin/go`.

## Start the stack

**One terminal (recommended):**

```bash
make dev          # starts Caddy detached, then backgrounds agent + brain + ui
                  # with their output prefixed [agent]/[brain]/[ui].
                  # Ctrl-C kills all three.
```

No extra tools — pure bash supervisor with a `trap` that kills the process
group on signal.

**Four terminals (no extra tools):**

```bash
make caddy        # 1. dev reverse proxy (Caddy container, detached)
make run-agent    # 2. fake host-agent (UNIX socket at .dev/agent.sock)
make run-brain    # 3. malmo-brain (:8080, native)
make ui           # 4. dashboard (Vite, :5173)
```

`make help` lists all targets. Either way, open <http://localhost:5173>. On
first run the **Setup** page asks you to create the first admin and shows a
recovery code once — save it. Then install **Whoami** from the catalog and
watch it appear as `running`.

## How the pieces talk

```
browser ──▶ Vite :5173 ──(proxy /api)──▶ brain :8080
                                           │  ├─ docker compose CLI ─▶ Docker
                                           │  ├─ UNIX socket ─────────▶ host-agent (fake)
                                           │  └─ admin API ───────────▶ Caddy :2019
app HTTP:  curl -H 'Host: <slug>.malmo.local' localhost:8088 ─▶ Caddy ─▶ app container
```

- **Caddy listens on host `:8088`** (not 80 — taken on the build box) and exposes
  its admin API on `:2019`. App containers join the `malmo-ingress` network so
  Caddy reaches them by per-instance alias.
- **`.local` URLs don't resolve yet** (no real Avahi). To hit an installed app,
  send its `Host` header to Caddy:
  ```bash
  curl -H 'Host: whoami.malmo.local' localhost:8088
  ```

## Where state lives

Everything dev-generated is under `.dev/` (git-ignored):

```
.dev/
├── agent.sock                    # host-agent UNIX socket
├── brain  host-agent             # compiled binaries
└── state/
    ├── malmo.db                  # brain SQLite
    └── instances/<id>/           # per-app: manifest, compose, override, .env, data/
```

Override defaults with env vars: `MALMO_LISTEN`, `MALMO_STATE_DIR`,
`MALMO_CATALOG_DIR`, `MALMO_AGENT_SOCK`, `MALMO_CADDY_ADMIN`,
`MALMO_CADDY_LISTEN`.

## Reset

```bash
make clean        # stop the dev Caddy, remove malmo app containers + networks,
                  # wipe .dev/state
```

## Debugging the wire protocols

Both protocols are plain HTTP/JSON and curl-debuggable by design:

```bash
# Brain (UI protocol)
curl -s localhost:8080/api/v1/apps
curl -sN localhost:8080/api/v1/events          # live SSE stream
curl -s localhost:8080/openapi.json            # generated schema

# host-agent (host protocol), over the UNIX socket
curl --unix-socket .dev/agent.sock http://agent/v1/system/status
curl --unix-socket .dev/agent.sock http://agent/v1/discovery/state
```
