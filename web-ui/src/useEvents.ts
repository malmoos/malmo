// useEvents subscribes once to the brain's global SSE stream
// (BRAIN_UI_PROTOCOL.md Pattern C) and invalidates Query caches on relevant
// events — the push/pull-share-one-cache pattern from WEB_UI.md.
import { onMounted, onUnmounted } from "vue";
import { useQueryClient } from "@tanstack/vue-query";

export function useEvents() {
  const qc = useQueryClient();
  let es: EventSource | null = null;

  const invalidateApps = () => qc.invalidateQueries({ queryKey: ["apps"] });

  onMounted(() => {
    es = new EventSource("/api/v1/events", { withCredentials: true });
    for (const kind of ["app.state_changed", "app.installed", "app.uninstalled"]) {
      es.addEventListener(kind, invalidateApps);
    }
  });

  onUnmounted(() => es?.close());
}
