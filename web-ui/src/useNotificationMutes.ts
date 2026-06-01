// useNotificationMutes wraps the per-category mute API (NOTIFICATIONS.md #
// Configuration): GET the caller's muted categories, PUT to mute / DELETE to
// unmute. A mute is a personal, per-user view preference (not audited) —
// everything is on by default, so an empty set means "receiving everything".
//
// It lives under the ["notifications"] query prefix so the same SSE-driven
// invalidation that refreshes the bell (useEvents → notification.updated, which
// the brain publishes on a mute change) also reconciles the mute set, and so
// flipping a mute re-reads the inbox + badge under their new read-time filter.
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { api } from "./api";

const MUTES_KEY = ["notifications", "mutes"];

export function useNotificationMutes() {
  const qc = useQueryClient();

  const mutes = useQuery({
    queryKey: MUTES_KEY,
    queryFn: () => api.get<{ muted: string[] }>("/notifications/mutes"),
  });

  // mute=PUT, unmute=DELETE (both idempotent). Optimistic: a toggle is a control
  // whose whole point is instant physical feedback, so flip the cached set in
  // onMutate rather than wait for the round-trip; roll back on error, then
  // invalidate the whole notifications prefix to reconcile the mute set and the
  // now-refiltered inbox/badge against the server's truth.
  const setMuted = useMutation({
    mutationFn: ({ category, muted }: { category: string; muted: boolean }) =>
      muted
        ? api.put<void>(`/notifications/mutes/${category}`)
        : api.del<void>(`/notifications/mutes/${category}`),
    onMutate: async ({ category, muted }) => {
      await qc.cancelQueries({ queryKey: MUTES_KEY });
      const prev = qc.getQueryData<{ muted: string[] }>(MUTES_KEY);
      const next = new Set(prev?.muted ?? []);
      if (muted) next.add(category);
      else next.delete(category);
      qc.setQueryData(MUTES_KEY, { muted: [...next] });
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev) qc.setQueryData(MUTES_KEY, ctx.prev);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ["notifications"] }),
  });

  return { mutes, setMuted };
}
