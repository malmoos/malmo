<script setup lang="ts">
// Password re-prompt for elevation (USERS_AND_GROUPS.md # Elevation in the UI).
// The sudo-in-UI pattern: a destructive admin op asks for the password once,
// then the brain's 5-minute window covers the rest. Mounted once in AppShell;
// driven entirely by elevate.ts singleton state.
import { ref, watch, nextTick } from "vue";
import {
  elevateVisible,
  elevateSubmitting,
  elevateError,
  submitElevation,
  cancelElevation,
} from "../elevate";

const password = ref("");
const input = ref<HTMLInputElement | null>(null);

// Clear the field on each open and focus it.
watch(elevateVisible, async (open) => {
  if (open) {
    password.value = "";
    await nextTick();
    input.value?.focus();
  }
});

function submit() {
  submitElevation(password.value);
}
</script>

<template>
  <div
    v-if="elevateVisible"
    class="fixed inset-0 z-50 grid place-items-center bg-black/40 px-4"
    @click.self="cancelElevation"
  >
    <form
      class="w-full max-w-sm space-y-4 rounded-xl border border-border bg-card p-6 shadow-lg"
      @submit.prevent="submit"
    >
      <div class="space-y-1">
        <h2 class="text-sm font-medium">Confirm it's you</h2>
        <p class="text-xs text-muted-foreground">
          Re-enter your password to make admin changes. We'll remember it for a few minutes.
        </p>
      </div>
      <input
        ref="input"
        v-model="password"
        type="password"
        placeholder="Password"
        autocomplete="current-password"
        class="w-full rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
      />
      <p v-if="elevateError" class="text-xs text-destructive">{{ elevateError }}</p>
      <div class="flex justify-end gap-2">
        <button
          type="button"
          class="rounded-lg border border-border px-4 py-1.5 text-sm hover:bg-muted"
          @click="cancelElevation"
        >
          Cancel
        </button>
        <button
          type="submit"
          class="rounded-lg border border-border bg-accent px-4 py-1.5 text-sm text-accent-foreground hover:opacity-90 disabled:opacity-50"
          :disabled="elevateSubmitting || !password"
        >
          {{ elevateSubmitting ? "Confirming…" : "Confirm" }}
        </button>
      </div>
    </form>
  </div>
</template>
