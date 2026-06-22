<script setup lang="ts">
// App is the auth-aware root. It picks one of these states from auth:
//   - bootstrapping (briefly, while /auth/state + /me settle)
//   - Setup (the first-run wizard — runs until first_run_complete, FIRST_RUN.md
//     # Phase 2; gated on the marker, not has_users, so a half-finished wizard
//     resumes rather than dropping the user onto the dashboard)
//   - the signed-in shell (AppShell + routed views)
//   - Recover (logged out, at /recover — the public recovery-code flow)
//   - Login (has users, no active session)
// Any 401 from a later API call drops currentUser via the handler wired in
// auth.ts, which flips us back to Login without a route change.
import { onMounted } from "vue";
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

onMounted(() => {
  bootstrap();
});
</script>

<template>
  <div v-if="!booted" class="grid h-full place-items-center text-muted-foreground">Loading…</div>
  <!-- Wizard interrupted after the admin was created but before completion, and
       the session is gone (e.g. a reload): the remaining steps are admin-gated,
       so re-auth first, then the wizard resumes at its next step. -->
  <Login v-else-if="!firstRunComplete && hasUsers && !currentUser" />
  <Setup v-else-if="!firstRunComplete" />
  <AppShell v-else-if="currentUser" />
  <RecoverView v-else-if="route.path === '/recover'" />
  <Login v-else />
</template>
