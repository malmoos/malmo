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
import { useAuth } from "../auth";

const props = defineProps<{ instance: Instance }>();
const { singleUserMode } = useAuth();

const running = computed(() => props.instance.state === "running");
const label = computed(() => (props.instance.scope === "household" ? "Shared" : "Personal"));
</script>

<template>
  <component
    :is="running ? 'a' : 'div'"
    :href="running ? instance.url : undefined"
    :target="running ? '_blank' : undefined"
    rel="noopener"
    class="group flex flex-col items-center gap-3 text-center"
    :class="running ? 'cursor-pointer' : 'cursor-default'"
    :title="running ? `Open ${instance.name}` : `${instance.name} is ${instance.state}`"
  >
    <!-- The big square button: parchment fill, bordered, single centered glyph
         (DASHBOARD.md # Tile). Grays out + corner alert when not running. -->
    <div
      class="relative grid aspect-square w-full place-items-center rounded-3xl border border-border bg-card text-muted-foreground transition"
      :class="running ? 'group-hover:shadow-md' : 'opacity-50'"
    >
      <AlertTriangle
        v-if="!running"
        class="absolute right-3 top-3 size-4 text-destructive"
      />
      <AppWindow class="size-9" />
    </div>
    <div class="min-w-0">
      <div class="truncate text-base font-medium">{{ instance.name }}</div>
      <div v-if="!singleUserMode" class="text-xs uppercase tracking-wide text-muted-foreground">{{ label }}</div>
    </div>
  </component>
</template>
