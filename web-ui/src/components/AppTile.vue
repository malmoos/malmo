<script setup lang="ts">
// A single app tile (DASHBOARD.md # Tile). Calm by default — icon, name, and a
// quiet label — with state surfacing *only* when not nominal. The non-running
// shapes:
//   - running:  a link that opens the app in a new tab at its own subdomain.
//   - stopped:  grayed; if the viewer may control it, a button that starts the
//               service, with a hover caption under the logo. A deliberate stop
//               is not an error, so it gets no corner alert mark.
//   - failed:   amber-tinted (warning, distinct from the gray stopped tile) with
//               the corner alert mark. For a controller it's a button that
//               re-runs the start transaction (click-to-retry, #154) plus a "View
//               details" link to the app page where the failure reason / logs
//               live; a non-controller sees the warning tile without either.
//   - starting/retrying: grayed with a persistent caption while the start job
//               runs (driven by the `starting` prop from HomeView).
//   - other:    grayed with the corner alert mark — needs attention.
//
// Icon rendering mirrors StoreAppCard: icon_url when present, AppGlyph fallback.
import { computed, ref, watch } from "vue";
import { RouterLink } from "vue-router";
import { AlertTriangle } from "lucide-vue-next";
import type { Instance } from "../api";
import { useAuth } from "../auth";
import AppGlyph from "./AppGlyph.vue";

const props = defineProps<{ instance: Instance; starting?: boolean }>();
const emit = defineEmits<{ start: [] }>();
const { singleUserMode, currentUser } = useAuth();

const running = computed(() => props.instance.state === "running");
const stopped = computed(() => props.instance.state === "stopped");
const failed = computed(() => props.instance.state === "failed");
const label = computed(() => (props.instance.scope === "household" ? "Shared" : "Personal"));

// Mirrors the brain's control gate: admins anything, members only their own
// personal instance. A member can't start a stopped household app, so its tile
// stays grayed but inert.
const canControl = computed(
  () => currentUser.value?.role === "admin" || props.instance.scope === "personal",
);
// stopped → start, failed → retry: the brain runs the identical Start
// transaction for both (the click-to-retry recovery, #154), so the tile emits
// the same `start` event from either state.
const canStart = computed(() => stopped.value && canControl.value && !props.starting);
const canRetry = computed(() => failed.value && canControl.value && !props.starting);
const canAct = computed(() => canStart.value || canRetry.value);

const brokenIcon = ref(false);
// Tracks whether the current in-flight start was initiated from `failed`. The
// server's optimistic SetState("running") fires before the frontend reflects
// starting=true, so reading `failed` in the template would always yield false
// by then; a click-time flag is the only way to show "Retrying…" correctly.
const retrying = ref(false);
watch(
  () => props.starting,
  (v) => { if (!v) retrying.value = false; },
);

// The corner alert is for trouble (failed/crashed), not a deliberate stop or an
// in-flight start/retry.
const showAlert = computed(() => !running.value && !stopped.value && !props.starting);

// Element + behavior: a link when running, a button when the viewer can start or
// retry it, otherwise an inert div.
const tag = computed(() => (running.value ? "a" : canAct.value ? "button" : "div"));

function onClick() {
  if (!canAct.value) return;
  if (canRetry.value) retrying.value = true;
  emit("start");
}
</script>

<template>
  <div class="flex flex-col items-center gap-2 text-center">
    <component
      :is="tag"
      :href="running ? instance.url : undefined"
      :target="running ? '_blank' : undefined"
      :type="tag === 'button' ? 'button' : undefined"
      rel="noopener"
      class="group/tile flex w-full flex-col items-center gap-3"
      :class="running || canAct ? 'cursor-pointer' : 'cursor-default'"
      :title="running ? `Open ${instance.name}` : `${instance.name} is ${instance.state}`"
      @click="onClick"
    >
      <!-- The big square button: parchment fill, bordered, the app icon (or glyph).
           Mirrors StoreAppCard. Grays out when not running; failed gets an amber
           warning tint instead, distinct from the calm gray stopped tile. -->
      <div
        class="relative grid aspect-square w-full place-items-center overflow-hidden rounded-3xl border text-muted-foreground transition"
        :class="
          running
            ? 'border-border bg-card group-hover/tile:shadow-md'
            : failed
              ? 'border-amber-400 bg-amber-50 opacity-90'
              : 'border-border bg-card opacity-50'
        "
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
        <!-- Status caption under the logo. "Starting up…" / "Retrying…" persists
             while the job runs; the click hint is revealed on hover. -->
        <div v-if="starting" class="text-xs text-muted-foreground">
          {{ retrying ? "Retrying…" : "Starting up…" }}
        </div>
        <div
          v-else-if="canRetry"
          class="text-xs text-amber-700 opacity-0 transition group-hover/tile:opacity-100"
        >
          Failed — click to retry
        </div>
        <div
          v-else-if="canStart"
          class="text-xs text-muted-foreground opacity-0 transition group-hover/tile:opacity-100"
        >
          Service stopped - click to start again
        </div>
        <div v-else-if="!singleUserMode" class="text-xs uppercase tracking-wide text-muted-foreground">{{ label }}</div>
      </div>
    </component>

    <!-- Don't retry blind: a controller can open the app page for the failure
         reason / logs rather than looping on the tile (#154). -->
    <RouterLink
      v-if="failed && canControl"
      :to="`/settings/apps/${instance.id}`"
      class="text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground"
    >
      View details
    </RouterLink>
  </div>
</template>
