# Door-2 custom install is admin-only

- **Status:** done
- **Date:** 2026-06-03
- **Specs touched:** none — realizes the already-locked 2026-06-02 decision (`DECISIONS.md` "Door-2 admission holds the line: same sandbox as store, admin-only", `APP_ISOLATION.md` # Trust tiers, `APP_LIFECYCLE.md` # admission policy). No divergence.

Closes issue #55. The 2026-06-02 spec drop locked that Door 2 (custom compose) is admin-only — pasting an arbitrary compose is a privileged operation — but the code lagged: `installCustomApp` (`internal/api/api.go`) did **not** call `requireAdmin`, so a member could `POST /api/v1/apps/custom` and `resolveOwnerScope` quietly forced them a personal custom instance. The `DECISIONS.md` entry names this exactly: "the one real code gap — `installCustomApp` lacks `requireAdmin` today, plus a door-symmetry regression test." This closes it with a single role gate, leaving admission and the store path untouched. It is the dependency-free root of the Door-2 cluster (#55 → #56 → #57).

## What was done

- `internal/api/api.go` — `installCustomApp` now calls `requireAdmin(ctx)` as its first statement, before `resolveOwnerScope` / `Synthesize` / `admission.Check`. A member gets `403` ("admin role required", via the existing helper). The rejection is an elevation-class mutation failure, so it emits `audit.Record(ctx, audit.ActionAppCustomCreate, …, success=false)` with the acting member as actor (mirrors `login.failure`) **before** any synthesize or admission runs. The store path (`POST /api/v1/apps`, `installApp`) is deliberately left member-allowed.
- `internal/api/custom_install_test.go` (new) — four regression tests:
  - **member → 403 + audit:** a member `POST /api/v1/apps/custom` is forbidden and an `app.custom.create` success=false row appears in the member's own audit view (the member is the actor).
  - **admin passes the gate:** an admin's request clears `requireAdmin` and is stopped only at the pure-Go synthesize pre-check (422 "a main port is required" when `main_port` is omitted) — proving the gate discriminates by role without reaching the install job.
  - **store door unaffected:** a member `POST /api/v1/apps` (bogus manifest) returns 404, not 403 — no admin gate leaked onto the store path.
  - **door-symmetry lock:** `admission.CheckStructure` rejects all four named primitives (`privileged`, `cap_add`, host `ports:`, absolute bind) — the single function both doors funnel through.

No spec edits: the `DECISIONS.md` 2026-06-02 entry, the `APP_ISOLATION.md` # Trust tiers "Who can install → Admin only" row, and the `APP_LIFECYCLE.md` # admission policy door-symmetric + admin-only note were already on `main` when this branched (the spec drop preceded the implementation). This entry is the code that satisfies them.

## How it maps to the specs

- `DECISIONS.md` 2026-06-02 "Door-2 admission holds the line" / `APP_ISOLATION.md` # Trust tiers — Door 2 is the escape hatch; on a multi-user box a container escape hits every member, not just the installer, so *access* to Door 2 is gated behind the box's privileged tier (`sudo`-group admins; `CLAUDE.md` "admins-get-sudo, UI is the path"). A tightening of an existing hatch, not a new capability.
- `APP_LIFECYCLE.md` # admission policy — admission stays **door-symmetric**: `lifecycle.install` runs `m.admit` (= `admission.Check`) for both doors, and `installCustomApp` runs `admission.Check` as a synchronous pre-check. The role gate is orthogonal to admission — it changes *who may paste*, never *what the sandbox allows*.
- `CLAUDE.md` # Go discipline — elevation-class mutations audit success *and* failure; the guard rejection records `success=false`, symmetric with `login.failure`, so the Activity view can answer "did someone unauthorized try to install a custom app?"
- `CLAUDE.md` # Surgical changes — the behavior change is one guard + one audit call; everything else (admission, store path, scope resolution, the job) is untouched.

## Known gaps & deviations

- **No elevation gate.** The issue scopes #55 to the *role* gate only; it does not ask for `requireElevated` (unlike `createUser`). I did not add one — that would be an out-of-scope design decision. If custom install should also require the 5-minute elevation window, that's a separate, spec-led change.
- **Admin happy-path not exercised end-to-end in the api harness.** The api test harness builds the server with `life=nil`, and `admission.Check` shells out to `docker compose config -q`; a *successful* custom install would reach the job goroutine (nil-panic) or depend on Docker. The admin test therefore asserts the synchronous "passed the guard" boundary (422 at synthesize), not a completed install. The full install transaction is covered by `internal/lifecycle` tests.
- **Door-symmetry test leans on `CheckStructure`.** It asserts the shared admission *policy* rejects the four primitives, not that each API door re-invokes it (which can't run hermetically — Docker for the custom door, a job for the catalog door). The wiring claim is carried by the code + comment; the policy claim is the test.

## Tests

`go test ./internal/api/ ./internal/admission/ ./internal/lifecycle/ ./internal/manifest/ ./internal/audit/` green; `go build ./cmd/brain/ ./internal/...` clean; `gofmt` clean. (The `cmd/host-agent` PAM cgo build needs `libpam0g-dev`, absent in this dev box — pre-existing, unrelated to this change.)

## What's next

- **#56** — the Door-2 custom-container install *flow* (admin-only paste-compose screen + `expose:` port inference + inline admission coaching). Builds directly on this gate; its spec is already locked (`DASHBOARD.md` # Door-2 custom container install flow, `DECISIONS.md` 2026-06-02).
- **#57** — the form authors the permission block (folders + GPU + Edit-as-YAML). Builds on #56; spec locked the same day.
