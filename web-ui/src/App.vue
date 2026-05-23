<script setup lang="ts">
// App is the auth-aware router. Three views, picked from auth state:
//   - bootstrapping (briefly, while /auth/state + /me settle)
//   - Setup (no users on the box yet)
//   - Login (users exist but no valid session)
//   - Dashboard (signed in)
// Any 401 from a later API call drops currentUser via the handler wired in
// auth.ts, which flips us back to Login without a route change.
import { onMounted } from "vue";
import { bootstrap, useAuth } from "./auth";
import Login from "./Login.vue";
import Setup from "./Setup.vue";
import Dashboard from "./Dashboard.vue";

const { currentUser, hasUsers, booted } = useAuth();

onMounted(() => {
  bootstrap();
});
</script>

<template>
  <div v-if="!booted" class="boot">Loading…</div>
  <Setup v-else-if="!hasUsers" />
  <Dashboard v-else-if="currentUser" />
  <Login v-else />
</template>

<style>
.boot { max-width: 720px; margin: 4rem auto; text-align: center; color: #999; font-family: ui-sans-serif, system-ui, sans-serif; }
</style>
