// useNotifications wraps the /api/v1/notifications read surface
// (NOTIFICATIONS.md # Surfaces) as TanStack Query state — the dashboard-bell
// sibling of the catalog/apps queries in Dashboard.vue. The list and unread
// count are pull; useEvents pushes cache invalidations on
// notification.created / notification.updated so the bell updates live without
// owning its own EventSource (WEB_UI.md: push and pull share one cache).
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { api, type Notification } from "./api";

export function useNotifications() {
  const qc = useQueryClient();
  // Both queries live under the ["notifications"] prefix so a single
  // invalidateQueries refetches the list and the badge together.
  const invalidate = () => qc.invalidateQueries({ queryKey: ["notifications"] });

  const list = useQuery({
    queryKey: ["notifications", "list"],
    queryFn: () => api.get<{ notifications: Notification[] }>("/notifications"),
  });

  // Dedicated count endpoint, not derived from the (paged) list: the badge must
  // reflect every unread row, including ones past the first page.
  const unreadCount = useQuery({
    queryKey: ["notifications", "unread-count"],
    queryFn: () => api.get<{ count: number }>("/notifications/unread-count"),
  });

  const markRead = useMutation({
    mutationFn: (id: number) => api.post<void>(`/notifications/${id}/read`),
    onSettled: invalidate,
  });
  const markAllRead = useMutation({
    mutationFn: () => api.post<void>("/notifications/read-all"),
    onSettled: invalidate,
  });
  const dismiss = useMutation({
    mutationFn: (id: number) => api.post<void>(`/notifications/${id}/dismiss`),
    onSettled: invalidate,
  });

  return { list, unreadCount, markRead, markAllRead, dismiss };
}
