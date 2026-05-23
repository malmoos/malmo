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

export function useAuth() {
  return {
    currentUser: computed(() => currentUser.value),
    hasUsers: computed(() => hasUsers.value),
    booted: computed(() => booted.value),
  };
}
