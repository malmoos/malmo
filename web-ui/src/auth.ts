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
const booted = ref(false);

setUnauthenticatedHandler(() => {
  currentUser.value = null;
});

export async function bootstrap() {
  if (booted.value) return;
  const state = await api.get<AuthState>("/auth/state");
  hasUsers.value = state.has_users;
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
// NOT set currentUser here — the Setup view needs to stay mounted to display
// the recovery code once. Call setupComplete() after the user acknowledges.
let pendingSetupUser: User | null = null;
export async function setup(username: string, password: string): Promise<SetupResult> {
  const res = await api.post<SetupResult>("/setup", { username, password });
  pendingSetupUser = res.user;
  return res;
}

// setupComplete is called after the user acknowledges the recovery code.
// We delay flipping hasUsers/currentUser until now so the App.vue router
// keeps showing the Setup view across the create-then-acknowledge transition.
export function setupComplete() {
  if (pendingSetupUser) {
    currentUser.value = pendingSetupUser;
    hasUsers.value = true;
    pendingSetupUser = null;
  }
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
export async function changeMyPassword(currentPassword: string, newPassword: string) {
  await api.post("/me/password", { current_password: currentPassword, new_password: newPassword });
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
    booted: computed(() => booted.value),
    singleUserMode: computed(() => currentUser.value?.single_user_mode ?? false),
    refreshCurrentUser,
  };
}
