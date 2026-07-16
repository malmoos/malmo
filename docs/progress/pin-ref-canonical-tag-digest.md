# The override pin is canonical even when the compose carries a tag and a digest

- **Status:** done
- **Date:** 2026-07-16
- **Specs touched:** none — this brings the code to what `APP_LIFECYCLE.md` # Locked: image digest pinning already says.
- **Issue:** #333 (closes). Found while reviewing #332; pre-existing, and #332 did not regress it.

## What was done

`repoOf` (`internal/lifecycle/pinning.go`) returned early on the `@`, so a reference carrying **both** a tag and a digest kept the tag:

```
repoOf("nextcloud:34.0.1-apache@sha256:abc") = "nextcloud:34.0.1-apache"
constructed pin = repoOf(image) + "@" + digest = "nextcloud:34.0.1-apache@sha256:abc"
```

`APP_LIFECYCLE.md` # Locked: image digest pinning says every install pins as `image: name@sha256:...`. This shape is a literal deviation from that, and it restates the tag in the one artifact where we least want it: after #331, the digest is the address and the tag is a label, so writing the label into the pin re-introduces the vocabulary that fix removed.

The impact was cosmetic, not functional — `name:tag@sha256:…` is a valid reference, the digest is what resolves, and the override stayed byte-deterministic because the digest still decided the bytes. It was only reachable when an author wrote both forms in the compose (a Door-2 / hand-authored path); catalog-built manifests don't emit that shape, which is why it went unnoticed.

Changes:
- **`internal/lifecycle/pinning.go`** — `repoOf` now strips the digest and then falls through to the existing tag-stripping branch rather than returning early, so both suffixes come off. The port-colon disambiguation is unchanged and now runs against the `@`-stripped remainder, which is the string it was always meant to see.
- **`internal/lifecycle/helpers_test.go`** — `TestRepoOf` gains the two tag+digest cases that were the coverage gap: `nextcloud:34.0.1-apache@sha256:…` and `ghcr.io:5000/foo/bar:v1@sha256:…` (the second holds the port-colon rule and the digest strip down at once).
- **`internal/lifecycle/pinning_test.go`** (new) — `TestInstallTagAndDigestPinsCanonicalRef` asserts the shape at the level the spec states it: a full install from a compose pinned `traefik/whoami:v1.10.3@sha256:…` writes `traefik/whoami@sha256:…` into `compose.override.yml` and records the digest in SQLite. The file is the home for pin-shape tests; the offline exception to the shape stays in `pinning_offline_test.go`.

Both tests were verified to fail against the pre-fix `repoOf` with exactly the strings #333 describes.

## What's next

Nothing outstanding for the pin shape. The tag+digest compose remains a Door-2 / hand-authored shape only; if the catalog ever emits it, the manifest-vs-compose contradiction check added by #331 is the guard that already covers the disagreement case.

## Known gaps

- No live-Docker assertion for this ref shape. `dockerlive_test.go` covers pulls against a real daemon, but the finding in #333 was already verified by hand against a real daemon (`name:tag@sha256:…` pulls fine), and the change is to the string we write, not to what the daemon accepts — the fake-driver install test observes the actual artifact.
