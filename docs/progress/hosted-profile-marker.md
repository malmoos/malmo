# Environment-profile marker + brain reads it (C1a)

- **Status:** done
- **Date:** 2026-06-19
- **Specs touched:** `ENVIRONMENT.md` (governing spec — implemented, not changed)

First implementation slice of the cloud-VM track in #196 (the C1 profile foundation, split marker → image profile → slim host-agent). The hosted environment profile was specced in #200 (`ENVIRONMENT.md`, pass 1); the most recent #196 progress entry is the [mkosi-no-ISO finding](iso-mkosi-finding.md). This is **C1a** (#202): the runtime-marker contract plus the brain-side reader. It is foundational and mkosi-independent — no image, host-agent, or behavioral-seam change. No seam consults the profile yet; each hosted behavior (skip mDNS publish, etc.) branches on it when *its* feature lands.

## What was done

- **`internal/profile/` (new)** — a tiny provider package, concrete types only (no interface; consumer-side seams take the value, per `CLAUDE.md` # Go code discipline). Exports `Profile` (`Appliance` | `Hosted`), `DefaultMarkerPath` (`/etc/malmo/profile`), and `Read(path) Profile`. `Read` never fails: an **absent** marker (the unmarked appliance box and `make dev`), an **unreadable** one, or **unrecognized** content all resolve to `Appliance` — the no-op default, so an unmarked box behaves exactly as today (`ENVIRONMENT.md` # How the profile is realized, "A runtime marker"). Surrounding whitespace / a trailing newline are tolerated (`strings.TrimSpace`). Logging is asymmetric on purpose: an absent marker is the expected appliance case and is **silent**, while a present-but-unreadable marker and a present-but-unrecognized value are both `slog.Warn`-ed — they signal a mis-stamped or mis-permissioned image, and silently defaulting could hide a hosted box running in the wrong posture.
- **`cmd/brain/main.go`** — new `profilePath` config (env `MALMO_PROFILE_PATH`, default `profile.DefaultMarkerPath`) so tests and `make dev` can point it elsewhere. The brain calls `profile.Read` **once at startup** (right after the logger is installed) into a local `prof` and logs the resolved value via `slog.Info("environment profile resolved", "profile", …)`. `prof` is held in `main` for future seams to consult; nothing threads it into a constructor yet, by design — that happens alongside the first consuming seam (don't build a DI container or thread a logger to carry it).
- **`CLAUDE.md`** # Go code discipline — added `profile` to the standard structured-fields list (with a one-line definition) so journalctl/jq filters on the new field stay stable.
- **Tests** — `internal/profile/profile_test.go` covers the value matrix: absent → appliance (no warn), `hosted` → hosted, `appliance` → appliance, trailing-whitespace/newline tolerated, empty / unknown / mis-cased → appliance + warn, and a present-but-unreadable marker (a directory, deterministic under any uid) → appliance + warn. 100% statement coverage of the package; `make check` green.

## What's next

- **C1b (#203)** — the lean hosted `mkosi` image profile that actually **stamps** `/etc/malmo/profile` with `hosted` (and installs only what a cloud VM needs). Until it lands, no real image carries the marker, so every box reads as `appliance`.
- **C1c (#204)** — the build-tagged slim cloud `host-agent`. host-agent's profile behavior is determined by its build tag, not this marker (`ENVIRONMENT.md` # How the profile is realized).
- **First behavioral seam** — the marker is read and logged but not yet consumed. The first hosted seam to wire (a candidate: skip the Avahi/mDNS publish in `hosted`) threads `prof` from `main` into that one call site, consumer-side.
