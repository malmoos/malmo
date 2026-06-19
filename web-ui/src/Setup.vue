<script setup lang="ts">
// First-run wizard shell (FIRST_RUN.md Phase 2; ENVIRONMENT.md # Provisioning —
// "Setup wizard, trimmed"). It owns the step cursor and renders one step at a
// time; each step emits `next` when satisfied. The Done step latches the
// first-run-complete marker (auth.finishFirstRun) and the App.vue gate then
// swaps the wizard for the dashboard.
import { computed, ref } from "vue";
import { useAuth } from "./auth";
import AdminStep from "./setup/AdminStep.vue";
import TimezoneStep from "./setup/TimezoneStep.vue";
import TelemetryStep from "./setup/TelemetryStep.vue";
import DoneStep from "./setup/DoneStep.vue";

const { hasUsers, profile } = useAuth();

// Profile-aware step set. Today both profiles share this list; bare-metal B4
// prepends the appliance-only Network/WiFi + Enrollment steps ahead of "admin"
// without touching this shell (ENVIRONMENT.md # Two layers). On a resumed wizard
// the admin already exists (created in a prior session, or before this marker on
// an upgraded box), so the admin step is skipped — its endpoint would 409.
const adminExists = hasUsers.value === true;
const steps = adminExists
  ? (["timezone", "telemetry", "done"] as const)
  : (["admin", "timezone", "telemetry", "done"] as const);

const index = ref(0);
const current = computed(() => steps[index.value]);

function next() {
  if (index.value < steps.length - 1) index.value++;
}
</script>

<template>
  <main class="auth">
    <h1>malmo</h1>
    <p class="step-count">Step {{ index + 1 }} of {{ steps.length }}</p>
    <AdminStep v-if="current === 'admin'" :profile="profile ?? 'appliance'" @next="next" />
    <TimezoneStep v-else-if="current === 'timezone'" @next="next" />
    <TelemetryStep v-else-if="current === 'telemetry'" @next="next" />
    <DoneStep v-else-if="current === 'done'" />
  </main>
</template>

<style>
.auth .step-count {
  text-align: center;
  color: #999;
  font-size: 0.75rem;
  letter-spacing: 0.04em;
  margin: -0.75rem 0 1rem;
}
</style>
