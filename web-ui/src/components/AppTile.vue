<script setup lang="ts">
// A single app tile (DASHBOARD.md # Tile). Calm by default — icon, name, and a
// quiet label — with state surfacing *only* when not nominal. Three non-running
// shapes:
//   - running:  a link that opens the app in a new tab at its own subdomain.
//   - stopped:  grayed; if the viewer may control it, a button that starts the
//               service, with a hover caption under the logo. A deliberate stop
//               is not an error, so it gets no corner alert mark.
//   - starting: grayed with a persistent "Starting up…" caption while the start
//               job runs (driven by the `starting` prop from HomeView).
//   - failed/other: grayed with the corner alert mark — needs attention.
//
// Icon rendering mirrors StoreAppCard: icon_url when present, AppGlyph fallback.
import { computed, ref } from "vue";
import { AlertTriangle } from "lucide-vue-next";
import type { Instance } from "../api";
import { useAuth } from "../auth";
import AppGlyph from "./AppGlyph.vue";

const props = defineProps<{ instance: Instance; starting?: boolean }>();
const emit = defineEmits<{ start: [] }>();
const { singleUserMode, currentUser } = useAuth();

const running = computed(() => props.instance.state === "running");
const stopped = computed(() => props.instance.state === "stopped");
const label = computed(() => (props.instance.scope === "household" ? "Shared" : "Personal"));

// Mirrors the brain's control gate: admins anything, members only their own
// personal instance. A member can't start a stopped household app, so its tile
// stays grayed but inert.
const canControl = computed(
  () => currentUser.value?.role === "admin" || props.instance.scope === "personal",
);
const canStart = computed(() => stopped.value && canControl.value && !props.starting);

const brokenIcon = ref(false);

// The corner alert is for trouble (failed/crashed), not a deliberate stop or an
// in-flight start.
const showAlert = computed(() => !running.value && !stopped.value && !props.starting);

// Element + behavior: a link when running, a button when the viewer can start
// it, otherwise an inert div.
const tag = computed(() => (running.value ? "a" : canStart.value ? "button" : "div"));

function onClick() {
  if (canStart.value) emit("start");
}
</script>

<template>
  <component
    :is="tag"
    :href="running ? instance.url : undefined"
    :target="running ? '_blank' : undefined"
    :type="tag === 'button' ? 'button' : undefined"
    rel="noopener"
    class="group flex flex-col items-center gap-3 text-center"
    :class="running || canStart ? 'cursor-pointer' : 'cursor-default'"
    :title="running ? `Open ${instance.name}` : `${instance.name} is ${instance.state}`"
    @click="onClick"
  >
    <!-- The big square button: parchment fill, bordered, the app icon (or glyph).
         Mirrors StoreAppCard. Grays out + corner alert when not running. -->
    <div
      class="relative grid aspect-square w-full place-items-center overflow-hidden rounded-3xl border border-border bg-card text-muted-foreground transition"
      :class="running ? 'group-hover:shadow-md' : 'opacity-50'"
    >
      <AlertTriangle
        v-if="showAlert"
        class="absolute right-3 top-3 size-4 text-destructive"
      />
      <img
        v-if="instance.icon_url && !brokenIcon"
        :src="instance.icon_url"
        :alt="`${instance.name} icon`"
        class="size-1/2 object-contain"
        @error="brokenIcon = true"
      />
      <AppGlyph v-else :name="instance.icon_glyph" class="size-1/2" />
    </div>
    <div class="min-w-0">
      <div class="truncate text-base font-medium">{{ instance.name }}</div>
      <!-- Status caption under the logo. "Starting up…" persists while the start
           job runs; the click-to-start hint is revealed on hover. -->
      <div v-if="starting" class="text-xs text-muted-foreground">Starting up…</div>
      <div
        v-else-if="canStart"
        class="text-xs text-muted-foreground opacity-0 transition group-hover:opacity-100"
      >
        Service stopped - click to start again
      </div>
      <div v-else-if="!singleUserMode" class="text-xs uppercase tracking-wide text-muted-foreground">{{ label }}</div>
    </div>
  </component>
</template>
