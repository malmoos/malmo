<script setup lang="ts">
// The top bar (DASHBOARD.md # the top bar): three quiet elements — a storage
// pill (left), and the notification bell + account menu (right). Everything
// here is deliberately understated; it goes loud only under pressure, which
// none of these surfaces do yet:
//   - Storage pill — static placeholder until a capacity endpoint exists; it
//     already routes to Settings, where Storage will live.
//   - Bell — no unread dot until NOTIFICATIONS.md is wired (queue item #2).
//   - Account menu — username + role; sign-out is intentionally omitted in the
//     single-user dev phase (re-enabled with the AUTH.md login screen).
import { ref } from "vue";
import { RouterLink } from "vue-router";
import { HardDrive } from "lucide-vue-next";
import { useAuth } from "../auth";
import NotificationBell from "../NotificationBell.vue";

const { currentUser } = useAuth();
const menuOpen = ref(false);

function initial(name: string | undefined): string {
  return (name ?? "?").charAt(0).toUpperCase();
}
</script>

<template>
  <header class="flex items-center justify-between px-4 py-3">
    <RouterLink
      to="/settings"
      class="inline-flex items-center gap-1.5 rounded-full border border-border bg-card px-3 py-1 text-xs text-muted-foreground hover:bg-muted"
      title="Storage"
    >
      <HardDrive class="size-3.5" />
      <span>— / — TB</span>
    </RouterLink>

    <div class="flex items-center gap-2">
      <NotificationBell />

      <div class="relative">
        <button
          type="button"
          class="grid size-8 place-items-center rounded-full bg-accent text-sm font-medium text-accent-foreground"
          :title="currentUser?.username"
          @click="menuOpen = !menuOpen"
        >
          {{ initial(currentUser?.username) }}
        </button>
        <div
          v-if="menuOpen"
          class="absolute right-0 z-10 mt-2 w-44 rounded-lg border border-border bg-card py-1 shadow-lg"
          @click="menuOpen = false"
        >
          <div class="px-3 py-2">
            <div class="text-sm font-medium">{{ currentUser?.username }}</div>
            <div class="text-xs capitalize text-muted-foreground">{{ currentUser?.role }}</div>
          </div>
          <RouterLink
            to="/settings"
            class="block px-3 py-2 text-sm hover:bg-muted"
          >
            Settings
          </RouterLink>
        </div>
      </div>
    </div>
  </header>
</template>
