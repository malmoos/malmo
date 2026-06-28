// Tiny auth composable. Owns the singleton currentUser ref and the bootstrap
// flow described in AUTH.md / BRAIN_UI_PROTOCOL.md / FIRST_RUN.md:
//
//   GET /auth/state -> { has_users, first_run_complete, profile }
//     first_run_complete? no  -> the first-run wizard (FIRST_RUN.md Phase 2)
//                          yes -> has_users? GET /me; 200 -> dashboard, 401 -> login
//
// The wizard (not just "an admin exists") is gated on first_run_complete so a
// half-finished wizard resumes rather than dropping the user onto the dashboard
// (FIRST_RUN.md # Phase 3). profile selects the bootstrap path: appliance shows a
// login/setup page, while hosted has neither — an unauthenticated hosted visitor
// is bounced to the portal, which signs them back in via the portal-to-box SSO
// handshake (ENVIRONMENT.md # Provisioning; cloud specs/AUTH_AND_ACCESS.md).
//
// Any 401 from a later API call drops currentUser to null via the handler
// registered with setUnauthenticatedHandler — that's the single route the
// router uses to fall back to the login screen (or, on hosted, the portal).

import { ref, computed } from "vue";
import { api, setUnauthenticatedHandler, type AuthState, type SetupResult, type User } from "./api";

// portalURL is the malmo.network control plane an unauthenticated hosted box
// bounces to. The box lives at "<box-id>.malmo.network"; the portal is the apex.
const portalURL = "https://malmo.network";

const currentUser = ref<User | null>(null);
const hasUsers = ref<boolean | null>(null);
const firstRunComplete = ref<boolean | null>(null);
const profile = ref<string>("appliance");
const booted = ref(false);

setUnauthenticatedHandler(() => {
  currentUser.value = null;
});

// isHosted reports whether the box runs the hosted profile (no login/setup page).
export function isHosted() {
  return profile.value === "hosted";
}

// redirectToPortal sends the browser to the malmo.network portal, which mints a
// fresh SSO assertion and lands the owner back on the box dashboard. Used as the
// hosted stand-in for the login screen. replace() so the unauthenticated box URL
// doesn't linger in history.
export function redirectToPortal() {
  window.location.replace(portalURL);
}

export async function bootstrap() {
  if (booted.value) return;
  const state = await api.get<AuthState>("/auth/state");
  hasUsers.value = state.has_users;
  firstRunComplete.value = state.first_run_complete;
  profile.value = state.profile;
  if (state.has_users) {
    try {
      currentUser.value = await api.get<User>("/me");
    } catch {
      currentUser.value = null;
    }
  }
  booted.value = true;
}

export async function login(username: string, password: string) {
  const res = await api.post<{ user: User }>("/login", { username, password });
  currentUser.value = res.user;
  hasUsers.value = true;
}

// SetupOptions carries the first-run admin step's choice: recovery (FIRST_RUN.md
// # Step 2a, on by default). /setup is the appliance bootstrap; the hosted box
// auto-creates its admin via the portal-to-box SSO handshake, so there is no
// hosted setup field here (ENVIRONMENT.md # Provisioning).
export interface SetupOptions {
  recovery: boolean;
}

// setup creates the first admin. Unlike login it is followed by the rest of the
// wizard, so we set currentUser/hasUsers immediately — the App stays on the
// wizard because first_run_complete is still false, and the admin-gated
// time-zone/telemetry/complete calls need the session this just minted.
export async function setup(
  username: string,
  password: string,
  opts: SetupOptions,
): Promise<SetupResult> {
  const res = await api.post<SetupResult>("/setup", {
    username,
    password,
    recovery: opts.recovery,
  });
  currentUser.value = res.user;
  hasUsers.value = true;
  return res;
}

// setSystemTimezone applies the system time zone (FIRST_RUN.md # Step 3 /
// TIME.md). Admin-only on the brain; the wizard's session is the admin just
// created.
export async function setSystemTimezone(timezone: string) {
  await api.post("/system/timezone", { timezone });
}

// setTelemetryConsent records the box-wide telemetry choice (FIRST_RUN.md
// # Step 4 / TELEMETRY.md). Off by default.
export async function setTelemetryConsent(enabled: boolean) {
  await api.post("/system/telemetry", { enabled });
}

// completeFirstRun writes the first-run-complete marker (FIRST_RUN.md # Phase 3)
// and flips local state so the App leaves the wizard for the dashboard. The
// wizard never reappears after this.
export async function completeFirstRun() {
  await api.post("/system/first-run-complete");
  firstRunComplete.value = true;
}

export async function logout() {
  try {
    await api.post("/logout");
  } finally {
    currentUser.value = null;
  }
}

// changeMyPassword changes the signed-in user's own password (AUTH.md # Password
// lifecycle # Setting a password). The brain verifies current_password via PAM
// (401 on mismatch) and revokes all of the user's sessions on success, so we
// force a local logout — re-auth is required, and App.vue drops to the login
// screen the moment currentUser clears.
//
// suppressAuthHandler is load-bearing here: a wrong current password returns
// 401, but the caller is still authenticated. Without suppression the global
// 401 handler would clear currentUser and unmount the Settings form before it
// could show "Incorrect password." — bouncing the user to login on a typo. We
// drop to login only on success, via the explicit logout() below.
export async function changeMyPassword(currentPassword: string, newPassword: string) {
  await api.post(
    "/me/password",
    { current_password: currentPassword, new_password: newPassword },
    { suppressAuthHandler: true },
  );
  await logout();
}

// redeemRecoveryCode runs the public recovery flow (AUTH.md # Using the recovery
// code) — no session required. On a correct username+code the brain resets the
// password via host-agent, consumes the old code, and returns a fresh one to
// show exactly once. We do NOT set currentUser: recovery terminates at the login
// screen, where the user signs in with the new password.
export async function redeemRecoveryCode(
  username: string,
  recoveryCode: string,
  newPassword: string,
): Promise<string> {
  const res = await api.post<{ new_recovery_code: string }>("/recover", {
    username,
    recovery_code: recoveryCode,
    new_password: newPassword,
  });
  return res.new_recovery_code;
}

// refreshCurrentUser re-fetches /me and updates currentUser. Call this in the
// onSettled hook of any user-management mutation (create/delete/role-change) so
// single_user_mode stays accurate without a page reload.
export async function refreshCurrentUser() {
  try {
    currentUser.value = await api.get<User>("/me");
  } catch {
    // 401 means the session is gone; the unauthenticated handler already clears
    // currentUser, so nothing extra to do here.
  }
}

export function useAuth() {
  return {
    currentUser: computed(() => currentUser.value),
    hasUsers: computed(() => hasUsers.value),
    firstRunComplete: computed(() => firstRunComplete.value),
    profile: computed(() => profile.value),
    booted: computed(() => booted.value),
    singleUserMode: computed(() => currentUser.value?.single_user_mode ?? false),
    refreshCurrentUser,
  };
}
