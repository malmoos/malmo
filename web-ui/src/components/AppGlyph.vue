<script setup lang="ts">
// AppGlyph — the icon a store card / detail header shows when an app has no
// raster icon (`icon_url`). It renders the Lucide glyph named by the manifest's
// `icon_glyph` (kebab-case, e.g. "notebook-pen"; APP_STORE.md # Catalog schema),
// or the generic AppWindow when no name is declared or the name doesn't resolve.
//
// Lucide's ~1700-icon set is imported lazily as a single chunk only when a glyph
// is actually needed, so the icon library never weighs on the main bundle. Class
// (size, etc.) falls through from the parent to the rendered <component> root.
import { shallowRef, watchEffect, type Component } from "vue";
import { AppWindow } from "lucide-vue-next";

const props = defineProps<{ name?: string | null }>();

// Resolved Lucide component, or null while loading / when unresolved → AppWindow.
const glyph = shallowRef<Component | null>(null);

// Lucide exports are PascalCase ("notebook-pen" → "NotebookPen"); the manifest
// carries the kebab-case name from lucide.dev. Same shape we validate brain-side.
const KEBAB = /^[a-z0-9]+(-[a-z0-9]+)*$/;
function exportName(kebab: string): string {
  return kebab
    .split("-")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join("");
}

watchEffect(async () => {
  glyph.value = null;
  const name = props.name;
  if (!name || !KEBAB.test(name)) return;
  try {
    const mod = (await import("lucide-vue-next")) as unknown as Record<string, Component>;
    glyph.value = mod[exportName(name)] ?? null;
  } catch {
    glyph.value = null; // network/parse failure → generic glyph
  }
});
</script>

<template>
  <component :is="glyph ?? AppWindow" />
</template>
