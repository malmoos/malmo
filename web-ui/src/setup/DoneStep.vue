<script setup lang="ts">
// First-run wizard, final step (FIRST_RUN.md # Step 6 / Phase 3). completeFirstRun
// writes the first-run-complete marker and flips firstRunComplete locally, so
// App.vue swaps the wizard for the dashboard the moment this resolves — no
// emit needed, this is the end of the wizard.
import { ref } from "vue";
import { completeFirstRun } from "../auth";
import type { ApiError } from "../api";
import Button from "@/components/ui/Button.vue";

const submitting = ref(false);
const error = ref("");

async function finish() {
  error.value = "";
  submitting.value = true;
  try {
    await completeFirstRun();
  } catch (e) {
    error.value = (e as ApiError).message || "Couldn't finish setup";
    submitting.value = false;
  }
}
</script>

<template>
  <form @submit.prevent="finish">
    <h2>You're all set</h2>
    <p class="hint">
      Your box is ready. Next you'll land on the dashboard, where you can
      install apps and invite the people you share the box with.
    </p>
    <Button type="submit" :disabled="submitting" class="mt-2">
      {{ submitting ? "Finishing…" : "Go to dashboard" }}
    </Button>
    <p v-if="error" class="error">{{ error }}</p>
  </form>
</template>
