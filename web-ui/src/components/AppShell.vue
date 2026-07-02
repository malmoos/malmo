<script setup lang="ts">
// The signed-in chrome: a quiet top bar, the routed view, and the floating
// four-item dock (DASHBOARD.md # the top bar / # global navigation). The SSE
// subscription lives here — one subscription for the whole signed-in app
// (WEB_UI.md: useEvents() at app mount), so cache invalidation works on every
// view, not just Home.
import TopBar from "./TopBar.vue";
import Dock from "./Dock.vue";
import ToastHost from "./ToastHost.vue";
import ElevateDialog from "./ElevateDialog.vue";
import HealthBanner from "./HealthBanner.vue";
import { useEvents } from "../useEvents";

useEvents();
</script>

<template>
  <div class="flex h-full flex-col">
    <TopBar />
    <!-- Degraded-mode bar: shows above the routed view on every page when an
         error/critical health issue is active (HEALTH.md # Display). -->
    <HealthBanner />
    <!-- The content wrapper is min-h-full (fills the viewport when short, grows
         with content when long) rather than a fixed h-full box — a fixed box lets
         tall pages overflow *past* the scroll container's padding, so the fixed
         dock would cover the last rows. pb-28 rides on the wrapper so the clearance
         is measured from the bottom of the content, guaranteeing the last row
         clears the dock on pages of any length. flex-col so a full-height section
         (Settings' logs pane) can still flex-1 to fill. -->
    <main class="min-h-0 flex-1 overflow-y-auto px-4 pt-2">
      <div class="mx-auto flex min-h-full max-w-6xl flex-col pb-28">
        <RouterView />
      </div>
    </main>
    <Dock />
    <ToastHost />
    <ElevateDialog />
  </div>
</template>
