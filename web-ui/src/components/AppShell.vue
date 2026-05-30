<script setup lang="ts">
// The signed-in chrome: a quiet top bar, the routed view, and the floating
// four-item dock (DASHBOARD.md # the top bar / # global navigation). The SSE
// subscription lives here — one subscription for the whole signed-in app
// (WEB_UI.md: useEvents() at app mount), so cache invalidation works on every
// view, not just Home.
import TopBar from "./TopBar.vue";
import Dock from "./Dock.vue";
import { useEvents } from "../useEvents";

useEvents();
</script>

<template>
  <div class="flex h-full flex-col">
    <TopBar />
    <!-- pb-28 keeps the last row clear of the floating dock. -->
    <main class="flex-1 overflow-y-auto px-4 pb-28 pt-2">
      <div class="mx-auto max-w-3xl">
        <RouterView />
      </div>
    </main>
    <Dock />
  </div>
</template>
