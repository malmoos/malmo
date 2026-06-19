<script setup lang="ts">
// App is the auth-aware root. It picks one of these states from auth:
//   - bootstrapping (briefly, while /auth/state + /me settle)
//   - Setup (the first-run wizard — runs until its Done step, FIRST_RUN.md # Phase 3)
//   - the signed-in shell (AppShell + routed views)
//   - Recover (logged out, at /recover — the public recovery-code flow)
//   - Login (first-run done, no active session)
// Any 401 from a later API call drops currentUser via the handler wired in
// auth.ts, which flips us back to Login without a route change.
import { computed, onMounted } from "vue";
import { useRoute } from "vue-router";
import { bootstrap, useAuth } from "./auth";
import Login from "./Login.vue";
import Setup from "./Setup.vue";
import RecoverView from "./RecoverView.vue";
import AppShell from "./components/AppShell.vue";

const { currentUser, hasUsers, firstRunComplete, booted } = useAuth();
// The recovery screen is the one logged-out destination that isn't Login. The
// router-view never mounts while logged out (it lives in AppShell), so App.vue
// renders RecoverView directly off the reactive path.
const route = useRoute();

// The wizard runs until first-run is marked complete (Phase 3), NOT just until
// an admin exists — the admin is created mid-wizard with time-zone/telemetry
// still ahead. The session guard keeps an already-provisioned box that is merely
// logged out (admin exists, no session) on the Login screen rather than dropping
// it into the wizard's admin-gated steps: show the wizard only on a fresh box
// (no admin yet) or when a session is active to drive the remaining steps.
const showWizard = computed(
  () => !firstRunComplete.value && (!hasUsers.value || !!currentUser.value),
);

onMounted(() => {
  bootstrap();
});
</script>

<template>
  <div v-if="!booted" class="grid h-full place-items-center text-muted-foreground">Loading…</div>
  <Setup v-else-if="showWizard" />
  <AppShell v-else-if="currentUser" />
  <RecoverView v-else-if="route.path === '/recover'" />
  <Login v-else />
</template>
