<script setup lang="ts">
// Store — browse the catalog as a grid of cards (DASHBOARD.md # global
// navigation, APP_STORE.md # Catalog schema). Each card shows the app's logo and
// name and links to its detail page (/store/:id), where the description,
// screenshots, and the Install flow live — mirroring an app store's browse →
// detail shape. The grid is flat for v1 (search/category grouping earns its place
// as the corpus grows, DASHBOARD.md # Search).
//
// Door 2 (custom-container install) is admin-only and lives at the bottom of the
// Store, never in the browse grid (DASHBOARD.md # Door-2). Members never see it.
import { computed } from "vue";
import { useQuery } from "@tanstack/vue-query";
import { useAuth } from "../auth";
import { api, type CatalogEntry } from "../api";
import StoreAppCard from "../components/StoreAppCard.vue";

const { currentUser } = useAuth();

const catalog = useQuery({
  queryKey: ["catalog"],
  queryFn: () => api.get<{ apps: CatalogEntry[] }>("/catalog"),
});

const apps = computed(() => catalog.data.value?.apps ?? []);
const isAdmin = computed(() => currentUser.value?.role === "admin");
</script>

<template>
  <div class="space-y-8 pt-2">
    <section class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Catalog</h2>
      <p v-if="catalog.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
      <p v-else-if="catalog.isError.value" class="text-sm text-destructive">
        Couldn't load the catalog. {{ (catalog.error.value as Error)?.message }}
      </p>
      <p v-else-if="apps.length === 0" class="text-sm text-muted-foreground">No apps in the catalog yet.</p>
      <div v-else class="grid grid-cols-2 gap-x-6 gap-y-8 sm:grid-cols-4 lg:grid-cols-6">
        <StoreAppCard v-for="c in apps" :key="c.id" :app="c" />
      </div>
    </section>

    <!-- Door 2: admin-only custom-container install, tucked at the bottom -->
    <section v-if="isAdmin" class="border-t border-border pt-4">
      <RouterLink
        to="/store/custom"
        class="inline-flex items-center gap-2 rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
      >
        Install a custom container
      </RouterLink>
      <p class="mt-1.5 text-xs text-muted-foreground">
        Paste a <code>docker-compose.yml</code> to run an app that isn't in the catalog.
      </p>
    </section>
  </div>
</template>
