<script setup lang="ts">
// App is the auth-aware root. It picks one of these states from auth:
//   - bootstrapping (briefly, while /auth/state + /me settle)
//   - Setup (no users on the box yet)
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

const { currentUser, hasUsers, booted } = useAuth();
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
  <Setup v-else-if="!hasUsers" />
  <AppShell v-else-if="currentUser" />
  <RecoverView v-else-if="route.path === '/recover'" />
  <Login v-else />
</template>
