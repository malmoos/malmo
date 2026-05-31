# malmo

A home-server OS in the same category as Umbrel / ZimaOS / CasaOS. North star: **simplicity for non-technical users**. Install it on an old laptop or PC, leave it running in the pantry, and run the apps you use daily — photos, notes, files, a shared grocery list — on hardware you own, with data you own. If the original app developer disappears, your app keeps working.

- **Files are first-class, apps are windows.** User content lives in
  `~/Photos/`, not inside an app's opaque library. Uninstalling an app never
  deletes your photos.
- **We are not a NAS.** Storage is plumbing in service of apps — no pools, no
  vdevs, no parity-as-first-class.
- **Closed by default.** No public exposure in v1; identity-based mesh only.

See [`docs/specs/SPEC.md`](docs/specs/SPEC.md) for the full vision.

## Status

**Spec + early implementation.** The design is captured as a set of detailed specs in [`docs/specs/`](docs/specs/); a **walking-skeleton** implementation now lives alongside them and proves the architecture spine end-to-end. See [`docs/progress/`](docs/progress/) for what's built and what's next.

## Repository layout

```
cmd/        Go entrypoints — brain (control plane) and host-agent
internal/   brain packages: api, lifecycle, catalog, manifest, store, caddy,
            hostclient, events, protocol
web-ui/     Vue 3 + Vite dashboard
catalog/    sample app manifests (manifest.yml + compose.yml)
dev/        local dev orchestration (Caddy container, config)
docs/       all documentation (specs, progress, dev guides)
Makefile    dev workflow — `make help`
```

## Quickstart (local dev, no VM)

Requires Docker, Node 20+, and Go 1.23+ (`make` uses `~/.local/go` if `go` isn't on `PATH`). Full guide: [`docs/dev/running-locally.md`](docs/dev/running-locally.md).

```bash
make caddy        # dev reverse proxy (container; apps on :80, admin :2019)
make run-agent    # fake host-agent (UNIX socket)   — separate terminal
make run-brain    # malmo-brain (:8080, native Go)   — separate terminal
make ui           # dashboard (Vite, :5173)          — separate terminal
```

Then open <http://localhost:5173> and install **Whoami** from the catalog.

`make dev` runs all of the above in one terminal and additionally publishes each app's `<slug>.malmo.local` name over real Avahi (`MALMO_DEV_AVAHI=1`), so installed apps are reachable by their portless `.local` URL from this box and other LAN devices (non-Android). Requires `avahi-daemon` running and host port `:80` free. See [`docs/dev/running-locally.md`](docs/dev/running-locally.md).

## Documentation

[`docs/README.md`](docs/README.md) is the map of everything. The [`CLAUDE.md`](CLAUDE.md) at the root holds working conventions for this repo, including the rule that **every change ships with documentation**.

## Contributing

New contributor (or pointing a coding agent at the repo)? Start with [`docs/dev/contributing.md`](docs/dev/contributing.md) — the end-to-end loop (orient → pick a task → branch → build → test → document → PR). Open implementation tasks live in [GitHub Issues](https://github.com/onel/malmo/issues) (`gh issue list --label P1`). All work happens on a branch and lands via a PR into `main`, with `Closes #<N>` to link the issue.
