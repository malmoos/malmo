# Door-2 custom-container install flow

- **Status:** done
- **Date:** 2026-06-03
- **Specs touched:** none changed. Realizes `DASHBOARD.md` # Door-2 custom container install flow and `APP_MANIFEST.md` # Custom container — synthetic manifest (both already locked, `DECISIONS.md` 2026-06-02). No `DECISIONS.md`/`NEXT.md` change.

Closes issue #56. The `POST /api/v1/apps/custom` endpoint already existed (synthesize + admission pre-check), but the only way to reach it was a bare three-field inline form at the bottom of the Store — no admin gate, no service dropdown, no port prefill, no scope choice, no inline coaching. This builds the **dedicated full-screen Door-2 screen** the spec locks, plus the best-effort `expose:`-derived main-port inference that drives it.

Door-2's *permission authoring* (LAN/GPU toggles, folder-grant rows, the Edit-as-YAML escape hatch, `Synthesize` accepting elected permissions, the `ports:`-container-side port signal) is the explicitly-separate follow-up #57, which layers onto this form. #56 ships the spine + the single `internet` election.

## What was done

**Backend — `expose:` port inference** (`internal/manifest/synthesize.go`): new `InferMainPort(composeBytes, mainService) int` reads the main service's single `expose:` value and returns it (1..65535), or `0` when the compose is silent, exposes several ports (ambiguous), or the value isn't a plain port (a range, say). Host-call-free and advisory: molma can't read the image's real `EXPOSE` without pulling it, so `main_port` stays required and editable in the form. The helper is written as the seam #57 grows to also mine the container side of a published `ports:` mapping.

**Backend — read-only inspect endpoint** (`internal/api/api.go` `inspectCustomApp`): `POST /api/v1/apps/custom/inspect` → `{services, main_port}`. Admin-only (`requireAdmin`; Door 2 is admin-only), host-call-free, and deliberately **not** admission-checked — admission gates on submit, where its field-named rejection is coached inline. It resolves the service whose `expose:` to read (the form's pick, or the sole service) so the form can prefill the port both on first paste and after a dropdown choice.

**Backend — internet election** (`internal/lifecycle/lifecycle.go`, `internal/api/api.go`): `CustomSpec` gains `Internet bool`; the request DTO gains `internet *bool` (nil ⇒ default on). `InstallCustom` sets `man.Permissions.Internet = spec.Internet` right after `Synthesize` (which still defaults it on). This is the minimal wire that lets the form's toggle turn internet *off* without touching `Synthesize`'s signature — #57 reworks `Synthesize` to accept the full elected permission set.

**Frontend — the screen** (`web-ui/src/views/CustomInstallView.vue`, new route `/store/custom`): a full-screen form rendered top-to-bottom per the spec — (1) paste-or-upload compose (textarea + file picker); (2) app name with a live `<slug>.local` URL preview; (3) main service auto-detected at one service, a required dropdown at several; (4) main port, prefilled from `expose:` via debounced inspect-on-paste, always editable, with the "port inside the container" help text; (5) an internet toggle (default on); plus the TOFU / no-auto-update honesty note; (6) scope via the store row's split-button convention (primary = personal, an admin on a multi-user box gets a "for the whole household" dropdown; single-user installs personal silently). A 422 from submit (synthesize or admission) is shown inline against the compose, never as a toast. The screen guards its own admin-only access (deep-link / role-change bounce to `/store`).

**Frontend — Store affordance** (`web-ui/src/views/StoreView.vue`): the bare inline custom form is replaced by an admin-only **"Install a custom container"** link tucked below the catalog, linking to `/store/custom`. Members never see it.

## How it maps to the specs

- `DASHBOARD.md` # Door-2 — the affordance lives at the bottom of the Store (admin-only, not a dock item, not in the grid); the form is a dedicated full-screen screen, not the catalog consent dialog; the six steps render in the locked order; admission stays door-symmetric (unchanged; gated on submit); scope uses the single-user-simplification button convention.
- `DASHBOARD.md` # Main port — best-effort inference from `expose:`, asked when silent, always editable with the exact help-text framing.
- `APP_MANIFEST.md` # Custom container — `InstallCustom` still produces the synthetic manifest (fresh id + entropy, TOFU image pin, no managed services); the only new election carried through is `internet`.
- `APP_ISOLATION.md` # Trust tiers — Door 2 is admin-only: the new inspect endpoint enforces `requireAdmin`; the UI hides the affordance from members and the view bounces non-admins.
- `CLAUDE.md` # Go discipline — consumer-side, no new interface (one consumer), `slog`-free read path, no new store surface; the inspect endpoint is a pure read with no audit (validation reads don't audit).

## Known gaps & deviations

- **`ports:`-container-side port inference is not built** — `InferMainPort` reads only `expose:`. `DASHBOARD.md` # Main port describes mining the container side of a `ports:` mapping (`8080:80` ⇒ `80`) too; that's explicitly issue #57, which extends this helper. A compose that declares only `ports:` prefills nothing today (the admin types the port) and still trips the admission rejection on submit.
- **Only the `internet` permission is elected** — LAN/mDNS, GPU, and folder grants (and the Edit-as-YAML escape hatch) are #57. A Door-2 app installed today gets no folder access and no GPU, matching the pre-#57 state the spec calls out as the gap #57 closes.
- **Admin gate on the *install* endpoint rides on #58.** This branch gates the *inspect* endpoint and the UI; the `requireAdmin` gate on `POST /api/v1/apps/custom` itself is PR #58 (#55). Merge #58 first; until then the install endpoint is UI-gated only. The two touch adjacent lines of `installCustomApp` and will conflict trivially at rebase (keep both the guard and the `internet` field).
- **Inspect is debounced (350 ms) and best-effort** — an incomplete paste silently leaves the dropdown empty and the port asked; inspect failures never surface (only submit errors do).

## Tests

- `internal/manifest/manifest_test.go` `TestInferMainPort` — table over expose-as-string, expose-as-int, reads only the named main service, no-expose, multiple-exposed (ambiguous → 0), non-numeric range → 0, out-of-range → 0, unknown service → 0, invalid YAML → 0 (never panics).
- `internal/api/inspect_custom_test.go` — single-service prefills the port without a pick; multi-service returns all services with `main_port` 0 until a service is named, then prefills from that service's `expose:`; member gets 403 (admin-only); empty `services: {}` → 422.
- `web-ui`: `vue-tsc --noEmit` + `vite build` both clean (`CustomInstallView` is a lazy chunk).
- Backend sweep green excluding the pam-cgo packages (`pamverifier`, `host-agent-real`) that need `libpam0g-dev` headers absent on this dev box — unrelated to this change; they build in CI.

## What's next

- **#57 — Door-2 permission authoring**: LAN/GPU toggles, folder-grant rows (Source picker + typed Destination `target`), the Edit-as-YAML escape hatch, `Synthesize` accepting the elected permission set, and extending `InferMainPort` to mine the `ports:` container side.
- **#58 (#55)** must merge to enforce the admin gate on the install endpoint server-side.
