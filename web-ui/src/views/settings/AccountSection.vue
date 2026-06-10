<script setup lang="ts">
// Settings → Account — the signed-in user's own identity and password. Extracted
// from the old single-page SettingsView when Settings became a left-nav shell
// (SettingsLayout.vue).
//
// Self-service password change (AUTH.md # Password lifecycle). No elevation
// window: supplying the current password IS the verification step, enforced
// server-side via PAM. On success the brain revokes our session, so
// changeMyPassword forces a local logout and App.vue drops to the login screen —
// this component unmounts, so we only ever reset submitting on the error path.
import { ref } from "vue";
import { api, type ApiError } from "@/api";
import { changeMyPassword, useAuth } from "@/auth";

const { currentUser } = useAuth();

const showPwForm = ref(false);
const currentPw = ref("");
const newPw = ref("");
const pwError = ref("");
const pwSubmitting = ref(false);

async function submitPasswordChange() {
  pwError.value = "";
  pwSubmitting.value = true;
  try {
    await changeMyPassword(currentPw.value, newPw.value);
  } catch (e) {
    const ae = e as ApiError;
    pwError.value = ae.status === 401 ? "Incorrect password." : ae.message || "Could not change password.";
    pwSubmitting.value = false;
  }
}

function cancelPwChange() {
  showPwForm.value = false;
  currentPw.value = "";
  newPw.value = "";
  pwError.value = "";
}
</script>

<template>
  <section class="space-y-3">
    <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Account</h2>
    <div class="space-y-3 rounded-xl border border-border bg-card px-4 py-3">
      <div class="flex items-center justify-between gap-4">
        <div class="min-w-0">
          <div class="text-sm font-medium">{{ currentUser?.username }}</div>
          <div class="text-xs capitalize text-muted-foreground">{{ currentUser?.role }}</div>
        </div>
        <button
          v-if="!showPwForm"
          class="shrink-0 rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
          @click="showPwForm = true"
        >
          Change password
        </button>
      </div>
      <form v-if="showPwForm" class="space-y-3 border-t border-border pt-3" @submit.prevent="submitPasswordChange">
        <label class="block space-y-1">
          <span class="text-xs text-muted-foreground">Current password</span>
          <input
            v-model="currentPw"
            type="password"
            autocomplete="current-password"
            required
            class="w-full rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
          />
        </label>
        <label class="block space-y-1">
          <span class="text-xs text-muted-foreground">New password</span>
          <input
            v-model="newPw"
            type="password"
            autocomplete="new-password"
            required
            class="w-full rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
          />
        </label>
        <p v-if="pwError" class="text-xs text-destructive">{{ pwError }}</p>
        <div class="flex justify-end gap-2">
          <button
            type="button"
            class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
            @click="cancelPwChange"
          >
            Cancel
          </button>
          <button
            type="submit"
            :disabled="pwSubmitting || !currentPw || !newPw"
            class="rounded-lg border border-border bg-accent px-3 py-1.5 text-sm text-accent-foreground hover:opacity-90 disabled:opacity-50"
          >
            {{ pwSubmitting ? "Changing…" : "Change password" }}
          </button>
        </div>
      </form>
    </div>
  </section>
</template>
