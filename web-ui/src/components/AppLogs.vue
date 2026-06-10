<script setup lang="ts">
// AppLogs is the per-app Logs panel (DASHBOARD.md; LOGGING.md # Per-app logs).
// It mounts when the user expands an app's Logs in Settings and unmounts when
// they collapse it — mount drives the SSE open/close, so the brain only follows
// host-agent while a panel is on screen.
//
// Visibility is pre-gated by the caller (the Installed apps section only renders the toggle
// when the viewer may see the logs): admins always, plus a member viewing their
// own personal app. The brain enforces the same rule (403/404) as defense in
// depth — but EventSource can't read a non-200 status, so pre-gating avoids a
// silent reconnect storm against a stream the viewer can never open.
import { ref, nextTick, watch } from "vue";
import { useLogStream } from "../useLogStream";

const props = defineProps<{ id: string }>();

const { lines, connected } = useLogStream(props.id);

// Auto-scroll that yields to the reader: stay pinned to the newest line, but the
// moment they scroll up to read history, stop following until they scroll back
// to the bottom. atBottom is recomputed on every scroll.
const scroller = ref<HTMLElement | null>(null);
const atBottom = ref(true);

function onScroll() {
  const el = scroller.value;
  if (!el) return;
  atBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
}

watch(
  () => lines.value.length,
  async () => {
    if (!atBottom.value) return;
    await nextTick();
    const el = scroller.value;
    if (el) el.scrollTop = el.scrollHeight;
  },
);

function jumpToLatest() {
  const el = scroller.value;
  if (!el) return;
  el.scrollTop = el.scrollHeight;
  atBottom.value = true;
}
</script>

<template>
  <div class="relative rounded-lg border border-border bg-muted/40">
    <div
      ref="scroller"
      class="max-h-64 overflow-y-auto px-3 py-2 font-mono text-xs leading-relaxed"
      @scroll="onScroll"
    >
      <p v-if="lines.length === 0" class="py-2 text-muted-foreground">
        {{ connected ? "Waiting for log output…" : "Connecting…" }}
      </p>
      <template v-for="(l, i) in lines" :key="i">
        <div v-if="l.lost" class="py-1 text-center text-[0.7rem] uppercase tracking-wide text-amber-600">
          — some earlier lines were dropped —
        </div>
        <div v-else class="whitespace-pre-wrap break-all" :class="l.stream === 'stderr' ? 'text-red-600' : 'text-foreground'">
          {{ l.line }}
        </div>
      </template>
    </div>

    <button
      v-if="!atBottom"
      class="absolute bottom-2 right-3 rounded-full border border-border bg-card px-2.5 py-1 text-[0.7rem] shadow hover:bg-muted"
      @click="jumpToLatest"
    >
      Jump to latest
    </button>
  </div>
</template>
