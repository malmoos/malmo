<script setup lang="ts">
// First-run Step 6 — done (FIRST_RUN.md # Phase 3). Finishing latches the
// first-run-complete marker in the brain; auth.finishFirstRun then flips the
// App.vue gate to the dashboard, and the wizard never reappears.
import { ref } from "vue";
import { finishFirstRun } from "../auth";
import type { ApiError } from "../api";

const submitting = ref(false);
const error = ref("");

async function finish() {
  error.value = "";
  submitting.value = true;
  try {
    await finishFirstRun();
    // No emit/navigation: finishFirstRun flips the auth gate and App.vue swaps
    // this wizard for the dashboard.
  } catch (e) {
    error.value = (e as ApiError).message || "Could not finish setup";
    submitting.value = false;
  }
}
</script>

<template>
  <form class="card" @submit.prevent="finish">
    <h2>You're all set</h2>
    <p class="hint">Your box is ready. You can install apps and invite others from the dashboard.</p>
    <button type="submit" :disabled="submitting">
      {{ submitting ? "Finishing…" : "Go to dashboard" }}
    </button>
    <p v-if="error" class="error">{{ error }}</p>
  </form>
</template>
