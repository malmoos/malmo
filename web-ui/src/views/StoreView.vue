<script setup lang="ts">
// Store — browse the catalog as a grid of cards (DASHBOARD.md # global
// navigation, APP_STORE.md # Catalog schema). Each card shows the app's logo and
// name and links to its detail page (/store/:id), where the description,
// screenshots, and the Install flow live — mirroring an app store's browse →
// detail shape.
//
// Browse is filterable along two axes (APP_STORE.md # Catalog schema: "Browse UI
// groups by category"): a free-text search over name + short description, and a
// row of category pills derived from the catalog's own `categories` (the union
// across loaded apps, never a hardcoded taxonomy — the taxonomy is open, NEXT.md).
// "All" is the default pill. Both filters compose and narrow the grid in place.
//
// Door 2 (custom-container install) is admin-only and lives at the bottom of the
// Store, never in the browse grid (DASHBOARD.md # Door-2). Members never see it.
import { computed, ref } from "vue";
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

// Free-text query and the active category pill ("all" = no category filter).
const query = ref("");
const activeCategory = ref("all");

// Categories offered as pills: the sorted union of every app's declared
// categories, with "all" prepended. Derived from the corpus so a new catalog
// category appears without a UI change rather than from a hardcoded taxonomy.
const categories = computed(() => {
  const seen = new Set<string>();
  for (const app of apps.value) for (const c of app.categories ?? []) seen.add(c);
  return ["all", ...[...seen].sort()];
});

function selectCategory(c: string) {
  activeCategory.value = c;
}

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase();
  const cat = activeCategory.value;
  return apps.value.filter((app) => {
    const matchesCategory = cat === "all" || (app.categories ?? []).includes(cat);
    const matchesQuery =
      q === "" ||
      app.name.toLowerCase().includes(q) ||
      (app.short_description ?? "").toLowerCase().includes(q);
    return matchesCategory && matchesQuery;
  });
});
</script>

<template>
  <div class="space-y-8 pt-2">
    <section class="space-y-4">
      <h2 class="text-2xl font-semibold tracking-tight">Store</h2>

      <!-- Page-wide search: filters the grid in place over name + short description. -->
      <input
        v-model="query"
        type="search"
        placeholder="Search apps…"
        aria-label="Search apps"
        class="w-full rounded-lg border border-border bg-background px-3 py-2 text-sm outline-none focus:border-accent"
      />

      <!-- Category pills: "All" plus the catalog's own categories. -->
      <div v-if="categories.length > 1" class="flex flex-wrap gap-2">
        <button
          v-for="c in categories"
          :key="c"
          type="button"
          class="cursor-pointer rounded-full border px-3 py-1 text-sm capitalize transition"
          :class="
            activeCategory === c
              ? 'border-accent bg-accent text-accent-foreground'
              : 'border-border bg-card text-muted-foreground hover:bg-muted'
          "
          @click="selectCategory(c)"
        >
          {{ c === "all" ? "All" : c }}
        </button>
      </div>

      <p v-if="catalog.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
      <p v-else-if="catalog.isError.value" class="text-sm text-destructive">
        Couldn't load the catalog. {{ (catalog.error.value as Error)?.message }}
      </p>
      <p v-else-if="apps.length === 0" class="text-sm text-muted-foreground">No apps in the catalog yet.</p>
      <p v-else-if="filtered.length === 0" class="text-sm text-muted-foreground">No apps match your search.</p>
      <div v-else class="grid grid-cols-2 gap-x-6 gap-y-8 sm:grid-cols-4 lg:grid-cols-6">
        <StoreAppCard v-for="c in filtered" :key="c.id" :app="c" />
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
