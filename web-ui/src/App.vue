<script setup lang="ts">
// App is the auth-aware root. It picks one of three states from auth:
//   - bootstrapping (briefly, while /auth/state + /me settle)
//   - Setup (no users on the box yet)
//   - the signed-in shell (AppShell + routed views)
//   - Login (has users, no active session)
// Any 401 from a later API call drops currentUser via the handler wired in
// auth.ts, which flips us back to Login without a route change.
import { onMounted } from "vue";
import { bootstrap, useAuth } from "./auth";
import Login from "./Login.vue";
import Setup from "./Setup.vue";
import AppShell from "./components/AppShell.vue";

const { currentUser, hasUsers, booted } = useAuth();

onMounted(() => {
  bootstrap();
});
</script>

<template>
  <div v-if="!booted" class="grid h-full place-items-center text-muted-foreground">Loading…</div>
  <Setup v-else-if="!hasUsers" />
  <AppShell v-else-if="currentUser" />
  <Login v-else />
</template>
