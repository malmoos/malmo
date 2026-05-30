<script setup lang="ts">
// Home = the app launcher (DASHBOARD.md # the home screen is the app launcher).
// A grid of tiles grouped into two sections:
//   - Household — shared instances the user is permitted to open.
//   - Yours — the current user's personal instances.
// The brain already scopes GET /apps per caller (household + own personal;
// admins additionally see others' personal). The home screen deliberately
// shows only Household + *my* Yours, so we filter personal tiles to the
// current user — other members' personal instances never appear here even for
// an admin (DASHBOARD.md: "they never see other members' personal instances").
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { useQuery } from "@tanstack/vue-query";
import { api, type Instance } from "../api";
import { useAuth } from "../auth";
import AppTile from "../components/AppTile.vue";

const { currentUser } = useAuth();

const apps = useQuery({
  queryKey: ["apps"],
  queryFn: () => api.get<{ apps: Instance[] }>("/apps"),
});

const all = computed(() => apps.data.value?.apps ?? []);
const household = computed(() => all.value.filter((a) => a.scope === "household"));
const yours = computed(() =>
  all.value.filter((a) => a.scope === "personal" && a.owner_user_id === currentUser.value?.id),
);
const empty = computed(() => household.value.length === 0 && yours.value.length === 0);
</script>

<template>
  <div class="space-y-8 pt-2">
    <p v-if="apps.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>

    <!-- First arrival / empty state: invite, don't shove (DASHBOARD.md). -->
    <div
      v-else-if="empty"
      class="mt-16 flex flex-col items-center gap-3 text-center text-muted-foreground"
    >
      <p>No apps yet.</p>
      <RouterLink
        to="/store"
        class="rounded-lg bg-accent px-4 py-2 text-sm font-medium text-accent-foreground"
      >
        Browse the Store
      </RouterLink>
    </div>

    <template v-else>
      <section v-if="household.length" class="space-y-3">
        <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Household</h2>
        <div class="grid grid-cols-3 gap-3 sm:grid-cols-4">
          <AppTile v-for="a in household" :key="a.id" :instance="a" />
        </div>
      </section>

      <section v-if="yours.length" class="space-y-3">
        <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Yours</h2>
        <div class="grid grid-cols-3 gap-3 sm:grid-cols-4">
          <AppTile v-for="a in yours" :key="a.id" :instance="a" />
        </div>
      </section>
    </template>
  </div>
</template>
