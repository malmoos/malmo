# The release manifest, on a timer: an appliance box learns about releases on its own

- **Status:** done
- **Date:** 2026-08-12
- **Specs touched:** docs/specs/RELEASE_MANIFEST.md (# What the manifest is, # Failure modes — two "as built" notes)

Closes #397. [release-manifest-read.md](release-manifest-read.md) built the pure half — verify, parse, decide, cache — and nothing called it. This gives it a clock and a network. After this an appliance box discovers on its own that a newer release exists, which is the first thing in the whole update design that does not need a human to type something.

## What was done

**The poller** (`internal/hostagent/relmanifest/poll.go`). One `Poll(ctx)` does a cycle: fetch `stable.json` and `stable.json.minisig`, verify, parse, cache. `Run(ctx)` polls once immediately, then on the interval. The HTTP surface is a one-method consumer-side interface and the clock is injected, so the tests drive a real `httptest` server and a year of ticks in milliseconds.

**A failed poll changes nothing on disk.** This is the promise `RELEASE_MANIFEST.md` # Failure modes makes, and it is the one thing in this slice worth getting right: an unreachable host, an HTTP 500, a truncated body, a tampered manifest, a signature from a key this box does not accept, or a manifest with the wrong schema all leave the cached manifest exactly as it was. Seven table cases assert it, each by caching a good manifest first and then breaking the CDN one way.

**Verify before cache, not after.** Ordering, again. Writing what was fetched and verifying afterwards would let anyone who can answer an HTTP request replace the box's idea of the current release, since the cache is what an offline box acts on.

**Keys are stamped at build time** (`keys.go`, `Makefile`), the same way `internal/version` is stamped: `-X ...relmanifest.BakedKeys=<key>[,<key>]`, empty by default. **There is deliberately no environment-variable override.** The point of baking the key in is that changing which releases a box accepts takes a new binary, which arrives through apt and is itself signed; an env override would move that decision to whoever can edit a unit file. A broken entry in the list is logged and skipped rather than fatal — a box with a half-broken key list should still boot and still refuse manifests, because the brain is how anyone finds out what is wrong.

**A build with no key does not poll at all.** `Run` says so once, loudly, and returns. That is every build today, and it is the safe state: a box that cannot check a manifest must ignore it. A polling loop that can only ever fail would also read in the journal as "no releases published" rather than "this build trusts nobody".

**Wired into `cmd/host-agent-real`, appliance only** (`releasepoll_appliance.go`, with a no-op twin in `releasepoll_hosted.go` so `main.go` carries no build tags). A hosted box never fetches this file — its target is held per box by the cloud (`UPDATES.md` # 8.1) — and polling here would give it a second, competing opinion about which version to run. The poll starts **last**, after the socket is serving and the brain is launched: it says which version *could* be installed, and nothing about booting depends on it, so a hanging CDN must not sit in front of anything.

**The cache is read at startup**, before the first fetch, so a box that boots without a network still reports the release it last heard about.

Two additions that are not in the spec, both now recorded there as "as built":

- **Jitter, up to five minutes per tick.** Boxes cluster (a provider maintenance window, a region-wide power cut), and an exact hourly boundary would turn that into a spike on a static file host for no benefit.
- **A 64 KiB body cap.** The manifest is a few hundred bytes. Anything in megabytes is a misconfigured host or a hostile one, and neither deserves the box's memory.

## Testing

18 tests in the poll file, driven by an `httptest` CDN that can be broken on demand. Three mutation checks, each by editing the code and restoring it:

1. Cache before verifying ⇒ six of the seven failed-poll cases fail.
2. Remove the no-keys guard from `Run` ⇒ the keyless-build test fails.
3. Remove the body cap ⇒ the oversized-body test fails.

**Mutation 3 exposed a bad test, which is the point of doing them.** The first version of that test served a huge string of `x`. It passed with the cap removed, because junk also fails signature verification — the test proved nothing about the cap. It now serves a **real, correctly signed, schema-valid** manifest padded past the limit, and asserts the error *names the size*. Without the explicit check the read is still truncated by the limit reader, so the poll fails anyway, but as "signature does not verify" — which sends whoever reads that log hunting a key problem that does not exist.

The `-ldflags` path was checked by building a throwaway program against the package: stamped reports one key, unstamped reports zero. A wrong symbol path there fails silently, which is exactly the shape of bug that would ship a box trusting nothing while the build log looks fine.

## Known gaps & deviations

- **Nothing consumes the state yet.** `Poller.State()` holds the last manifest, the last poll time and the last error, and no one reads it. The host-socket endpoint lands with the dashboard slice, where there is a consumer. `Decide` is still not called anywhere — the poller deliberately does not know the box's running versions.
- **No signing key and no `releases.malmo.network`.** Every build today refuses every manifest and does not poll, so this is proven against a test CDN and test keys only. Nothing here has fetched a real file.
- **No three-strikes pin** (`UPDATES.md` # 3 step 5) and **no 24-hour "signature keeps failing" warning** (`RELEASE_MANIFEST.md` # Failure modes). The state needed for the second one is recorded (`LastSuccessAt`, `LastError`); the health surface that would show it is not wired.
- **Version → image ref is still undecided**, and the published ghcr packages are **still private** (`BUILD.md` # 6). Even with a real manifest, a box could not pull the images it names.
- **No VM proof.** The poll has never run on a booted box. The cloud lane's `update` boot drives the *apply* path, not this one.
- **`writeFileAtomic` is still duplicated** between `controlplane` and `relmanifest`, as noted in the previous entry.

## What's next

1. **The dashboard surface** (`UPDATES.md` # 6): expose the poller state over the host socket, have the brain turn it into an "Update available" answer with `Decide`, and show it. That is the slice where the state finally has a consumer.
2. **Three consecutive failures then pin**, which needs the trigger that retries.
3. **Version → image digest**, blocked on a maintainer decision.
