# Single-user simplification + split-button install

- **Status:** done
- **Date:** 2026-05-31
- **Specs touched:** docs/specs/DASHBOARD.md (new # Single-user simplification section; install-flow description updated), docs/specs/DECISIONS.md (new 2026-05-31 entry), docs/specs/BRAIN_UI_PROTOCOL.md (`single_user_mode` on session-bearing responses; scope-picker note)

## What was done

When the box has exactly one registered user, the household/personal distinction is meaningless. Suppressed it everywhere and cleaned up the install flow for all users.

### Backend

- `internal/store/store.go` — `UserCount() (int, error)`: `SELECT COUNT(*) FROM users`.
- `internal/api/auth.go` — `fullUserDTO(u store.User) (UserDTO, error)`: builds a `UserDTO` with `single_user_mode: bool` set to `user_count == 1`. Used by `/setup`, `/login`, and `/me` so the flag is present from the first session-establishing response, with no page refresh needed. Other endpoints returning `UserDTO` (user-management list/patch) omit the field (`omitempty` pointer).

### Frontend

- `web-ui/src/api.ts` — `single_user_mode?: boolean` added to `User`.
- `web-ui/src/auth.ts` — `singleUserMode` exposed from `useAuth()` as `currentUser?.single_user_mode ?? false`.
- `web-ui/src/components/SplitButton.vue` — new component. Primary button + optional chevron dropdown using reka-ui `DropdownMenu` + lucide `ChevronDown`. When `items` prop is empty it renders as a plain button with no chevron, so the same component covers both the single-user (plain) and multi-user-admin (split) cases.
- `web-ui/src/components/InstallDialog.vue` — scope picker removed entirely. `scope: Scope` is now a required prop passed by the parent (set by whichever button was clicked). `single_user_mode` used to relabel the shared folder source from "The household's shared X" to "Shared X (accessible from your other devices)".
- `web-ui/src/views/StoreView.vue` — `dialogScope` ref tracks the scope elected by the button; `openInstallDialog(id, scope)` sets it before opening the plan dialog. `householdDropdownItems()` returns the dropdown item only for `role == admin && !singleUserMode`; Install buttons replaced with `SplitButton`.
- `web-ui/src/views/HomeView.vue` — Household/Yours section headers hidden when `singleUserMode`.
- `web-ui/src/components/AppTile.vue` — "Shared"/"Personal" tile label hidden when `singleUserMode`.
- `web-ui/src/views/SettingsView.vue` — scope/owner label hidden when `singleUserMode`.

## How it maps to the specs

Realizes `DASHBOARD.md` # Single-user simplification. The split-button moves scope selection out of the consent dialog and onto the install action, which is cleaner for multi-user too: "who is this app for?" is answered before entering the permissions flow.

## Known gaps & deviations

None. The `scope_options`/`scope_default` fields are still returned by the install-plan endpoint and retained in the response for future use, but the dashboard no longer renders a picker from them.

## What's next

- The `Admin` row in the install-authorization table (`DASHBOARD.md`) still says "Chooses Household or Just for me" — that description is now realized by the split-button rather than the dialog picker. No spec change needed; behavior is correct.
- When a per-app detail page lands and uninstall moves there, the Settings scope label (suppressed here) goes away entirely. No further work needed on that path.

Builds on [hostname-uniqueness-not-ownership.md](hostname-uniqueness-not-ownership.md) (the same session's first-come slug work) and [install-consent-ui.md](install-consent-ui.md) (the consent dialog this slice refactors).
