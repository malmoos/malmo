<div align="center">

# molma

**A home-server OS for people who want to own their data — not become sysadmins.**

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)

[What is molma?](#what-is-molma) · [Principles](#principles) · [Status](#status) · [Quickstart](#quickstart-local-dev-no-vm) · [Documentation](#documentation) · [Contributing](#contributing) · [Contributors](#contributors)

</div>

---

## What is molma

molma is a home-server OS in the same category as **Umbrel / ZimaOS / CasaOS**. Its north star is **simplicity for non-technical users**.

Install it on an old laptop or PC, leave it running in the pantry, and run the apps you use daily — photos, notes, files, a shared grocery list — on hardware you own, with data you own. If the original app developer disappears, your app keeps working.

> See [`docs/specs/SPEC.md`](docs/specs/SPEC.md) for the full vision.

## Principles

- **Files are first-class, apps are windows.** Your content lives in `~/Photos/`, not inside an app's opaque library. Uninstalling an app never deletes your photos.
- **We are not a NAS.** Storage is plumbing in service of apps — no pools, no vdevs, no parity-as-first-class.
- **Closed by default.** No public exposure in v1; identity-based mesh only.
- **The UI is the path.** Every privileged operation has a UI path; SSH is rescue-only, never required for daily use.

## Status

**Spec + early implementation.** The design is captured as a set of detailed specs in [`docs/specs/`](docs/specs/); a **walking-skeleton** implementation now lives alongside them and proves the architecture spine end-to-end (UI → brain → docker compose → Caddy route → SSE → uninstall).

See [`docs/progress/`](docs/progress/) for what's built and what's next.

## Repository layout

| Path | What lives here |
|---|---|
| `cmd/` | Go entrypoints — `brain` (control plane) and `host-agent` |
| `internal/` | brain packages: `api`, `lifecycle`, `catalog`, `manifest`, `store`, `caddy`, `hostclient`, `events`, `protocol` |
| `web-ui/` | Vue 3 + Vite dashboard |
| `catalog/` | sample app manifests (`manifest.yml` + `compose.yml`) |
| `dev/` | local dev orchestration (Caddy container, config) |
| `docs/` | all documentation (specs, progress, dev guides) |
| `Makefile` | dev workflow — run `make help` |

## Quickstart (local dev, no VM)

**Requirements:** Docker, Node 20+, and Go 1.23+ (`make` uses `~/.local/go` if `go` isn't on `PATH`).
Full guide: [`docs/dev/running-locally.md`](docs/dev/running-locally.md).

Run each in a separate terminal:

```bash
make caddy        # dev reverse proxy (container; apps on :80, admin :2019)
make run-agent    # fake host-agent (UNIX socket)
make run-brain    # molma-brain (:8080, native Go)
make ui           # dashboard (Vite, :5173)
```

Then open <http://localhost:5173> and install **Whoami** from the catalog.

> **Tip:** `make dev` runs all of the above in one terminal and additionally publishes each app's `<slug>.local` name over real Avahi (`MOLMA_DEV_AVAHI=1`), so installed apps are reachable by their portless `.local` URL from this box and other LAN devices (non-Android). Requires `avahi-daemon` running and host port `:80` free.

## Documentation

| Link | What it covers |
|---|---|
| [`docs/README.md`](docs/README.md) | The map of everything — one line per spec doc |
| [`docs/specs/SPEC.md`](docs/specs/SPEC.md) | Top-level vision and architecture |
| [`docs/specs/`](docs/specs/) | All design specs (source of truth) |
| [`docs/progress/`](docs/progress/) | Implementation progress (what's built, what's next) |
| [`docs/dev/`](docs/dev/) | Developer how-to (running locally, code-level architecture) |
| [`docs/dev/contributing.md`](docs/dev/contributing.md) | The contributor workflow, end to end |
| [`CLAUDE.md`](CLAUDE.md) | Working conventions for this repo |

## Contributing

New contributor (or pointing a coding agent at the repo)? Start with [`docs/dev/contributing.md`](docs/dev/contributing.md) — the end-to-end loop (orient → pick a task → branch → build → test → document → PR).

- Open implementation tasks live in [GitHub Issues](https://github.com/molmaos/molma/issues) (`gh issue list --label P1`).
- All work happens on a branch and lands via a PR into `main`.
- Link the issue your PR closes with `Closes #<N>`.
- **Every change ships with documentation** — a code change is not complete until its docs are written in the same change.

## License

molma is licensed under the [GNU Affero General Public License v3.0](LICENSE) (AGPL-3.0-only). By contributing, you agree to the [Contributor License Agreement](CLA.md).

## Contributors

<table>
  <tr>
    <td align="center">
      <a href="https://github.com/onel">
        <img src="https://github.com/onel.png" width="100" height="100" alt="onel" style="border-radius:50%"><br>
        <sub><b>onel</b></sub>
      </a>
    </td>
    <td align="center">
      <a href="https://github.com/bogdanpydev">
        <img src="https://github.com/bogdanpydev.png" width="100" height="100" alt="bogdanpydev" style="border-radius:50%"><br>
        <sub><b>bogdanpydev</b></sub>
      </a>
    </td>
  </tr>
</table>
