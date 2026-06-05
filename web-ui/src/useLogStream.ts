// useLogStream tails one app's logs over the brain's per-app SSE stream
// (BRAIN_UI_PROTOCOL.md Pattern C; LOGGING.md # Per-app logs). It opens the
// stream on mount and closes it on unmount — the open-opens-stream contract the
// brain ref-counts, so the upstream host-agent follow runs only while a Logs
// panel is on screen.
//
// EventSource auto-reconnects on a drop and resends Last-Event-ID; the brain
// replays the missed tail from its ring (or sends a {lost} frame when the gap
// was evicted), so we just append whatever arrives — no manual reconnect logic.
import { ref, onMounted, onUnmounted } from "vue";

// LogLine is the `data:` payload of each frame. A frame with lost=true is the
// gap marker (no text); everything else is a real journald line.
export interface LogLine {
  ts?: string;
  stream?: "stdout" | "stderr";
  line?: string;
  lost?: boolean;
}

// MAX_LINES caps the in-memory tail so a long-open panel doesn't grow without
// bound; the brain's ring is the source of truth for replay, not this buffer.
const MAX_LINES = 1000;

export function useLogStream(id: string) {
  const lines = ref<LogLine[]>([]);
  const connected = ref(false);
  let es: EventSource | null = null;

  function connect() {
    if (es) return;
    lines.value = [];
    // EventSource bypasses the api.ts fetch wrapper; write the full path and pass
    // the session cookie (withCredentials), exactly as LiveResources.vue does.
    es = new EventSource(`/api/v1/apps/${encodeURIComponent(id)}/log`, { withCredentials: true });
    es.onopen = () => {
      connected.value = true;
    };
    es.onmessage = (e) => {
      const data = JSON.parse(e.data) as LogLine;
      const next = lines.value.concat(data);
      lines.value = next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next;
    };
    es.onerror = () => {
      // EventSource reconnects on its own; mark disconnected so the UI can hint.
      connected.value = false;
    };
  }

  function disconnect() {
    es?.close();
    es = null;
    connected.value = false;
  }

  onMounted(connect);
  onUnmounted(disconnect);

  return { lines, connected };
}
