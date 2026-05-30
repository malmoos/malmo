// useEvents subscribes once to the brain's global SSE stream
// (BRAIN_UI_PROTOCOL.md Pattern C) and invalidates Query caches on relevant
// events — the push/pull-share-one-cache pattern from WEB_UI.md.
import { onMounted, onUnmounted } from "vue";
import { useQueryClient } from "@tanstack/vue-query";

export function useEvents() {
  const qc = useQueryClient();
  let es: EventSource | null = null;

  const invalidateApps = () => qc.invalidateQueries({ queryKey: ["apps"] });
  // Notification events are advisory refetch triggers (NOTIFICATIONS.md #
  // Surfaces): re-read the caller's audience-scoped list/badge rather than
  // merging payloads off the shared, unfiltered bus.
  const invalidateNotifications = () => qc.invalidateQueries({ queryKey: ["notifications"] });

  onMounted(() => {
    es = new EventSource("/api/v1/events", { withCredentials: true });
    for (const kind of ["app.state_changed", "app.installed", "app.uninstalled"]) {
      es.addEventListener(kind, invalidateApps);
    }
    for (const kind of ["notification.created", "notification.updated"]) {
      es.addEventListener(kind, invalidateNotifications);
    }
  });

  onUnmounted(() => es?.close());
}
