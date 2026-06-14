# Start re-asserts the mDNS name, not just the Caddy route

- **Status:** done
- **Date:** 2026-06-13
- **Specs touched:** `APP_LIFECYCLE.md`, `DECISIONS.md`

A `stopped → running` transition driven by `Start` re-registered only the Caddy route; it never re-published the app's mDNS name. So an app recovered via Start — after a mid-life host-agent restart dropped its process-local Avahi entry group, or after a prior install/start that failed before publishing — came back reachable by Host-header proxy (`curl -H 'Host: jupyter.local' localhost` worked) but **unresolvable by name** (`jupyter.local` returned nothing in the browser) until the next brain reboot's reconcile pass re-asserted it. Discovered while recovering a `failed` Jupyter instance (#136/#124). Closes #153.

This is the user-initiated counterpart to the durability the reconcile pass already provides: `lifecycle.Reconcile` re-publishes every `running` instance at brain startup (`DISCOVERY.md` # Restart durability), but a Start in between did not. The fix extends the same "Caddy block and Avahi record are siblings, lockstep" rule (`DISCOVERY.md` # The reconciler owns Avahi state) to Start.

## What was done

### `internal/lifecycle`

- **New `Manager.publishHost(ctx, inst) (host string, avahiOK bool)`** — extracted verbatim from `reassertRouting`'s inline publish block. Calls `host.Publish(inst.Slug)`, uses the returned name as the route host (it may be the box-qualified `<slug>-<box>.local` collision fallback), persists a *changed* name via `store.SetMDNSName`, and falls back to the stored name then the reconstructed `<slug>.local` so the route always exists even when mDNS is down. Idempotent — re-publishing an already-announced name is a host-side no-op — which is what lets every re-assertion path call it freely. Sits next to `routeHost`, the passive sibling used by callers (Stop) that flip the splash *without* re-announcing.
- **`Start` now resolves its route host via `publishHost` instead of `routeHost`** (one line), so a Start re-asserts mDNS **and** Caddy. The publish happens **up-front**, before the "starting" splash and the `compose up` — mirroring install's step 9, not deferred to after-healthy. Rationale (`DECISIONS.md` 2026-06-13): the name-lifecycle model keeps a name announced for an app's whole life with only the *route content* varying (Stop deliberately keeps `<slug>.local` resolving, pointing it at the stopped splash), so the name should resolve to the *starting* splash during recovery too; and one `host` value then keys the starting splash, the real upstream, and the failed splash without divergence if the published name changed.
- **`reassertRouting` (reconcile) now calls `publishHost`** instead of its own copy of the block — genuinely reusing the logic the issue pointed at, so the two paths can't drift. Behavior unchanged (same publish, same persist-on-change, same fallback, same `avahiOK` return feeding the "avahi replay" startup summary); only the publish-failure log message normalized from `"reconcile: mDNS publish"` to the shared `"mDNS publish failed (continuing)"`.
- **Stop is untouched** — it must NOT re-publish (the stopped splash keeps resolving on the install-time announcement; explicitly out of scope per #153).

### Tests (`internal/lifecycle`, hermetic — fakes, no docker/root)

- **`TestStartReassertsMDNSName`** — the regression guard. Install publishes once; Stop does **not** re-publish (count stays 1 — name-on-stop unchanged); then the test drops the fake host's entry group (`dropPublished`, simulating a host-agent restart) and asserts Start re-publishes (count → 2), re-announces the dropped name (`isPublished` true again), and leaves the stored `MDNSName` in sync — recovery without a brain restart.
- **`TestPublishHostBranches`** — drives `publishHost`'s non-happy paths directly to 100%: publish failure with no stored name → reconstructed `<slug>.local` fallback + `avahiOK` false + nothing persisted; and a returned name differing from the stored one (collision fallback) → persisted through `SetMDNSName`.
- **Fake host gained** `publishCount(slug)` (assert a *re*-publish, not just eventual state), `dropPublished(slug)` (simulate a lost entry group), and `publishErr`/`publishName` fields (force the mDNS-down and collision-fallback paths). Default zero values preserve existing fake behavior exactly.
- `make check` green (gofmt, vet, OpenAPI freshness, full Go suite). New code fully covered (`publishHost` 100%; the Start call site and the regression are on covered paths). No web-ui, OpenAPI, or protocol change.

## What's next

- **The broader "mid-life host-agent restart while the brain runs" gap is still open for untouched apps.** Start now recovers an app's name *when the user starts it*, but an app left `running` across a host-agent restart still has a dark name until the next brain reboot. The general mitigation — the brain polling `GET /v1/system/status` for `uptime_s` decreasing and replaying all names on detection — remains tracked in `DISCOVERY.md` # Restart durability and `docs/progress/avahi-dbus-publisher.md`. #153 narrows the window for the Start-able case; it does not close that gap.
- **Install's step-9 publish block was left as its own copy** (it also sets the local `inst.MDNSName` field and runs inside the rollback-wrapped install transaction). Folding it onto `publishHost` is a possible follow-up but was kept out of scope to keep the change surgical and off the install transaction's failure paths.
