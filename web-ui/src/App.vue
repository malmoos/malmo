<script setup lang="ts">
// App is the auth-aware root. It picks one of four states from auth:
//   - bootstrapping (briefly, while /auth/state + /me settle)
//   - Setup (no users on the box yet)
//   - the signed-in shell (AppShell + routed views)
//   - a dev-only "session unavailable" notice (sign-in is disabled in the
//     single-user dev phase; see auth.ts)
// Any 401 from a later API call drops currentUser via the handler wired in
// auth.ts, which flips us back here without a route change.
import { onMounted } from "vue";
import { bootstrap, useAuth } from "./auth";
// Login is kept in the tree but not rendered in v1 — the dev phase is
// single-user and we don't surface sign-in/out yet. Will come back when
// AUTH.md # Login screen UX (the user-list picker) lands.
import Login from "./Login.vue";
void Login;
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
  <div v-else class="mx-auto mt-16 max-w-xl px-4 text-center text-muted-foreground">
    Session unavailable. Sign-in is disabled in this dev build — reset
    <code>.dev/state</code> to re-bootstrap.
  </div>
</template>
