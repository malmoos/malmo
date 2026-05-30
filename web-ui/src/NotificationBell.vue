<script setup lang="ts">
// NotificationBell is the dashboard-chrome bell (NOTIFICATIONS.md # Surfaces):
// an unread-count badge plus a click-to-open dropdown inbox — reverse-chron,
// severity-colored, grouped unread/read, with relative time, an action hint,
// and per-row dismiss. Live updates ride the global SSE channel via useEvents
// (wired in Dashboard.vue), which invalidates the queries this composable owns.
import { ref, computed, onMounted, onUnmounted } from "vue";
import { useNotifications } from "./useNotifications";
import type { Notification } from "./api";

const { list, unreadCount, markRead, markAllRead, dismiss } = useNotifications();

const open = ref(false);
const root = ref<HTMLElement | null>(null);

const notifications = computed(() => list.data.value?.notifications ?? []);
const badge = computed(() => unreadCount.data.value?.count ?? 0);
// Two groups, unread first; the list is already newest-first per group.
const groups = computed(() =>
  [
    { label: "Unread", items: notifications.value.filter((n) => !n.read) },
    { label: "Earlier", items: notifications.value.filter((n) => n.read) },
  ].filter((g) => g.items.length > 0),
);

function toggle() {
  open.value = !open.value;
}

function onRowClick(n: Notification) {
  if (!n.read) markRead.mutate(n.id);
  // No client router in v1 — action_route deep-links are deferred (progress
  // 0027). The action label is shown as an intent hint, not a live link.
}

// Click-outside closes the dropdown (no modal — the bell waits to be looked at).
function onDocClick(e: MouseEvent) {
  if (open.value && root.value && !root.value.contains(e.target as Node)) {
    open.value = false;
  }
}
onMounted(() => document.addEventListener("click", onDocClick));
onUnmounted(() => document.removeEventListener("click", onDocClick));

function relativeTime(ms: number): string {
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 60) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.round(h / 24)}d ago`;
}
</script>

<template>
  <div ref="root" class="bell-wrap">
    <button
      class="bell"
      :class="{ active: open }"
      :aria-label="badge > 0 ? `Notifications, ${badge} unread` : 'Notifications'"
      @click="toggle"
    >
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor"
        stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9" />
        <path d="M13.73 21a2 2 0 0 1-3.46 0" />
      </svg>
      <span v-if="badge > 0" class="badge">{{ badge > 99 ? "99+" : badge }}</span>
    </button>

    <div v-if="open" class="inbox">
      <div class="inbox-head">
        <strong>Notifications</strong>
        <button v-if="badge > 0" class="link" @click="markAllRead.mutate()">Mark all read</button>
      </div>

      <p v-if="list.isLoading.value" class="empty">Loading…</p>
      <p v-else-if="notifications.length === 0" class="empty">You're all caught up.</p>

      <template v-else>
        <div v-for="g in groups" :key="g.label" class="group">
          <div class="group-label">{{ g.label }}</div>
          <div
            v-for="n in g.items"
            :key="n.id"
            class="row"
            :class="{ unread: !n.read }"
            role="button"
            tabindex="0"
            @click="onRowClick(n)"
            @keydown.enter="onRowClick(n)"
            @keydown.space.prevent="onRowClick(n)"
          >
            <span class="dot" :data-sev="n.severity"></span>
            <div class="row-main">
              <div class="row-summary">{{ n.summary }}</div>
              <div v-if="n.body" class="row-body">{{ n.body }}</div>
              <div class="row-meta">
                <span class="time">{{ relativeTime(n.ts) }}</span>
                <span v-if="n.resolved_at" class="resolved">resolved</span>
                <span v-if="n.action_label" class="action">{{ n.action_label }} →</span>
              </div>
            </div>
            <button class="dismiss" title="Dismiss" @click.stop="dismiss.mutate(n.id)">×</button>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.bell-wrap { position: relative; }
.bell {
  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 2rem;
  height: 2rem;
  padding: 0;
  color: #555;
  border: 1px solid #ddd;
  background: #fff;
  border-radius: 8px;
  cursor: pointer;
}
.bell:hover { background: #f4f4f5; }
.bell.active { background: #eef1f5; border-color: #c8cfd8; }
.badge {
  position: absolute;
  top: -6px;
  right: -6px;
  min-width: 1rem;
  height: 1rem;
  padding: 0 3px;
  font-size: 0.62rem;
  font-weight: 600;
  line-height: 1rem;
  text-align: center;
  color: #fff;
  background: #e03131;
  border: 1px solid #fff;
  border-radius: 999px;
}

.inbox {
  position: absolute;
  top: calc(100% + 8px);
  right: 0;
  width: 340px;
  max-height: 460px;
  overflow-y: auto;
  background: #fff;
  border: 1px solid #e6e6e8;
  border-radius: 10px;
  box-shadow: 0 8px 28px rgba(0, 0, 0, 0.12);
  z-index: 50;
}
.inbox-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.6rem 0.85rem;
  border-bottom: 1px solid #eee;
  position: sticky;
  top: 0;
  background: #fff;
}
.inbox-head strong { font-size: 0.85rem; }
.link {
  border: none;
  background: none;
  padding: 0;
  color: #2b6cb0;
  font-size: 0.78rem;
  cursor: pointer;
}
.link:hover { text-decoration: underline; }
.empty { color: #999; font-size: 0.85rem; text-align: center; padding: 1.4rem 1rem; margin: 0; }

.group-label {
  font-size: 0.65rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: #999;
  padding: 0.5rem 0.85rem 0.25rem;
}
.row {
  display: flex;
  gap: 0.55rem;
  align-items: flex-start;
  padding: 0.6rem 0.85rem;
  border-top: 1px solid #f1f1f2;
  cursor: pointer;
}
.row:hover { background: #fafafa; }
.row.unread { background: #f7faff; }
.row.unread:hover { background: #eef4fd; }
.row.unread .row-summary { font-weight: 600; }
.dot {
  flex: 0 0 8px;
  width: 8px;
  height: 8px;
  margin-top: 0.35rem;
  border-radius: 999px;
  background: #adb5bd;
}
.dot[data-sev="info"] { background: #4dabf7; }
.dot[data-sev="warning"] { background: #f59f00; }
.dot[data-sev="error"] { background: #e8590c; }
.dot[data-sev="critical"] { background: #e03131; }
.row-main { flex: 1; min-width: 0; }
.row-summary { font-size: 0.85rem; color: #1a1a1a; }
.row-body { font-size: 0.78rem; color: #777; margin-top: 2px; }
.row-meta { display: flex; gap: 0.5rem; align-items: center; margin-top: 4px; font-size: 0.72rem; color: #999; }
.resolved { color: #2f9e44; }
.action { color: #2b6cb0; }
.dismiss {
  flex: 0 0 auto;
  border: none;
  background: none;
  color: #bbb;
  font-size: 1.1rem;
  line-height: 1;
  padding: 0 0.15rem;
  cursor: pointer;
}
.dismiss:hover { color: #777; }
</style>
