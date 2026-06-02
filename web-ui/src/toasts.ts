// toasts.ts — the app-wide ephemeral toast channel (WEB_UI.md's toast model:
// ephemeral, in-tab, non-blocking). A module-singleton list plus an imperative
// push, the same shape as auth.ts's singleton state: any module can call
// pushErrorToast(message) and <ToastHost> renders the live list once in the
// signed-in shell. No per-call-site UI (issue #44).
//
// Error-only for now: the only callers are notification mutations whose
// optimistic state rolls back silently on failure, and this is their
// user-visible "something failed" feedback. Confirm/success toasts (WEB_UI.md
// "toast on clear", the 409 "View" toast) are not built here — they extend this
// channel when they land.
import { reactive } from "vue";

export interface Toast {
  id: number;
  message: string;
}

// Live toasts, newest last. Reactive so <ToastHost> re-renders on push/dismiss.
const toasts = reactive<Toast[]>([]);

// How long a toast lingers before auto-dismissing — long enough to read a short
// failure line, short enough to stay out of the way.
const TOAST_TTL_MS = 6000;

let seq = 0;

export function pushErrorToast(message: string) {
  const id = ++seq;
  toasts.push({ id, message });
  setTimeout(() => dismissToast(id), TOAST_TTL_MS);
}

export function dismissToast(id: number) {
  const i = toasts.findIndex((t) => t.id === id);
  if (i !== -1) toasts.splice(i, 1);
}

export function useToasts() {
  return { toasts };
}
