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
- **`libpam0g-dev`** (Linux only). Required to build/test the
  `internal/hostagent/pamverifier` package and `cmd/host-agent-real`. Without
  it, `go test ./...` will fail on those targets with `fatal error:
  security/pam_appl.h: No such file or directory`. The fake binary
  (`cmd/host-agent`) and everything else build without it.

  ```bash
  sudo apt install libpam0g-dev
  ```

  You also need `CGO_CFLAGS=-D_GNU_SOURCE` because `msteinert/pam` v2.1.0
  uses `RTLD_NEXT` (a GNU extension) without setting `_GNU_SOURCE` itself.
  The `Makefile` exports this globally; if you run `go` directly, prefix:

  ```bash
  CGO_CFLAGS=-D_GNU_SOURCE go test ./...
  # or use the Makefile targets:
  make test          # full suite, needs libpam0g-dev
  make test-nopam    # skip pamverifier, no libpam0g-dev needed
  ```

  macOS devs: skip — `host-agent-real` is Linux-only by design (it talks to
  `/etc/shadow` via Linux PAM). Use `make test-nopam` or just run the fake.

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

## Running host-agent-real

`cmd/host-agent-real` is the production binary: it uses real PAM for
`verify-password` while `set-password`, `set-role`, and `delete-user` remain
in-memory fakes (tracked in `docs/progress/0011-host-agent-pam-verify.md`).

**Prerequisites:**

```bash
apt install libpam0g-dev      # PAM headers for CGO
sudo cp dev/pam/malmo /etc/pam.d/malmo
```

**Build and run:**

```bash
go build ./cmd/host-agent-real
sudo ./cmd/host-agent-real    # must run as root — pam_unix.so requires privilege
```

Point the brain at it by setting `MALMO_AGENT_SOCK` to the same path the real
binary listens on. Note: because `set-password` is still fake, dashboard login
will fail until real `useradd`/`passwd` integration lands — use
`cmd/host-agent` (fake) for all normal dev work.

## Verifying routing

malmo uses **Host-header-based subdomain routing** — each installed app gets a
virtual host (`<slug>.malmo.local`), never a path prefix. This keeps apps in
separate browser origins (same-origin policy enforcement — see `SPEC.md`).

**Dev port wrinkle:** in dev, Caddy listens on `:8088` because host port 80 is
typically taken on a laptop. In production it's `:80`. The `.local` mDNS names
resolve to port 80, so LAN browser testing via `<slug>.malmo.local` doesn't
work from another device against the dev stack — the port mismatch breaks it.
The test script and the ad-hoc recipe below both work around this with `--resolve`
or an explicit `-H Host:` header.

**Quick ad-hoc check** (after installing whoami from the catalog):

```bash
# Host-header method — should return the whoami echo page
curl -H "Host: whoami.malmo.local" http://localhost:8088/

# --resolve variant — same effect, avoids quoting issues in scripts
curl --resolve "whoami.malmo.local:8088:127.0.0.1" http://whoami.malmo.local:8088/

# Path-based — should NOT return 200 (route does not exist)
curl -s -o /dev/null -w "%{http_code}" http://localhost:8088/whoami/
```

**Automated end-to-end test** (installs whoami, exercises positive/negative
routing, then uninstalls — requires `make dev` already running):

```bash
make test-caddy
```

The script (`dev/test-caddy-routing.sh`) boots a `caddytest` admin user on
first run and reuses it on subsequent runs. Reset with `make clean && make dev`.

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
