// Tiny auth composable. Owns the singleton currentUser ref and the bootstrap
// flow described in AUTH.md / BRAIN_UI_PROTOCOL.md:
//
//   GET /auth/state -> has_users?
//     no  -> setup view (call POST /setup)
//     yes -> GET /me; on 200 -> dashboard, on 401 -> login (POST /login)
//
// Any 401 from a later API call drops currentUser to null via the handler
// registered with setUnauthenticatedHandler — that's the single route the
// router uses to fall back to the login screen.

import { ref, computed } from "vue";
import { api, setUnauthenticatedHandler, type AuthState, type SetupResult, type User } from "./api";

const currentUser = ref<User | null>(null);
const hasUsers = ref<boolean | null>(null);
// firstRunComplete latches at the wizard's Done step (FIRST_RUN.md # Phase 3).
// It — not hasUsers — gates the wizard: the admin can exist mid-wizard (time
// zone / telemetry still ahead), so App.vue keeps the wizard up until this flips.
const firstRunComplete = ref<boolean | null>(null);
// profile is the resolved environment profile ("appliance" | "hosted",
// ENVIRONMENT.md). The wizard reads it to pick its step set and to show the
// admin-bootstrap-secret field on hosted only.
const profile = ref<string | null>(null);
const booted = ref(false);

setUnauthenticatedHandler(() => {
  currentUser.value = null;
});

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

// setup creates the admin and returns the recovery code. We deliberately do
// NOT set currentUser here — the wizard has more steps (time zone, telemetry)
// and stays mounted until finishFirstRun() at Done. /setup mints the session
// cookie, so the later admin-gated wizard steps are authorized. skipRecovery
// opts out of a recovery code (FIRST_RUN.md # Step 2a); secret is the hosted
// admin-bootstrap secret (empty/omitted on appliance).
let pendingSetupUser: User | null = null;
export async function setup(
  username: string,
  password: string,
  opts?: { skipRecovery?: boolean; secret?: string },
): Promise<SetupResult> {
  const body: Record<string, unknown> = { username, password };
  if (opts?.skipRecovery) body.skip_recovery_code = true;
  if (opts?.secret) body.bootstrap_secret = opts.secret;
  const res = await api.post<SetupResult>("/setup", body);
  pendingSetupUser = res.user;
  return res;
}

// finishFirstRun latches the first-run-complete marker at the Done step and only
// then flips into the dashboard (FIRST_RUN.md # Phase 3). Centralizing the flip
// here keeps App.vue's gate honest: the wizard stays mounted across every step
// until this resolves. Works for both a fresh box (currentUser comes from the
// just-created admin) and a resumed wizard (currentUser already set by bootstrap).
export async function finishFirstRun() {
  await api.post("/first-run/complete");
  if (pendingSetupUser) {
    currentUser.value = pendingSetupUser;
    pendingSetupUser = null;
  }
  hasUsers.value = true;
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
