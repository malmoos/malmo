<script setup lang="ts">
// StoreSpotlight — the landing page's banner: the one authored spotlight app
// (store's home.yml `spotlight:`), rendered wider than a regular card and
// carrying its tagline. It is a promotion of an ordinary app, not a different
// kind of thing, so the whole card is still a RouterLink to the detail page —
// mirrors the marketing store's banner() (../cloud internal/web/static/store.js).
import { ref } from "vue";
import { RouterLink } from "vue-router";
import { Sparkles } from "lucide-vue-next";
import type { CatalogEntry } from "../api";
import AppGlyph from "./AppGlyph.vue";

defineProps<{ app: CatalogEntry }>();

// brokenIcon flips if the <img> fails to load (e.g. asset 404) so we fall back
// to the glyph rather than show a broken-image chrome — same guard as
// StoreAppCard.
const brokenIcon = ref(false);
</script>

<template>
  <RouterLink
    :to="`/store/${app.id}`"
    class="group flex flex-col gap-4 rounded-3xl border border-border bg-card p-6 transition hover:shadow-md sm:flex-row sm:items-center sm:gap-6"
    :title="`View ${app.name}`"
  >
    <div
      class="grid size-20 shrink-0 place-items-center overflow-hidden rounded-2xl border border-border bg-background text-muted-foreground"
    >
      <img
        v-if="app.icon_url && !brokenIcon"
        :src="app.icon_url"
        :alt="`${app.name} icon`"
        class="size-3/5 object-contain"
        @error="brokenIcon = true"
      />
      <AppGlyph v-else :name="app.icon_glyph" class="size-8" />
    </div>

    <div class="flex flex-col gap-1">
      <span class="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-accent">
        <Sparkles class="size-3.5" aria-hidden="true" />
        Spotlight
      </span>
      <span class="text-lg font-semibold text-foreground">{{ app.name }}</span>
      <span v-if="app.short_description" class="text-sm text-muted-foreground">{{ app.short_description }}</span>
    </div>
  </RouterLink>
</template>
