// Elevation flow (USERS_AND_GROUPS.md # Elevation in the UI). Destructive /
// far-reaching admin operations re-prompt for the password — the brain marks
// the session "elevated" for a 5-minute window (POST /api/v1/auth/elevate) and
// the user-mutation endpoints reject with `elevation_required` (403) until then.
//
// Shape: a single ElevateDialog instance (mounted in AppShell) renders the
// password prompt; this module owns the singleton state and the `withElevation`
// helper that wraps a mutation, catches the 403, drives the prompt, and retries
// once. Within a live window the prompt never shows — the first call elevates,
// the rest pass straight through.
import { ref } from "vue";
import { api, ApiError } from "./api";

// elevationCancelled is thrown when the user dismisses the prompt. Callers map
// it to "no message" so a cancel reads as a no-op, not an error.
export const elevationCancelled = new ApiError("elevation_cancelled", "", 0);

export const elevateVisible = ref(false);
export const elevateSubmitting = ref(false);
export const elevateError = ref("");

let pending: { resolve: () => void; reject: (e: unknown) => void } | null = null;

// requestElevation shows the prompt and resolves once the session is elevated,
// or rejects with elevationCancelled if the user backs out.
function requestElevation(): Promise<void> {
  elevateError.value = "";
  elevateVisible.value = true;
  return new Promise((resolve, reject) => {
    pending = { resolve, reject };
  });
}

export async function submitElevation(password: string) {
  if (!password) return;
  elevateSubmitting.value = true;
  elevateError.value = "";
  try {
    await api.post("/auth/elevate", { password });
    elevateVisible.value = false;
    pending?.resolve();
    pending = null;
  } catch (e) {
    elevateError.value = e instanceof ApiError ? e.message : "Incorrect password.";
  } finally {
    elevateSubmitting.value = false;
  }
}

export function cancelElevation() {
  elevateVisible.value = false;
  pending?.reject(elevationCancelled);
  pending = null;
}

// withElevation runs fn; if the brain rejects with `elevation_required`, it
// drives the password prompt and retries fn exactly once. Any other error (and
// a cancelled prompt) propagates to the caller's onError.
export async function withElevation<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn();
  } catch (e) {
    if (e instanceof ApiError && e.code === "elevation_required") {
      await requestElevation();
      return await fn();
    }
    throw e;
  }
}
