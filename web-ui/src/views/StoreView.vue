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
// Door 2 (custom-container install) is admin-only and sits as a "Custom app"
// link beside the search, never in the browse grid (DASHBOARD.md # Door-2).
// Members never see it.
//
// The layout follows the Oatmeal-skinned Tailwind Plus application-UI patterns:
// a page heading with an inline search (input-group with a leading icon), pill
// tabs for categories, a grid list of cards, and an empty-state block for the
// zero-result cases. All colour flows from the olive semantic tokens
// (style.css); the display heading reuses the owned components/ui idiom rather
// than re-deriving it here.
import { computed, ref } from "vue";
import { useQuery } from "@tanstack/vue-query";
import { Search, SearchX, PackageOpen } from "lucide-vue-next";
import { useAuth } from "../auth";
import { api, type CatalogEntry } from "../api";
import StoreAppCard from "../components/StoreAppCard.vue";
import Heading from "@/components/ui/Heading.vue";
import Button from "@/components/ui/Button.vue";

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

// Reset both filters — surfaced from the "no matches" empty state so a dead-end
// search is one click from the full grid again.
function clearFilters() {
  query.value = "";
  activeCategory.value = "all";
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
  <div class="space-y-10 pt-2">
    <section class="space-y-6">
      <!-- Page heading: title + description on the left, the search input-group
           inline on the right (stacks above the pills on narrow screens). -->
      <div class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <Heading :level="2">Store</Heading>
          <p class="mt-1 text-sm text-muted-foreground">Browse apps to run on your malmo.</p>
        </div>

        <!-- Right cluster: the admin-only "Custom app" link (Door 2) sits to the
             left of the page-wide search. -->
        <div class="flex items-center gap-3">
          <!-- Door 2: custom-container install, as a plain text link. -->
          <RouterLink
            v-if="isAdmin"
            to="/store/custom"
            class="shrink-0 text-sm text-muted-foreground transition-colors hover:text-foreground"
          >
            Custom app
          </RouterLink>

          <!-- Page-wide search: filters the grid in place over name + short
               description. Leading-icon input-group, pill-shaped to match the idiom. -->
          <div class="relative w-full sm:w-64">
            <Search
              class="pointer-events-none absolute left-3.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
              aria-hidden="true"
            />
            <input
              v-model="query"
              type="search"
              placeholder="Search apps…"
              aria-label="Search apps"
              class="w-full rounded-full border border-border bg-card py-2 pl-10 pr-4 text-sm outline-none placeholder:text-muted-foreground focus:border-accent"
            />
          </div>
        </div>
      </div>

      <!-- Category pills: "All" plus the catalog's own categories. -->
      <div v-if="categories.length > 1" class="flex flex-wrap gap-2">
        <button
          v-for="c in categories"
          :key="c"
          type="button"
          class="cursor-pointer rounded-full border px-3.5 py-1 text-sm font-medium capitalize transition-colors"
          :class="
            activeCategory === c
              ? 'border-accent bg-accent text-accent-foreground'
              : 'border-border bg-card text-muted-foreground hover:bg-muted hover:text-foreground'
          "
          @click="selectCategory(c)"
        >
          {{ c === "all" ? "All" : c }}
        </button>
      </div>

      <!-- Transient states stay as a quiet line; content-absence states get a
           proper empty-state block below. -->
      <p v-if="catalog.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
      <p v-else-if="catalog.isError.value" class="text-sm text-destructive">
        Couldn't load the catalog. {{ (catalog.error.value as Error)?.message }}
      </p>

      <!-- Empty catalog: nothing to browse yet. -->
      <div
        v-else-if="apps.length === 0"
        class="rounded-2xl border border-dashed border-border py-16 text-center"
      >
        <PackageOpen class="mx-auto size-8 text-muted-foreground" aria-hidden="true" />
        <h3 class="mt-3 text-sm font-semibold text-foreground">No apps in the catalog yet</h3>
        <p class="mt-1 text-sm text-muted-foreground">Check back soon — the catalog is still filling out.</p>
      </div>

      <!-- Filters matched nothing: offer a one-click reset. -->
      <div
        v-else-if="filtered.length === 0"
        class="rounded-2xl border border-dashed border-border py-16 text-center"
      >
        <SearchX class="mx-auto size-8 text-muted-foreground" aria-hidden="true" />
        <h3 class="mt-3 text-sm font-semibold text-foreground">No apps match your search</h3>
        <p class="mt-1 text-sm text-muted-foreground">Try a different search term or category.</p>
        <Button variant="secondary" size="sm" class="mt-4" @click="clearFilters">Clear filters</Button>
      </div>

      <!-- Grid list of app cards. -->
      <div v-else class="grid grid-cols-2 gap-x-6 gap-y-8 sm:grid-cols-4 lg:grid-cols-6">
        <StoreAppCard v-for="c in filtered" :key="c.id" :app="c" />
      </div>
    </section>
  </div>
</template>
