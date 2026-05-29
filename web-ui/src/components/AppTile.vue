<script setup lang="ts">
// A single app tile (DASHBOARD.md # Tile). Calm by default — icon, name, and a
// quiet label — with state surfacing *only* when not nominal: a down/stopped
// instance grays out and gets a corner alert mark. Clicking a running tile
// opens the app in a new tab at its own subdomain (DASHBOARD.md # Open-app
// interaction); the dashboard is the launcher, not a frame around apps.
//
// Per-app icons aren't in the manifest/DTO yet, so every tile uses a generic
// glyph for now; real icons are a follow-up when the catalog carries them.
import { computed } from "vue";
import { AppWindow, AlertTriangle } from "lucide-vue-next";
import type { Instance } from "../api";

const props = defineProps<{ instance: Instance }>();

const running = computed(() => props.instance.state === "running");
const label = computed(() => (props.instance.scope === "household" ? "Shared" : "Personal"));
</script>

<template>
  <component
    :is="running ? 'a' : 'div'"
    :href="running ? instance.url : undefined"
    :target="running ? '_blank' : undefined"
    rel="noopener"
    class="relative flex flex-col items-center gap-2 rounded-2xl border border-border bg-card p-4 text-center transition"
    :class="running ? 'cursor-pointer hover:shadow-md' : 'cursor-default opacity-50'"
    :title="running ? `Open ${instance.name}` : `${instance.name} is ${instance.state}`"
  >
    <AlertTriangle
      v-if="!running"
      class="absolute right-2 top-2 size-4 text-destructive"
    />
    <div class="grid size-12 place-items-center rounded-xl bg-muted text-muted-foreground">
      <AppWindow class="size-6" />
    </div>
    <div class="min-w-0">
      <div class="truncate text-sm font-medium">{{ instance.name }}</div>
      <div class="text-xs text-muted-foreground">{{ label }}</div>
    </div>
  </component>
</template>
