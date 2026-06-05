<script setup lang="ts">
// ToastHost renders the app-wide ephemeral toast stack (WEB_UI.md's toast
// model). Mounted once in AppShell; reads the singleton list from toasts.ts and
// renders it bottom-right, clear of the centered dock. Toasts carry a variant
// (error / success) that colors the accent; the region stays assertive so
// screen readers announce failures promptly.
import { useToasts, dismissToast } from "../toasts";

const { toasts } = useToasts();
</script>

<template>
  <div class="toast-host" role="alert" aria-live="assertive">
    <div v-for="t in toasts" :key="t.id" class="toast" :data-variant="t.variant">
      <span class="toast-msg">{{ t.message }}</span>
      <button class="toast-close" aria-label="Dismiss" @click="dismissToast(t.id)">×</button>
    </div>
  </div>
</template>

<style scoped>
/* Empty region ignores pointer events so it never blocks the UI behind it;
   each toast re-enables them so its dismiss button stays clickable. */
.toast-host {
  position: fixed;
  right: 1rem;
  bottom: 1rem;
  z-index: 100;
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  width: max-content;
  max-width: min(92vw, 22rem);
  pointer-events: none;
}
.toast {
  display: flex;
  align-items: flex-start;
  gap: 0.6rem;
  padding: 0.6rem 0.85rem;
  border-radius: 8px;
  box-shadow: 0 8px 28px rgba(0, 0, 0, 0.12);
  pointer-events: auto;
}
.toast[data-variant="error"] {
  background: #fff5f5;
  border: 1px solid #f3c0c0;
  border-left: 3px solid #e03131;
}
.toast[data-variant="success"] {
  background: #ebfbee;
  border: 1px solid #b2e3bd;
  border-left: 3px solid #2f9e44;
}
.toast-msg {
  flex: 1;
  font-size: 0.82rem;
  line-height: 1.3;
}
.toast[data-variant="error"] .toast-msg { color: #842029; }
.toast[data-variant="success"] .toast-msg { color: #1b5e2a; }
.toast-close {
  flex: 0 0 auto;
  padding: 0 0.1rem;
  border: none;
  background: none;
  font-size: 1.1rem;
  line-height: 1;
  cursor: pointer;
}
.toast[data-variant="error"] .toast-close { color: #c08484; }
.toast[data-variant="error"] .toast-close:hover { color: #842029; }
.toast[data-variant="success"] .toast-close { color: #8bbf97; }
.toast[data-variant="success"] .toast-close:hover { color: #1b5e2a; }
</style>
